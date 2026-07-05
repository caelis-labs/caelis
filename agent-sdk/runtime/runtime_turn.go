package runtime

import (
	"context"
	"errors"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// RunState returns the last known run state for one session.
func (r *Runtime) RunState(
	_ context.Context,
	ref session.SessionRef,
) (agent.RunState, error) {
	ref = session.NormalizeSessionRef(ref)
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, ok := r.runStates[ref.SessionID]
	if !ok {
		return agent.RunState{}, session.ErrSessionNotFound
	}
	return state, nil
}

func (r *Runtime) resolveAgent(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	state map[string]any,
	runID string,
	turnID string,
	req agent.RunRequest,
) (agent.Agent, error) {
	if req.Agent != nil {
		return req.Agent, nil
	}
	spec := r.applyAssemblySpec(state, req.AgentSpec)
	spec.Request = req.Request.WithDefaults(spec.Request)
	modeName, _ := r.policyForName(ctx, r.policyMode(spec))
	spec.Model = r.wrapModelForAutoCompaction(ref, spec.Model)
	spec.Tools = r.wrapToolsForRuntime(activeSession, ref, spec, runtimeToolContext{
		mode:              modeName,
		approvalMode:      string(r.currentApprovalMode(state)),
		approvalRequester: req.ApprovalRequester,
		runID:             strings.TrimSpace(runID),
		turnID:            strings.TrimSpace(turnID),
	})
	spec.Tools = r.wrapToolsForPolicy(activeSession, ref, state, spec, approvalContext{
		ctx:        ctx,
		requester:  req.ApprovalRequester,
		runtime:    r,
		session:    session.CloneSession(activeSession),
		sessionRef: session.NormalizeSessionRef(ref),
		runID:      strings.TrimSpace(runID),
		turnID:     strings.TrimSpace(turnID),
	})
	return r.agentFactory.NewAgent(ctx, spec)
}

func (r *Runtime) setRunState(sessionID string, state agent.RunState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runStates[strings.TrimSpace(sessionID)] = state
}

func (r *Runtime) runWithOverflowRecovery(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runID string,
	turnID string,
	req agent.RunRequest,
	pendingInput *session.Event,
	batch *[]*session.Event,
	sink *runner,
) error {
	compactionRecoveries := 0
	for {
		attemptBatch, _, inputPersisted, err := r.runAttempt(ctx, activeSession, ref, runID, turnID, req, pendingInput, sink)
		if inputPersisted {
			pendingInput = nil
		}
		if err == nil {
			*batch = append(*batch, attemptBatch...)
			return nil
		}
		if recovery, ok := compactionRecoveryFromError(err); ok {
			*batch = append(*batch, attemptBatch...)
			if compactionRecoveries >= overflowCompactionRecoveryLimit {
				return recovery.limitError(overflowCompactionRecoveryLimit, err)
			}
			compacted, compactErr := r.recoverByCompacting(ctx, activeSession, ref, turnID, req, recovery, sink)
			if compactErr != nil {
				return compactErr
			}
			if !compacted {
				return err
			}
			compactionRecoveries++
			continue
		}
		*batch = append(*batch, attemptBatch...)
		return err
	}
}

func (r *Runtime) runAttempt(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runID string,
	turnID string,
	req agent.RunRequest,
	pendingInput *session.Event,
	sink *runner,
) ([]*session.Event, bool, bool, error) {
	invocation, err := r.prepareInvocationContext(ctx, activeSession, ref, req, pendingInput)
	if err != nil {
		var compactErr *compactionFailureError
		if errors.As(err, &compactErr) {
			r.publishCompactFailureNotice(activeSession, turnID, sink, compactErr)
		}
		return nil, false, false, err
	}

	batch := make([]*session.Event, 0, 3)
	inputPersisted := false
	if pendingInput != nil {
		persisted, appendErr := r.appendRuntimeEventOrLifecycle(ctx, activeSession, ref, turnID, pendingInput)
		if appendErr != nil {
			return nil, false, false, appendErr
		}
		batch = append(batch, persisted)
		invocation.PromptEvents = append(invocation.PromptEvents, session.CloneEvent(persisted))
		inputPersisted = true
		if sink != nil {
			sink.publishEvent(persisted)
		}
	}
	if invocation.LiveCompact != nil {
		batch = append(batch, session.CloneEvent(invocation.LiveCompact))
		if sink != nil {
			notice := buildCompactNoticeEvent(activeSession, turnID, r.now())
			sink.publishEvent(normalizeEvent(activeSession, turnID, notice))
		}
	}

	activeAgent, err := r.resolveAgent(ctx, activeSession, ref, invocation.State, runID, turnID, req)
	if err != nil {
		return batch, false, inputPersisted, err
	}
	var drainSubmissions func() []agent.Submission
	if sink != nil {
		drainSubmissions = sink.drainSubmissions
	}
	runCtx := agent.NewContext(agent.ContextSpec{
		Context:          ctx,
		Session:          activeSession,
		Events:           invocation.PromptEvents,
		State:            invocation.State,
		DrainSubmissions: drainSubmissions,
	})

	emitted := false
	for event, runErr := range activeAgent.Run(runCtx) {
		if runErr != nil {
			return batch, emitted, inputPersisted, runErr
		}
		if event == nil {
			continue
		}
		emitted = true
		normalized := normalizeEvent(activeSession, turnID, event)
		if session.IsCanonicalHistoryEvent(normalized) {
			normalized, err = r.appendRuntimeEventOrLifecycle(ctx, activeSession, ref, turnID, normalized)
			if err != nil {
				return batch, emitted, inputPersisted, err
			}
			_ = r.tasks.syncCanonicalToolResult(ctx, ref, normalized)
		}
		batch = append(batch, session.CloneEvent(normalized))
		if sink != nil {
			sink.publishEvent(normalized)
		}
		if planEvent, handled, planErr := r.handlePlanEvent(ctx, ref, turnID, normalized); planErr != nil {
			return batch, emitted, inputPersisted, planErr
		} else if handled {
			batch = append(batch, session.CloneEvent(planEvent))
			if sink != nil {
				sink.publishEvent(planEvent)
			}
		}
	}
	if err := r.updateCompactionUsageFromBatch(ctx, ref, batch); err != nil {
		return batch, emitted, inputPersisted, err
	}
	return batch, emitted, inputPersisted, nil
}

func (r *Runtime) appendRuntimeEventOrLifecycle(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	turnID string,
	event *session.Event,
) (*session.Event, error) {
	persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event:      event,
	})
	if err == nil {
		return persisted, nil
	}
	if !errors.Is(err, session.ErrInvalidEvent) {
		return nil, err
	}
	if runtimeAppendEventIsModelVisible(event) {
		return nil, err
	}
	lifecycle := recoverableRuntimeEvent(activeSession, turnID, event, err)
	persisted, lifecycleErr := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event:      lifecycle,
	})
	if lifecycleErr == nil {
		return persisted, nil
	}
	if errors.Is(lifecycleErr, session.ErrInvalidEvent) {
		return session.MarkUIOnly(lifecycle), nil
	}
	return nil, errors.Join(err, lifecycleErr)
}

func runtimeAppendEventIsModelVisible(event *session.Event) bool {
	switch session.EventTypeOf(event) {
	case session.EventTypeUser,
		session.EventTypeAssistant,
		session.EventTypeToolCall,
		session.EventTypeToolResult,
		session.EventTypeSystem,
		session.EventTypeCompact:
		return true
	default:
		return false
	}
}

func recoverableRuntimeEvent(
	activeSession session.Session,
	turnID string,
	event *session.Event,
	err error,
) *session.Event {
	scope := defaultScope(activeSession, turnID)
	eventType := ""
	if event != nil {
		eventType = string(session.EventTypeOf(event))
	}
	return &session.Event{
		Type:       session.EventTypeLifecycle,
		Visibility: session.VisibilityCanonical,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Scope:      &scope,
		Lifecycle: &session.EventLifecycle{
			Status: "recovered",
			Reason: "recoverable_event_normalization_error",
			Meta: map[string]any{
				"event_type": eventType,
				"error":      session.EventValidationDetail(err),
			},
		},
	}
}
