package controlclient

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestFeedBrokerDurableSequencerSerializesGapFillAndPublishesOnce(t *testing.T) {
	reader := &blockingPageReader{
		started: make(chan struct{}), release: make(chan struct{}),
		events: []*session.Event{durableProtocolEvent(1, "one"), durableProtocolEvent(2, "two"), durableProtocolEvent(3, "three")},
	}
	broker, codec := newTestFeedBroker(t, reader, FeedBrokerConfig{RingEvents: 1, SubscriberQueue: 8})
	resume, err := codec.Encode("session-1", eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{Seq: 1}})
	if err != nil {
		t.Fatal(err)
	}

	resultCh := make(chan controlport.SubscribeResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := broker.Subscribe(context.Background(), controlport.SubscribeRequest{SessionID: "session-1", Cursor: resume})
		resultCh <- result
		errCh <- err
	}()
	<-reader.started
	publishDone := make(chan error, 1)
	go func() { publishDone <- broker.Publish(projectedEnvelope(3, "three")) }()
	select {
	case err := <-publishDone:
		t.Fatalf("Publish completed before the durable gap fill: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(reader.release)
	if err := <-publishDone; err != nil {
		t.Fatal(err)
	}
	result := <-resultCh
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	if result.Mode != controlport.ResumeModeDurableFallback || !result.TransientGap {
		t.Fatalf("result = %#v, want durable fallback with transient gap", result)
	}
	got := receiveEnvelopes(t, result.Subscription.Events(), 2)
	if ids := []string{got[0].EventID, got[1].EventID}; !reflect.DeepEqual(ids, []string{"event-2", "event-3"}) {
		t.Fatalf("event IDs = %v, want once and ordered", ids)
	}
}

func TestFeedBrokerPrimePublishesCommittedMutationToLiveSubscriber(t *testing.T) {
	reader := &mutablePageReader{}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 8})
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	reader.events = append(reader.events, durableProtocolEvent(1, "participant attached"))
	if err := broker.Prime(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := receiveEnvelopes(t, subscription.Events(), 1)[0]
	if got.EventID != "event-1" || got.Position == nil || got.Position.Durable == nil || got.Position.Durable.Seq != 1 {
		t.Fatalf("live durable mutation = %#v", got)
	}
}

func TestFeedBrokerDurablePublishGapFillsFromStorage(t *testing.T) {
	reader := &mutablePageReader{}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 8})
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	reader.events = []*session.Event{
		durableProtocolEvent(1, "one"),
		durableProtocolEvent(2, "two"),
	}
	if err := broker.Publish(projectedEnvelope(2, "two")); err != nil {
		t.Fatal(err)
	}
	got := receiveEnvelopes(t, subscription.Events(), 2)
	if ids := []string{got[0].EventID, got[1].EventID}; !reflect.DeepEqual(ids, []string{"event-1", "event-2"}) {
		t.Fatalf("gap-filled IDs = %v", ids)
	}
}

func TestFeedBrokerSubscribeFromNowEstablishesHistoryBaseline(t *testing.T) {
	reader := &mutablePageReader{events: []*session.Event{
		durableProtocolEvent(1, "old one"),
		durableProtocolEvent(2, "old two"),
	}}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 8})
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	select {
	case historical := <-subscription.Events():
		t.Fatalf("SubscribeFromNow replayed historical event: %#v", historical)
	case <-time.After(30 * time.Millisecond):
	}

	reader.events = append(reader.events, durableProtocolEvent(3, "current turn"))
	if err := broker.Publish(projectedEnvelope(3, "current turn")); err != nil {
		t.Fatal(err)
	}
	got := receiveEnvelopes(t, subscription.Events(), 1)[0]
	if got.EventID != "event-3" {
		t.Fatalf("current event = %#v, want only event-3", got)
	}
}

func TestFeedBrokerDurablePublishPrimesOnlyThroughIncomingPosition(t *testing.T) {
	reader := &mutablePageReader{}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 8})
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	reader.events = []*session.Event{
		durableProtocolEvent(1, "first ingress"),
		durableProtocolEvent(2, "future ingress"),
	}
	if err := broker.Publish(projectedEnvelope(1, "first ingress")); err != nil {
		t.Fatal(err)
	}
	first := receiveEnvelopes(t, subscription.Events(), 1)[0]
	if first.EventID != "event-1" {
		t.Fatalf("first publish = %#v, want event-1", first)
	}
	select {
	case future := <-subscription.Events():
		t.Fatalf("event beyond ingress position was published early: %#v", future)
	case <-time.After(30 * time.Millisecond):
	}
	if err := broker.Publish(projectedEnvelope(2, "future ingress")); err != nil {
		t.Fatal(err)
	}
	second := receiveEnvelopes(t, subscription.Events(), 1)[0]
	if second.EventID != "event-2" {
		t.Fatalf("second publish = %#v, want event-2", second)
	}
}

