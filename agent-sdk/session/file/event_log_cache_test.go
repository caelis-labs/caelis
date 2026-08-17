package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

func TestAppendEventsUsesCompactIndexBeyondFullHistoryCacheLimit(t *testing.T) {
	t.Parallel()

	store, active := newEventPageIndexFixture(t, 8)
	store.eventLogCacheMaxBytes = 512
	documentPath, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(eventLogPath(documentPath))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= store.eventLogCacheLimitBytes() {
		t.Fatalf("fixture event log size = %d, want above compact-index threshold %d", info.Size(), store.eventLogCacheLimitBytes())
	}

	reads := 0
	store.eventLogLineRead = func(_ string, _ int, _ int64) { reads++ }
	appendLifecycleEvents(t, store, active.SessionRef, 8, 1)
	if reads != 8 {
		t.Fatalf("first oversized append decoded lines = %d, want one index build over 8 historical lines", reads)
	}

	reads = 0
	appendLifecycleEvents(t, store, active.SessionRef, 9, 1)
	if reads != 1 {
		t.Fatalf("second oversized append decoded lines = %d, want only one appended tail line", reads)
	}

	reads = 0
	retry := &session.Event{
		ID:         "event-0001",
		Type:       session.EventTypeLifecycle,
		Visibility: session.VisibilityCanonical,
		Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "event-0001"},
	}
	prior, err := store.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: retry})
	if err != nil {
		t.Fatalf("oversized idempotent AppendEvent() error = %v", err)
	}
	if prior.Seq != 1 || reads != 1 {
		t.Fatalf("oversized idempotent append = seq %d/%d decoded lines, want seq 1 plus one tail line", prior.Seq, reads)
	}
	conflict := session.CloneEvent(retry)
	conflict.Lifecycle.Reason = "different"
	if _, err := store.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: active.SessionRef,
		Event:      conflict,
	}); !errors.Is(err, session.ErrEventConflict) {
		t.Fatalf("oversized conflicting AppendEvent() error = %v, want event conflict", err)
	}
	if reads != 1 {
		t.Fatalf("warm compact-index conflict decoded %d lines, want no additional history scan", reads)
	}

	other := NewStore(Config{RootDir: store.rootDir})
	other.eventLogCacheMaxBytes = store.eventLogCacheMaxBytes
	appendLifecycleEvents(t, other, active.SessionRef, 10, 1)
	reads = 0
	appendLifecycleEvents(t, store, active.SessionRef, 11, 1)
	if reads != 1 {
		t.Fatalf("compact index after cross-Store append decoded lines = %d, want one external tail line", reads)
	}

	store.transactionFault = func(phase string) error {
		if phase == "after_event_log" {
			return errors.New("simulated process loss after oversized event append")
		}
		return nil
	}
	committed := &session.Event{
		ID:         "event-0013",
		Type:       session.EventTypeLifecycle,
		Visibility: session.VisibilityCanonical,
		Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "event-0013"},
	}
	if _, err := store.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: active.SessionRef,
		Event:      committed,
	}); !session.IsCommitted(err) {
		t.Fatalf("oversized AppendEvent() after event-log fault = %v, want committed error", err)
	}
	store.transactionFault = nil
	loaded, err := store.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	committedCount := 0
	for _, event := range loaded.Events {
		if event.ID == committed.ID {
			committedCount++
		}
	}
	if committedCount != 1 {
		t.Fatalf("recovered oversized event count = %d, want exactly one", committedCount)
	}
}

