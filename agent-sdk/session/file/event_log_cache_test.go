package file

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestAppendEventsCacheReadsHistoryOnceThenOnlyAppendedTail(t *testing.T) {
	t.Parallel()

	store, active := newEventPageIndexFixture(t, 800)
	reads := 0
	store.eventLogLineRead = func(_ string, _ int, _ int64) { reads++ }

	appendLifecycleEvents(t, store, active.SessionRef, 800, 1)
	if reads != 800 {
		t.Fatalf("first warm append decoded lines = %d, want 800 once", reads)
	}
	reads = 0
	appendLifecycleEvents(t, store, active.SessionRef, 801, 1)
	if reads != 1 {
		t.Fatalf("second append decoded lines = %d, want only appended tail line", reads)
	}

	reads = 0
	if _, err := store.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef}); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("cache catch-up decoded lines = %d, want one newly committed line", reads)
	}
	other := NewStore(Config{RootDir: store.rootDir})
	appendLifecycleEvents(t, other, active.SessionRef, 802, 1)
	reads = 0
	appendLifecycleEvents(t, store, active.SessionRef, 803, 1)
	if reads != 1 {
		t.Fatalf("append after cross-Store commit decoded lines = %d, want external tail only", reads)
	}
}

func TestEventLogCacheInvalidatesAfterTruncateAndKeepsResultsIsolated(t *testing.T) {
	t.Parallel()

	store, active := newEventPageIndexFixture(t, 200)
	events, err := store.Events(context.Background(), session.EventsRequest{
		SessionRef: active.SessionRef, IncludeTransient: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 200 {
		t.Fatalf("Events() = %d, want 200", len(events))
	}
	events[0].Lifecycle.Reason = "caller mutation"
	again, err := store.Events(context.Background(), session.EventsRequest{
		SessionRef: active.SessionRef, IncludeTransient: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Lifecycle.Reason == "caller mutation" {
		t.Fatal("public Events result mutated the Store-local cache")
	}

	first, err := store.EventsPage(context.Background(), session.EventPageRequest{
		SessionRef: active.SessionRef, Limit: 100, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	documentPath, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	logPath := eventLogPath(documentPath)
	checkpoint, ok := eventPageCheckpointForSeq(store.eventPageIndexes[logPath], first.NextSeq)
	if !ok {
		t.Fatalf("checkpoint seq %d missing", first.NextSeq)
	}
	if err := rollbackEventLogAppend(store.durability, logPath, checkpoint.Offset); err != nil {
		t.Fatal(err)
	}

	reads := 0
	store.eventLogLineRead = func(_ string, _ int, _ int64) { reads++ }
	again, err = store.Events(context.Background(), session.EventsRequest{
		SessionRef: active.SessionRef, IncludeTransient: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 100 || reads != 100 {
		t.Fatalf("Events() after truncate = %d events/%d decoded lines, want 100/100 rebuild", len(again), reads)
	}
}

func TestEventLogCacheIsStrictlyBounded(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{})
	store.eventLogCaches = map[string]*eventLogCache{}
	for i := 0; i < maxEventLogCaches+3; i++ {
		store.storeEventLogCache(string(rune('a'+i)), &eventLogCache{size: 1})
	}
	if len(store.eventLogCaches) != maxEventLogCaches {
		t.Fatalf("event log cache count = %d, want %d", len(store.eventLogCaches), maxEventLogCaches)
	}
	if store.eventLogCacheBytes != int64(maxEventLogCaches) {
		t.Fatalf("event log cache bytes = %d, want %d", store.eventLogCacheBytes, maxEventLogCaches)
	}
	store.storeEventLogCache("oversized", &eventLogCache{size: maxEventLogCacheBytes + 1})
	if _, ok := store.eventLogCaches["oversized"]; ok {
		t.Fatal("oversized event log was cached")
	}
}

func TestEventLogCacheFillHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	store, active := newEventPageIndexFixture(t, 400)
	ctx, cancel := context.WithCancel(context.Background())
	reads := 0
	store.eventLogLineRead = func(_ string, _ int, _ int64) {
		reads++
		if reads == 10 {
			cancel()
		}
	}
	_, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Events() error = %v, want context canceled", err)
	}
	if reads != 10 {
		t.Fatalf("decoded lines before cancellation = %d, want 10", reads)
	}
	if len(store.eventLogCaches) != 0 {
		t.Fatalf("canceled fill retained %d cache entries, want 0", len(store.eventLogCaches))
	}
}

func TestEventLogCachePreservesIdempotencyAndConflictChecks(t *testing.T) {
	t.Parallel()

	store, active := newEventPageIndexFixture(t, 800)
	if _, err := store.Events(context.Background(), session.EventsRequest{
		SessionRef: active.SessionRef, IncludeTransient: true,
	}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	store.eventLogLineRead = func(_ string, _ int, _ int64) { reads++ }
	retry := &session.Event{
		ID:         "event-0001",
		Type:       session.EventTypeLifecycle,
		Visibility: session.VisibilityCanonical,
		Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "event-0001"},
	}
	prior, err := store.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: retry})
	if err != nil {
		t.Fatalf("idempotent AppendEvent() error = %v", err)
	}
	if prior.Seq != 1 || reads != 0 {
		t.Fatalf("idempotent append = seq %d/%d decoded lines, want 1/0", prior.Seq, reads)
	}
	conflict := session.CloneEvent(retry)
	conflict.Lifecycle.Reason = "different"
	if _, err := store.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: conflict}); !errors.Is(err, session.ErrEventConflict) {
		t.Fatalf("conflicting AppendEvent() error = %v, want event conflict", err)
	}
	if reads != 0 {
		t.Fatalf("warm-cache conflict decoded %d lines, want 0", reads)
	}
}

func BenchmarkAppendEventWarmCache835(b *testing.B) {
	store, active := newEventPageIndexFixture(b, 835)
	if _, err := store.Events(context.Background(), session.EventsRequest{
		SessionRef: active.SessionRef, IncludeTransient: true,
	}); err != nil {
		b.Fatal(err)
	}
	retry := &session.Event{
		ID:         "event-0001",
		Type:       session.EventTypeLifecycle,
		Visibility: session.VisibilityCanonical,
		Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "event-0001"},
	}
	b.ReportMetric(835, "history_events")
	b.ResetTimer()
	for range b.N {
		if _, err := store.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: retry}); err != nil {
			b.Fatal(err)
		}
	}
}
