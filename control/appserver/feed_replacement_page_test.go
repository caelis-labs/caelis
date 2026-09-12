package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

func TestFeedReplacementPageBuilderPacksMultipleEnvelopesAndFlushesRemainder(t *testing.T) {
	t.Parallel()

	builder := newFeedReplacementPageBuilder("snapshot-1")
	builder.maxEvents = 2
	var pages []FeedDelivery
	for i := 1; i <= 5; i++ {
		flushed, ok, err := builder.add(testPackedEnvelope(fmt.Sprintf("event-%d", i), "n"))
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			pages = append(pages, flushed)
		}
	}
	if remainder, ok := builder.flush(); ok {
		pages = append(pages, remainder)
	}
	if _, ok := builder.flush(); ok {
		t.Fatal("second flush emitted an empty page")
	}
	if len(pages) != 3 || builder.nextPage() != 3 {
		t.Fatalf("pages = %d next=%d, want 3 pages", len(pages), builder.nextPage())
	}
	wantCounts := []int{2, 2, 1}
	for i, page := range pages {
		if page.Kind != FeedDeliveryReplacePage || page.Source != FeedSourceReplacement || page.SnapshotID != "snapshot-1" || page.Page != uint32(i) {
			t.Fatalf("page %d = %#v", i, page)
		}
		if len(page.Events) != wantCounts[i] {
			t.Fatalf("page %d event count = %d, want %d", i, len(page.Events), wantCounts[i])
		}
		if page.Events[0].EventID != fmt.Sprintf("event-%d", 2*i+1) {
			t.Fatalf("page %d first event = %q, want event-%d", i, page.Events[0].EventID, 2*i+1)
		}
	}
	if pages[2].Events[0].EventID != "event-5" {
		t.Fatalf("final flush lost remainder: %#v", pages[2])
	}
}

func TestFeedReplacementPageBuilderSplitsOnByteLimit(t *testing.T) {
	t.Parallel()

	first := testPackedEnvelope("event-1", "aa")
	second := testPackedEnvelope("event-2", "bb")
	rawFirst, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	rawSecond, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	builder := newFeedReplacementPageBuilder("snapshot-1")
	builder.maxBytes = len(rawFirst) + len(rawSecond) - 1
	if flushed, ok, err := builder.add(first); err != nil || ok {
		t.Fatalf("first add = (%#v, %v, %v)", flushed, ok, err)
	}
	flushed, ok, err := builder.add(second)
	if err != nil || !ok {
		t.Fatalf("byte split = (%#v, %v, %v)", flushed, ok, err)
	}
	if flushed.Page != 0 || len(flushed.Events) != 1 || flushed.Events[0].EventID != "event-1" {
		t.Fatalf("flushed page = %#v", flushed)
	}
	remainder, ok := builder.flush()
	if !ok || remainder.Page != 1 || len(remainder.Events) != 1 || remainder.Events[0].EventID != "event-2" {
		t.Fatalf("remainder = (%#v, %v)", remainder, ok)
	}
}

func TestFeedReplacementPageBuilderEmitsOversizedEnvelopeAlone(t *testing.T) {
	t.Parallel()

	small := testPackedEnvelope("event-1", "n")
	large := testPackedEnvelope("event-2", strings.Repeat("x", 64))
	rawLarge, err := json.Marshal(large)
	if err != nil {
		t.Fatal(err)
	}
	builder := newFeedReplacementPageBuilder("snapshot-1")
	builder.maxBytes = len(rawLarge) - 1
	if flushed, ok, err := builder.add(small); err != nil || ok {
		t.Fatalf("small add = (%#v, %v, %v)", flushed, ok, err)
	}
	flushed, ok, err := builder.add(large)
	if err != nil || !ok || flushed.Page != 0 || len(flushed.Events) != 1 || flushed.Events[0].EventID != "event-1" {
		t.Fatalf("transport overflow = (%#v, %v, %v)", flushed, ok, err)
	}
	remainder, ok := builder.flush()
	if !ok || remainder.Page != 1 || len(remainder.Events) != 1 || remainder.Events[0].EventID != "event-2" {
		t.Fatalf("solo oversized page = (%#v, %v)", remainder, ok)
	}
}