func TestCompactIndexColdBuildDoesNotBlockLeaseHeartbeat(t *testing.T) {
	store, active := newEventPageIndexFixture(t, 8)
	store.eventLogCacheMaxBytes = 512
	lease, err := store.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "runtime-1", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	indexStarted := make(chan struct{})
	releaseIndex := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseIndex) }) }
	defer release()
	var blockOnce sync.Once
	store.eventLogLineRead = func(_ string, _ int, _ int64) {
		blockOnce.Do(func() {
			close(indexStarted)
			<-releaseIndex
		})
	}
	appendDone := make(chan error, 1)
	go func() {
		_, appendErr := store.AppendEvent(context.Background(), session.AppendEventRequest{
			SessionRef: active.SessionRef,
			MutationGuard: session.MutationGuard{
				Authority: session.MutationAuthorityRuntime,
				LeaseID:   lease.LeaseID, OwnerID: lease.OwnerID, FencingToken: lease.FencingToken,
			},
			Event: &session.Event{
				ID: "event-0009", Type: session.EventTypeLifecycle, Visibility: session.VisibilityCanonical,
				Lifecycle: &session.EventLifecycle{Status: "completed", Reason: "event-0009"},
			},
		})
		appendDone <- appendErr
	}()
	select {
	case <-indexStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("cold compact-index build did not start")
	}

	heartbeatCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	heartbeat, heartbeatErr := store.HeartbeatSessionLease(heartbeatCtx, session.HeartbeatSessionLeaseRequest{
		SessionRef: active.SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID,
		ExpectedLeaseRevision: lease.Revision, TTL: time.Minute,
	})
	cancel()
	if heartbeatErr != nil {
		release()
		t.Fatalf("HeartbeatSessionLease() while cold index is blocked = %v", heartbeatErr)
	}
	if heartbeat.Revision != lease.Revision+1 {
		release()
		t.Fatalf("heartbeat revision = %d, want %d", heartbeat.Revision, lease.Revision+1)
	}

	release()
	if err := <-appendDone; err != nil {
		t.Fatalf("AppendEvent() after cold index release = %v", err)
	}
}

func TestCompactIndexColdBuildAcceptsConcurrentAppendOnlyGrowth(t *testing.T) {
	store, active := newEventPageIndexFixture(t, 8)
	store.eventLogCacheMaxBytes = 512
	indexStarted := make(chan struct{})
	releaseIndex := make(chan struct{})
	var blockOnce sync.Once
	reads := 0
	store.eventLogLineRead = func(_ string, _ int, _ int64) {
		reads++
		blockOnce.Do(func() {
			close(indexStarted)
			<-releaseIndex
		})
	}
	appendDone := make(chan error, 1)
	go func() {
		_, appendErr := store.AppendEvent(context.Background(), session.AppendEventRequest{
			SessionRef: active.SessionRef,
			Event: &session.Event{
				ID: "event-0010", Type: session.EventTypeLifecycle, Visibility: session.VisibilityCanonical,
				Lifecycle: &session.EventLifecycle{Status: "completed", Reason: "event-0010"},
			},
		})
		appendDone <- appendErr
	}()
	select {
	case <-indexStarted:
	case <-time.After(5 * time.Second):
		close(releaseIndex)
		t.Fatal("cold compact-index build did not start")
	}

	other := NewStore(Config{RootDir: store.rootDir})
	other.eventLogCacheMaxBytes = store.eventLogCacheMaxBytes
	appendLifecycleEvents(t, other, active.SessionRef, 8, 1)
	close(releaseIndex)
	if err := <-appendDone; err != nil {
		t.Fatal(err)
	}
	if reads != 9 {
		t.Fatalf("cold build plus locked catch-up decoded %d lines, want initial 8 plus one concurrent tail", reads)
	}
	checkpoint, err := store.EventCheckpoint(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.ThroughSeq != 10 {
		t.Fatalf("checkpoint through seq = %d, want 10", checkpoint.ThroughSeq)
	}
}

func TestOversizedWALRecoveryDoesNotScanFullLogDuringLeaseHeartbeat(t *testing.T) {
	store, active := newEventPageIndexFixture(t, 8)
	store.eventLogCacheMaxBytes = 512
	lease, err := store.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "runtime-1", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.transactionFault = func(phase string) error {
		if phase == "after_commit" {
			return errors.New("simulated process loss after WAL commit")
		}
		return nil
	}
	if _, err := store.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: active.SessionRef,
		MutationGuard: session.MutationGuard{
			Authority: session.MutationAuthorityRuntime,
			LeaseID:   lease.LeaseID, OwnerID: lease.OwnerID, FencingToken: lease.FencingToken,
		},
		Event: &session.Event{
			ID: "event-0009", Type: session.EventTypeLifecycle, Visibility: session.VisibilityCanonical,
			Lifecycle: &session.EventLifecycle{Status: "completed", Reason: "event-0009"},
		},
	}); !session.IsCommitted(err) {
		t.Fatalf("AppendEvent() after WAL fault = %v, want committed error", err)
	}

	reopened := NewStore(Config{RootDir: store.rootDir})
	reopened.eventLogCacheMaxBytes = store.eventLogCacheMaxBytes
	reads := 0
	reopened.eventLogLineRead = func(_ string, _ int, _ int64) { reads++ }
	heartbeat, err := reopened.HeartbeatSessionLease(context.Background(), session.HeartbeatSessionLeaseRequest{
		SessionRef: active.SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID,
		ExpectedLeaseRevision: lease.Revision, TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.Revision != lease.Revision+1 {
		t.Fatalf("heartbeat revision = %d, want %d", heartbeat.Revision, lease.Revision+1)
	}
	if reads != 0 {
		t.Fatalf("WAL recovery during heartbeat decoded %d full-log lines, want bounded tail recovery", reads)
	}

	reopened.eventLogLineRead = nil
	loaded, err := reopened.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 9 || loaded.Events[8].ID != "event-0009" {
		t.Fatalf("recovered events = %#v, want committed event-0009 exactly once", loaded.Events)
	}
}

func TestEventAppendIndexIsStrictlyBounded(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{})
	for i := 0; i < maxEventAppendIndexes+3; i++ {
		store.storeEventAppendIndex(string(rune('a'+i)), &eventAppendIndex{bytes: 1})
	}
	if len(store.eventAppendIndexes) != maxEventAppendIndexes {
		t.Fatalf("event append index count = %d, want %d", len(store.eventAppendIndexes), maxEventAppendIndexes)
	}
	if store.eventAppendIndexBytes != int64(maxEventAppendIndexes) {
		t.Fatalf("event append index bytes = %d, want %d", store.eventAppendIndexBytes, maxEventAppendIndexes)
	}
	store.storeEventAppendIndex("oversized", &eventAppendIndex{bytes: maxEventAppendIndexBytes + 1})
	if _, ok := store.eventAppendIndexes["oversized"]; ok {
		t.Fatal("oversized event append index was cached")
	}
}

