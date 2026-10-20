package file

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestLargeJournalReceiptSurvivesColdAppendAndReopen(t *testing.T) {
	store, active := newLargeJournalFixture(t, 1)
	root := store.rootDir
	store = NewStore(Config{RootDir: root})
	// Exercise the same cold compact-index path as a log crossing 32 MiB,
	// without making the correctness fixture depend on machine throughput.
	store.eventLogCacheMaxBytes = 1024
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	receipt := session.NewModelInvocationReceipt(model.Invocation{
		ID: "large-journal-receipt", Provider: "test", Model: "test", Outcome: "completed",
	}, "chat")
	persisted, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: receipt})
	if err != nil {
		t.Fatal(err)
	}
	message := model.NewTextMessage(model.RoleAssistant, "final reply after the screenshot")
	if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{
		SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeAssistant, Message: &message},
	}); err != nil {
		t.Fatal(err)
	}
	reopened := NewStore(Config{RootDir: root})
	loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 3 || loaded.Events[1].ID != persisted.ID || !session.IsModelInvocationReceipt(loaded.Events[1]) {
		t.Fatal("cold append or reopen lost the journal, receipt or final reply")
	}
	if !bytes.Equal(loaded.Events[0].Journal.ToolExecution.Result, largeJournalResult()) {
		t.Fatal("journal result changed across persistence")
	}
	got, ok := session.ModelMessageOf(loaded.Events[2])
	want, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil || !ok || !bytes.Equal(raw, want) {
		t.Fatal("reopened final reply differs from the produced model message")
	}
	// Retrying the original receipt must return its original durable identity.
	again, err := reopened.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: receipt})
	if err != nil || again.ID != persisted.ID || again.Seq != persisted.Seq {
		t.Fatalf("receipt retry = %#v, %v", again, err)
	}
}

func newLargeJournalFixture(t testing.TB, count int) (*Store, session.Session) {
	t.Helper()
	store, active := newEventPageIndexFixture(t, 0)
	result := largeJournalResult()
	events := make([]*session.Event, 0, count)
	for i := range count {
		record := session.NormalizeToolExecution(session.ToolExecution{
			Key:      session.ExecutionKey{SessionID: active.SessionID, RunID: "run", TurnID: "turn", StepID: fmt.Sprintf("step-%d", i), ToolCallID: fmt.Sprintf("call-%d", i)},
			Revision: 1, Status: session.ToolExecutionSucceeded, ToolName: "screenshot", Result: result,
		})
		events = append(events, &session.Event{
			ID: fmt.Sprintf("journal-%d", i), Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal,
			Journal: &session.ExecutionJournalEntry{Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindToolExecution, ToolExecution: &record},
		})
	}
	if _, err := store.AppendEvents(context.Background(), session.AppendEventsRequest{SessionRef: active.SessionRef, Events: events}); err != nil {
		t.Fatal(err)
	}
	return store, active
}

func largeJournalResult() json.RawMessage {
	return json.RawMessage(`{"image":"` + strings.Repeat("A", 1<<20) + `","integer":900719925474099312345678901234567890,"decimal":1.00}`)
}

func BenchmarkColdReceiptAppendLargeJournal(b *testing.B) {
	store, active := newLargeJournalFixture(b, 33)
	path, err := store.resolveWritePath(active)
	if err != nil {
		b.Fatal(err)
	}
	info, err := os.Stat(eventLogPath(path))
	if err != nil {
		b.Fatal(err)
	}
	if info.Size() <= maxEventLogCacheBytes {
		b.Fatal("fixture must exceed the production full-history cache limit")
	}
	b.SetBytes(info.Size())
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		cold := NewStore(Config{RootDir: store.rootDir})
		receipt := session.NewModelInvocationReceipt(model.Invocation{
			ID: fmt.Sprintf("receipt-%d", i), Provider: "test", Model: "test", Outcome: "completed",
		}, "chat")
		if _, err := cold.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: receipt}); err != nil {
			b.Fatal(err)
		}
	}
}
