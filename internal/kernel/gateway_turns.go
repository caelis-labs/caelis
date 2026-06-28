package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/internal/kernel/hooks"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/eventsource"
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

	// Dispatch SessionStart hooks before resolving the first model invocation.
	// Hook outputs are persisted as plugin context hints, not provider system messages.
	if err := g.dispatchSessionStartHooks(ctx, session, handle); err != nil {
		cancelFn()
		handle.finish()
		g.releaseActive(session.SessionID, handle)
		return BeginTurnResult{}, fmt.Errorf("gateway: failed to dispatch SessionStart hooks: %w", err)
	}

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
		handle.publishError(err)
		return
	}
	if result.Handle == nil {
		return
	}
	handle.setRunner(result.Handle)
	defer result.Handle.Close()
	if sourceHandle, ok := result.Handle.(eventsource.Handle); ok && sourceHandle != nil {
		g.forwardSourceEvents(session, handle, sourceHandle)
		return
	}
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			handle.publishError(seqErr)
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
	delete(req.AgentSpec.Metadata, policyapi.MetadataLegacyPolicyMode)
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
		DisplayInput:  strings.TrimSpace(req.DisplayInput),
		DisplayTitle:  strings.TrimSpace(req.DisplayTitle),
		ContentParts:  append([]model.ContentPart(nil), req.ContentParts...),
		Source:        strings.TrimSpace(req.Source),
		Stream:        true,
	}
	runReq.ApprovalRequester = approvalRequesterFunc(func(approvalCtx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		return g.resolveApprovalRequest(ctx, approvalCtx, handle, &req, nil)
	})

	result, err := g.control.PromptParticipant(ctx, runReq)
	if err != nil {
		handle.publishError(err)
		return
	}
	if result.Handle == nil {
		return
	}
	handle.setRunner(result.Handle)
	defer result.Handle.Close()
	if sourceHandle, ok := result.Handle.(eventsource.Handle); ok && sourceHandle != nil {
		g.forwardSourceEvents(session, handle, sourceHandle)
		return
	}
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			handle.publishError(seqErr)
			return
		}
		handle.publishSessionEvent(event)
		g.noteSessionCursor(session.SessionID, event.ID)
	}
}

