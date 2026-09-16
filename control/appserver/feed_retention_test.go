package appserver

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/streamspool"
	streamspoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
)

func retentionStore(t *testing.T) *streamspoolfile.Store {
	t.Helper()
	s, err := streamspoolfile.New(t.Context(), streamspoolfile.Config{RootDir: t.TempDir(), GCInterval: -1, MaxBytes: 64 << 10, MaxStreamBytes: 12 << 10, SegmentBytes: 2 << 10, PartitionAllocationCharge: 128, SegmentAllocationCharge: 64})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func retentionPressure(t *testing.T, s streamspool.Store) {
	t.Helper()
	for n := 0; n < 16; n++ {
		w, err := s.Register(t.Context(), streamspool.LogicalKey{Namespace: streamspool.NamespaceTask, Digest: streamspool.DigestStrings("pressure", fmt.Sprint(n))}, streamspool.WriterOptions{OriginComplete: true})
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 32; j++ {
			if _, err = w.Append(t.Context(), 1, time.Now(), make([]byte, 1024)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func retentionBroker(t *testing.T, s streamspool.Store, id string, r session.PagedReader) *FeedBroker {
	t.Helper()
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFeedBroker(FeedBrokerConfig{SessionRef: session.SessionRef{SessionID: id}, Spool: s, Reader: r, CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func retentionApproval(id, call string) eventstream.Envelope {
	return eventstream.Envelope{Kind: eventstream.KindApprovalReview, SessionID: id, Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}, ApprovalReview: &eventstream.ApprovalReview{ToolCallID: call, ToolName: "RunCommand", Status: "approved", Text: "approved"}}
}

func TestFeedRetentionNewSessionAfterGlobalPressure(t *testing.T) {
	s := retentionStore(t)
	retentionPressure(t, s)
	b := retentionBroker(t, s, "new-session", nil)
	if err := b.Publish(retentionApproval("new-session", "new-call")); err != nil {
		t.Fatal(err)
	}
	b.acceptMu.Lock()
	spoolErr := b.spoolErr
	b.acceptMu.Unlock()
	if spoolErr != nil {
		t.Fatal(spoolErr)
	}
	result, err := b.Subscribe(t.Context(), SubscribeRequest{SessionID: "new-session"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	events := receiveFeedEvents(t, result.Subscription, 1)
	if len(events) != 1 || events[0].ApprovalReview == nil || events[0].ApprovalReview.Status != "approved" {
		t.Fatalf("approval missing: %#v", events)
	}
	t.Log("512 KiB written through a 64 KiB global budget; a new Session delivers approved")
}

type retentionPausedStore struct {
	streamspool.Store
	key     streamspool.Key
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}
type retentionPausedReader struct {
	streamspool.Reader
	owner *retentionPausedStore
	once  sync.Once
}

func (s *retentionPausedStore) Reader(ctx context.Context, key streamspool.Key, offset streamspool.Offset) (streamspool.Reader, error) {
	r, err := s.Store.Reader(ctx, key, offset)
	if err == nil && key == s.key {
		r = &retentionPausedReader{Reader: r, owner: s}
	}
	return r, err
}
func (r *retentionPausedReader) Next(ctx context.Context) (streamspool.Record, error) {
	r.once.Do(func() {
		first := false
		r.owner.once.Do(func() { first = true; close(r.owner.entered) })
		if first {
			select {
			case <-ctx.Done():
			case <-r.owner.release:
			}
		}
	})
	return r.Reader.Next(ctx)
}

// A barrier holds a real file reader behind its retained low-water mark while
// another namespace exhausts the global budget. Recovery must not poison the
// shared producer, including when another observer already follows its tail.
func TestFeedRetentionReplacesExpiredReaderAndResumesApprovals(t *testing.T) {
	s := retentionStore(t)
	paused := &retentionPausedStore{Store: s, entered: make(chan struct{}), release: make(chan struct{})}
	canonical := &checkpointPageReader{active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}
	decision := &session.Event{ID: "decision", Seq: 1, Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal,
		Journal: &session.ExecutionJournalEntry{Kind: session.JournalKindPauseToken, PauseToken: &session.PauseToken{
			Status: session.PauseTokenResolved, TurnID: "turn-1", ToolCallID: "before-pressure", ToolName: "RunCommand", Approved: true, ReviewText: "approved",
		}},
	}
	b := retentionBroker(t, paused, "session-1", canonical)
	canonical.setEvents(decision)
	paused.key = b.key
	if err := b.Prime(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := b.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	select {
	case <-paused.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("reader did not reach barrier")
	}
	retentionPressure(t, s)
	bounds, err := b.writer.Bounds(t.Context())
	if err != nil || bounds.Low == 0 {
		t.Fatalf("expected actual eviction: %+v %v", bounds, err)
	}
	// A fresh attachment can already recover from the durable decision.
	healthy, err := b.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer healthy.Subscription.Close()
	assertRetainedDecision(t, receiveFeedReplacement(t, healthy.Subscription))
	close(paused.release)
	assertRetainedDecision(t, receiveFeedReplacement(t, result.Subscription))
	b.acceptMu.Lock()
	spoolErr := b.spoolErr
	b.acceptMu.Unlock()
	if spoolErr != nil {
		t.Fatalf("reader expiry disabled shared writer: %v", spoolErr)
	}
	for _, status := range []string{"approved", "denied"} {
		event := retentionApproval("session-1", "after-pressure-"+status)
		event.ApprovalReview.Status = status
		if err := b.Publish(event); err != nil {
			t.Fatal(err)
		}
		for _, sub := range []FeedSubscription{result.Subscription, healthy.Subscription} {
			events := receiveFeedEvents(t, sub, 1)
			if len(events) != 1 || events[0].ApprovalReview == nil || events[0].ApprovalReview.Status != status {
				t.Fatalf("live review missing after recovery: %#v", events)
			}
		}
	}
}

func assertRetainedDecision(t *testing.T, events []eventstream.Envelope) {
	t.Helper()
	if len(events) != 1 || events[0].ApprovalReview == nil || events[0].ApprovalReview.ToolCallID != "before-pressure" || events[0].ApprovalReview.Status != "approved" || events[0].TurnID != "turn-1" {
		t.Fatalf("durable review missing from replacement: %#v", events)
	}
}

// Block replacement after its cut is fixed; events accepted while the snapshot
// is being read must arrive once in the resumed exact suffix.
func TestFeedRetentionKeepsEventsAcceptedDuringReplacement(t *testing.T) {
	s := retentionStore(t)
	paused := &retentionPausedStore{Store: s, entered: make(chan struct{}), release: make(chan struct{})}
	canonical := &retentionReplacementReader{checkpointPageReader: &checkpointPageReader{}, entered: make(chan struct{}), release: make(chan struct{})}
	b := retentionBroker(t, paused, "session-1", canonical)
	paused.key = b.key
	first := durableProtocolEvent(1, "before pressure")
	canonical.setEvents(first)
	if err := b.Prime(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := b.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	select {
	case <-paused.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("reader did not reach barrier")
	}
	retentionPressure(t, s)
	close(paused.release)
	begin := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceBegin)
	select {
	case <-canonical.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement did not reach barrier")
	}
	second := durableProtocolEvent(2, "committed during replacement")
	canonical.setEvents(first, second)
	if err := b.Publish(retentionApproval("session-1", "during-replacement")); err != nil {
		t.Fatal(err)
	}
	if err := b.Prime(t.Context()); err != nil {
		t.Fatal(err)
	}
	close(canonical.release)
	page := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplacePage)
	if page.SnapshotID != begin.SnapshotID || len(page.Events) != 1 || page.Events[0].Position.Durable.Seq != 1 {
		t.Fatalf("replacement crossed accepted cut: %#v", page)
	}
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryReplaceEnd)
	assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliverySync)
	events := receiveFeedEvents(t, result.Subscription, 2)
	if len(events) != 2 || events[0].ApprovalReview == nil || events[0].ApprovalReview.ToolCallID != "during-replacement" || events[1].Position.Durable.Seq != 2 {
		t.Fatalf("suffix lost or duplicated: %#v", events)
	}
}

type retentionReplacementReader struct {
	*checkpointPageReader
	entered, release chan struct{}
	once             sync.Once
}

func (r *retentionReplacementReader) EventsPage(ctx context.Context, req session.EventPageRequest) (session.EventPage, error) {
	if req.AfterSeq == 0 && req.ThroughSeq > 0 {
		first := false
		r.once.Do(func() { first = true; close(r.entered) })
		if first {
			select {
			case <-ctx.Done():
				return session.EventPage{}, ctx.Err()
			case <-r.release:
			}
		}
	}
	return r.checkpointPageReader.EventsPage(ctx, req)
}

func TestFeedRetentionDuringInitialHistoryScan(t *testing.T) {
	s := retentionStore(t)
	paused := &retentionPausedStore{Store: s, entered: make(chan struct{}), release: make(chan struct{})}
	canonical := &checkpointPageReader{}
	b := retentionBroker(t, paused, "session-1", canonical)
	paused.key = b.key
	canonical.setEvents(feedIdentifiedNarrative(1, "message-1", "turn-1", "retained history"))
	if err := b.Prime(t.Context()); err != nil {
		t.Fatal(err)
	}
	type result struct {
		subscription SubscribeResult
		err          error
	}
	ready := make(chan result, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		r, err := b.Subscribe(ctx, SubscribeRequest{SessionID: "session-1", HistoryTurns: 1})
		ready <- result{r, err}
	}()
	select {
	case <-paused.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("history scan did not reach barrier")
	}
	retentionPressure(t, s)
	close(paused.release)
	var got result
	select {
	case got = <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("subscribe did not recover from retention")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	defer got.subscription.Subscription.Close()
	events := receiveFeedReplacement(t, got.subscription.Subscription)
	if len(events) != 1 || events[0].TurnID != "turn-1" {
		t.Fatalf("history scan recovery = %#v", events)
	}
	if err := b.Publish(retentionApproval("session-1", "new-approval")); err != nil {
		t.Fatal(err)
	}
	if events := receiveFeedEvents(t, got.subscription.Subscription, 1); events[0].ApprovalReview == nil {
		t.Fatalf("live approval missing: %#v", events)
	}
}
