package appserver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestClientEventsNeverCrossesCapturedBoundaryDuringLiveSplice(t *testing.T) {
	// The backfill barrier fixes the interleaving; one execution covers it.
	reader := &backfillSplicePageReader{
		events:  []*session.Event{durableProtocolEvent(1, "captured")},
		blocked: make(chan struct{}),
		release: make(chan struct{}),
	}
	broker, codec := newTestFeedBroker(t, reader, FeedBrokerConfig{})
	if err := broker.Prime(context.Background()); err != nil {
		t.Fatalf("Prime() error = %v", err)
	}
	client := &Client{config: ClientConfig{
		Feeds:      singleSessionFeedRegistry{feed: broker},
		Authorizer: eventBatchAllowAuthorizer{},
	}}
	type eventsResult struct {
		batch EventBatch
		err   error
	}
	result := make(chan eventsResult, 1)
	go func() {
		batch, err := client.Events(context.Background(), Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1"})
		result <- eventsResult{batch: batch, err: err}
	}()

	select {
	case <-reader.blocked:
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not reach durable backfill")
	}
	if err := broker.Publish(terminalEnvelope("live")); err != nil {
		t.Fatalf("live Publish() error = %v", err)
	}
	_, liveCursor := broker.Boundary()
	close(reader.release)

	var got eventsResult
	select {
	case got = <-result:
	case <-time.After(time.Second):
		t.Fatal("finite Events did not return")
	}
	if got.err != nil {
		t.Fatalf("Events() error = %v", got.err)
	}
	if got.batch.BoundaryCursor == "" || got.batch.BoundaryCursor == liveCursor {
		t.Fatalf("captured boundary = %q, live cursor = %q", got.batch.BoundaryCursor, liveCursor)
	}
	if len(got.batch.Events) != 1 || got.batch.Events[0].EventID != "event-1" {
		t.Fatalf("finite batch = %#v", got.batch)
	}
	if _, err := codec.Decode("session-1", got.batch.BoundaryCursor); err != nil {
		t.Fatalf("decode boundary: %v", err)
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
	feed SessionFeed
}

func (r singleSessionFeedRegistry) Session(session.SessionRef) (SessionFeed, error) {
	return r.feed, nil
}

type eventBatchAllowAuthorizer struct{}

func (eventBatchAllowAuthorizer) Authorize(context.Context, Principal, Action, string) error {
	return nil
}