func TestPrewarmedEventAppendIndexSurvivesCacheEvictionAndAdmissionLimit(t *testing.T) {
	store, active := newEventPageIndexFixture(t, 8)
	store.eventLogCacheMaxBytes = 512
	reads := 0
	store.eventLogLineRead = func(_ string, _ int, _ int64) { reads++ }
	prewarm, err := store.prewarmEventAppendIndexContext(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	documentPath, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	seed := prewarm.indexFor(documentPath)
	if prewarm == nil || seed == nil || reads != 8 {
		t.Fatalf("prewarm = %#v with %d reads, want one 8-line snapshot", prewarm, reads)
	}
	logPath := filepath.Clean(eventLogPath(documentPath))
	for i := 0; i < maxEventAppendIndexes; i++ {
		store.storeEventAppendIndex(fmt.Sprintf("eviction-%d", i), &eventAppendIndex{bytes: 1})
	}
	if _, ok := store.eventAppendIndexes[logPath]; ok {
		t.Fatal("prewarmed index was not evicted from bounded cache")
	}

	reads = 0
	index, err := store.readEventAppendIndexContext(context.Background(), documentPath, seed)
	if err != nil {
		t.Fatal(err)
	}
	if index.lastSeq != 8 || reads != 0 {
		t.Fatalf("evicted prewarm = seq %d/%d reads, want seq 8 without rescan", index.lastSeq, reads)
	}

	oversized := cloneEventAppendIndex(index)
	oversized.bytes = maxEventAppendIndexBytes + 1
	store.removeEventAppendIndex(logPath)
	reads = 0
	index, err = store.readEventAppendIndexContext(context.Background(), documentPath, oversized)
	if err != nil {
		t.Fatal(err)
	}
	if index.lastSeq != 8 || reads != 0 {
		t.Fatalf("over-limit prewarm = seq %d/%d reads, want seq 8 without rescan", index.lastSeq, reads)
	}
	if _, ok := store.eventAppendIndexes[logPath]; ok {
		t.Fatal("over-limit prewarmed index was admitted to bounded cache")
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
