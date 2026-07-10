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
	req agent.RunRequest,
	pendingInput *session.Event,
) (invocationContext, error) {
	if err := r.recoverRuntimeState(ctx, ref); err != nil {
		return invocationContext{}, err
	}
	loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		return invocationContext{}, err
	}
	events := mainInvocationEvents(loaded.Events)
	state := loaded.State
	if state == nil {
		state = map[string]any{}
	}
	result, err := r.compactor.Prepare(ctx, compact.Request{
		Session:       loaded.Session,
		SessionRef:    ref,
		Events:        events,
		PendingEvents: pendingEventsForCompaction(pendingInput),
		Model:         req.AgentSpec.Model,
	})
	if err != nil {
		return invocationContext{}, wrapCompactionFailure("prepare", err)
	}
	if result.Compacted && result.CompactEvent != nil {
		persisted, appendErr := r.persistCompactionArtifacts(ctx, loaded.Session, ref, loaded.Session.Revision, result)
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
	Model      model.LLM
	Trigger    string
}

type CompactResult struct {
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
	loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		return CompactResult{}, err
	}
	activeSession := loaded.Session
	events := loaded.Events
	forceCompactor, ok := r.compactor.(compact.ForceEngine)
	if !ok {
		return CompactResult{}, errors.New("agent-sdk/runtime: compactor does not support forced compaction")
	}
	result, err := forceCompactor.Force(ctx, compact.Request{
		Session:    activeSession,
		SessionRef: ref,
		Events:     events,
		Model:      req.Model,
	}, req.Trigger)
	if err != nil {
		return CompactResult{}, err
	}
	out := CompactResult{
		Session:   activeSession,
		Compacted: result.Compacted,
		Usage:     result.Usage,
	}
	if result.Compacted && result.CompactEvent != nil {
		persisted, appendErr := r.persistCompactionArtifacts(ctx, activeSession, ref, loaded.Session.Revision, result)
		if appendErr != nil {
			return CompactResult{}, appendErr
		}
		out.Event = persisted
	}
	return out, nil
}

func (r *Runtime) updateCompactionUsageFromBatch(_ context.Context, _ session.SessionRef, _ []*session.Event) error {
	return nil
}

func (r *Runtime) persistCompactionArtifacts(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	sourceRevision uint64,
	result compact.Result,
) (*session.Event, error) {
	if result.CompactEvent == nil {
		return nil, errors.New("agent-sdk/runtime: compact event is required")
	}
	compactEvent := normalizeEvent(activeSession, "", result.CompactEvent)
	if strings.TrimSpace(compactEvent.IdempotencyKey) == "" {
		if data, ok := compact.CompactEventDataFromEvent(compactEvent); ok && data.SummarizedThroughSeq > 0 {
			compactEvent.IdempotencyKey = fmt.Sprintf("compact:%d:%s:%s", data.SummarizedThroughSeq, data.Generator, data.Trigger)
		}
	}
	persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:       ref,
		ExpectedRevision: &sourceRevision,
		Event:            compactEvent,
	})
	if err != nil {
		return nil, err
	}
	return persisted, nil
}

func (r *Runtime) compactAfterOverflow(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	turnID string,
	req agent.RunRequest,
	cause error,
	sink *runner,
) (compactionProgress, bool, error) {
	return r.compactAndNotify(ctx, activeSession, ref, turnID, nil, nil, sink, func(events []*session.Event) (compact.Result, error) {
		return r.compactor.CompactOnOverflow(ctx, compact.Request{
			Session:    activeSession,
			SessionRef: ref,
			Events:     events,
			Model:      req.AgentSpec.Model,
		}, cause)
	})
}

func (r *Runtime) compactAfterModelRequestWatermark(
	ctx context.Context,
	activeSession session.Session,
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
	var events []*session.Event
	sourceRevision := decision.SourceRevision
	if decision.Events != nil {
		events = decision.Events
	}
	return r.compactAndNotify(ctx, activeSession, ref, turnID, events, &sourceRevision, sink, func(events []*session.Event) (compact.Result, error) {
		return forceCompactor.Force(ctx, compact.Request{
			Session:    activeSession,
			SessionRef: ref,
			Events:     events,
			Model:      decision.Model,
		}, trigger)
	})
}

func (r *Runtime) compactAndNotify(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	turnID string,
	events []*session.Event,
	sourceRevision *uint64,
	sink *runner,
	compactFn func([]*session.Event) (compact.Result, error),
) (compactionProgress, bool, error) {
	if compactFn == nil {
		return compactionProgress{}, false, errors.New("agent-sdk/runtime: compact function is required")
	}
	var err error
	if events == nil || sourceRevision == nil {
		loaded, loadErr := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
		err = loadErr
		if err != nil {
			return compactionProgress{}, false, err
		}
		events = loaded.Events
		revision := loaded.Session.Revision
		sourceRevision = &revision
	}
	var result compact.Result
	err = r.executeLifecycle(ctx, r.lifecycleEvent(ctx, agent.LifecycleCompact, "", ""), func(context.Context) error {
		var compactErr error
		result, compactErr = compactFn(events)
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
	persisted, err := r.persistCompactionArtifacts(ctx, activeSession, ref, *sourceRevision, result)
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
