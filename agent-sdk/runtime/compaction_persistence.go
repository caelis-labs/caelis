package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) persistCompactionArtifacts(ctx context.Context, active *session.Session, source session.LoadedSession, turnID string, result compact.Result) (*session.Event, error) {
	return r.persistCompactionArtifactsValidated(ctx, active, source, turnID, result, func(current session.LoadedSession) bool {
		return reflect.DeepEqual(mainInvocationEvents(source.Events), mainInvocationEvents(current.Events)) &&
			reflect.DeepEqual(source.State, current.State)
	})
}

// Synchronous, asynchronous and idle compaction commit through this sole owner.
// The caller supplies the source validation policy and holds the write queue.
func (r *Runtime) persistCompactionArtifactsValidated(ctx context.Context, active *session.Session, source session.LoadedSession, turnID string, result compact.Result, valid func(session.LoadedSession) bool) (*session.Event, error) {
	return r.persistCompactionArtifactsUsing(ctx, active, source, turnID, result, valid, nil)
}

type checkpointAppender func(context.Context, session.AppendEventRequest) (*session.Event, error)

func (r *Runtime) persistCompactionArtifactsUsing(ctx context.Context, active *session.Session, source session.LoadedSession, turnID string, result compact.Result, valid func(session.LoadedSession) bool, appendCheckpoint checkpointAppender) (*session.Event, error) {
	if appendCheckpoint == nil {
		appendCheckpoint = r.sessions.AppendEvent
	}

	if result.CompactEvent == nil {
		return nil, errors.New("agent-sdk/runtime: compact event is required")
	}
	if validate, ok := ctx.Value(checkpointValidationKey{}).(checkpointValidator); ok && validate != nil {
		release, err := validate(ctx)
		if err != nil {
			return nil, err
		}
		if release == nil {
			return nil, errors.New("runtime: checkpoint validation requires a release guard")
		}
		defer release()
	}
	event := normalizeEvent(*active, turnID, result.CompactEvent)
	if event.ID == "" {
		event.ID = r.nextID("compact", nil)
	}
	if strings.TrimSpace(event.IdempotencyKey) == "" {
		if data, ok := compact.CompactEventDataFromEvent(event); ok && data.SummarizedThroughSeq > 0 {
			event.IdempotencyKey = fmt.Sprintf("compact:%d:%s:%s", data.SummarizedThroughSeq, data.Generator, data.Trigger)
		}
	}
	var err error
	for range 8 {
		r.checkpointExposure(event, source.Events, turnID != "" || r.activeRunForSession(active.SessionRef).handle == nil)
		var persisted *session.Event
		persisted, err = appendCheckpoint(ctx, session.AppendEventRequest{
			SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
			MutationGuard: session.RuntimeMutationGuard(ctx), Event: event,
		})
		if err == nil {
			r.clearCompactionRequest(active.SessionRef)
			return persisted, nil
		}
		if !errors.Is(err, session.ErrRevisionConflict) {
			persisted, readErr := r.reconcileCompactionAppend(ctx, active.SessionRef, event)
			if readErr == nil && persisted != nil {
				r.clearCompactionRequest(active.SessionRef)
				return persisted, nil
			}
			return nil, errors.Join(err, readErr)
		}
		current, loadErr := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
		if loadErr != nil {
			return nil, loadErr
		}
		// The write queue is Runtime-local. Approval accounting and asynchronous
		// Task journals can advance the Store while the model compacts. Rebase
		// only an unchanged context and state; never bless a stale checkpoint by
		// merely reading the latest revision, or drop the original fence.
		if !valid(current) {
			return nil, err
		}
		*active = current.Session
		source = current
	}
	return nil, err
}

// A failed write may already have committed. Read back the original ID/key and
// compare through the Store's full-payload idempotency contract. Absence is not
// permission to replay with a new identity. This function never writes.
func (r *Runtime) reconcileCompactionAppend(ctx context.Context, ref session.SessionRef, want *session.Event) (*session.Event, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	loaded, err := r.sessions.LoadSession(cleanup, session.LoadSessionRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return nil, err
	}
	for _, event := range loaded.Events {
		if event.ID != want.ID && (want.IdempotencyKey == "" || event.IdempotencyKey != want.IdempotencyKey) {
			continue
		}
		prepared, err := session.PrepareEventsForAppend(session.PrepareEventsForAppendRequest{
			SessionID: ref.SessionID, Events: []*session.Event{want}, ExistingEvents: []*session.Event{event}, Now: r.now(),
		})
		if err != nil {
			return nil, err
		}
		if len(prepared.Persisted) == 0 && len(prepared.Events) == 1 {
			return prepared.Events[0], nil
		}
	}
	return nil, nil
}
