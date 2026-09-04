package appserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/streamspool"
	streamspoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
)

func TestFeedBrokerSpoolDeliversWithoutProducerConsumerCoupling(t *testing.T) {
	t.Parallel()

	store, err := streamspoolfile.New(context.Background(), streamspoolfile.Config{
		RootDir: t.TempDir(), GCInterval: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewFeedBroker(FeedBrokerConfig{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Spool: store, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	_, cursor := broker.Boundary()
	result, err := broker.Subscribe(context.Background(), SubscribeRequest{SessionID: "session-1", Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	subscription := result.Subscription
	t.Cleanup(func() { _ = subscription.Close() })
	assertFeedDelivery(t, subscription.Deliveries(), FeedDeliverySync)

	published := make(chan error, 1)
	go func() {
		published <- broker.Publish(eventstream.Envelope{
			Kind: eventstream.KindNotice, SessionID: "session-1", Notice: "ready",
			Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		})
	}()
	select {
	case err := <-published:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("producer waited for the consumer")
	}
	delivery := assertFeedDelivery(t, subscription.Deliveries(), FeedDeliveryAppendPage)
	if len(delivery.Events) != 1 || delivery.Events[0].Notice != "ready" || delivery.NextCursor == "" {
		t.Fatalf("delivery = %#v", delivery)
	}
}

func TestFeedRegistrySessionCloseBoundsRegistrationsByConcurrencyNotLifetime(t *testing.T) {
	t.Parallel()

	sessions := sessionmemory.NewStore(sessionmemory.Config{})
	store, err := streamspoolfile.New(t.Context(), streamspoolfile.Config{
		RootDir: t.TempDir(), GCInterval: -1, MaxRegistrations: 1, MaxPartitions: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewFeedRegistry(FeedRegistryConfig{Reader: sessions, Spool: store, CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })

	var last session.Session
	for index := range 8 {
		active, err := sessions.StartSession(t.Context(), session.StartSessionRequest{
			AppName: "caelis", UserID: "owner", PreferredSessionID: fmt.Sprintf("closed-%d", index),
		})
		if err != nil {
			t.Fatal(err)
		}
		feed, err := registry.Session(active.SessionRef)
		if err != nil {
			t.Fatalf("Session(%d): %v", index, err)
		}
		if err := feed.Publish(eventstream.Envelope{
			Kind: eventstream.KindNotice, SessionID: active.SessionID, Notice: "trace",
			Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		}); err != nil {
			t.Fatal(err)
		}
		closed, err := CloseSession(t.Context(), sessions, active, "test complete")
		if err != nil {
			t.Fatal(err)
		}
		releaseCtx := t.Context()
		if index%2 == 0 {
			var cancel context.CancelFunc
			releaseCtx, cancel = context.WithCancel(t.Context())
			cancel()
		}
		if err := registry.CloseSession(releaseCtx, closed.SessionRef); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("CloseSession(%d): %v", index, err)
		}
		last = closed
	}

	concrete := registry.(*feedRegistry)
	concrete.mu.Lock()
	retained := len(concrete.feeds)
	concrete.mu.Unlock()
	if retained != 0 {
		t.Fatalf("feed registry retained %d closed Sessions", retained)
	}

	// A closed Session remains readable from canonical truth, but gets neither
	// a new writer nor a process-lifetime registry entry.
	closedFeed, err := registry.Session(last.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if _, cursor := closedFeed.Boundary(); cursor != "" {
		t.Fatalf("closed Session allocated spool cursor %q", cursor)
	}
	result, err := closedFeed.Subscribe(t.Context(), SubscribeRequest{SessionID: last.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	assembler := &FeedDeliveryAssembler{}
	sawClosed := false
	for {
		select {
		case delivery, open := <-result.Subscription.Deliveries():
			if !open {
				if err := result.Subscription.Err(); err != nil {
					t.Fatal(err)
				}
				if !sawClosed {
					t.Fatal("canonical closed Session replay omitted lifecycle")
				}
				return
			}
			events, _, err := assembler.Accept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			for _, envelope := range events {
				sawClosed = sawClosed || envelope.Lifecycle != nil && envelope.Lifecycle.State == "closed"
			}
		case <-time.After(2 * time.Second):
			t.Fatal("closed Session feed did not finish")
		}
	}
}

func TestFeedRegistryRetainsBrokerUntilPhysicalSealCanRetry(t *testing.T) {
	t.Parallel()

	sessions := sessionmemory.NewStore(sessionmemory.Config{})
	physical, err := streamspoolfile.New(t.Context(), streamspoolfile.Config{
		RootDir: t.TempDir(), GCInterval: -1, MaxRegistrations: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = physical.Close() })
	store := &failFirstSealStore{Store: physical}
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewFeedRegistry(FeedRegistryConfig{Reader: sessions, Spool: store, CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	active, err := sessions.StartSession(t.Context(), session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "retry-seal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Session(active.SessionRef); err != nil {
		t.Fatal(err)
	}
	closed, err := CloseSession(t.Context(), sessions, active, "done")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.CloseSession(t.Context(), closed.SessionRef); !errors.Is(err, errTestSeal) {
		t.Fatalf("first CloseSession error = %v, want retryable seal failure", err)
	}
	concrete := registry.(*feedRegistry)
	concrete.mu.Lock()
	retained := concrete.feeds[closed.SessionID] != nil
	concrete.mu.Unlock()
	if !retained {
		t.Fatal("registry discarded broker before physical seal succeeded")
	}
	if err := registry.CloseSession(t.Context(), closed.SessionRef); err != nil {
		t.Fatalf("retry CloseSession: %v", err)
	}
	next, err := sessions.StartSession(t.Context(), session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "after-retry-seal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Session(next.SessionRef); err != nil {
		t.Fatalf("successful retry did not release registration: %v", err)
	}
}

func TestFeedBrokerSealedEOFDrainsCanonicalTailAfterInterruptedPrime(t *testing.T) {
	t.Parallel()

	canonical := &checkpointPageReader{active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}
	reader := &cancelableCheckpointPageReader{checkpointPageReader: canonical}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{})
	result, err := broker.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliverySync)

	canonical.setEvents(durableProtocolEvent(1, "committed before close"))
	primeCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := broker.Prime(primeCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Prime error = %v, want context cancellation", err)
	}
	if err := broker.Seal(primeCtx); err != nil {
		t.Fatalf("Seal after interrupted Prime: %v", err)
	}

	events := receiveFeedReplacement(t, result.Subscription)
	if len(events) != 1 || events[0].EventID != "event-1" {
		t.Fatalf("canonical tail = %#v, want event-1", events)
	}
	select {
	case _, open := <-result.Subscription.Deliveries():
		if open {
			t.Fatal("sealed subscription remained open after canonical tail")
		}
		if err := result.Subscription.Err(); err != nil {
			t.Fatalf("sealed subscription error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sealed subscription did not finish after canonical tail")
	}
}

func TestFeedBrokerWithoutSpoolKeepsCanonicalFallbackAvailable(t *testing.T) {
	t.Parallel()

	reader := &checkpointPageReader{active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewFeedBroker(FeedBrokerConfig{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Reader: reader, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	reader.setEvents(durableProtocolEvent(1, "canonical fallback"))
	if err := broker.Prime(context.Background()); err != nil {
		t.Fatalf("Prime without spool error = %v", err)
	}
	result, err := broker.Subscribe(context.Background(), SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Subscription.Close() })
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceBegin)
	delivery := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplacePage)
	if len(delivery.Events) != 1 || delivery.Events[0].EventID != "event-1" {
		t.Fatalf("canonical replacement = %#v", delivery)
	}
	if delivery.Events[0].Cursor != "" || delivery.Events[0].Position == nil || delivery.Events[0].Position.Durable == nil {
		t.Fatalf("canonical replacement resume shape = %#v, want cursorless durable provenance", delivery.Events[0])
	}
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceEnd)
}

func TestFeedBrokerWithoutSpoolFollowsCanonicalEventsAfterSubscription(t *testing.T) {
	t.Parallel()

	reader := &checkpointPageReader{active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewFeedBroker(FeedBrokerConfig{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Reader: reader, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	result, err := broker.Subscribe(context.Background(), SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Subscription.Close() })
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceBegin)
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceEnd)
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliverySync)

	reader.setEvents(durableProtocolEvent(1, "post-subscription final"))
	delivery := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
	if len(delivery.Events) != 1 || delivery.Events[0].EventID != "event-1" || delivery.NextCursor == "" {
		t.Fatalf("canonical follower delivery = %#v", delivery)
	}
}

func TestFeedBrokerWithoutSpoolDoesNotSkipCheckpointAheadOfAcceptedWatermark(t *testing.T) {
	t.Parallel()

	reader := &checkpointPageReader{
		active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}},
		events: []*session.Event{durableProtocolEvent(1, "accepted")},
	}
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewFeedBroker(FeedBrokerConfig{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Reader: reader, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	// The Session commit is now visible to the checkpoint, but its producer has
	// not yet crossed FeedBroker.Publish, so the accepted watermark remains 1.
	reader.setEvents(durableProtocolEvent(1, "accepted"), durableProtocolEvent(2, "committed before publish"))
	result, err := broker.Subscribe(context.Background(), SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Subscription.Close() })
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceBegin)
	replacement := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplacePage)
	if len(replacement.Events) != 1 || replacement.Events[0].EventID != "event-1" {
		t.Fatalf("replacement = %#v, want accepted watermark only", replacement)
	}
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceEnd)
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliverySync)
	appendDelivery := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
	if len(appendDelivery.Events) != 1 || appendDelivery.Events[0].EventID != "event-2" {
		t.Fatalf("canonical follower = %#v, want committed event after accepted watermark", appendDelivery)
	}
}

func TestFeedBrokerWithoutSpoolDeliversBoundedTerminalFallback(t *testing.T) {
	t.Parallel()

	reader := &checkpointPageReader{active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewFeedBroker(FeedBrokerConfig{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Reader: reader, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	result, err := broker.Subscribe(context.Background(), SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Subscription.Close() })
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceBegin)
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceEnd)
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliverySync)

	terminal := eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Now())
	terminal.SessionID = "session-1"
	terminal.Cursor = "producer-local-cursor"
	if err := broker.Publish(terminal); err != nil {
		t.Fatal(err)
	}
	delivery := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
	if delivery.Source != FeedSourceResult || len(delivery.Events) != 1 || !eventstream.IsTurnTerminalLifecycle(delivery.Events[0]) ||
		delivery.Events[0].Cursor != "" || delivery.NextCursor != "" {
		t.Fatalf("terminal fallback = %#v, want cursorless basic result", delivery)
	}
}

func TestFeedDeliveryAssemblerRejectsMalformedTerminalResult(t *testing.T) {
	t.Parallel()

	terminal := eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Now())
	terminal.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
	for name, mutate := range map[string]func(*FeedDelivery){
		"cursor":       func(delivery *FeedDelivery) { delivery.NextCursor = "not-resumable" },
		"event cursor": func(delivery *FeedDelivery) { delivery.Events[0].Cursor = "not-resumable" },
		"nonterminal": func(delivery *FeedDelivery) {
			delivery.Events[0] = eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "progress"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			delivery := FeedDelivery{Kind: FeedDeliveryAppendPage, Source: FeedSourceResult, Events: []eventstream.Envelope{terminal}}
			mutate(&delivery)
			if _, _, err := new(FeedDeliveryAssembler).Accept(delivery); err == nil {
				t.Fatalf("Accept(%s) succeeded", name)
			}
		})
	}
}

