package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
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
	for id, waiter := range r.approvalWaiters {
		if waiter.blockingRunID != "" && waiter.blockingRunID == state.ActiveRunID && waiter.owner.Err() == nil {
			state.WaitingApproval = true
			state.ApprovalRequestID = id
			state.Status = agent.RunLifecycleStatusWaitingApproval
		}
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
	concrete := spec.Tools
	spec.Tools = r.wrapToolsForDispatch(ref, spec.Tools)
	spec.Tools = r.wrapToolsForPolicy(activeSession, ref, state, spec, approvalContext{
		ctx: ctx, requester: requester, runtime: r, session: session.CloneSession(activeSession), sessionRef: session.NormalizeSessionRef(ref), runID: runID, turnID: turnID,
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
	batch *[]*session.Event,
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
		attemptBatch, _, inputPersisted, err := r.runAttempt(ctx, activeSession, ref, runID, turnID, req, pendingInputs, sink, &toolFactOrdinal, toolStepSequence)
		if inputPersisted {
			pendingInputs = nil
		}
		if err == nil {
			*batch = append(*batch, attemptBatch...)
			return nil
		}
		var adopted *asyncCompactAdoptedError
		if errors.As(err, &adopted) {
			*batch = append(*batch, attemptBatch...)
			if sink != nil {
				sink.publishEvent(normalizeEvent(activeSession, turnID, buildCompactNoticeEvent(activeSession, turnID, r.now())))
			}
			continue
		}
		if recovery, ok := compactionRecoveryFromError(err); ok {
			*batch = append(*batch, attemptBatch...)
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

	ctx = mcpdiscovery.WithEvents(ctx, invocation.SourceEvents)
	if sink != nil {
		ctx = context.WithValue(ctx, nestedToolEventSinkKey{}, sink.publishEvent)
	}
	var source *requestsource.Tracker
	if r.compaction.Async.Enabled || r.compaction.Idle.Enabled || len(r.hooks.ModelPrepare) > 0 {
		ctx, source = requestsource.WithTracker(ctx, invocation.SourceEvents)
	}
	batch := make([]*session.Event, 0, 3)
	persistedInputs, appendErr := r.appendInputEvents(ctx, ref, pendingInputs)
	if appendErr != nil {
		return nil, false, false, appendErr
	}
	source.Append(persistedInputs...)
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
		DrainSubmissions: drainSubmissions,
		InputReady:       inputReady,
	})

	emitted := false
	var pendingCalls []*session.Event
	pendingCount := 0
	if sink != nil {
		runCtx = &localInputContext{Context: runCtx, runner: sink,
			contextSource: func() (agent.ContextSource, error) { return r.contextEditSource(ctx, ref, source.Events()) },
			contextEdit: func(logical, name string, edit agent.ContextEdit, validate func(context.Context) (func(), error)) ([]*session.Event, error) {
				events, err := r.commitContextEdit(ctx, ref, turnID, runID, logical, name, edit, source.Events(), validate)
				if err != nil {
					return nil, err
				}
				source.Append(events...)
				for _, e := range events {
					batch = append(batch, e)
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
		group := []*session.Event{normalized}
		batchCommitted := false
		if pendingCount > 0 || normalized.Type == session.EventTypeToolCall && normalized.Message != nil && len(normalized.Message.ToolCalls()) > 1 {
			if pendingCount == 0 {
				pendingCount = len(normalized.Message.ToolCalls())
			}
			if normalized.Type != session.EventTypeToolCall || normalized.Tool == nil {
				return batch, emitted, inputPersisted, errors.New("incomplete frozen tool batch")
			}
			pendingCalls = append(pendingCalls, normalized)
			if len(pendingCalls) < pendingCount {
				continue
			}
			group, err = r.appendToolCallBatch(ctx, ref, pendingCalls)
			if err != nil {
				return batch, emitted, inputPersisted, err
			}
			pendingCalls = nil
			pendingCount = 0
			batchCommitted = true
		}
		for _, normalized := range group {
			if !batchCommitted && runtimeAgentEventShouldPersist(normalized) {
				normalized, err = r.appendRuntimeEventOrLifecycle(ctx, activeSession, ref, turnID, normalized)
				if err != nil {
					return batch, emitted, inputPersisted, err
				}
				if session.IsCanonicalHistoryEvent(normalized) {
					_ = r.tasks.syncCanonicalToolResult(ctx, ref, normalized)
				}
			}
			source.Append(normalized)
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
	}
	if pendingCount > 0 {
		return batch, emitted, inputPersisted, errors.New("incomplete frozen tool batch")
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
		confirmed, readErr := r.reconcileCompactionAppend(ctx, ref, event)
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
