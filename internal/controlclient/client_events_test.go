package controlclient

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestClientEventsNeverCrossesCapturedBoundaryDuringLiveSplice(t *testing.T) {
	const iterations = 64
	for iteration := range iterations {
		reader := &backfillSplicePageReader{
			events:  []*session.Event{durableProtocolEvent(1, "captured")},
			blocked: make(chan struct{}),
			release: make(chan struct{}),
		}
		broker, codec := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 8})
		if err := broker.Prime(context.Background()); err != nil {
			t.Fatalf("iteration %d: Prime() error = %v", iteration, err)
		}
		client := &Client{config: ClientConfig{
			Feeds:      singleSessionFeedRegistry{feed: broker},
			Authorizer: eventBatchAllowAuthorizer{},
		}}
		type eventsResult struct {
			batch controlport.EventBatch
			err   error
		}
		result := make(chan eventsResult, 1)
		go func() {
			batch, err := client.Events(context.Background(), controlport.Principal{ID: "owner"}, controlport.SubscribeRequest{SessionID: "session-1"})
			result <- eventsResult{batch: batch, err: err}
		}()

		select {
		case <-reader.blocked:
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: Subscribe did not reach durable backfill", iteration)
		}
		if err := broker.Publish(terminalEnvelope(fmt.Sprintf("live-%d", iteration))); err != nil {
			t.Fatalf("iteration %d: live Publish() error = %v", iteration, err)
		}
		_, liveCursor := broker.Boundary()
		close(reader.release)

		var got eventsResult
		select {
		case got = <-result:
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: finite Events did not return", iteration)
		}
		if got.err != nil {
			t.Fatalf("iteration %d: Events() error = %v", iteration, got.err)
		}
		if got.batch.BoundaryCursor == "" || got.batch.BoundaryCursor == liveCursor {
			t.Fatalf("iteration %d: captured boundary = %q, live cursor = %q", iteration, got.batch.BoundaryCursor, liveCursor)
		}
		if len(got.batch.Events) != 1 || got.batch.Events[0].EventID != "event-1" || got.batch.Events[0].Cursor != got.batch.BoundaryCursor {
			t.Fatalf("iteration %d: finite batch = %#v", iteration, got.batch)
		}
		boundary, err := codec.Decode("session-1", got.batch.BoundaryCursor)
		if err != nil {
			t.Fatalf("iteration %d: decode boundary: %v", iteration, err)
		}
		position := got.batch.Events[0].Position
		if position == nil || position.Durable == nil || boundary.Durable == nil || eventstream.CompareDurablePosition(*position.Durable, *boundary.Durable) > 0 {
			t.Fatalf("iteration %d: event position %#v crosses boundary %#v", iteration, position, boundary)
		}
	}
}

type backfillSplicePageReader struct {
	calls   atomic.Int32
	events  []*session.Event
	blocked chan struct{}
	release chan struct{}
}

func (r *backfillSplicePageReader) EventsPage(ctx context.Context, req session.EventPageRequest) (session.EventPage, error) {
	if r.calls.Add(1) == 3 {
		close(r.blocked)
		select {
		case <-ctx.Done():
			return session.EventPage{}, ctx.Err()
		case <-r.release:
		}
	}
	return session.PageEvents(r.events, req), nil
}

type singleSessionFeedRegistry struct {
	feed controlport.SessionFeed
}

func (r singleSessionFeedRegistry) Session(session.SessionRef) (controlport.SessionFeed, error) {
	return r.feed, nil
}

type eventBatchAllowAuthorizer struct{}

func (eventBatchAllowAuthorizer) Authorize(context.Context, controlport.Principal, controlport.Action, string) error {
	return nil
}
