package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) prepareInvocationContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	turnID string,
	req agent.RunRequest,
	pendingInput *session.Event,
	sink *runner,
) (invocationContext, error) {
	if err := r.recoverRuntimeState(ctx, ref); err != nil {
		return invocationContext{}, err
	}
	release, err := r.acquireSessionWrite(ctx, ref)
	if err != nil {
		return invocationContext{}, err
	}
	defer release()
	loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		return invocationContext{}, err
	}
	events := mainInvocationEvents(loaded.Events)
	state := loaded.State
	if state == nil {
		state = map[string]any{}
	}
	appendix, err := r.runtimeCompactionAppendix(ctx, ref, state)
	if err != nil {
		return invocationContext{}, err
	}
	compactCtx := r.withCompactActivity(ctx, loaded.Session, turnID, sink)
	result, err := r.compactor.Prepare(compactCtx, compact.Request{
		Session:          loaded.Session,
		SessionRef:       ref,
		Events:           events,
		PendingEvents:    pendingEventsForCompaction(pendingInput),
		Model:            req.AgentSpec.Model,
		InContextRequest: r.inContextRequest(ref, req.AgentSpec.Model, events),
		RuntimeAppendix:  appendix,
	})
	if err != nil {
		return invocationContext{}, wrapCompactionFailure("prepare", err)
	}
	if result.Compacted && result.CompactEvent != nil {
		persisted, appendErr := r.persistCompactionArtifacts(ctx, loaded.Session, ref, turnID, loaded.Session.Revision, result)
		if appendErr != nil {
			return invocationContext{}, wrapCompactionFailure("persist", appendErr)
		}
		sourceEvents := append(session.CloneEvents(events), persisted)
		return invocationContext{
			PromptEvents: promptEventsWithToolVisibilityMetadata(compact.PromptEventsFromLatestCompact(sourceEvents), sourceEvents),
			State:        state,
			LiveCompact:  persisted,
		}, nil
	}
	return invocationContext{
		PromptEvents: promptEventsWithToolVisibilityMetadata(result.PromptEvents, events),
		State:        state,
	}, nil
}

type invocationContext struct {
	PromptEvents []*session.Event
	State        map[string]any
	LiveCompact  *session.Event
}

type CompactRequest struct {
	SessionRef session.SessionRef
	// ExpectedRevision is checked after this compaction is admitted to the
	// Runtime's Session write queue. Nil accepts the latest admitted revision.
	ExpectedRevision *uint64
	Model            model.LLM
	Trigger          string
}

type CompactResult struct {
	// Session is the admitted Session snapshot, advanced to the committed
	// revision when Compacted is true.
	Session   session.Session
	Compacted bool
	Event     *session.Event
	Usage     compact.UsageSnapshot
}

func (r *Runtime) Compact(ctx context.Context, req CompactRequest) (CompactResult, error) {
	if r == nil {
		return CompactResult{}, errors.New("agent-sdk/runtime: runtime is unavailable")
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	if err := r.recoverRuntimeState(ctx, ref); err != nil {
		return CompactResult{}, err
	}
	release, err := r.acquireSessionWrite(ctx, ref)
	if err != nil {
		return CompactResult{}, err
	}
	defer release()
	loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		return CompactResult{}, err
	}
	activeSession := loaded.Session
	if err := session.CheckExpectedRevision(activeSession, req.ExpectedRevision); err != nil {
		return CompactResult{Session: activeSession}, err
	}
	events := mainInvocationEvents(loaded.Events)
	appendix, err := r.runtimeCompactionAppendix(ctx, ref, loaded.State)
	if err != nil {
		return CompactResult{}, err
	}
	forceCompactor, ok := r.compactor.(compact.ForceEngine)
	if !ok {
		return CompactResult{}, errors.New("agent-sdk/runtime: compactor does not support forced compaction")
	}
	scope := lifecycleScopeFromContext(ctx)
	scope.sessionRef = ref
	compactCtx := withLifecycleScope(ctx, scope)
	var result compact.Result
	var persisted *session.Event
	err = r.executeLifecycle(compactCtx, r.lifecycleEvent(compactCtx, agent.LifecycleCompact, "", ""), func(callCtx context.Context) error {
		var compactErr error
		result, compactErr = forceCompactor.Force(callCtx, compact.Request{
			Session:          activeSession,
			SessionRef:       ref,
			Events:           events,
			Model:            req.Model,
			InContextRequest: r.inContextRequest(ref, req.Model, events),
			RuntimeAppendix:  appendix,
		}, req.Trigger)
		if compactErr != nil || !result.Compacted {
			return compactErr
		}
		if result.CompactEvent == nil {
			return errors.New("agent-sdk/runtime: compact event is required")
		}
		persisted, compactErr = r.persistCompactionArtifacts(
			callCtx,
			activeSession,
			ref,
			"",
			loaded.Session.Revision,
			result,
		)
		return compactErr
	})
	if err != nil {
		return CompactResult{Session: activeSession}, err
	}
	out := CompactResult{
		Session:   activeSession,
		Compacted: persisted != nil,
		Usage:     result.Usage,
	}
	if persisted != nil {
		out.Event = persisted
		out.Session.Revision = activeSession.Revision + 1
		if !persisted.Time.IsZero() {
			out.Session.UpdatedAt = persisted.Time
		}
	}
	return out, nil
}