func TestFeedBrokerDurablePublishPreservesIncomingTransportIdentity(t *testing.T) {
	reader := &mutablePageReader{}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 8})
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	reader.events = []*session.Event{durableProtocolEvent(1, "current turn")}
	incoming := projectedEnvelope(1, "current turn")
	incoming.HandleID = "handle-current"
	incoming.RunID = "run-current"
	incoming.TurnID = "turn-current"
	if err := broker.Publish(incoming); err != nil {
		t.Fatal(err)
	}
	got := receiveEnvelopes(t, subscription.Events(), 1)[0]
	if got.HandleID != incoming.HandleID || got.RunID != incoming.RunID || got.TurnID != incoming.TurnID {
		t.Fatalf("published transport identity = (%q,%q,%q), want incoming (%q,%q,%q)",
			got.HandleID, got.RunID, got.TurnID, incoming.HandleID, incoming.RunID, incoming.TurnID)
	}
	select {
	case duplicate := <-subscription.Events():
		t.Fatalf("storage reconstruction duplicated incoming position: %#v", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestFeedBrokerEvictionDoesNotForgetDurableDedupe(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{RingEvents: 1, SubscriberQueue: 8})
	if err := broker.Publish(projectedEnvelope(1, "one")); err != nil {
		t.Fatal(err)
	}
	if err := broker.Publish(projectedEnvelope(2, "two")); err != nil {
		t.Fatal(err)
	}
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if err := broker.Publish(projectedEnvelope(1, "one")); err != nil {
		t.Fatal(err)
	}
	select {
	case duplicate := <-subscription.Events():
		t.Fatalf("evicted durable duplicate was republished: %#v", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestFeedBrokerExactReconnectPreservesTransientBytes(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{RingEvents: 8, SubscriberQueue: 8})
	first := terminalEnvelope("\x1b[31m中")
	second := terminalEnvelope("文\x1b[0m\n")
	second.Meta["exit_code"] = float64(7)
	if err := broker.Publish(first); err != nil {
		t.Fatal(err)
	}
	_, cursor := broker.Boundary()
	if err := broker.Publish(second); err != nil {
		t.Fatal(err)
	}
	result, err := broker.Subscribe(context.Background(), controlport.SubscribeRequest{SessionID: "session-1", Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	if result.Mode != controlport.ResumeModeExact || result.TransientGap {
		t.Fatalf("result = %#v, want exact", result)
	}
	got := receiveEnvelopes(t, result.Subscription.Events(), 1)[0]
	if !reflect.DeepEqual(got.Meta, second.Meta) {
		t.Fatalf("terminal payload = %#v, want %#v", got.Meta, second.Meta)
	}
	if got.Position == nil || got.Position.Transient == nil || got.Cursor == "" {
		t.Fatalf("terminal delivery lacks transient position/cursor: %#v", got)
	}
}

func TestFeedBrokerEmptyCursorReplayPreservesRetainedTransientInterleaving(t *testing.T) {
	reader := &mutablePageReader{events: []*session.Event{durableProtocolEvent(1, "command started")}}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{RingEvents: 8, SubscriberQueue: 8})
	if err := broker.Publish(projectedEnvelope(1, "command started")); err != nil {
		t.Fatal(err)
	}
	if err := broker.Publish(terminalEnvelope("步骤 1\n步骤 2\n")); err != nil {
		t.Fatal(err)
	}
	reader.events = append(reader.events, durableProtocolEvent(2, "command completed"))
	if err := broker.Publish(projectedEnvelope(2, "command completed")); err != nil {
		t.Fatal(err)
	}

	result, err := broker.Subscribe(context.Background(), controlport.SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	if result.Mode != controlport.ResumeModeExact || result.TransientGap {
		t.Fatalf("result = %#v, want exact retained replay", result)
	}
	got := result.Backfill
	if len(got) != 3 {
		t.Fatalf("backfill = %#v, want durable start, transient bytes, durable completion", got)
	}
	if got[0].EventID != "event-1" || got[1].Meta["terminal_output"] != "步骤 1\n步骤 2\n" || got[2].EventID != "event-2" {
		t.Fatalf("interleaved backfill = %#v", got)
	}
	if got[1].Position == nil || got[1].Position.Transient == nil || got[1].Position.Transient.Anchor.Seq != 1 {
		t.Fatalf("terminal position = %#v, want transient anchored after durable seq 1", got[1].Position)
	}
}

func TestFeedBrokerReconcilesCrossTurnTerminalReplayByAbsoluteCursor(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{RingEvents: 8, SubscriberQueue: 8})
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	const prefix = "步骤 1: 正在处理...\n"
	const tail = "步骤 2: 正在处理...\n"
	if err := broker.Publish(terminalEnvelopeAtCursor(prefix, int64(len([]byte(prefix))))); err != nil {
		t.Fatal(err)
	}
	first := receiveEnvelopes(t, subscription.Events(), 1)[0]
	if got, ok := terminalEnvelopeOutput(first); !ok || got != prefix {
		t.Fatalf("first terminal output = %q, %v; want prefix", got, ok)
	}

	// A later TASK observer reads the physical command from zero and returns a
	// cumulative retained frame. The shared Session feed must emit only the
	// unaccepted suffix, not duplicate the already displayed prefix.
	full := prefix + tail
	if err := broker.Publish(terminalEnvelopeAtCursor(full, int64(len([]byte(full))))); err != nil {
		t.Fatal(err)
	}
	second := receiveEnvelopes(t, subscription.Events(), 1)[0]
	if got, ok := terminalEnvelopeOutput(second); !ok || got != tail {
		t.Fatalf("replayed terminal output = %q, %v; want only tail", got, ok)
	}

	// Identical bytes at a new absolute cursor are real repeated output and must
	// remain visible.
	repeatedEnd := int64(len([]byte(full + tail)))
	if err := broker.Publish(terminalEnvelopeAtCursor(tail, repeatedEnd)); err != nil {
		t.Fatal(err)
	}
	third := receiveEnvelopes(t, subscription.Events(), 1)[0]
	if got, ok := terminalEnvelopeOutput(third); !ok || got != tail {
		t.Fatalf("repeated terminal output = %q, %v; want exact repeated delta", got, ok)
	}
}

func TestFeedBrokerEmptyCursorReplaySignalsEvictedTransientGap(t *testing.T) {
	reader := &mutablePageReader{events: []*session.Event{durableProtocolEvent(1, "command started")}}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{RingEvents: 2, SubscriberQueue: 8})
	if err := broker.Publish(projectedEnvelope(1, "command started")); err != nil {
		t.Fatal(err)
	}
	if err := broker.Publish(terminalEnvelope("evicted bytes")); err != nil {
		t.Fatal(err)
	}
	reader.events = append(reader.events, durableProtocolEvent(2, "command completed"))
	if err := broker.Publish(projectedEnvelope(2, "command completed")); err != nil {
		t.Fatal(err)
	}
	if err := broker.Publish(terminalEnvelope("retained tail")); err != nil {
		t.Fatal(err)
	}

	result, err := broker.Subscribe(context.Background(), controlport.SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	if !result.TransientGap {
		t.Fatalf("result = %#v, want explicit transient gap after ring eviction", result)
	}
	got := result.Backfill
	if len(got) != 3 || got[0].EventID != "event-1" || got[1].EventID != "event-2" || got[2].Meta["terminal_output"] != "retained tail" {
		t.Fatalf("gap backfill = %#v, want durable history plus retained transient suffix", got)
	}
}

func TestFeedBrokerEvictionFallsBackToDurableReplay(t *testing.T) {
	reader := &mutablePageReader{events: []*session.Event{durableProtocolEvent(1, "one")}}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{RingEvents: 1})
	if err := broker.Publish(projectedEnvelope(1, "one")); err != nil {
		t.Fatal(err)
	}
	_, cursor := broker.Boundary()
	reader.events = append(reader.events, durableProtocolEvent(2, "two"))
	if err := broker.Publish(projectedEnvelope(2, "two")); err != nil {
		t.Fatal(err)
	}
	result, err := broker.Subscribe(context.Background(), controlport.SubscribeRequest{SessionID: "session-1", Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	if result.Mode != controlport.ResumeModeDurableFallback || !result.TransientGap {
		t.Fatalf("result = %#v", result)
	}
	got := receiveEnvelopes(t, result.Subscription.Events(), 1)
	if got[0].EventID != "event-2" {
		t.Fatalf("replayed event = %#v", got[0])
	}
}

func TestFeedBrokerFallbackSignalsEmptyBackfill(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{RingEvents: 1})
	if err := broker.Publish(terminalEnvelope("first")); err != nil {
		t.Fatal(err)
	}
	_, cursor := broker.Boundary()
	if err := broker.Publish(terminalEnvelope("second")); err != nil {
		t.Fatal(err)
	}
	result, err := broker.Subscribe(context.Background(), controlport.SubscribeRequest{SessionID: "session-1", Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	if result.Mode != controlport.ResumeModeDurableFallback || !result.TransientGap {
		t.Fatalf("result = %#v, want durable fallback with transient gap", result)
	}
	select {
	case <-result.Subscription.BackfillDone():
	case <-time.After(time.Second):
		t.Fatal("empty durable fallback did not signal backfill completion")
	}
}

func TestFeedBrokerEvictsByEncodedBytesAndTTL(t *testing.T) {
	probe, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{RingBytes: 1 << 20})
	if err := probe.Publish(terminalEnvelope("same-size")); err != nil {
		t.Fatal(err)
	}
	probe.mu.Lock()
	oneEnvelopeBytes := probe.ringByteCount
	probe.mu.Unlock()
	if oneEnvelopeBytes <= 0 {
		t.Fatal("encoded ring byte accounting did not advance")
	}

	byteBounded, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{RingEvents: 8, RingBytes: oneEnvelopeBytes + 8})
	if err := byteBounded.Publish(terminalEnvelope("same-size")); err != nil {
		t.Fatal(err)
	}
	if err := byteBounded.Publish(terminalEnvelope("same-size")); err != nil {
		t.Fatal(err)
	}
	byteBounded.mu.Lock()
	byteCount, eventCount := byteBounded.ringByteCount, len(byteBounded.ring)
	byteBounded.mu.Unlock()
	if byteCount > oneEnvelopeBytes+8 || eventCount != 1 {
		t.Fatalf("byte-bounded ring = %d bytes/%d events, want one retained Envelope", byteCount, eventCount)
	}

	now := time.Unix(100, 0)
	ttlBounded, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{RingTTL: time.Second, Now: func() time.Time { return now }})
	if err := ttlBounded.Publish(terminalEnvelope("expires")); err != nil {
		t.Fatal(err)
	}
	_, cursor := ttlBounded.Boundary()
	if cursor == "" {
		t.Fatal("TTL test did not capture initial cursor")
	}
	now = now.Add(2 * time.Second)
	if position, current := ttlBounded.Boundary(); position != nil || current != "" {
		t.Fatalf("expired Boundary() = (%#v, %q), want empty transient ring", position, current)
	}
}

func TestFeedBrokerDisconnectsSlowSubscriberWithoutBlockingPublish(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{SubscriberQueue: 1})
	result, err := broker.Subscribe(context.Background(), controlport.SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	subscription := result.Subscription
	defer subscription.Close()
	for index := 0; index < 8; index++ {
		if err := broker.Publish(terminalEnvelope(string(rune('a' + index)))); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for !errors.Is(subscription.Err(), controlport.ErrSlowConsumer) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(subscription.Err(), controlport.ErrSlowConsumer) {
		t.Fatalf("subscription error = %v, want slow consumer", subscription.Err())
	}
}

func TestFeedBrokerUnreadInternalSubscriberDoesNotBlockActiveSibling(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{SubscriberQueue: 1})
	unread, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unread.Close()
	active, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()

	for index := 0; index < 8; index++ {
		published := make(chan error, 1)
		text := string(rune('a' + index))
		go func() { published <- broker.Publish(terminalEnvelope(text)) }()
		select {
		case err := <-published:
			if err != nil {
				t.Fatalf("Publish(%d) error = %v", index, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("Publish(%d) blocked on unread internal subscriber", index)
		}
		got := receiveEnvelopes(t, active.Events(), 1)[0]
		if got.Meta["terminal_output"] != text {
			t.Fatalf("active sibling event %d = %#v, want %q", index, got.Meta, text)
		}
	}

	deadline := time.Now().Add(time.Second)
	for !errors.Is(unread.Err(), controlport.ErrSlowConsumer) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(unread.Err(), controlport.ErrSlowConsumer) {
		t.Fatalf("unread subscription error = %v, want slow consumer", unread.Err())
	}
	position, cursor := broker.Boundary()
	if position == nil || position.Transient == nil || position.Transient.Sequence != 8 || cursor == "" {
		t.Fatalf("Boundary() = (%#v, %q), want all eight publishes committed", position, cursor)
	}
}

func TestFeedBrokerInternalSubscriptionClosesWithContext(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{SubscriberQueue: 1})
	ctx, cancel := context.WithCancel(context.Background())
	subscription, err := broker.SubscribeFromNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	select {
	case _, ok := <-subscription.Events():
		if ok {
			t.Fatal("context-cancelled subscription delivered an event")
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not close internal subscription")
	}

	broker.mu.Lock()
	remaining := len(broker.subscribers)
	broker.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("subscribers after context cancellation = %d, want zero", remaining)
	}
	if err := broker.Publish(terminalEnvelope("after cancellation")); err != nil {
		t.Fatalf("Publish() after subscriber cancellation = %v", err)
	}
}

func TestFeedBrokerAttachToPreservesPreMaterializedDurableBatchWithQueueOne(t *testing.T) {
	reader := &mutablePageReader{events: []*session.Event{durableProtocolEvent(1, "baseline")}}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 1})
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	const lastSeq = 8
	for seq := uint64(2); seq <= lastSeq; seq++ {
		reader.events = append(reader.events, durableProtocolEvent(seq, "materialized"))
	}
	ingress := make(chan eventstream.Envelope, lastSeq-1)
	for seq := uint64(2); seq <= lastSeq; seq++ {
		ingress <- projectedEnvelope(seq, "materialized")
	}
	close(ingress)
	attached := broker.AttachTo(subscription, ingress)

	got := receiveEnvelopes(t, subscription.Events(), lastSeq-1)
	for index, envelope := range got {
		wantSeq := uint64(index + 2)
		if envelope.Position == nil || envelope.Position.Durable == nil || envelope.Position.Durable.Seq != wantSeq {
			t.Fatalf("durable batch event %d = %#v, want seq %d", index, envelope.Position, wantSeq)
		}
	}
	select {
	case err := <-attached:
		if err != nil {
			t.Fatalf("AttachTo() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AttachTo() did not finish after durable batch delivery")
	}
	if err := subscription.Err(); err != nil {
		t.Fatalf("subscription error = %v, want ordered durable batch", err)
	}
}

func TestFeedBrokerAttachToDeliversDurableGapBeforeIncomingWithQueueOne(t *testing.T) {
	reader := &mutablePageReader{events: []*session.Event{durableProtocolEvent(1, "baseline")}}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 1})
	target, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	// Keep the target at queue one (the behavior under test), while giving the
	// ordinary sibling enough independent capacity for the two-event batch.
	// An unread ordinary queue of one is correctly classified as slow and must
	// not make this ordering regression scheduler-dependent.
	broker.queueSize = 2
	sibling, err := broker.SubscribeFromNow(context.Background())
	broker.queueSize = 1
	if err != nil {
		t.Fatal(err)
	}
	defer sibling.Close()

	reader.events = append(reader.events,
		durableProtocolEvent(2, "gap"),
		durableProtocolEvent(3, "incoming"),
	)
	ingress := make(chan eventstream.Envelope, 1)
	ingress <- projectedEnvelope(3, "incoming")
	close(ingress)
	attached := broker.AttachTo(target, ingress)

	results := map[string][]eventstream.Envelope{
		"target":  receiveEnvelopes(t, target.Events(), 2),
		"sibling": receiveEnvelopes(t, sibling.Events(), 2),
	}
	for name, got := range results {
		if len(got) != 2 {
			t.Fatalf("%s received %d events, want 2 (subscription error: %v)", name, len(got), sibling.Err())
		}
		for index, want := range []uint64{2, 3} {
			position := got[index].Position
			if position == nil || position.Durable == nil || position.Durable.Seq != want {
				t.Fatalf("%s event %d position = %#v, want durable seq %d", name, index, position, want)
			}
		}
	}
	waitFeedAttachmentClosed(t, attached, "durable gap delivery")
	if err := target.Err(); err != nil {
		t.Fatalf("target error = %v, want no self-reservation slow-consumer failure", err)
	}
	if err := sibling.Err(); err != nil {
		t.Fatalf("sibling error = %v, want ordered sibling delivery", err)
	}
}

func TestFeedBrokerAttachToPreservesGlobalOrderAcrossConcurrentGapPublish(t *testing.T) {
	reader := newStagedGapPageReader()
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 1})
	target, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	broker.queueSize = 3
	sibling, err := broker.SubscribeFromNow(context.Background())
	broker.queueSize = 1
	if err != nil {
		t.Fatal(err)
	}
	defer sibling.Close()
	reader.arm()

	ingress := make(chan eventstream.Envelope, 1)
	ingress <- projectedEnvelope(3, "incoming")
	close(ingress)
	attached := broker.AttachTo(target, ingress)
	waitCancellableReaderSignal(t, reader.blocked, "second durable gap page")
	if err := broker.Publish(terminalEnvelope("interleaved transient")); err != nil {
		t.Fatal(err)
	}
	close(reader.release)

	for name, events := range map[string]<-chan eventstream.Envelope{
		"target": target.Events(), "sibling": sibling.Events(),
	} {
		got := receiveEnvelopes(t, events, 3)
		if got[0].Position == nil || got[0].Position.Durable == nil || got[0].Position.Durable.Seq != 2 {
			t.Fatalf("%s first event = %#v, want durable gap seq 2", name, got[0])
		}
		if got[1].Position == nil || got[1].Position.Transient == nil || got[1].Meta["terminal_output"] != "interleaved transient" {
			t.Fatalf("%s second event = %#v, want concurrent transient", name, got[1])
		}
		if got[2].Position == nil || got[2].Position.Durable == nil || got[2].Position.Durable.Seq != 3 {
			t.Fatalf("%s third event = %#v, want incoming durable seq 3", name, got[2])
		}
	}
	waitFeedAttachmentClosed(t, attached, "concurrent durable gap delivery")
	if err := target.Err(); err != nil {
		t.Fatalf("target error = %v", err)
	}
}