func TestFeedDeliveryAssemblerEnforcesSourceResumeShape(t *testing.T) {
	t.Parallel()

	exact := projectedEnvelope(1, "exact")
	exact.Cursor = "cursor-1"
	if events, replacement, err := new(FeedDeliveryAssembler).Accept(FeedDelivery{
		Kind: FeedDeliveryAppendPage, Source: FeedSourceExact,
		Events: []eventstream.Envelope{exact}, NextCursor: exact.Cursor,
	}); err != nil || replacement || len(events) != 1 {
		t.Fatalf("valid exact append = (%#v, %v, %v)", events, replacement, err)
	}
	missingPosition := eventstream.CloneEnvelope(exact)
	missingPosition.Position = nil
	if _, _, err := new(FeedDeliveryAssembler).Accept(FeedDelivery{
		Kind: FeedDeliveryAppendPage, Source: FeedSourceExact,
		Events: []eventstream.Envelope{missingPosition}, NextCursor: missingPosition.Cursor,
	}); err == nil {
		t.Fatal("exact append without position was accepted")
	}

	replacementEnvelope := eventstream.CloneEnvelope(exact)
	replacementEnvelope.Cursor = ""
	replacementEnvelope.Position = nil
	assembler := new(FeedDeliveryAssembler)
	if _, _, err := assembler.Accept(FeedDelivery{
		Kind: FeedDeliveryReplaceBegin, Source: FeedSourceReplacement, SnapshotID: "snapshot-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := assembler.Accept(FeedDelivery{
		Kind: FeedDeliveryReplacePage, Source: FeedSourceReplacement, SnapshotID: "snapshot-1",
		Events: []eventstream.Envelope{replacementEnvelope},
	}); err != nil {
		t.Fatalf("cursorless replacement page: %v", err)
	}

	resumableReplacement := new(FeedDeliveryAssembler)
	_, _, _ = resumableReplacement.Accept(FeedDelivery{
		Kind: FeedDeliveryReplaceBegin, Source: FeedSourceReplacement, SnapshotID: "snapshot-2",
	})
	if _, _, err := resumableReplacement.Accept(FeedDelivery{
		Kind: FeedDeliveryReplacePage, Source: FeedSourceReplacement, SnapshotID: "snapshot-2",
		Events: []eventstream.Envelope{exact},
	}); err == nil {
		t.Fatal("replacement page carrying resume identity was accepted")
	}
}