func TestFeedReplacementPageBuilderMarshalErrorDoesNotFlushPending(t *testing.T) {
	t.Parallel()

	builder := newFeedReplacementPageBuilder("snapshot-1")
	builder.maxEvents = 2
	if flushed, ok, err := builder.add(testPackedEnvelope("event-1", "n")); err != nil || ok {
		t.Fatalf("first add = (%#v, %v, %v)", flushed, ok, err)
	}
	bad := testPackedEnvelope("event-2", "n")
	bad.Meta = map[string]any{"ch": make(chan int)}
	if flushed, ok, err := builder.add(bad); err == nil || ok || flushed.Kind != "" {
		t.Fatalf("marshal error = (%#v, %v, %v)", flushed, ok, err)
	}
	remainder, ok := builder.flush()
	if !ok || remainder.Page != 0 || len(remainder.Events) != 1 || remainder.Events[0].EventID != "event-1" {
		t.Fatalf("marshal error dropped or committed pending: (%#v, %v)", remainder, ok)
	}
}

func TestFeedBrokerCanonicalReplacementPacksStorePages(t *testing.T) {
	t.Parallel()

	const count = 250
	events := numberedDurableProtocolEvents(count)
	result := subscribeSealedCanonicalReplacement(t, events)
	begin, end, pages, assembled := collectCanonicalReplacement(t, result.Subscription)
	if begin.SnapshotID == "" || end.SnapshotID != begin.SnapshotID || end.Page != 1 || len(pages) != 1 {
		t.Fatalf("packed replacement begin=%#v end=%#v pages=%d", begin, end, len(pages))
	}
	if len(pages[0].Events) != count {
		t.Fatalf("packed page count = %d, want %d envelopes in one page (was one envelope per page)", len(pages[0].Events), count)
	}
	assertCursorlessReplacementPages(t, begin.SnapshotID, pages)
	assertReplacementMatchesProjection(t, events, assembled)
	if err := result.Subscription.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestFeedBrokerCanonicalReplacementSplitsOnCountAndFlushesFinalPage(t *testing.T) {
	t.Parallel()

	count := feedReplacementPageEventLimit + 1
	events := numberedDurableProtocolEvents(count)
	result := subscribeSealedCanonicalReplacement(t, events)
	begin, end, pages, assembled := collectCanonicalReplacement(t, result.Subscription)
	if len(pages) != 2 || end.Page != 2 || end.SnapshotID != begin.SnapshotID {
		t.Fatalf("count split pages=%d end=%#v", len(pages), end)
	}
	if len(pages[0].Events) != feedReplacementPageEventLimit || pages[0].Page != 0 {
		t.Fatalf("first packed page = %#v", pages[0])
	}
	if len(pages[1].Events) != 1 || pages[1].Page != 1 || pages[1].Events[0].EventID != fmt.Sprintf("event-%d", count) {
		t.Fatalf("final flush page = %#v", pages[1])
	}
	assertCursorlessReplacementPages(t, begin.SnapshotID, pages)
	if len(assembled) != count || assembled[0].EventID != "event-1" || assembled[len(assembled)-1].EventID != fmt.Sprintf("event-%d", count) {
		t.Fatalf("assembled = %d events, first=%q last=%q", len(assembled), assembled[0].EventID, assembled[len(assembled)-1].EventID)
	}
	assertReplacementMatchesProjection(t, events, assembled)
}

func TestFeedBrokerCanonicalReplacementSplitsOnByteLimit(t *testing.T) {
	t.Parallel()

	events := []*session.Event{
		largeDurableProtocolEvent(1, 700<<10),
		largeDurableProtocolEvent(2, 700<<10),
	}
	result := subscribeSealedCanonicalReplacement(t, events)
	begin, end, pages, assembled := collectCanonicalReplacement(t, result.Subscription)
	if len(pages) != 2 || end.Page != 2 {
		t.Fatalf("byte split pages=%d end=%#v", len(pages), end)
	}
	for i, page := range pages {
		if len(page.Events) != 1 || page.Page != uint32(i) || page.Events[0].EventID != fmt.Sprintf("event-%d", i+1) {
			t.Fatalf("byte page %d = %#v", i, page)
		}
		raw, err := json.Marshal(page.Events[0])
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) <= feedReplacementPageByteLimit/2 || len(raw) > maxFeedReplacementPageBytes {
			t.Fatalf("byte page %d encoded size = %d", i, len(raw))
		}
	}
	assertCursorlessReplacementPages(t, begin.SnapshotID, pages)
	assertReplacementMatchesProjection(t, events, assembled)
}

func TestFeedBrokerCanonicalReplacementErrorDoesNotCommitUnflushedPage(t *testing.T) {
	t.Parallel()

	inner := &checkpointPageReader{active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}
	inner.setEvents(numberedDurableProtocolEvents(250)...)
	reader := &limitedPageReader{checkpointPageReader: inner, remaining: 1, err: errors.New("canonical page failed")}
	result := subscribeSealedCanonicalReplacementReader(t, reader)
	deliveries := drainReplacementUntilClose(t, result.Subscription)
	if len(deliveries) != 1 || deliveries[0].Kind != FeedDeliveryReplaceBegin {
		t.Fatalf("unflushed error deliveries = %#v, want only begin", deliveries)
	}
	if err := result.Subscription.Err(); err == nil || !strings.Contains(err.Error(), "canonical page failed") {
		t.Fatalf("subscription error = %v", err)
	}
}