func TestFeedBrokerAttachToPropagatesPartialGapReadFailure(t *testing.T) {
	reader := newPartialGapFailureReader()
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 1})
	target, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	reader.arm()

	ingress := make(chan eventstream.Envelope, 1)
	ingress <- projectedEnvelope(2, "incoming")
	close(ingress)
	attached := broker.AttachTo(target, ingress)
	select {
	case err := <-attached:
		if err == nil || !strings.Contains(err.Error(), "partial durable gap read") {
			t.Fatalf("AttachTo() error = %v, want original partial read failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AttachTo() did not report partial durable gap failure")
	}
	if err := target.Err(); err == nil || !strings.Contains(err.Error(), "partial publication") {
		t.Fatalf("target error = %v, want explicit partial-publication disconnect", err)
	}
	waitFeedSubscriptionClosed(t, target, "partial-gap target")
}

func TestFeedBrokerAttachToRetriesEmptyHoldAfterRecoverableReadFailure(t *testing.T) {
	reader := &recoverablePageReader{failAt: 2}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 1})
	target, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	reader.events = []*session.Event{durableProtocolEvent(1, "retried target")}

	ingress := make(chan eventstream.Envelope, 1)
	ingress <- projectedEnvelope(1, "retried target")
	close(ingress)
	attached := broker.AttachTo(target, ingress)
	got := receiveEnvelopes(t, target.Events(), 1)[0]
	if got.Position == nil || got.Position.Durable == nil || got.Position.Durable.Seq != 1 {
		t.Fatalf("retried target envelope = %#v", got)
	}
	waitFeedAttachmentClosed(t, attached, "recoverable target read")
	if err := target.Err(); err != nil {
		t.Fatalf("target error after recoverable read = %v", err)
	}
}

