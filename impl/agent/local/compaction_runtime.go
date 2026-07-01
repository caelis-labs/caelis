package local

import (
	"context"
	"errors"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
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
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return invocationContext{}, err
	}
	events = mainInvocationEvents(events)
	state, err := r.sessions.SnapshotState(ctx, ref)
	if err != nil {
		return invocationContext{}, err
	}
	if state == nil {
		state = map[string]any{}
	}
	result, err := r.compactor.Prepare(ctx, compact.Request{
		Session:       activeSession,
		SessionRef:    ref,
		Events:        events,
		PendingEvents: pendingEventsForCompaction(pendingInput),
		Model:         req.AgentSpec.Model,
	})
	if err != nil {
		return invocationContext{}, err
	}
	if result.Compacted && result.CompactEvent != nil {
		persisted, appendErr := r.persistCompactionArtifacts(ctx, activeSession, ref, result)
		if appendErr != nil {
			return invocationContext{}, appendErr
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
		return CompactResult{}, errors.New("impl/agent/local: runtime is unavailable")
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return CompactResult{}, err
	}
	if err := r.recoverRuntimeState(ctx, ref); err != nil {
		return CompactResult{}, err
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return CompactResult{}, err
	}
	forceCompactor, ok := r.compactor.(compact.ForceEngine)
	if !ok {
		return CompactResult{}, errors.New("impl/agent/local: compactor does not support forced compaction")
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
		persisted, appendErr := r.persistCompactionArtifacts(ctx, activeSession, ref, result)
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
	result compact.Result,
) (*session.Event, error) {
	if result.CompactEvent == nil {
		return nil, errors.New("impl/agent/local: compact event is required")
	}
	persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event:      normalizeEvent(activeSession, "", result.CompactEvent),
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
) (bool, error) {
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return false, err
	}
	result, err := r.compactor.CompactOnOverflow(ctx, compact.Request{
		Session:    activeSession,
		SessionRef: ref,
		Events:     events,
		Model:      req.AgentSpec.Model,
	}, cause)
	if err != nil {
		return false, err
	}
	if !result.Compacted || result.CompactEvent == nil {
		return false, nil
	}
	_, err = r.persistCompactionArtifacts(ctx, activeSession, ref, result)
	if err != nil {
		return false, err
	}
	if sink != nil {
		notice := buildCompactNoticeEvent(activeSession, turnID, r.now())
		sink.publishEvent(normalizeEvent(activeSession, turnID, notice))
	}
	return true, nil
}
