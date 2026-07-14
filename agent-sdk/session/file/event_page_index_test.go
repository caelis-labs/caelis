package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestEventsPageCheckpointDoesNotRereadConsumedPrefix(t *testing.T) {
	t.Parallel()

	store, active := newEventPageIndexFixture(t, 450)
	var lines []int
	store.eventPageLineRead = func(_ string, lineNo int, _ int64) {
		lines = append(lines, lineNo)
	}
	first, err := store.EventsPage(context.Background(), session.EventPageRequest{
		SessionRef: active.SessionRef, Limit: 100, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextSeq != 100 || !first.HasMore || len(first.Events) != 100 {
		t.Fatalf("first page = %#v, want seq 100 with more", first)
	}
	if len(lines) != 101 || lines[0] != 1 || lines[len(lines)-1] != 101 {
		t.Fatalf("first page physical lines = %#v, want 1..101", lines)
	}

	lines = nil
	second, err := store.EventsPage(context.Background(), session.EventPageRequest{
		SessionRef: active.SessionRef, AfterSeq: first.NextSeq, Limit: 100, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.NextSeq != 200 || !second.HasMore || len(second.Events) != 100 {
		t.Fatalf("second page = %#v, want seq 200 with more", second)
	}
	if len(lines) != 101 || lines[0] != 101 || lines[len(lines)-1] != 201 {
		t.Fatalf("second page physical lines = %#v, want 101..201 without prefix reread", lines)
	}
}

func TestEventsPageCheckpointSurvivesCrossStoreAppend(t *testing.T) {
	t.Parallel()

	store, active := newEventPageIndexFixture(t, 220)
	page, err := store.EventsPage(context.Background(), session.EventPageRequest{
		SessionRef: active.SessionRef, Limit: 300, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextSeq != 220 || page.HasMore {
		t.Fatalf("initial page = %#v, want complete seq 220", page)
	}

	other := NewStore(Config{RootDir: store.rootDir})
	appendLifecycleEvents(t, other, active.SessionRef, 220, 3)
	var lines []int
	store.eventPageLineRead = func(_ string, lineNo int, _ int64) {
		lines = append(lines, lineNo)
	}
	page, err = store.EventsPage(context.Background(), session.EventPageRequest{
		SessionRef: active.SessionRef, AfterSeq: 220, Limit: 10, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextSeq != 223 || page.HasMore || len(page.Events) != 3 {
		t.Fatalf("appended page = %#v, want seq 221..223", page)
	}
	if len(lines) != 3 || lines[0] != 221 || lines[2] != 223 {
		t.Fatalf("cross-Store append physical lines = %#v, want only 221..223", lines)
	}
}

func TestEventsPageCheckpointInvalidatesAfterTruncate(t *testing.T) {
	t.Parallel()

	store, active := newEventPageIndexFixture(t, 240)
	first, err := store.EventsPage(context.Background(), session.EventPageRequest{
		SessionRef: active.SessionRef, Limit: 100, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextSeq != 100 {
		t.Fatalf("first.NextSeq = %d, want 100", first.NextSeq)
	}
	documentPath, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	logPath := eventLogPath(documentPath)
	index := store.eventPageIndexes[logPath]
	checkpoint, ok := eventPageCheckpointForSeq(index, 100)
	if !ok {
		t.Fatalf("checkpoint seq 100 missing: %#v", index)
	}
	if err := rollbackEventLogAppend(logPath, checkpoint.Offset); err != nil {
		t.Fatal(err)
	}

	var lines []int
	store.eventPageLineRead = func(_ string, lineNo int, _ int64) {
		lines = append(lines, lineNo)
	}
	page, err := store.EventsPage(context.Background(), session.EventPageRequest{
		SessionRef: active.SessionRef, AfterSeq: 100, Limit: 10, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 0 || page.NextSeq != 100 || page.HasMore {
		t.Fatalf("page after truncate = %#v, want empty at seq 100", page)
	}
	if len(lines) != 100 || lines[0] != 1 || lines[len(lines)-1] != 100 {
		t.Fatalf("truncate rebuild physical lines = %#v, want safe scan from line 1", lines)
	}
}

func TestEventsPageCheckpointValidatesAnchorBeforeSeek(t *testing.T) {
	t.Parallel()

	store, active := newEventPageIndexFixture(t, 140)
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
	index := store.eventPageIndexes[logPath]
	checkpoint, ok := eventPageCheckpointForSeq(index, first.NextSeq)
	if !ok {
		t.Fatalf("checkpoint seq %d missing", first.NextSeq)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := append([]byte(nil), data[checkpoint.AnchorStart:checkpoint.Offset]...)
	changed := bytes.Replace(anchor, []byte("event-0100"), []byte("other-0100"), 1)
	if bytes.Equal(anchor, changed) || len(anchor) != len(changed) {
		t.Fatal("failed to construct same-size valid anchor rewrite")
	}
	copy(data[checkpoint.AnchorStart:checkpoint.Offset], changed)
	if err := os.WriteFile(logPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Restore the cached mtime so this test exercises the anchor hash rather
	// than the cheaper same-size timestamp invalidation.
	if err := os.Chtimes(logPath, index.modTime, index.modTime); err != nil {
		t.Fatal(err)
	}

	var lines []int
	store.eventPageLineRead = func(_ string, lineNo int, _ int64) {
		lines = append(lines, lineNo)
	}
	page, err := store.EventsPage(context.Background(), session.EventPageRequest{
		SessionRef: active.SessionRef, AfterSeq: 100, Limit: 1, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Seq != 101 || !page.HasMore {
		t.Fatalf("page after anchor rewrite = %#v, want seq 101 with more", page)
	}
	if len(lines) == 0 || lines[0] != 1 {
		t.Fatalf("anchor mismatch read lines = %#v, want safe rebuild from line 1", lines)
	}
}

func TestEventsPageCheckpointScanHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	store, active := newEventPageIndexFixture(t, 400)
	ctx, cancel := context.WithCancel(context.Background())
	reads := 0
	store.eventPageLineRead = func(_ string, _ int, _ int64) {
		reads++
		if reads == 10 {
			cancel()
		}
	}
	_, err := store.EventsPage(ctx, session.EventPageRequest{
		SessionRef: active.SessionRef, AfterSeq: 300, Limit: 10, Visibility: session.EventPageAllDurable,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EventsPage() error = %v, want context canceled", err)
	}
	if reads != 10 {
		t.Fatalf("physical reads before cancellation = %d, want 10", reads)
	}
}

func TestEventsPageCheckpointBoundsProductionShapedLiveChildBurst(t *testing.T) {
	store, active := newEventPageIndexFixture(t, 73)
	page, err := store.EventsPage(context.Background(), session.EventPageRequest{
		SessionRef: active.SessionRef, ThroughSeq: 73, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextSeq != 73 || len(page.Events) != 73 {
		t.Fatalf("initial page = seq %d/events %d, want 73/73", page.NextSeq, len(page.Events))
	}
	documentPath, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(eventLogPath(documentPath), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	reads := 0
	store.eventPageLineRead = func(_ string, _ int, _ int64) { reads++ }

	// The observed demo produced child mirror seq 73..913 in roughly nine
	// seconds. Model that 840-event live tail without paying 840 fsyncs in the
	// test: each ingress append is followed by the same bounded AfterSeq read.
	for seq := uint64(74); seq <= 913; seq++ {
		event := &session.Event{
			Schema:     session.EventSchemaVersion,
			ID:         fmt.Sprintf("live-child-%04d", seq),
			SessionID:  active.SessionID,
			Seq:        seq,
			Type:       session.EventTypeLifecycle,
			Visibility: session.VisibilityCanonical,
			Lifecycle:  &session.EventLifecycle{Status: "running", Reason: "child mirror"},
		}
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
		page, err = store.EventsPage(context.Background(), session.EventPageRequest{
			SessionRef: active.SessionRef, AfterSeq: seq - 1, ThroughSeq: seq,
			Visibility: session.EventPageAllDurable,
		})
		if err != nil {
			t.Fatalf("EventsPage(seq %d) error = %v", seq, err)
		}
		if len(page.Events) != 1 || page.NextSeq != seq || page.Events[0].Seq != seq {
			t.Fatalf("EventsPage(seq %d) = %#v, want one exact live event", seq, page)
		}
	}
	if reads != 840 {
		t.Fatalf("live child burst physical line reads = %d, want 840 (one per appended event)", reads)
	}
}

func newEventPageIndexFixture(t testing.TB, count int) (*Store, session.Session) {
	t.Helper()
	store := NewStore(Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "event-page-index-session" },
	})
	active, err := store.GetOrCreate(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	appendLifecycleEvents(t, store, active.SessionRef, 0, count)
	return store, active
}

func appendLifecycleEvents(t testing.TB, store *Store, ref session.SessionRef, start int, count int) {
	t.Helper()
	events := make([]*session.Event, count)
	for i := range events {
		n := start + i + 1
		events[i] = &session.Event{
			ID:         fmt.Sprintf("event-%04d", n),
			Type:       session.EventTypeLifecycle,
			Visibility: session.VisibilityCanonical,
			Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: fmt.Sprintf("event-%04d", n)},
		}
	}
	if _, err := store.AppendEvents(context.Background(), session.AppendEventsRequest{
		SessionRef: ref,
		Events:     events,
	}); err != nil {
		t.Fatal(err)
	}
}

func eventPageCheckpointForSeq(index *eventPageIndex, seq uint64) (eventPageCheckpoint, bool) {
	if index == nil {
		return eventPageCheckpoint{}, false
	}
	for _, checkpoint := range index.checkpoints {
		if checkpoint.Seq == seq {
			return checkpoint, true
		}
	}
	return eventPageCheckpoint{}, false
}