func (r *Runtime) updateCompactionUsageFromBatch(_ context.Context, ref session.SessionRef, events []*session.Event) error {
	r.advanceCompactionRequest(ref, session.LastEventSeq(mainInvocationEvents(events)))
	return nil
}

func (r *Runtime) persistCompactionArtifacts(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	turnID string,
	admittedRevision uint64,
	result compact.Result,
) (*session.Event, error) {
	if result.CompactEvent == nil {
		return nil, errors.New("agent-sdk/runtime: compact event is required")
	}
	compactEvent := normalizeEvent(activeSession, turnID, result.CompactEvent)
	if strings.TrimSpace(compactEvent.IdempotencyKey) == "" {
		if data, ok := compact.CompactEventDataFromEvent(compactEvent); ok && data.SummarizedThroughSeq > 0 {
			compactEvent.IdempotencyKey = fmt.Sprintf("compact:%d:%s:%s", data.SummarizedThroughSeq, data.Generator, data.Trigger)
		}
	}
	persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:       ref,
		ExpectedRevision: &admittedRevision,
		MutationGuard:    session.RuntimeMutationGuard(ctx),
		Event:            compactEvent,
	})
	if err != nil {
		return nil, err
	}
	r.clearCompactionRequest(ref)
	return persisted, nil
}

func (r *Runtime) compactAfterOverflow(
	ctx context.Context,
	ref session.SessionRef,
	turnID string,
	req agent.RunRequest,
	currentTurnInput *session.Event,
	cause error,
	sink *runner,
) (compactionProgress, bool, error) {
	return r.compactAndNotify(ctx, ref, turnID, sink, func(compactCtx context.Context, current session.Session, events []*session.Event, state map[string]any) (compact.Result, error) {
		sourceEvents, pendingEvents := overflowCompactionEvents(events, currentTurnInput)
		appendix, err := r.runtimeCompactionAppendix(compactCtx, ref, state)
		if err != nil {
			return compact.Result{}, err
		}
		var inContext *model.Request
		if len(pendingEvents) == 0 {
			inContext = r.inContextRequest(ref, req.AgentSpec.Model, sourceEvents)
		}
		return r.compactor.CompactOnOverflow(compactCtx, compact.Request{
			Session:          current,
			SessionRef:       ref,
			Events:           sourceEvents,
			PendingEvents:    pendingEvents,
			Model:            req.AgentSpec.Model,
			InContextRequest: inContext,
			RuntimeAppendix:  appendix,
		}, cause)
	})
}

func overflowCompactionEvents(events []*session.Event, currentTurnInput *session.Event) ([]*session.Event, []*session.Event) {
	if currentTurnInput == nil {
		return session.CloneEvents(events), nil
	}
	wantKey := strings.TrimSpace(currentTurnInput.IdempotencyKey)
	wantID := strings.TrimSpace(currentTurnInput.ID)
	for index, event := range events {
		if event == nil {
			continue
		}
		matches := wantKey != "" && strings.TrimSpace(event.IdempotencyKey) == wantKey
		if !matches && wantID != "" {
			matches = strings.TrimSpace(event.ID) == wantID
		}
		if !matches {
			continue
		}
		for _, later := range events[index+1:] {
			if session.EventTypeOf(later) == session.EventTypeToolResult {
				// A post-tool overflow must checkpoint the complete durable Turn,
				// including the accepted Tool call and result. Keeping only the
				// User input would lose progress and invite a repeated side effect.
				return session.CloneEvents(events), nil
			}
		}
		// Without a completed Tool effect, preserve the current input exactly.
		// This is required for approval and other system-managed prompts that
		// must not be replaced by a model-authored summary.
		return session.CloneEvents(events[:index]), []*session.Event{session.CloneEvent(event)}
	}
	return session.CloneEvents(events), []*session.Event{session.CloneEvent(currentTurnInput)}
}

