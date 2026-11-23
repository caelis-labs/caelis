package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// RunState reports only execution still owned by this Runtime process.
func (r *Runtime) RunState(ctx context.Context, ref session.SessionRef) (agent.RunState, error) {
	active := r.activeRunForSession(ref)
	if active.handle == nil {
		return agent.RunState{}, nil
	}
	state := agent.RunState{Status: agent.RunLifecycleStatusRunning, ActiveRunID: active.handle.RunID()}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, waiter := range r.approvalWaiters {
		if waiter.blockingRunID != "" && waiter.blockingRunID == state.ActiveRunID && waiter.owner.Err() == nil {
			state.WaitingApproval = true
			state.Status = agent.RunLifecycleStatusWaitingApproval
			break
		}
	}
	return state, nil
}

// wrapTurnTools is shared by native and externally controlled execution.
func (r *Runtime) wrapTurnTools(ctx context.Context, activeSession session.Session, ref session.SessionRef, state map[string]any, spec agent.AgentSpec, requester approval.Requester, runID, turnID string, sequence *atomic.Uint64) []tool.Tool {
	concrete := spec.Tools
	spec.Tools = r.wrapToolsForDispatch(ref, spec.Tools)
	spec.Tools = r.wrapToolsForPolicy(activeSession, ref, state, spec, approvalContext{
		ctx: ctx, requester: requester, runtime: r, runID: runID, turnID: turnID,
	})
	spec.Tools = wrapToolsForInvocation(ref, turnID, sequence, spec.Tools)
	return ownCodeModeProducers(ctx, concrete, wrapToolResults(concrete, spec.Tools))
}

func cloneAgentSpec(in agent.AgentSpec) agent.AgentSpec {
	out := in
	out.Models = make(map[string]model.LLM, len(in.Models))
	for k, v := range in.Models {
		out.Models[k] = v
	}
	out.Tools = append([]tool.Tool(nil), in.Tools...)
	out.Metadata = session.CloneState(in.Metadata)
	return out
}