func TestFeedBrokerReadFailureReplacesDeliveredPrefix(t *testing.T) {
	t.Parallel()

	rawStore, err := streamspoolfile.New(context.Background(), streamspoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawStore.Close() })
	store := &failReaderAfterStore{Store: rawStore, records: 1}
	reader := &checkpointPageReader{active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewFeedBroker(FeedBrokerConfig{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Reader: reader, Spool: store, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	result, err := broker.Subscribe(context.Background(), SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Subscription.Close() })
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliverySync)

	reader.setEvents(durableProtocolEvent(1, "first"), durableProtocolEvent(2, "second"))
	if err := broker.Publish(projectedEnvelope(1, "first")); err != nil {
		t.Fatal(err)
	}
	if err := broker.Publish(projectedEnvelope(2, "second")); err != nil {
		t.Fatal(err)
	}
	first := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
	if len(first.Events) != 1 || first.Events[0].EventID != "event-1" {
		t.Fatalf("first exact delivery = %#v", first)
	}
	events := receiveFeedReplacement(t, result.Subscription)
	if len(events) != 2 || events[0].EventID != "event-1" || events[1].EventID != "event-2" {
		t.Fatalf("canonical replacement = %#v", events)
	}

}

func TestFeedBrokerValidStaleCursorStartsCanonicalReplacement(t *testing.T) {
	t.Parallel()

	reader := &checkpointPageReader{
		active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}},
		events: []*session.Event{durableProtocolEvent(1, "canonical")},
	}
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := streamspoolfile.New(context.Background(), streamspoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first, err := NewFeedBroker(FeedBrokerConfig{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Reader: reader, Spool: store, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, staleCursor := first.Boundary()
	if staleCursor == "" {
		t.Fatal("first broker returned no cache cursor")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewFeedBroker(FeedBrokerConfig{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Reader: reader, Spool: store, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	result, err := second.Subscribe(context.Background(), SubscribeRequest{SessionID: "session-1", Cursor: staleCursor})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Subscription.Close() })
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceBegin)
	delivery := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplacePage)
	if len(delivery.Events) != 1 || delivery.Events[0].EventID != "event-1" {
		t.Fatalf("stale cursor replacement = %#v", delivery)
	}
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceEnd)
}