func TestFeedBrokerAttachToRetriesUntargetedWhenTargetClosesAfterReceive(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{SubscriberQueue: 2})
	target, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sibling.Close()

	received := make(chan struct{})
	release := make(chan struct{})
	var hookOnce sync.Once
	broker.testBeforeAttachPublish = func() {
		hookOnce.Do(func() { close(received) })
		<-release
	}
	ingress := make(chan eventstream.Envelope, 1)
	ingress <- terminalEnvelope("must survive target handoff")
	close(ingress)
	attached := broker.AttachTo(target, ingress)
	waitCancellableReaderSignal(t, received, "attachment receive-before-publish seam")
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)

	got := receiveEnvelopes(t, sibling.Events(), 1)[0]
	if got.Meta["terminal_output"] != "must survive target handoff" {
		t.Fatalf("sibling handoff envelope = %#v", got)
	}
	waitFeedAttachmentClosed(t, attached, "receive-before-target-close handoff")
	waitFeedSubscriptionClosed(t, target, "receive-before-target-close target")
	if position, _ := broker.Boundary(); position == nil || position.Transient == nil || position.Transient.Sequence != 1 {
		t.Fatalf("handoff boundary = %#v, want one globally accepted transient", position)
	}
}

func TestFeedBrokerAttachToDisconnectsStalledSurfaceWithinBound(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{
		SubscriberQueue:        1,
		SubscriberStallTimeout: 20 * time.Millisecond,
	})
	defer broker.Close()
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ingress := make(chan eventstream.Envelope, 3)
	for _, text := range []string{"in flight", "queued", "must not wait forever"} {
		ingress <- terminalEnvelope(text)
	}
	close(ingress)
	attached := broker.AttachTo(subscription, ingress)

	deadline := time.Now().Add(time.Second)
	for !errors.Is(subscription.Err(), controlport.ErrSlowConsumer) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(subscription.Err(), controlport.ErrSlowConsumer) {
		t.Fatalf("stalled subscription error = %v, want slow consumer", subscription.Err())
	}
	select {
	case err, ok := <-attached:
		if ok && err != nil {
			t.Fatalf("AttachTo() error after bounded disconnect = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AttachTo() worker survived the subscriber stall timeout")
	}
	waitFeedSubscriptionClosed(t, subscription, "stalled subscription")
}