func (g *Gateway) dispatchSessionStartHooks(ctx context.Context, sessionObj session.Session, handle *turnHandle) error {
	if len(g.sessionStartHooks) == 0 {
		return nil
	}

	for _, hook := range g.sessionStartHooks {
		// Calculate the digest of the full hook configuration
		hookBytes, err := json.Marshal(hook)
		if err != nil {
			return fmt.Errorf("plugin hooks: failed to marshal HookSpec: %w", err)
		}
		hasher := sha256.New()
		hasher.Write(hookBytes)
		digest := hex.EncodeToString(hasher.Sum(nil))

		stateKey := fmt.Sprintf("plugin.hooks.session_start.v1.%s.%s", hook.PluginID, digest)

		// Check if it has already been executed in this session
		state, err := g.sessions.SnapshotState(ctx, sessionObj.SessionRef)
		if err != nil {
			return fmt.Errorf("plugin hooks: failed to snapshot session state: %w", err)
		}
		if val, ok := state[stateKey].(bool); ok && val {
			continue
		}

		// Fallback check: scan session events to ensure exactly-once even if state update failed.
		// A hook has successfully run and persisted its output if we find a corresponding plugin context event.
		events, err := g.sessions.Events(ctx, session.EventsRequest{
			SessionRef:       sessionObj.SessionRef,
			IncludeTransient: true,
		})
		if err != nil {
			return fmt.Errorf("plugin hooks: failed to list session events: %w", err)
		}
		alreadyRun := false
		for _, ev := range events {
			meta := ev.Meta
			if meta["plugin_id"] == hook.PluginID && meta["event"] == "SessionStart" && meta["digest"] == digest && meta["source"] == "plugin_hook" {
				alreadyRun = true
				break
			}
		}
		if alreadyRun {
			// Best-effort backfill session state key to avoid event scans on subsequent turns
			_ = g.sessions.UpdateState(ctx, sessionObj.SessionRef, func(state map[string]any) (map[string]any, error) {
				if state == nil {
					state = make(map[string]any)
				}
				state[stateKey] = true
				return state, nil
			})
			continue
		}

		// Run the hook
		res := hooks.Run(ctx, hook, sessionObj.SessionRef, sessionObj.CWD)

		if res.Error != nil || res.ExitCode != 0 {
			errMsg := ""
			if res.Error != nil {
				errMsg = res.Error.Error()
			}
			stderrSummary := strings.TrimSpace(res.Stderr)
			if len(stderrSummary) > 200 {
				stderrSummary = stderrSummary[:200] + "..."
			}
			lifecycleEvent := &session.Event{
				Type:       session.EventTypeLifecycle,
				Visibility: session.VisibilityCanonical,
				Meta: map[string]any{
					"plugin_id": hook.PluginID,
					"event":     "SessionStart",
					"digest":    digest,
					"source":    "plugin_hook",
				},
				Lifecycle: &session.EventLifecycle{
					Status: "failed",
					Reason: fmt.Sprintf("SessionStart hook for plugin %q failed", hook.PluginID),
					Meta: map[string]any{
						"plugin_id": hook.PluginID,
						"event":     "SessionStart",
						"exit_code": res.ExitCode,
						"error":     errMsg,
						"stderr":    stderrSummary,
					},
				},
			}
			if appendedEvent, appendErr := g.sessions.AppendEvent(ctx, session.AppendEventRequest{
				SessionRef: sessionObj.SessionRef,
				Event:      lifecycleEvent,
			}); appendErr == nil {
				handle.publishSessionEvent(appendedEvent)
				g.noteSessionCursor(sessionObj.SessionID, appendedEvent.ID)
			}

			if err := g.markSessionStartHookExecuted(ctx, sessionObj.SessionRef, stateKey); err != nil {
				return fmt.Errorf("plugin hooks: failed to update session state: %w", err)
			}
			continue
		}

		stdoutTrimmed := strings.TrimSpace(res.Stdout)
		vis := session.VisibilityCanonical
		text := ""
		if stdoutTrimmed == "" {
			vis = session.VisibilityMirror
		} else {
			text = fmt.Sprintf("[Plugin context: %s]\n%s", hook.PluginID, stdoutTrimmed)
			if res.StdoutTruncated {
				text += "\n[Plugin context output truncated]"
			}
		}
		message := model.NewTextMessage(model.RoleUser, text)
		pluginContextEvent := &session.Event{
			Type:       session.EventTypeCustom,
			Visibility: vis,
			Message:    &message,
			Text:       text,
			Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "plugin"},
			Meta: map[string]any{
				"plugin_id":              hook.PluginID,
				"event":                  "SessionStart",
				"digest":                 digest,
				"source":                 "plugin_hook",
				"model_context_role":     string(model.RoleUser),
				"hidden_from_transcript": true,
				"truncated":              res.StdoutTruncated,
			},
		}
		appendedEvent, err := g.sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: sessionObj.SessionRef,
			Event:      pluginContextEvent,
		})
		if err != nil {
			return fmt.Errorf("plugin hooks: failed to append plugin context event: %w", err)
		}
		handle.publishSessionEvent(appendedEvent)
		g.noteSessionCursor(sessionObj.SessionID, appendedEvent.ID)

		if err := g.markSessionStartHookExecuted(ctx, sessionObj.SessionRef, stateKey); err != nil {
			return fmt.Errorf("plugin hooks: failed to update session state: %w", err)
		}
	}

	return nil
}

func (g *Gateway) markSessionStartHookExecuted(ctx context.Context, ref session.SessionRef, stateKey string) error {
	return g.sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		if state == nil {
			state = make(map[string]any)
		}
		state[stateKey] = true
		return state, nil
	})
}
