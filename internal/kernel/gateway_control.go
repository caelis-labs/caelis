package kernel

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (g *Gateway) HandoffController(ctx context.Context, req HandoffControllerRequest) (session.Session, error) {
	if g.control == nil {
		return session.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err := g.control.HandoffController(ctx, agent.HandoffControllerRequest{
		SessionRef:              ref,
		ExpectedRevision:        req.ExpectedRevision,
		ExpectedControllerEpoch: strings.TrimSpace(req.ExpectedControllerEpoch),
		Kind:                    req.Kind,
		Agent:                   strings.TrimSpace(req.Agent),
		Source:                  strings.TrimSpace(req.Source),
		Reason:                  strings.TrimSpace(req.Reason),
	})
	if err != nil {
		return session.Session{}, err
	}
	g.bind(req.BindingKey, activeSession.SessionRef, BindingDescriptor{})
	return activeSession, nil
}

func (g *Gateway) AttachParticipant(ctx context.Context, req AttachParticipantRequest) (session.Session, error) {
	if g.control == nil {
		return session.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return session.Session{}, err
	}
	agentName := strings.TrimSpace(req.Agent)
	if placedAgent := strings.TrimSpace(req.Placement.Agent); placedAgent != "" {
		agentName = placedAgent
	}
	activeSession, err := g.control.AttachParticipant(ctx, agent.AttachParticipantRequest{
		SessionRef: ref,
		Agent:      agentName,
		Role:       req.Role,
		Source:     strings.TrimSpace(req.Source),
		Label:      strings.TrimSpace(req.Label),
		Placement:  req.Placement,
	})
	if err != nil {
		return session.Session{}, err
	}
	g.bind(req.BindingKey, activeSession.SessionRef, BindingDescriptor{})
	return activeSession, nil
}

func (g *Gateway) DetachParticipant(ctx context.Context, req DetachParticipantRequest) (session.Session, error) {
	if g.control == nil {
		return session.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err := g.control.DetachParticipant(ctx, agent.DetachParticipantRequest{
		SessionRef:    ref,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Source:        strings.TrimSpace(req.Source),
	})
	if err != nil {
		return session.Session{}, err
	}
	g.bind(req.BindingKey, activeSession.SessionRef, BindingDescriptor{})
	return activeSession, nil
}

func (g *Gateway) PromptParticipant(ctx context.Context, req PromptParticipantRequest) (BeginTurnResult, error) {
	if err := g.waitForTurnStart(ctx); err != nil {
		return BeginTurnResult{}, err
	}
	if g.control == nil {
		return BeginTurnResult{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return BeginTurnResult{}, err
	}
	session, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return BeginTurnResult{}, wrapSessionError(err)
	}
	if err := g.recoverLostActiveRun(ctx, session.SessionRef); err != nil {
		return BeginTurnResult{}, err
	}
	req, err = g.preparePromptParticipantRequest(ctx, session, req)
	if err != nil {
		return BeginTurnResult{}, err
	}
	g.bind(req.BindingKey, session.SessionRef, BindingDescriptor{})
	runtimeParent := ctx
	if req.RuntimeContext != nil {
		runtimeParent = req.RuntimeContext
	}
	runCtx, cancel := context.WithCancel(runtimeParent)
	cancelFn := sync.OnceValue(func() bool {
		cancel()
		return true
	})
	approvals := g.sessionApprovals(session.SessionRef)
	g.mu.Lock()
	if g.quiescing {
		g.mu.Unlock()
		cancel()
		return BeginTurnResult{}, hostClosingError()
	}
	if _, ok := g.active[session.SessionID]; ok {
		g.mu.Unlock()
		cancel()
		return BeginTurnResult{}, activeRunConflictError()
	}
	handleID := g.allocateID("handle")
	runID := g.allocateID("participant-run")
	turnID := newParticipantTurnID()
	handle := newTurnHandle(turnHandleConfig{
		handleID:                handleID,
		runID:                   runID,
		turnID:                  turnID,
		activeKind:              ActiveTurnKindParticipant,
		participantID:           req.ParticipantID,
		sessionRef:              session.SessionRef,
		createdAt:               g.clock(),
		allowPendingSubmissions: true,
		prepareSubmission: func(submitCtx context.Context, submitReq SubmitRequest) (SubmitRequest, error) {
			return g.prepareSubmitRequest(submitCtx, session, submitReq)
		},
		cancel: func() bool {
			return cancelFn()
		},
		approvals:       approvals,
		persistApproval: g.approvalPersister(session.SessionRef, turnID),
		settleApproval:  g.approvalSettler(session.SessionRef, turnID),
	})
	g.active[session.SessionID] = handle
	g.signalActiveChangedLocked()
	g.noteActiveHandleLocked(session.SessionID, handle)
	g.mu.Unlock()

	go g.runParticipantTurn(runCtx, cancelFn, session, req, handle)

	return BeginTurnResult{
		Session: session,
		Handle:  handle,
	}, nil
}

// newParticipantTurnID allocates the Control-owned identity that is also
// persisted by Runtime for one participant turn. It must remain globally
// unique across Gateway process restarts.
func newParticipantTurnID() string {
	return "participant-turn-" + strings.ToLower(rand.Text())
}

func (g *Gateway) ControlPlaneState(ctx context.Context, req ControlPlaneStateRequest) (ControlPlaneState, error) {
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return ControlPlaneState{}, err
	}
	activeSession, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return ControlPlaneState{}, wrapSessionError(err)
	}
	events, err := g.sessions.Events(ctx, session.EventsRequest{
		SessionRef: ref,
	})
	if err != nil {
		return ControlPlaneState{}, wrapSessionError(err)
	}
	runState, err := g.runtime.RunState(ctx, ref)
	if err != nil && !errors.Is(err, session.ErrSessionNotFound) {
		return ControlPlaneState{}, err
	}
	return buildControlPlaneState(activeSession, runState, events), nil
}