func TestFeedBrokerAttachToDoesNotRepublishGloballyAcceptedStalledEvent(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{
		SubscriberQueue:        1,
		SubscriberStallTimeout: 20 * time.Millisecond,
	})
	defer broker.Close()
	target, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	broker.queueSize = 3
	sibling, err := broker.SubscribeFromNow(context.Background())
	broker.queueSize = 1
	if err != nil {
		t.Fatal(err)
	}
	defer sibling.Close()

	ingress := make(chan eventstream.Envelope, 3)
	for _, text := range []string{"first", "second", "accepted-before-target-stall"} {
		ingress <- terminalEnvelope(text)
	}
	close(ingress)
	attached := broker.AttachTo(target, ingress)
	waitFeedAttachmentClosed(t, attached, "globally accepted target stall")
	if !errors.Is(target.Err(), controlport.ErrSlowConsumer) {
		t.Fatalf("target error = %v, want slow consumer", target.Err())
	}

	got := receiveEnvelopes(t, sibling.Events(), 3)
	for index, want := range []string{"first", "second", "accepted-before-target-stall"} {
		if got[index].Meta["terminal_output"] != want {
			t.Fatalf("sibling event %d = %#v, want %q", index, got[index], want)
		}
	}
	select {
	case duplicate := <-sibling.Events():
		t.Fatalf("globally accepted event was republished untargeted: %#v", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
	position, _ := broker.Boundary()
	if position == nil || position.Transient == nil || position.Transient.Sequence != 3 {
		t.Fatalf("boundary after target stall = %#v, want transient sequence 3", position)
	}
}

func TestFeedBrokerAttachToBoundsTargetHoldByEncodedBytes(t *testing.T) {
	reader := &mutablePageReader{events: []*session.Event{durableProtocolEvent(1, "baseline")}}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 1})
	target, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	broker.queueSize = 2
	sibling, err := broker.SubscribeFromNow(context.Background())
	broker.queueSize = 1
	if err != nil {
		t.Fatal(err)
	}
	defer sibling.Close()
	// Force the first durable gap Envelope to exceed the target-hold byte
	// budget. This bound is independent from the target's delivery queue.
	broker.ringBytes = 1
	reader.events = append(reader.events,
		durableProtocolEvent(2, "oversized target gap"),
		durableProtocolEvent(3, "incoming"),
	)
	ingress := make(chan eventstream.Envelope, 1)
	ingress <- projectedEnvelope(3, "incoming")
	close(ingress)
	attached := broker.AttachTo(target, ingress)
	waitFeedAttachmentClosed(t, attached, "encoded-byte bounded target hold")
	if !errors.Is(target.Err(), controlport.ErrSlowConsumer) {
		t.Fatalf("byte-bounded target error = %v, want slow consumer", target.Err())
	}
	got := receiveEnvelopes(t, sibling.Events(), 2)
	for index, want := range []uint64{2, 3} {
		if got[index].Position == nil || got[index].Position.Durable == nil || got[index].Position.Durable.Seq != want {
			t.Fatalf("sibling byte-bound event %d = %#v, want durable seq %d", index, got[index], want)
		}
	}
}

