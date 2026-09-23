package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// RunState returns the last known run state for one session.
func (r *Runtime) RunState(
	ctx context.Context,
	ref session.SessionRef,
) (agent.RunState, error) {
	ref = session.NormalizeSessionRef(ref)
	r.mu.RLock()
	state, ok := r.runStates[ref.SessionID]
	r.mu.RUnlock()
	if !ok {
		return r.persistedRunState(ctx, ref, "")
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
	toolStepSequence *atomic.Uint64,
) (agent.Agent, error) {
	if req.Agent != nil {
		return req.Agent, nil
	}
	spec := cloneAgentSpec(req.AgentSpec)
	spec.Request = req.Request.WithDefaults(spec.Request)
	if spec.ResolveModelRequest != nil {
		spec.ResolveModelRequest = r.wrapModelRequestResolver(ctx, activeSession, ref, state, spec, req, runID, turnID, toolStepSequence)
	} else if err := r.prepareAgentSpec(ctx, activeSession, ref, state, &spec, req, runID, turnID, toolStepSequence); err != nil {
		return nil, err
	}

	return r.agentFactory.NewAgent(ctx, spec)
}

// wrapTurnTools is shared by native and externally controlled execution.
func (r *Runtime) wrapTurnTools(ctx context.Context, activeSession session.Session, ref session.SessionRef, state map[string]any, spec agent.AgentSpec, requester agent.ApprovalRequester, runID, turnID string, sequence *atomic.Uint64) []tool.Tool {
	spec.Tools = r.wrapToolsForExecutionJournal(ref, runID, turnID, sequence, spec.Tools)
	spec.Tools = r.wrapToolsForPolicy(activeSession, ref, state, spec, approvalContext{
		ctx: ctx, requester: requester, runtime: r, session: session.CloneSession(activeSession), sessionRef: session.NormalizeSessionRef(ref), runID: runID, turnID: turnID,
	})
	return r.wrapToolsForLifecycle(spec.Tools)
}

func cloneAgentSpec(in agent.AgentSpec) agent.AgentSpec {
	out := in
	out.Tools = append([]tool.Tool(nil), in.Tools...)
	out.Metadata = session.CloneState(in.Metadata)
	return out
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
	pendingInputs []*session.Event,
	batch *[]*session.Event,
	sink *runner,
) error {
	currentTurnInputs := session.CloneEvents(pendingInputs)
	var toolFactOrdinal uint64
	// Share the durable tool-step counter across overflow retries so a reused
	// provider-local call ID cannot collide with a prior successful execution.
	toolStepSequence := &atomic.Uint64{}
	for {
		attemptBatch, _, inputPersisted, err := r.runAttempt(ctx, activeSession, ref, runID, turnID, req, pendingInputs, sink, &toolFactOrdinal, toolStepSequence)
		if inputPersisted {
			pendingInputs = nil
		}
		if err == nil {
			*batch = append(*batch, attemptBatch...)
			return nil
		}
		if recovery, ok := compactionRecoveryFromError(err); ok {
			*batch = append(*batch, attemptBatch...)
			progress, compacted, compactErr := r.recoverByCompacting(ctx, ref, turnID, req, recovery, currentTurnInputs, sink)
			if compactErr != nil {
				return compactErr
			}
			if !compacted {
				return err
			}
			if !progress.madeDurableProgress() {
				return recovery.noProgressError(err)
			}
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
	pendingInputs []*session.Event,
	sink *runner,
	toolFactOrdinal *uint64,
	toolStepSequence *atomic.Uint64,
) ([]*session.Event, bool, bool, error) {
	invocation, err := r.prepareInvocationContext(ctx, activeSession, ref, turnID, req, pendingInputs, sink)
	if err != nil {
		var compactErr *compactionFailureError
		if errors.As(err, &compactErr) {
			r.publishCompactFailureNotice(activeSession, turnID, sink, compactErr)
		}
		return nil, false, false, err
	}

	batch := make([]*session.Event, 0, 3)
	persistedInputs, appendErr := r.appendInputEvents(ctx, ref, pendingInputs)
	if appendErr != nil {
		return nil, false, false, appendErr
	}
	inputPersisted := len(persistedInputs) > 0
	for _, persisted := range persistedInputs {
		batch = append(batch, persisted)
		invocation.PromptEvents = append(invocation.PromptEvents, session.CloneEvent(persisted))
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

	activeAgent, err := r.resolveAgent(ctx, activeSession, ref, invocation.State, runID, turnID, req, toolStepSequence)
	if err != nil {
		return batch, false, inputPersisted, err
	}
	var drainSubmissions func() []agent.Submission
	var inputReady func() <-chan struct{}
	if sink != nil {
		drainSubmissions = sink.drainSubmissions
		inputReady = sink.inputReadySignal
	}
	runCtx := agent.NewContext(agent.ContextSpec{
		Context:          ctx,
		Session:          activeSession,
		Events:           invocation.PromptEvents,
		State:            invocation.State,
		DrainSubmissions: drainSubmissions,
		InputReady:       inputReady,
	})

	emitted := false
	if sink != nil {
		runCtx = &localInputContext{Context: runCtx, runner: sink, commit: func(inputs []agent.AgentCommunicationInput) ([]model.Message, error) {
			events, err := buildSteeringInputEvents(activeSession, turnID, agent.Submission{Kind: agent.SubmissionKindAgentCommunication, Inputs: inputs})
			if err != nil {
				return nil, err
			}
			persisted, err := r.appendInputEvents(ctx, ref, events)
			if err != nil {
				return nil, err
			}
			messages := make([]model.Message, 0, len(persisted))
			for _, event := range persisted {
				batch = append(batch, event)
				sink.publishEvent(event)
				messages = append(messages, *event.Message)
			}
			emitted = emitted || len(persisted) > 0
			return messages, nil
		}}
	}
	for event, runErr := range activeAgent.Run(runCtx) {
		if runErr != nil {
			return batch, emitted, inputPersisted, runErr
		}
		if event == nil {
			continue
		}
		emitted = true
		normalized := normalizeEvent(activeSession, turnID, event)
		if toolFactOrdinal != nil && scopeRuntimeToolFactIdentity(normalized, runID, turnID, *toolFactOrdinal+1) {
			(*toolFactOrdinal)++
		}
		if runtimeAgentEventShouldPersist(normalized) {
			normalized, err = r.appendRuntimeEventOrLifecycle(ctx, activeSession, ref, turnID, normalized)
			if err != nil {
				return batch, emitted, inputPersisted, err
			}
			if session.IsCanonicalHistoryEvent(normalized) {
				_ = r.tasks.syncCanonicalToolResult(ctx, ref, normalized)
			}
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
	if err := ctx.Err(); err != nil {
		return batch, emitted, inputPersisted, err
	}
	if err := r.updateCompactionUsageFromBatch(ctx, ref, batch); err != nil {
		return batch, emitted, inputPersisted, err
	}
	return batch, emitted, inputPersisted, nil
}

// runtimeAgentEventShouldPersist keeps semantic history and Agent-owned
// execution checkpoints durable without promoting journal facts into model or
// client replay lanes. Journal payloads other than lifecycle checkpoints do
// not enter the ordinary Agent event path.
func runtimeAgentEventShouldPersist(event *session.Event) bool {
	return session.IsCanonicalHistoryEvent(event) ||
		(session.IsJournal(event) && session.EventTypeOf(event) == session.EventTypeLifecycle)
}

func (r *Runtime) appendRuntimeEventOrLifecycle(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	turnID string,
	event *session.Event,
) (*session.Event, error) {
	if session.IsModelInvocationReceipt(event) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return r.sessions.AppendEvent(cleanup, session.AppendEventRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Event: event})
	}
	persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.RuntimeMutationGuard(ctx),
		Event:         event,
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
		SessionRef:    ref,
		MutationGuard: session.RuntimeMutationGuard(ctx),
		Event:         lifecycle,
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
