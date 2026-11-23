package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) persistCompactionArtifacts(ctx context.Context, active *session.Session, source session.LoadedSession, turnID string, result compact.Result) (*session.Event, error) {
	return r.persistContextArtifactUsing(ctx, active, turnID, result.CompactEvent, func(current session.LoadedSession) bool {
		return reflect.DeepEqual(mainInvocationEvents(source.Events), mainInvocationEvents(current.Events)) &&
			reflect.DeepEqual(source.State, current.State)
	}, nil)
}

type checkpointAppender func(context.Context, session.AppendEventRequest) (*session.Event, error)

// Synchronous, asynchronous and idle compaction commit through this sole owner.
func (r *Runtime) persistContextArtifactUsing(ctx context.Context, active *session.Session, turnID string, artifact *session.Event, valid func(session.LoadedSession) bool, appendCheckpoint checkpointAppender) (*session.Event, error) {
	if appendCheckpoint == nil {
		appendCheckpoint = r.sessions.AppendEvent
	}

	if artifact == nil {
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
	event := normalizeEvent(*active, turnID, artifact)
	if event.ID == "" {
		event.ID = r.nextID("compact", nil)
	}
	if strings.TrimSpace(event.IdempotencyKey) == "" {
		if data, ok := session.CheckpointDataFromEvent(event); ok && data.SummarizedThroughSeq > 0 {
			event.IdempotencyKey = fmt.Sprintf("compact:%d:%s:%s", data.SummarizedThroughSeq, data.Generator, data.Trigger)
		}
	}
	var err error
	for range 8 {
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
			persisted, readErr := r.reconcileEventAppend(ctx, active.SessionRef, event)
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
	}
	return nil, err
}