func TestFeedBrokerAttachToReleasesOnSubscriptionContextCancellation(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{
		SubscriberQueue:        1,
		SubscriberStallTimeout: time.Minute,
	})
	defer broker.Close()
	ctx, cancel := context.WithCancel(context.Background())
	subscription, err := broker.SubscribeFromNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ingress := make(chan eventstream.Envelope, 3)
	for _, text := range []string{"in flight", "queued", "blocked"} {
		ingress <- terminalEnvelope(text)
	}
	close(ingress)
	attached := broker.AttachTo(subscription, ingress)
	waitFeedTransientSequence(t, broker, 2)

	cancel()
	select {
	case <-attached:
	case <-time.After(time.Second):
		t.Fatal("subscription context cancellation did not release AttachTo")
	}
	waitFeedSubscriptionClosed(t, subscription, "context-cancelled subscription")
}

func TestFeedBrokerAttachToReleasesOnBrokerClose(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{
		SubscriberQueue:        1,
		SubscriberStallTimeout: time.Minute,
	})
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ingress := make(chan eventstream.Envelope, 3)
	for _, text := range []string{"in flight", "queued", "blocked"} {
		ingress <- terminalEnvelope(text)
	}
	close(ingress)
	attached := broker.AttachTo(subscription, ingress)
	waitFeedTransientSequence(t, broker, 2)

	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-attached:
	case <-time.After(time.Second):
		t.Fatal("broker Close did not release AttachTo")
	}
	waitFeedSubscriptionClosed(t, subscription, "broker-closed subscription")
}

func TestFeedBrokerAttachToCancelsBlockingDurableRead(t *testing.T) {
	reader := newCancellableBlockingPageReader(2)
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 1})
	defer broker.Close()
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ingress := make(chan eventstream.Envelope, 1)
	ingress <- projectedEnvelope(1, "blocked durable read")
	close(ingress)
	attached := broker.AttachTo(subscription, ingress)
	waitCancellableReaderSignal(t, reader.started, "durable EventsPage start")

	if err := subscription.Close(); err != nil {
		t.Fatal(err)
	}
	waitFeedAttachmentClosed(t, attached, "blocking durable read cancellation")
	waitCancellableReaderSignal(t, reader.exited, "durable EventsPage cancellation")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := broker.Prime(ctx); err != nil {
		t.Fatalf("Prime() after cancelled attachment = %v, want released sequencer", err)
	}
	if err := broker.Publish(terminalEnvelope("Session broker remains usable")); err != nil {
		t.Fatalf("Publish() after cancelled attachment = %v", err)
	}
}

func TestFeedBrokerCloseCancelsBlockingDurablePublish(t *testing.T) {
	reader := newCancellableBlockingPageReader(1)
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{})
	published := make(chan error, 1)
	go func() { published <- broker.Publish(projectedEnvelope(1, "blocked durable publish")) }()
	waitCancellableReaderSignal(t, reader.started, "broker-owned durable EventsPage start")

	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	waitCancellableReaderSignal(t, reader.exited, "broker-owned durable EventsPage cancellation")
	select {
	case err := <-published:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Publish() after broker Close = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker Close did not release blocking durable Publish")
	}
}

func TestFeedBrokerAttachToDetachesTargetWhileWaitingDurableSequencer(t *testing.T) {
	reader := newCancellableBlockingPageReader(2)
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 1})
	defer broker.Close()
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	primeCtx, cancelPrime := context.WithCancel(context.Background())
	primeResult := make(chan error, 1)
	go func() { primeResult <- broker.Prime(primeCtx) }()
	waitCancellableReaderSignal(t, reader.started, "competing durable sequencer holder")

	ingress := make(chan eventstream.Envelope, 1)
	ingress <- projectedEnvelope(1, "waiting for durable sequencer")
	close(ingress)
	attached := broker.AttachTo(subscription, ingress)
	deadline := time.Now().Add(time.Second)
	for len(ingress) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(ingress) != 0 {
		t.Fatal("AttachTo did not consume ingress before waiting for the durable sequencer")
	}

	if err := subscription.Close(); err != nil {
		t.Fatal(err)
	}
	waitFeedSubscriptionClosed(t, subscription, "durable sequencer target cancellation")
	// Target teardown must cancel its own wait immediately, but the ingress now
	// continues untargeted so Session publication is not lost. It therefore
	// remains sequenced behind the competing durable Prime until that owner
	// releases the global gate.
	cancelPrime()
	select {
	case err := <-primeResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("competing Prime() error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("competing Prime() did not release after context cancellation")
	}
	waitCancellableReaderSignal(t, reader.exited, "competing durable reader cancellation")
	waitFeedAttachmentClosed(t, attached, "untargeted durable sequencer continuation")
}

func TestFeedBrokerCloseDoesNotWaitForUnreadInternalSubscriber(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{SubscriberQueue: 1})
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Publish(terminalEnvelope("queued before close")); err != nil {
		t.Fatal(err)
	}

	closed := make(chan error, 1)
	go func() { closed <- broker.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() blocked on unread internal subscriber")
	}
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-subscription.Events():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("broker Close did not stop unread subscription")
		}
	}
}