func TestFeedBrokerCapturesFallbackAndSpoolHighWaterAtomically(t *testing.T) {
	t.Parallel()

	rawStore, err := streamspoolfile.New(context.Background(), streamspoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawStore.Close() })
	started := make(chan struct{})
	release := make(chan struct{})
	store := &incompleteBlockingBoundsStore{Store: rawStore, started: started, release: release}
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	reader := &checkpointPageReader{active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}
	broker, err := NewFeedBroker(FeedBrokerConfig{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Reader: reader, Spool: store, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	subscribed := make(chan SubscribeResult, 1)
	subscribeErr := make(chan error, 1)
	go func() {
		result, err := broker.Subscribe(context.Background(), SubscribeRequest{SessionID: "session-1"})
		subscribed <- result
		subscribeErr <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("subscription did not reach its spool high-water capture")
	}
	published := make(chan error, 1)
	go func() { published <- broker.Publish(projectedEnvelope(1, "concurrent")) }()
	select {
	case err := <-published:
		t.Fatalf("Publish crossed an incomplete fallback cut: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-subscribeErr; err != nil {
		t.Fatal(err)
	}
	result := <-subscribed
	t.Cleanup(func() { _ = result.Subscription.Close() })
	if err := <-published; err != nil {
		t.Fatal(err)
	}
	events := receiveFeedEvents(t, result.Subscription, 1)
	if len(events) != 1 || events[0].EventID != "event-1" {
		t.Fatalf("atomic fallback delivery = %#v", events)
	}
}

func TestFeedDeliveryAssemblerDiscardsIncompleteReplacement(t *testing.T) {
	t.Parallel()

	var assembler FeedDeliveryAssembler
	if events, replacement, err := assembler.Accept(FeedDelivery{
		Kind: FeedDeliveryReplaceBegin, Source: FeedSourceReplacement, SnapshotID: "snapshot-1",
	}); err != nil || replacement || len(events) != 0 {
		t.Fatalf("begin = (%#v, %v, %v)", events, replacement, err)
	}
	if events, replacement, err := assembler.Accept(FeedDelivery{
		Kind: FeedDeliveryReplacePage, Source: FeedSourceReplacement, SnapshotID: "snapshot-1",
		Events: []eventstream.Envelope{{Kind: eventstream.KindNotice, Notice: "history"}},
	}); err != nil || replacement || len(events) != 0 {
		t.Fatalf("page = (%#v, %v, %v)", events, replacement, err)
	}
	assembler.Reset()
	if assembler.Pending() {
		t.Fatal("incomplete replacement remained visible after reset")
	}
}

func assertFeedDelivery(t *testing.T, deliveries <-chan FeedDelivery, kind FeedDeliveryKind) FeedDelivery {
	t.Helper()
	select {
	case delivery, ok := <-deliveries:
		if !ok {
			t.Fatal("delivery stream closed")
		}
		if delivery.Kind != kind {
			t.Fatalf("delivery kind = %q, want %q", delivery.Kind, kind)
		}
		return delivery
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %q", kind)
		return FeedDelivery{}
	}
}

func newTestFeedBroker(t *testing.T, reader session.PagedReader, override FeedBrokerConfig) (*FeedBroker, *CursorCodec) {
	t.Helper()
	store, err := streamspoolfile.New(context.Background(), streamspoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	override.SessionRef = session.SessionRef{SessionID: "session-1"}
	override.Reader = reader
	override.Spool = store
	override.CursorCodec = codec
	broker, err := NewFeedBroker(override)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	return broker, codec
}

func newTestFeedRegistry(t *testing.T, config FeedRegistryConfig) FeedRegistry {
	t.Helper()
	store, err := streamspoolfile.New(context.Background(), streamspoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	config.Spool = store
	feeds, err := NewFeedRegistry(config)
	if err != nil {
		t.Fatal(err)
	}
	return feeds
}

func durableProtocolEvent(seq uint64, text string) *session.Event {
	return &session.Event{
		ID: "event-" + string(rune('0'+seq)), SessionID: "session-1", Seq: seq,
		Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), Content: session.ProtocolTextContent(text),
		}},
	}
}

func projectedEnvelope(seq uint64, text string) eventstream.Envelope {
	eventID := "event-" + string(rune('0'+seq))
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", EventID: eventID,
		ProjectionID: "test-projection:" + eventID,
		Position:     &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{Seq: seq}},
		Delivery:     &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: text},
		},
	}
}