func (r *Runtime) compactAfterModelRequestWatermark(
	ctx context.Context,
	ref session.SessionRef,
	turnID string,
	decision autoCompactDecision,
	sink *runner,
) (compactionProgress, bool, error) {
	forceCompactor, ok := r.compactor.(compact.ForceEngine)
	if !ok {
		return compactionProgress{}, false, errors.New("agent-sdk/runtime: compactor does not support forced model-request compaction")
	}
	trigger := strings.TrimSpace(decision.Reason)
	if trigger == "" {
		trigger = "model_request_context_watermark"
	}
	return r.compactAndNotify(ctx, ref, turnID, sink, func(compactCtx context.Context, current session.Session, events []*session.Event, state map[string]any) (compact.Result, error) {
		appendix, err := r.runtimeCompactionAppendix(compactCtx, ref, state)
		if err != nil {
			return compact.Result{}, err
		}
		return forceCompactor.Force(compactCtx, compact.Request{
			Session:          current,
			SessionRef:       ref,
			Events:           events,
			Model:            decision.Model,
			InContextRequest: model.CloneRequest(decision.Request),
			RuntimeAppendix:  appendix,
		}, trigger)
	})
}

func (r *Runtime) compactAndNotify(
	ctx context.Context,
	ref session.SessionRef,
	turnID string,
	sink *runner,
	compactFn func(context.Context, session.Session, []*session.Event, map[string]any) (compact.Result, error),
) (compactionProgress, bool, error) {
	if compactFn == nil {
		return compactionProgress{}, false, errors.New("agent-sdk/runtime: compact function is required")
	}
	release, err := r.acquireSessionWrite(ctx, ref)
	if err != nil {
		return compactionProgress{}, false, err
	}
	defer release()
	loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		return compactionProgress{}, false, err
	}
	activeSession := loaded.Session
	events := loaded.Events
	var result compact.Result
	compactCtx := r.withCompactActivity(ctx, activeSession, turnID, sink)
	err = r.executeLifecycle(compactCtx, r.lifecycleEvent(compactCtx, agent.LifecycleCompact, "", ""), func(callCtx context.Context) error {
		var compactErr error
		result, compactErr = compactFn(callCtx, activeSession, events, loaded.State)
		return compactErr
	})
	if err != nil {
		r.publishCompactFailureNotice(activeSession, turnID, sink, err)
		return compactionProgress{}, false, err
	}
	if !result.Compacted || result.CompactEvent == nil {
		return compactionProgress{}, false, nil
	}
	if progress := compactionProgressFromEvent(result.CompactEvent); progress.hasCompactData && !progress.hasSourceProgress() {
		return compactionProgress{}, true, nil
	}
	persisted, err := r.persistCompactionArtifacts(ctx, activeSession, ref, turnID, activeSession.Revision, result)
	if err != nil {
		r.publishCompactFailureNotice(activeSession, turnID, sink, err)
		return compactionProgress{}, false, err
	}
	if sink != nil {
		notice := buildCompactNoticeEvent(activeSession, turnID, r.now())
		sink.publishEvent(normalizeEvent(activeSession, turnID, notice))
	}
	return compactionProgressFromEvent(persisted), true, nil
}

type compactionProgress struct {
	eventID             string
	hasCompactData      bool
	sourceEventCount    int
	summarizedThroughID string
}

func compactionProgressFromEvent(event *session.Event) compactionProgress {
	if event == nil {
		return compactionProgress{}
	}
	progress := compactionProgress{
		eventID: strings.TrimSpace(event.ID),
	}
	if data, ok := compact.CompactEventDataFromEvent(event); ok {
		progress.hasCompactData = true
		progress.sourceEventCount = data.SourceEventCount
		progress.summarizedThroughID = strings.TrimSpace(data.SummarizedThroughID)
	}
	return progress
}

func (p compactionProgress) madeDurableProgress() bool {
	if strings.TrimSpace(p.eventID) == "" {
		return false
	}
	if !p.hasCompactData {
		return true
	}
	return p.hasSourceProgress()
}

func (p compactionProgress) hasSourceProgress() bool {
	return p.sourceEventCount > 0 || p.summarizedThroughID != ""
}

func (r *Runtime) publishCompactFailureNotice(activeSession session.Session, turnID string, sink *runner, cause error) {
	if sink == nil || cause == nil {
		return
	}
	notice := buildCompactFailureNoticeEvent(activeSession, turnID, r.now(), cause)
	sink.publishEvent(normalizeEvent(activeSession, turnID, notice))
}