func (r *Runtime) runWithOverflowRecovery(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runID string,
	turnID string,
	req agent.RunRequest,
	pendingInputs []*session.Event,
	sink *runner,
) (runErr error) {
	if r.compaction.Enabled && r.compaction.Async.Enabled {
		scope := &asyncCompactScope{ctx: ctx}
		ctx = context.WithValue(ctx, asyncCompactScopeKey{}, scope)
		defer func() { runErr = errors.Join(runErr, scope.drain()) }()
	}
	currentTurnInputs := session.CloneEvents(pendingInputs)
	var toolFactOrdinal uint64
	// Share the durable tool-step counter across overflow retries so a reused
	// provider-local call ID cannot collide with a prior successful execution.
	toolStepSequence := &atomic.Uint64{}
	for {
		inputPersisted, err := r.runAttempt(ctx, activeSession, ref, runID, turnID, req, pendingInputs, sink, &toolFactOrdinal, toolStepSequence)
		if inputPersisted {
			pendingInputs = nil
		}
		if err == nil {
			return nil
		}
		var adopted *asyncCompactAdoptedError
		if errors.As(err, &adopted) {
			r.publishCompactCompletion(sink, adopted.checkpoint)
			continue
		}
		if recovery, ok := compactionRecoveryFromError(err); ok {
			if err := asyncScope(ctx).beforeRecovery(); err != nil {
				if recovery.decision.Release != nil {
					recovery.decision.Release()
				}
				return err
			}
			recoveryCtx := r.compactionRequestContext(ctx)
			progress, compacted, compactErr := r.recoverByCompacting(recoveryCtx, ref, turnID, req, recovery, currentTurnInputs, sink)
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
) (bool, error) {
	invocation, err := r.prepareInvocationContext(ctx, ref, turnID, req, pendingInputs, sink)
	if err != nil {
		var compactErr *compactionFailureError
		if errors.As(err, &compactErr) {
			r.publishCompactFailureNotice(activeSession, turnID, sink, compactErr)
		}
		return false, err
	}

	ctx = context.WithValue(ctx, nestedToolEventSinkKey{}, sink.publishEvent)
	var source *requestsource.Tracker
	if r.compaction.Async.Enabled || r.compaction.Idle.Enabled || len(r.hooks.ModelPrepare) > 0 {
		ctx, source = requestsource.WithTracker(ctx, invocation.SourceEvents)
	}
	var throughSeq, visibleCount uint64
	observeSequence := func(event *session.Event) {
		if !session.IsMainInvocationVisibleEvent(event) {
			return
		}
		visibleCount++
		seq := event.Seq
		if seq == 0 {
			// Match the sequence fallback for legacy, unnumbered events.
			seq = visibleCount
		}
		throughSeq = max(throughSeq, seq)
	}
	// Publish the confirmed checkpoint before appending pending input. The
	// companion notice still follows the user echo.
	if invocation.LiveCompact != nil {
		observeSequence(invocation.LiveCompact)
		sink.publishEvent(invocation.LiveCompact)
	}
	persistedInputs, appendErr := r.appendInputEvents(ctx, ref, pendingInputs)
	if appendErr != nil {
		return false, appendErr
	}
	source.Append(persistedInputs...)
	inputPersisted := len(persistedInputs) > 0
	for _, persisted := range persistedInputs {
		observeSequence(persisted)
		invocation.PromptEvents = append(invocation.PromptEvents, session.CloneEvent(persisted))
		sink.publishEvent(persisted)
	}
	if invocation.LiveCompact != nil {
		r.publishCompactNotice(sink, invocation.LiveCompact)
	}

	activeAgent, err := r.resolveAgent(ctx, activeSession, ref, invocation.State, runID, turnID, req, toolStepSequence)
	if err != nil {
		return inputPersisted, err
	}
	baseCtx := agent.NewContext(agent.ContextSpec{
		Context:          ctx,
		Session:          activeSession,
		Events:           invocation.PromptEvents,
		DrainSubmissions: sink.drainSubmissions,
		InputReady:       sink.inputReadySignal,
	})

	var pendingCalls []*session.Event
	pendingCount := 0
	runCtx := &localInputContext{Context: baseCtx, runner: sink,
		contextSource: func() (hook.ContextSource, error) { return r.contextEditSource(ctx, ref, source.Events()) },
		contextEdit: func(logical, name string, edit hook.ContextEdit, validate func(context.Context) (func(), error)) ([]*session.Event, error) {
			events, err := r.commitContextEdit(ctx, ref, turnID, runID, logical, name, edit, source.Events(), validate)
			if err != nil {
				return nil, err
			}
			source.Append(events...)
			for _, e := range events {
				observeSequence(e)
				sink.publishEvent(e)
			}
			return compact.PromptEventsFromLatestCompact(source.Events()), nil
		}, commit: func(submission agent.Submission) ([]model.Message, error) {
			events, err := buildSteeringInputEvents(activeSession, turnID, submission)
			if err != nil {
				return nil, err
			}
			events, err = r.prepareHookInputs(ctx, events)
			if err != nil {
				return nil, err
			}
			persisted, err := r.appendInputEvents(ctx, ref, events)
			if err != nil {
				return nil, err
			}
			source.Append(persisted...)
			messages := make([]model.Message, 0, len(persisted))
			for _, event := range persisted {
				observeSequence(event)
				sink.publishEvent(event)
				messages = append(messages, *event.Message)
			}
			return messages, nil
		}}
	for event, runErr := range activeAgent.Run(runCtx) {
		if runErr != nil {
			return inputPersisted, runErr
		}
		if event == nil {
			continue
		}
		normalized := normalizeEvent(activeSession, turnID, event)
		if scopeRuntimeToolFactIdentity(normalized, runID, turnID, *toolFactOrdinal+1) {
			(*toolFactOrdinal)++
		}
		group := []*session.Event{normalized}
		batchCommitted := false
		if pendingCount > 0 || normalized.Type == session.EventTypeToolCall && normalized.Message != nil && len(normalized.Message.ToolCalls()) > 1 {
			if pendingCount == 0 {
				pendingCount = len(normalized.Message.ToolCalls())
			}
			if normalized.Type != session.EventTypeToolCall || normalized.Tool == nil {
				return inputPersisted, errors.New("incomplete frozen tool batch")
			}
			pendingCalls = append(pendingCalls, normalized)
			if len(pendingCalls) < pendingCount {
				continue
			}
			group, err = r.appendToolCallBatch(ctx, ref, pendingCalls)
			if err != nil {
				return inputPersisted, err
			}
			pendingCalls = nil
			pendingCount = 0
			batchCommitted = true
		}
		for _, normalized := range group {
			if !batchCommitted && runtimeAgentEventShouldPersist(normalized) {
				normalized, err = r.appendRuntimeEventOrLifecycle(ctx, activeSession, ref, turnID, normalized)
				if err != nil {
					return inputPersisted, err
				}
				if session.IsCanonicalHistoryEvent(normalized) {
					_ = r.tasks.syncCanonicalToolResult(ctx, ref, normalized)
				}
			}
			source.Append(normalized)
			observeSequence(normalized)
			sink.publishEvent(normalized)
			if planEvent, handled, planErr := r.handlePlanEvent(ctx, ref, turnID, normalized); planErr != nil {
				return inputPersisted, planErr
			} else if handled {
				observeSequence(planEvent)
				sink.publishEvent(planEvent)
			}
		}
	}
	if pendingCount > 0 {
		return inputPersisted, errors.New("incomplete frozen tool batch")
	}
	if err := ctx.Err(); err != nil {
		return inputPersisted, err
	}
	r.advanceCompactionRequest(ref, throughSeq)
	return inputPersisted, nil
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
	if session.IsCanonicalHistoryEvent(event) && event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == mcp.SearchToolName {
		// Native discovery receipts share the binding-claim write order with
		// nested discovery, accepted ToolCalls and dispatch validation.
		release, err := r.acquireSessionWrite(ctx, ref)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	if session.IsModelInvocationReceipt(event) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return r.sessions.AppendEvent(cleanup, session.AppendEventRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Event: event})
	}
	appendCtx := ctx
	if event.Type == session.EventTypeToolResult {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), approvalResolutionRecoveryTimeout)
		defer cancel()
		appendCtx = cleanup
	}
	persisted, err := r.sessions.AppendEvent(appendCtx, session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.RuntimeMutationGuard(ctx),
		Event:         event,
	})
	if err == nil {
		return persisted, nil
	}
	if event.Type == session.EventTypeToolCall || event.Type == session.EventTypeToolResult || event.Type == session.EventTypeContext && strings.HasPrefix(event.ID, "hook-feedback:") {
		confirmed, readErr := r.reconcileEventAppend(ctx, ref, event)
		if confirmed != nil {
			return confirmed, nil
		}
		return nil, errors.Join(err, readErr)
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