func terminalEnvelope(text string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1",
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update:   eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "command-1"},
		Meta:     map[string]any{"terminal_output": text},
	}
}

func receiveFeedEvents(t *testing.T, subscription FeedSubscription, count int) []eventstream.Envelope {
	t.Helper()
	assembler := &FeedDeliveryAssembler{}
	out := make([]eventstream.Envelope, 0, count)
	for len(out) < count {
		select {
		case delivery, ok := <-subscription.Deliveries():
			if !ok {
				t.Fatalf("deliveries closed after %d of %d events: %v", len(out), count, subscription.Err())
			}
			events, _, err := assembler.Accept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, events...)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d of %d events", len(out), count)
		}
	}
	return out
}

type checkpointPageReader struct {
	mu     sync.RWMutex
	events []*session.Event
	active session.Session
}

type cancelableCheckpointPageReader struct {
	*checkpointPageReader
}

func (r *cancelableCheckpointPageReader) EventsPage(ctx context.Context, req session.EventPageRequest) (session.EventPage, error) {
	if err := ctx.Err(); err != nil {
		return session.EventPage{}, err
	}
	return r.checkpointPageReader.EventsPage(ctx, req)
}

func (r *checkpointPageReader) EventsPage(_ context.Context, req session.EventPageRequest) (session.EventPage, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return session.PageEvents(r.events, req), nil
}

