package kernel

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	policyapi "github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (g *Gateway) BeginTurn(ctx context.Context, req BeginTurnRequest) (BeginTurnResult, error) {
	session, err := g.sessions.Session(ctx, req.SessionRef)
	if err != nil {
		return BeginTurnResult{}, wrapSessionError(err)
	}
	req.SessionRef = session.SessionRef
	runCtx, cancel := context.WithCancel(ctx)
	cancelFn := sync.OnceValue(func() bool {
		cancel()
		return true
	})
	g.mu.Lock()
	if _, ok := g.active[session.SessionID]; ok {
		g.mu.Unlock()
		return BeginTurnResult{}, &Error{
			Kind:        KindConflict,
			Code:        CodeActiveRunConflict,
			UserVisible: true,
			Message:     "gateway: session already has an active run",
		}
	}
	handle := newTurnHandle(turnHandleConfig{
		handleID:                g.allocateID("handle"),
		runID:                   g.allocateID("run"),
		turnID:                  g.allocateID("turn"),
		activeKind:              ActiveTurnKindKernel,
		sessionRef:              session.SessionRef,
		createdAt:               g.clock(),
		allowPendingSubmissions: true,
		cancel: func() bool {
			return cancelFn()
		},
	})
	g.active[session.SessionID] = handle
	g.mu.Unlock()

	resolved, err := g.resolveBeginTurn(ctx, session, req)
	if err != nil {
		cancelFn()
		handle.finish()
		g.releaseActive(session.SessionID, handle)
		return BeginTurnResult{}, err
	}
	resolved.RunRequest.Request = resolved.RunRequest.Request.WithDefaults(g.requestOptions(req))
	g.mu.Lock()
	if g.active[session.SessionID] == handle {
		g.noteActiveHandleLocked(session.SessionID, handle)
	}
	g.mu.Unlock()

	go g.runTurn(runCtx, session, req, resolved, handle)

	return BeginTurnResult{
		Session: session,
		Handle:  handle,
	}, nil
}

func (g *Gateway) resolveBeginTurn(ctx context.Context, activeSession session.Session, req BeginTurnRequest) (ResolvedTurn, error) {
	if activeSession.Controller.Kind == session.ControllerKindACP {
		if resolver, ok := g.resolver.(ControllerTurnResolver); ok && resolver != nil {
			return resolver.ResolveControllerTurn(ctx, req)
		}
		return ResolvedTurn{
			RunRequest: agent.RunRequest{
				SessionRef:   activeSession.SessionRef,
				Input:        req.Input,
				ContentParts: append([]model.ContentPart(nil), req.ContentParts...),
			},
		}, nil
	}
	return g.resolver.ResolveTurn(ctx, req)
}

func (g *Gateway) requestOptions(req BeginTurnRequest) agent.ModelRequestOptions {
	if g == nil || g.request == nil {
		return req.Request
	}
	return req.Request.WithDefaults(g.request.ResolveTurnRequest(req))
}

func (g *Gateway) allocateID(prefix string) string {
	id := g.nextID.Add(1)
	return fmt.Sprintf("%s-%d", prefix, id)
}

func (g *Gateway) runTurn(
	ctx context.Context,
	session session.Session,
	req BeginTurnRequest,
	resolved ResolvedTurn,
	handle *turnHandle,
) {
	defer handle.finish()
	defer g.releaseActive(session.SessionID, handle)

	runReq := resolved.RunRequest
	runReq.SessionRef = session.SessionRef
	if strings.TrimSpace(runReq.Input) == "" {
		runReq.Input = req.Input
	}
	if len(runReq.ContentParts) == 0 && len(req.ContentParts) > 0 {
		runReq.ContentParts = append([]model.ContentPart(nil), req.ContentParts...)
	}
	normalizeRunRequestPolicyProfile(&runReq)
	runReq.ApprovalRequester = approvalRequesterFunc(func(approvalCtx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		return g.resolveApprovalRequest(ctx, approvalCtx, handle, &req, runReq.AgentSpec.Model)
	})

	result, err := g.runtime.Run(ctx, runReq)
	if err != nil {
		handle.publish(EventEnvelope{
			Event: Event{
				Kind:       EventKindLifecycle,
				HandleID:   handle.handleID,
				RunID:      handle.runID,
				TurnID:     handle.turnID,
				SessionRef: handle.sessionRef,
			},
			Err: EventError(err),
		})
		return
	}
	if result.Handle == nil {
		return
	}
	handle.setRunner(result.Handle)
	defer result.Handle.Close()
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			handle.publish(EventEnvelope{
				Event: Event{
					Kind:       EventKindLifecycle,
					HandleID:   handle.handleID,
					RunID:      handle.runID,
					TurnID:     handle.turnID,
					SessionRef: handle.sessionRef,
				},
				Err: EventError(seqErr),
			})
			return
		}
		handle.publishSessionEvent(event)
		g.noteSessionCursor(session.SessionID, event.ID)
	}
}

func normalizeRunRequestPolicyProfile(req *agent.RunRequest) {
	if req == nil || len(req.AgentSpec.Metadata) == 0 {
		return
	}
	if raw, ok := req.AgentSpec.Metadata[policyapi.MetadataPolicyProfile].(string); ok {
		profile := policyapi.NormalizeProfileName(raw)
		if profile == "" {
			delete(req.AgentSpec.Metadata, policyapi.MetadataPolicyProfile)
			return
		}
		req.AgentSpec.Metadata[policyapi.MetadataPolicyProfile] = profile
		return
	}
	raw, ok := req.AgentSpec.Metadata[policyapi.MetadataLegacyPolicyMode].(string)
	if !ok {
		return
	}
	profile := policyapi.NormalizeProfileName(raw)
	delete(req.AgentSpec.Metadata, policyapi.MetadataLegacyPolicyMode)
	if profile == "" {
		return
	}
	req.AgentSpec.Metadata[policyapi.MetadataPolicyProfile] = profile
}

func (g *Gateway) runParticipantTurn(
	ctx context.Context,
	session session.Session,
	req PromptParticipantRequest,
	handle *turnHandle,
) {
	defer handle.finish()
	defer g.releaseActive(session.SessionID, handle)

	runReq := agent.PromptParticipantRequest{
		SessionRef:    session.SessionRef,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Input:         strings.TrimSpace(req.Input),
		ContentParts:  append([]model.ContentPart(nil), req.ContentParts...),
		Source:        strings.TrimSpace(req.Source),
		Stream:        true,
	}
	runReq.ApprovalRequester = approvalRequesterFunc(func(approvalCtx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		return g.resolveApprovalRequest(ctx, approvalCtx, handle, &req, nil)
	})

	result, err := g.control.PromptParticipant(ctx, runReq)
	if err != nil {
		handle.publish(EventEnvelope{
			Event: Event{
				Kind:       EventKindLifecycle,
				HandleID:   handle.handleID,
				RunID:      handle.runID,
				TurnID:     handle.turnID,
				SessionRef: handle.sessionRef,
			},
			Err: EventError(err),
		})
		return
	}
	if result.Handle == nil {
		return
	}
	handle.setRunner(result.Handle)
	defer result.Handle.Close()
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			handle.publish(EventEnvelope{
				Event: Event{
					Kind:       EventKindLifecycle,
					HandleID:   handle.handleID,
					RunID:      handle.runID,
					TurnID:     handle.turnID,
					SessionRef: handle.sessionRef,
				},
				Err: EventError(seqErr),
			})
			return
		}
		handle.publishSessionEvent(event)
		g.noteSessionCursor(session.SessionID, event.ID)
	}
}