func TestFeedBrokerAttachStartsImmediatelyAndChildTerminalDoesNotCloseSession(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{SubscriberQueue: 8})
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	ingress := make(chan eventstream.Envelope)
	broker.Attach(ingress)
	child := eventstream.TurnCompleted("child-handle", "child-run", "child-turn", time.Now())
	child.SessionID = "session-1"
	child.Scope = eventstream.ScopeSubagent
	main := terminalEnvelope("parent continues")
	ingress <- child
	ingress <- main
	close(ingress)
	got := receiveEnvelopes(t, subscription.Events(), 2)
	if got[0].Lifecycle == nil || got[0].Scope != eventstream.ScopeSubagent || got[1].Meta["terminal_output"] != "parent continues" {
		t.Fatalf("attached events = %#v", got)
	}
	if err := subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if err := broker.Publish(terminalEnvelope("runtime still live")); err != nil {
		t.Fatalf("subscriber Close affected broker: %v", err)
	}
}

func TestFeedBrokerAttachRetriesRecoverableDurablePublishFailure(t *testing.T) {
	reader := &recoverablePageReader{failAt: 2}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 8})
	subscription, err := broker.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	reader.events = []*session.Event{durableProtocolEvent(1, "retry durable child")}

	ingress := make(chan eventstream.Envelope, 1)
	attached := broker.Attach(ingress)
	ingress <- projectedEnvelope(1, "retry durable child")
	close(ingress)

	select {
	case got := <-subscription.Events():
		if got.EventID != "event-1" || got.Position == nil || got.Position.Durable == nil || got.Position.Durable.Seq != 1 {
			t.Fatalf("retried attached envelope = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("recoverable Attach publish failure permanently dropped the durable envelope")
	}
	if calls := reader.calls.Load(); calls < 2 {
		t.Fatalf("paged reader calls = %d, want failed attempt plus retry", calls)
	}
	if err := <-attached; err != nil {
		t.Fatalf("Attach() result = %v, want recovered delivery", err)
	}
}

func TestFeedBrokerAttachReportsPermanentPublishFailureAfterBoundedRetries(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{})
	ingress := make(chan eventstream.Envelope, 1)
	attached := broker.Attach(ingress)
	ingress <- eventstream.Envelope{
		Kind:      eventstream.KindNotice,
		SessionID: "foreign-session",
		Notice:    "invalid ingress",
	}
	close(ingress)

	select {
	case err := <-attached:
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("Attach() error = %v, want permanent session mismatch", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Attach() remained blocked after permanent publish failure")
	}
}

func TestFeedBrokerRestartPrimesDurableBoundaryAndBootstrap(t *testing.T) {
	reader := staticPageReader{events: []*session.Event{
		durableProtocolEvent(1, "before restart"),
		durableProtocolEvent(2, "after restart"),
	}}
	broker, codec := newTestFeedBroker(t, reader, FeedBrokerConfig{})

	if err := broker.Prime(context.Background()); err != nil {
		t.Fatalf("Prime() error = %v", err)
	}
	position, cursor := broker.Boundary()
	if position == nil || position.Durable == nil || position.Durable.Seq != 2 || cursor == "" {
		t.Fatalf("Boundary() = (%#v, %q), want durable seq 2", position, cursor)
	}
	decoded, err := codec.Decode("session-1", cursor)
	if err != nil {
		t.Fatalf("Decode(boundary) error = %v", err)
	}
	if decoded.Durable == nil || decoded.Durable.Seq != 2 {
		t.Fatalf("decoded boundary = %#v, want durable seq 2", decoded)
	}

	result, err := broker.Subscribe(context.Background(), controlport.SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer result.Subscription.Close()
	got := receiveEnvelopes(t, result.Subscription.Events(), 2)
	if got[0].Position == nil || got[0].Position.Durable == nil || got[0].Position.Durable.Seq != 1 ||
		got[1].Position == nil || got[1].Position.Durable == nil || got[1].Position.Durable.Seq != 2 {
		t.Fatalf("restart bootstrap positions = %#v", got)
	}
}

func TestFeedBrokerPrimeRefreshesDurableTailAfterInitialScan(t *testing.T) {
	reader := &mutablePageReader{}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{})
	if err := broker.Prime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if position, cursor := broker.Boundary(); position != nil || cursor != "" {
		t.Fatalf("initial Boundary() = (%#v, %q), want empty", position, cursor)
	}
	reader.events = append(reader.events, durableProtocolEvent(1, "committed before publish"))
	if err := broker.Prime(context.Background()); err != nil {
		t.Fatal(err)
	}
	position, cursor := broker.Boundary()
	if position == nil || position.Durable == nil || position.Durable.Seq != 1 || cursor == "" {
		t.Fatalf("refreshed Boundary() = (%#v, %q), want durable seq 1", position, cursor)
	}
}

func newTestFeedBroker(t *testing.T, reader session.PagedReader, override FeedBrokerConfig) (*FeedBroker, *eventstream.CursorCodec) {
	t.Helper()
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	override.SessionRef = session.SessionRef{SessionID: "session-1"}
	override.Reader = reader
	override.CursorCodec = codec
	if override.Generation == "" {
		override.Generation = "generation-1"
	}
	broker, err := NewFeedBroker(override)
	if err != nil {
		t.Fatal(err)
	}
	return broker, codec
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
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", EventID: "event-" + string(rune('0'+seq)),
		ProjectionID: eventstream.FormatProjectionID("event-"+string(rune('0'+seq)), 0),
		Position:     &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{Seq: seq}},
		Delivery:     &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: text},
		},
	}
}

func terminalEnvelope(text string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1",
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update:   schema.ToolCallUpdate{SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1"},
		Meta:     map[string]any{"terminal_output": text},
	}
}

func terminalEnvelopeAtCursor(text string, cursor int64) eventstream.Envelope {
	meta := metautil.WithTerminalOutput(nil, "command-1", text)
	meta = metautil.WithCompactRuntimeSection(meta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID:         "task-1",
		metautil.RuntimeTaskTerminalID: "terminal-1",
		"output_cursor":                cursor,
	})
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "command-1",
			Meta:          meta,
		},
	}
}

