package controlclient

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
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
	subscription, err := broker.SubscribeFromNow()
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
	reader := staticPageReader{events: []*session.Event{
		durableProtocolEvent(1, "one"),
		durableProtocolEvent(2, "two"),
	}}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 8})
	subscription, err := broker.SubscribeFromNow()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	if err := broker.Publish(projectedEnvelope(2, "two")); err != nil {
		t.Fatal(err)
	}
	got := receiveEnvelopes(t, subscription.Events(), 2)
	if ids := []string{got[0].EventID, got[1].EventID}; !reflect.DeepEqual(ids, []string{"event-1", "event-2"}) {
		t.Fatalf("gap-filled IDs = %v", ids)
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
	subscription, err := broker.SubscribeFromNow()
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
	subscription, err := broker.SubscribeFromNow()
	if err != nil {
		t.Fatal(err)
	}
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

func TestFeedBrokerAttachStartsImmediatelyAndChildTerminalDoesNotCloseSession(t *testing.T) {
	broker, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{SubscriberQueue: 8})
	subscription, err := broker.SubscribeFromNow()
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
	reader := &recoverablePageReader{events: []*session.Event{durableProtocolEvent(1, "retry durable child")}}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{SubscriberQueue: 8})
	subscription, err := broker.SubscribeFromNow()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	ingress := make(chan eventstream.Envelope, 1)
	broker.Attach(ingress)
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
	events []*session.Event
}

func (r *recoverablePageReader) EventsPage(_ context.Context, req session.EventPageRequest) (session.EventPage, error) {
	if r.calls.Add(1) == 1 {
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