func TestFeedBrokerCanonicalReplacementErrorDoesNotCommitFlushedPrefix(t *testing.T) {
	t.Parallel()

	inner := &checkpointPageReader{active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}
	inner.setEvents(numberedDurableProtocolEvents(500)...)
	reader := &limitedPageReader{checkpointPageReader: inner, remaining: 2, err: errors.New("canonical page failed")}
	result := subscribeSealedCanonicalReplacementReader(t, reader)
	deliveries := drainReplacementUntilClose(t, result.Subscription)
	if len(deliveries) != 2 || deliveries[0].Kind != FeedDeliveryReplaceBegin || deliveries[1].Kind != FeedDeliveryReplacePage {
		t.Fatalf("flushed-prefix error deliveries = %#v", deliveries)
	}
	if deliveries[1].Page != 0 || len(deliveries[1].Events) != feedReplacementPageEventLimit {
		t.Fatalf("flushed prefix page = %#v", deliveries[1])
	}
	for _, delivery := range deliveries {
		if delivery.Kind == FeedDeliveryReplaceEnd || delivery.Kind == FeedDeliverySync {
			t.Fatalf("error path committed replacement: %#v", delivery)
		}
	}
	if err := result.Subscription.Err(); err == nil || !strings.Contains(err.Error(), "canonical page failed") {
		t.Fatalf("subscription error = %v", err)
	}
}

func TestFeedBrokerCanonicalReplacementPreservesCompleteProjectedOrder(t *testing.T) {
	t.Parallel()

	events := numberedDurableProtocolEvents(300)
	suppressed := &session.Event{
		ID: "child-frame", SessionID: "session-1", Seq: 150,
		Type: session.EventTypeAssistant, Visibility: session.VisibilityMirror,
		ChildOrigin: &session.EventChildOrigin{Scope: session.EventChildScopeSubagent, ScopeID: "task-1", TaskID: "task-1"},
	}
	events = append(events[:149], append([]*session.Event{suppressed}, events[149:]...)...)
	for i, event := range events {
		event.Seq = uint64(i + 1)
	}
	result := subscribeSealedCanonicalReplacement(t, events)
	_, end, pages, assembled := collectCanonicalReplacement(t, result.Subscription)
	if len(pages) != 2 || end.Page != 2 {
		t.Fatalf("ordered replacement pages=%d end=%#v", len(pages), end)
	}
	assertReplacementMatchesProjection(t, events, assembled)
	for _, envelope := range assembled {
		if envelope.EventID == "child-frame" {
			t.Fatalf("suppressed child frame entered replacement: %#v", envelope)
		}
		if envelope.Cursor != "" {
			t.Fatalf("replacement envelope carried a cursor: %#v", envelope)
		}
	}
}

func testPackedEnvelope(id, notice string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindNotice, EventID: id, SessionID: "session-1", Notice: notice,
		Position: &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{Seq: 1}},
	}
}

func numberedDurableProtocolEvents(count int) []*session.Event {
	events := make([]*session.Event, count)
	for i := range events {
		seq := uint64(i + 1)
		events[i] = durableProtocolEvent(seq, fmt.Sprintf("message-%d", seq))
		events[i].ID = fmt.Sprintf("event-%d", seq)
	}
	return events
}

func largeDurableProtocolEvent(seq uint64, size int) *session.Event {
	event := durableProtocolEvent(seq, strings.Repeat("x", size))
	event.ID = fmt.Sprintf("event-%d", seq)
	return event
}

func subscribeSealedCanonicalReplacement(t *testing.T, events []*session.Event) SubscribeResult {
	t.Helper()
	reader := &checkpointPageReader{active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}
	reader.setEvents(events...)
	return subscribeSealedCanonicalReplacementReader(t, reader)
}

func subscribeSealedCanonicalReplacementReader(t *testing.T, reader session.PagedReader) SubscribeResult {
	t.Helper()
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
	if err := broker.Seal(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := broker.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Subscription.Close() })
	return result
}

