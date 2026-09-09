package runtime

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// appendInputEvents commits one admission before any of its events are published.
// A multi-message admission requires atomic storage; partial input is not a
// recoverable lifecycle record and must never fall back to individual appends.
func (r *Runtime) appendInputEvents(ctx context.Context, ref session.SessionRef, events []*session.Event) ([]*session.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	guard := session.RuntimeMutationGuard(ctx)
	if len(events) == 1 {
		event, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, MutationGuard: guard, Event: events[0]})
		if err != nil {
			return nil, err
		}
		return []*session.Event{event}, nil
	}
	batch, ok := r.sessions.(session.EventBatchService)
	if !ok {
		return nil, errorcode.New(errorcode.Unsupported, "agent-sdk/runtime: batched input requires atomic Session append")
	}
	return batch.AppendEvents(ctx, session.AppendEventsRequest{SessionRef: ref, MutationGuard: guard, Events: events})
}
