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
	if result.CompactEvent == nil {
		return nil, errors.New("agent-sdk/runtime: compact event is required")
	}
	event := normalizeEvent(*active, turnID, result.CompactEvent)
	if strings.TrimSpace(event.IdempotencyKey) == "" {
		if data, ok := compact.CompactEventDataFromEvent(event); ok && data.SummarizedThroughSeq > 0 {
			event.IdempotencyKey = fmt.Sprintf("compact:%d:%s:%s", data.SummarizedThroughSeq, data.Generator, data.Trigger)
		}
	}
	var err error
	for range 8 {
		var persisted *session.Event
		persisted, err = r.sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
			MutationGuard: session.RuntimeMutationGuard(ctx), Event: event,
		})
		if err == nil {
			r.clearCompactionRequest(active.SessionRef)
			return persisted, nil
		}
		if !errors.Is(err, session.ErrRevisionConflict) {
			return nil, err
		}
		current, loadErr := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
		if loadErr != nil {
			return nil, loadErr
		}
		// The write queue is Runtime-local. Approval accounting and asynchronous
		// Task journals can advance the Store while the model compacts. Rebase
		// only an unchanged context and state; never bless a stale checkpoint by
		// merely reading the latest revision, or drop the original fence.
		if !reflect.DeepEqual(mainInvocationEvents(source.Events), mainInvocationEvents(current.Events)) ||
			!reflect.DeepEqual(source.State, current.State) {
			return nil, err
		}
		*active = current.Session
	}
	return nil, err
}