func collectCanonicalReplacement(t *testing.T, sub FeedSubscription) (begin, end FeedDelivery, pages []FeedDelivery, events []eventstream.Envelope) {
	t.Helper()
	assembler := new(FeedDeliveryAssembler)
	begin = assertFeedDelivery(t, sub.Deliveries(), FeedDeliveryReplaceBegin)
	if _, _, err := assembler.Accept(begin); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case delivery, open := <-sub.Deliveries():
			if !open {
				t.Fatalf("replacement closed before commit: %v", sub.Err())
			}
			assembled, replaced, err := assembler.Accept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			switch delivery.Kind {
			case FeedDeliveryReplacePage:
				if replaced || len(assembled) != 0 {
					t.Fatalf("page became visible before end: %#v", delivery)
				}
				pages = append(pages, delivery)
			case FeedDeliveryReplaceEnd:
				if !replaced {
					t.Fatal("replace end did not commit")
				}
				end = delivery
				events = assembled
				assertFeedDelivery(t, sub.Deliveries(), FeedDeliverySync)
				return begin, end, pages, events
			default:
				t.Fatalf("unexpected delivery %#v", delivery)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("replacement did not commit")
		}
	}
}

func drainReplacementUntilClose(t *testing.T, sub FeedSubscription) []FeedDelivery {
	t.Helper()
	assembler := new(FeedDeliveryAssembler)
	var out []FeedDelivery
	for {
		select {
		case delivery, open := <-sub.Deliveries():
			if !open {
				if !assembler.Pending() {
					t.Fatal("error path committed or never began replacement")
				}
				return out
			}
			_, replaced, err := assembler.Accept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			if replaced {
				t.Fatalf("error path committed replacement: %#v", delivery)
			}
			out = append(out, delivery)
		case <-time.After(2 * time.Second):
			t.Fatal("subscription did not close after replacement error")
		}
	}
}

func assertCursorlessReplacementPages(t *testing.T, snapshotID string, pages []FeedDelivery) {
	t.Helper()
	for i, page := range pages {
		if page.Kind != FeedDeliveryReplacePage || page.Source != FeedSourceReplacement || page.SnapshotID != snapshotID || page.Page != uint32(i) || page.NextCursor != "" {
			t.Fatalf("page %d identity = %#v", i, page)
		}
		if len(page.Events) == 0 || len(page.Events) > feedReplacementPageEventLimit {
			t.Fatalf("page %d event count = %d", i, len(page.Events))
		}
		pageBytes := 0
		for _, envelope := range page.Events {
			if envelope.Cursor != "" {
				t.Fatalf("page %d carried a cursor: %#v", i, envelope)
			}
			if envelope.Position == nil || envelope.Position.Durable == nil {
				t.Fatalf("page %d lost durable provenance: %#v", i, envelope)
			}
			raw, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			pageBytes += len(raw)
		}
		if len(page.Events) > 1 && pageBytes > feedReplacementPageByteLimit {
			t.Fatalf("page %d packed %d bytes, want <= %d", i, pageBytes, feedReplacementPageByteLimit)
		}
	}
}

func assertReplacementMatchesProjection(t *testing.T, events []*session.Event, assembled []eventstream.Envelope) {
	t.Helper()
	ref := session.SessionRef{SessionID: "session-1"}
	var through eventstream.DurableFeedPosition
	if last := events[len(events)-1]; last != nil {
		through.Seq = last.Seq
	}
	want := projectedCanonicalEnvelopes(ref, events, through)
	if len(assembled) != len(want) {
		t.Fatalf("assembled %d envelopes, want projected %d", len(assembled), len(want))
	}
	for i := range want {
		if assembled[i].EventID != want[i].EventID || assembled[i].Kind != want[i].Kind || assembled[i].Cursor != "" {
			t.Fatalf("assembled[%d] = %#v, want %#v", i, assembled[i], want[i])
		}
	}
}

func projectedCanonicalEnvelopes(ref session.SessionRef, events []*session.Event, through eventstream.DurableFeedPosition) []eventstream.Envelope {
	var out []eventstream.Envelope
	for _, event := range events {
		if event == nil || suppressHistoricalChildStreamMirror(event) {
			continue
		}
		base := projection.EnvelopeBaseFromSessionEvent(ref, event, projection.SessionEventTransport{})
		for _, envelope := range projection.ProjectSessionEventEnvelope(base, event) {
			if envelope.Position == nil || envelope.Position.Durable == nil || compareDurablePosition(*envelope.Position.Durable, through) > 0 {
				continue
			}
			envelope.Cursor = ""
			out = append(out, envelope)
		}
	}
	return out
}

type limitedPageReader struct {
	*checkpointPageReader
	mu        sync.Mutex
	remaining int
	err       error
}

func (r *limitedPageReader) EventsPage(ctx context.Context, req session.EventPageRequest) (session.EventPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.remaining <= 0 {
		return session.EventPage{}, r.err
	}
	r.remaining--
	return r.checkpointPageReader.EventsPage(ctx, req)
}