func terminalEnvelopeOutput(envelope eventstream.Envelope) (string, bool) {
	update, ok := envelope.Update.(schema.ToolCallUpdate)
	if !ok {
		return "", false
	}
	output, ok := metautil.TerminalOutput(update.Meta)
	return output.Data, ok
}

func receiveEnvelopes(t *testing.T, events <-chan eventstream.Envelope, count int) []eventstream.Envelope {
	t.Helper()
	out := make([]eventstream.Envelope, 0, count)
	for len(out) < count {
		select {
		case envelope, ok := <-events:
			if !ok {
				t.Fatalf("events closed after %d of %d", len(out), count)
			}
			out = append(out, envelope)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d of %d events", len(out), count)
		}
	}
	return out
}

func waitFeedTransientSequence(t *testing.T, broker *FeedBroker, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		position, _ := broker.Boundary()
		if position != nil && position.Transient != nil && position.Transient.Sequence >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	position, _ := broker.Boundary()
	t.Fatalf("feed transient boundary = %#v, want sequence >= %d", position, want)
}

func waitFeedSubscriptionClosed(t *testing.T, subscription controlport.FeedSubscription, name string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-subscription.Events():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("%s worker did not stop", name)
		}
	}
}

func waitFeedAttachmentClosed(t *testing.T, attached <-chan error, name string) {
	t.Helper()
	select {
	case err, ok := <-attached:
		if ok && err != nil {
			t.Fatalf("%s returned error: %v", name, err)
		}
	case <-time.After(time.Second):
		t.Fatalf("%s did not stop", name)
	}
}

func waitCancellableReaderSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

type staticPageReader struct{ events []*session.Event }

func (r staticPageReader) EventsPage(_ context.Context, req session.EventPageRequest) (session.EventPage, error) {
	return session.PageEvents(r.events, req), nil
}

type mutablePageReader struct{ events []*session.Event }

func (r *mutablePageReader) EventsPage(_ context.Context, req session.EventPageRequest) (session.EventPage, error) {
	return session.PageEvents(r.events, req), nil
}

type blockingPageReader struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
	events  []*session.Event
}

type recoverablePageReader struct {
	calls  atomic.Int32
	failAt int32
	events []*session.Event
}

type stagedGapPageReader struct {
	mu      sync.Mutex
	armed   bool
	calls   int
	blocked chan struct{}
	release chan struct{}
}

func newStagedGapPageReader() *stagedGapPageReader {
	return &stagedGapPageReader{blocked: make(chan struct{}), release: make(chan struct{})}
}

func (r *stagedGapPageReader) arm() {
	r.mu.Lock()
	r.armed = true
	r.calls = 0
	r.mu.Unlock()
}

func (r *stagedGapPageReader) EventsPage(ctx context.Context, _ session.EventPageRequest) (session.EventPage, error) {
	r.mu.Lock()
	if !r.armed {
		r.mu.Unlock()
		return session.EventPage{}, nil
	}
	r.calls++
	call := r.calls
	r.mu.Unlock()
	switch call {
	case 1:
		return session.EventPage{
			Events:  []*session.Event{durableProtocolEvent(2, "gap")},
			NextSeq: 2,
			HasMore: true,
		}, nil
	case 2:
		close(r.blocked)
		select {
		case <-r.release:
			return session.EventPage{
				Events:  []*session.Event{durableProtocolEvent(3, "incoming")},
				NextSeq: 3,
			}, nil
		case <-ctx.Done():
			return session.EventPage{}, ctx.Err()
		}
	default:
		return session.EventPage{}, nil
	}
}

type partialGapFailureReader struct {
	mu    sync.Mutex
	armed bool
	calls int
}

func newPartialGapFailureReader() *partialGapFailureReader { return &partialGapFailureReader{} }

func (r *partialGapFailureReader) arm() {
	r.mu.Lock()
	r.armed = true
	r.calls = 0
	r.mu.Unlock()
}

func (r *partialGapFailureReader) EventsPage(_ context.Context, _ session.EventPageRequest) (session.EventPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.armed {
		return session.EventPage{}, nil
	}
	r.calls++
	if r.calls == 1 {
		return session.EventPage{
			Events:  []*session.Event{durableProtocolEvent(1, "partial gap")},
			NextSeq: 1,
			HasMore: true,
		}, nil
	}
	return session.EventPage{}, errors.New("partial durable gap read")
}

type cancellableBlockingPageReader struct {
	blockAt   int32
	calls     atomic.Int32
	started   chan struct{}
	exited    chan struct{}
	startOnce sync.Once
	exitOnce  sync.Once
}

func newCancellableBlockingPageReader(blockAt int32) *cancellableBlockingPageReader {
	return &cancellableBlockingPageReader{
		blockAt: blockAt,
		started: make(chan struct{}),
		exited:  make(chan struct{}),
	}
}

func (r *cancellableBlockingPageReader) EventsPage(ctx context.Context, _ session.EventPageRequest) (session.EventPage, error) {
	if r.calls.Add(1) != r.blockAt {
		return session.EventPage{}, nil
	}
	r.startOnce.Do(func() { close(r.started) })
	<-ctx.Done()
	r.exitOnce.Do(func() { close(r.exited) })
	return session.EventPage{}, ctx.Err()
}

func (r *recoverablePageReader) EventsPage(_ context.Context, req session.EventPageRequest) (session.EventPage, error) {
	call := r.calls.Add(1)
	failAt := r.failAt
	if failAt == 0 {
		failAt = 1
	}
	if call == failAt {
		return session.EventPage{}, errors.New("recoverable durable reader failure")
	}
	return session.PageEvents(r.events, req), nil
}

func (r *blockingPageReader) EventsPage(ctx context.Context, req session.EventPageRequest) (session.EventPage, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-ctx.Done():
		return session.EventPage{}, ctx.Err()
	case <-r.release:
		return session.PageEvents(r.events, req), nil
	}
}
