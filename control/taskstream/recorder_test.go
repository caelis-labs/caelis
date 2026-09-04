package taskstream

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/control/streamspool"
	spoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
)

func TestRecorderReleasesTerminalPartitionsAndCanRebindAfterRemoval(t *testing.T) {
	store, err := spoolfile.New(t.Context(), spoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	recorder := NewRecorder(store, nil)

	for index := range 128 {
		observer := recorder.BindTaskOutput(t.Context(), output.Binding{
			SessionID: "session-1", TaskID: fmt.Sprintf("task-%d", index),
			Kind: output.TaskKindCommand, StartsAtTaskOrigin: true,
		})
		if err := observer.ObserveTaskOutput(t.Context(), output.Event{
			OccurredAt: time.Now(), State: "completed", Closed: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	recorder.mu.Lock()
	retained := len(recorder.writers)
	recorder.mu.Unlock()
	if retained != 0 {
		t.Fatalf("terminal Recorder entries = %d, want 0", retained)
	}

	logical := streamspool.LogicalKey{
		Namespace: streamspool.NamespaceTask,
		Digest:    streamspool.DigestStrings("session-1", "task-0"),
	}
	key, _, err := store.Resolve(context.Background(), logical)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	observer := recorder.BindTaskOutput(t.Context(), output.Binding{
		SessionID: "session-1", TaskID: "task-0", Kind: output.TaskKindCommand, StartsAtTaskOrigin: false,
	})
	if err := observer.ObserveTaskOutput(t.Context(), output.Event{State: "completed", Closed: true}); err != nil {
		t.Fatalf("rebound terminal ObserveTaskOutput() error = %v", err)
	}
}

func TestRecorderReleasesFailedPartitionEntry(t *testing.T) {
	store, err := spoolfile.New(t.Context(), spoolfile.Config{
		RootDir: t.TempDir(), GCInterval: -1, MaxRecordBytes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	recorder := NewRecorder(store, nil)
	observer := recorder.BindTaskOutput(t.Context(), output.Binding{
		SessionID: "session-1", TaskID: "task-failed", Kind: output.TaskKindCommand, StartsAtTaskOrigin: true,
	})
	if err := observer.ObserveTaskOutput(t.Context(), output.Event{Text: "too large"}); err == nil {
		t.Fatal("ObserveTaskOutput unexpectedly succeeded")
	}
	recorder.mu.Lock()
	retained := len(recorder.writers)
	recorder.mu.Unlock()
	if retained != 0 {
		t.Fatalf("failed Recorder entries = %d, want 0", retained)
	}
}

func TestRecorderTaskReleaseBoundsRegistrationsByConcurrencyNotLifetime(t *testing.T) {
	store, err := spoolfile.New(t.Context(), spoolfile.Config{
		RootDir: t.TempDir(), GCInterval: -1, MaxRegistrations: 2, MaxPartitions: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	recorder := NewRecorder(store, nil)

	for index := range 16 {
		taskID := fmt.Sprintf("subagent-%d", index)
		observer := recorder.BindTaskOutput(t.Context(), output.Binding{
			SessionID: "session-1", TaskID: taskID, ActivityID: "activity-1",
			Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true,
		})
		if err := observer.ObserveTaskOutput(t.Context(), output.Event{Text: "final", State: "completed", Closed: true}); err != nil {
			t.Fatal(err)
		}
		logical := streamspool.LogicalKey{
			Namespace: streamspool.NamespaceTask,
			Digest:    streamspool.DigestStrings("session-1", taskID),
		}
		if _, _, err := store.Resolve(t.Context(), logical); err != nil {
			t.Fatalf("task %d did not obtain a spool partition: %v", index, err)
		}
		releaseCtx := t.Context()
		if index%2 == 0 {
			var cancel context.CancelFunc
			releaseCtx, cancel = context.WithCancel(t.Context())
			cancel()
		}
		if err := recorder.ReleaseTask(releaseCtx, taskapi.Ref{SessionID: "session-1", TaskID: taskID}); err != nil {
			t.Fatalf("ReleaseTask(%d): %v", index, err)
		}
	}
	recorder.mu.Lock()
	retained := len(recorder.writers)
	recorder.mu.Unlock()
	if retained != 0 {
		t.Fatalf("Recorder retained %d released Task writers", retained)
	}
}

func TestRecorderSessionReleaseClosesEveryOwnedTaskPartition(t *testing.T) {
	store, err := spoolfile.New(t.Context(), spoolfile.Config{
		RootDir: t.TempDir(), GCInterval: -1, MaxRegistrations: 2, MaxPartitions: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	recorder := NewRecorder(store, nil)
	for _, taskID := range []string{"task-1", "task-2"} {
		observer := recorder.BindTaskOutput(t.Context(), output.Binding{
			SessionID: "session-1", TaskID: taskID, Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true,
		})
		if err := observer.ObserveTaskOutput(t.Context(), output.Event{Text: taskID, Running: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := recorder.ReleaseSession(t.Context(), session.SessionRef{SessionID: "session-1"}); err != nil {
		t.Fatal(err)
	}
	observer := recorder.BindTaskOutput(t.Context(), output.Binding{
		SessionID: "session-2", TaskID: "next", Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true,
	})
	if err := observer.ObserveTaskOutput(t.Context(), output.Event{Text: "next", Running: true}); err != nil {
		t.Fatal(err)
	}
	logical := streamspool.LogicalKey{
		Namespace: streamspool.NamespaceTask,
		Digest:    streamspool.DigestStrings("session-2", "next"),
	}
	if _, _, err := store.Resolve(t.Context(), logical); err != nil {
		t.Fatalf("new Session could not reuse released registrations: %v", err)
	}
}

func TestRecorderProducerCloseReleasesSubagentPartition(t *testing.T) {
	store, err := spoolfile.New(t.Context(), spoolfile.Config{
		RootDir: t.TempDir(), GCInterval: -1, MaxRegistrations: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	recorder := NewRecorder(store, nil)
	observer := recorder.BindTaskOutput(t.Context(), output.Binding{
		SessionID: "session-1", TaskID: "subagent-1", Kind: output.TaskKindSubagent,
		StartsAtTaskOrigin: true,
	})
	if err := observer.ObserveTaskOutput(t.Context(), output.Event{Text: "partial", Running: true}); err != nil {
		t.Fatal(err)
	}
	if err := observer.ObserveTaskOutput(t.Context(), output.Event{ProducerClosed: true}); err != nil {
		t.Fatal(err)
	}

	next := recorder.BindTaskOutput(t.Context(), output.Binding{
		SessionID: "session-1", TaskID: "subagent-2", Kind: output.TaskKindSubagent,
		StartsAtTaskOrigin: true,
	})
	if err := next.ObserveTaskOutput(t.Context(), output.Event{Text: "next", Running: true}); err != nil {
		t.Fatalf("released producer kept the registration slot: %v", err)
	}
}