func (r *checkpointPageReader) EventCheckpoint(_ context.Context, ref session.SessionRef) (session.EventCheckpoint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	active := session.CloneSession(r.active)
	if active.SessionID == "" {
		active.SessionRef = ref
	}
	checkpoint := session.EventCheckpoint{Session: active}
	for index := len(r.events) - 1; index >= 0; index-- {
		event := r.events[index]
		if event == nil || session.IsTransient(event) {
			continue
		}
		if checkpoint.ThroughSeq == 0 {
			checkpoint.ThroughSeq = event.Seq
		}
		if session.IsClientReplayEvent(event) {
			checkpoint.LastClientReplayEvent = session.CloneEvent(event)
			break
		}
	}
	return checkpoint, nil
}

func (r *checkpointPageReader) setEvents(events ...*session.Event) {
	r.mu.Lock()
	r.events = append([]*session.Event(nil), events...)
	r.mu.Unlock()
}

type incompleteBlockingBoundsStore struct {
	streamspool.Store
	started chan struct{}
	release <-chan struct{}
}

var errTestSeal = errors.New("test seal failure")

type failFirstSealStore struct {
	streamspool.Store
}

func (s *failFirstSealStore) Register(ctx context.Context, key streamspool.LogicalKey, options streamspool.WriterOptions) (streamspool.Writer, error) {
	writer, err := s.Store.Register(ctx, key, options)
	if err != nil {
		return nil, err
	}
	return &failFirstSealWriter{Writer: writer}, nil
}

type failFirstSealWriter struct {
	streamspool.Writer
	mu     sync.Mutex
	failed bool
}

func (w *failFirstSealWriter) Seal(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.failed {
		w.failed = true
		return errTestSeal
	}
	return w.Writer.Seal(ctx)
}

func (s *incompleteBlockingBoundsStore) Register(ctx context.Context, key streamspool.LogicalKey, options streamspool.WriterOptions) (streamspool.Writer, error) {
	options.OriginComplete = false
	writer, err := s.Store.Register(ctx, key, options)
	if err != nil {
		return nil, err
	}
	return &blockingBoundsWriter{Writer: writer, started: s.started, release: s.release}, nil
}

type blockingBoundsWriter struct {
	streamspool.Writer
	once    sync.Once
	started chan struct{}
	release <-chan struct{}
}

type failReaderAfterStore struct {
	streamspool.Store
	records int
}

func (s *failReaderAfterStore) Reader(ctx context.Context, key streamspool.Key, offset streamspool.Offset) (streamspool.Reader, error) {
	reader, err := s.Store.Reader(ctx, key, offset)
	if err != nil {
		return nil, err
	}
	return &failReaderAfter{Reader: reader, remaining: s.records}, nil
}

type failReaderAfter struct {
	streamspool.Reader
	remaining int
}

func (r *failReaderAfter) Next(ctx context.Context) (streamspool.Record, error) {
	if r.remaining == 0 {
		return streamspool.Record{}, streamspool.ErrUnavailable
	}
	r.remaining--
	return r.Reader.Next(ctx)
}

func (w *blockingBoundsWriter) Bounds(ctx context.Context) (streamspool.Bounds, error) {
	w.once.Do(func() {
		close(w.started)
		select {
		case <-ctx.Done():
		case <-w.release:
		}
	})
	return w.Writer.Bounds(ctx)
}
