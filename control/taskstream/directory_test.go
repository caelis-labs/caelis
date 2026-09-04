package taskstream

import (
	"context"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestDirectoryIndexFansStatusToIndependentObserversAndReleasesSession(t *testing.T) {
	t.Parallel()

	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateRunning
	entry.Running = true
	entry.Metadata["child_activity_id"] = "activity-1"
	store := newTaskStreamTestStore(entry)
	index := NewDirectoryIndex()
	created, err := New(Config{
		Tasks:      store,
		Directory:  index,
		Authorizer: taskStreamTestAuthorizer{},
		Secret:     taskStreamTestSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	directory := created.(DirectoryService)
	first, err := directory.WatchDirectory(context.Background(), Principal{ID: "owner"}, DirectoryWatchRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := directory.WatchDirectory(context.Background(), Principal{ID: "owner"}, DirectoryWatchRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Subscription.Close()
	defer second.Subscription.Close()

	firstInitial := receiveDirectorySnapshot(t, first.Subscription)
	secondInitial := receiveDirectorySnapshot(t, second.Subscription)
	for _, snapshot := range []DirectorySnapshot{firstInitial, secondInitial} {
		if snapshot.Revision != 1 || len(snapshot.Tasks) != 1 || !snapshot.Tasks[0].Running || snapshot.Tasks[0].ActivityID != "activity-1" {
			t.Fatalf("initial directory snapshot = %#v", snapshot)
		}
	}
	if sessions, observers := directoryIndexCounts(index); sessions != 1 || observers != 2 {
		t.Fatalf("directory retention = %d Sessions/%d observers, want 1/2", sessions, observers)
	}

	outputOnly := task.CloneEntry(entry)
	outputOnly.Revision++
	outputOnly.UpdatedAt = time.Now()
	outputOnly.Result = map[string]any{"output_preview": "new chunk"}
	if err := store.Upsert(context.Background(), outputOnly); err != nil {
		t.Fatal(err)
	}
	index.Notify(outputOnly)
	if revision := index.revision("session-1"); revision != 1 {
		t.Fatalf("output-only Task commit advanced directory revision to %d, want 1", revision)
	}
	for _, subscription := range []DirectorySubscription{first.Subscription, second.Subscription} {
		select {
		case snapshot := <-subscription.Snapshots():
			t.Fatalf("output-only Task commit published directory snapshot %#v", snapshot)
		default:
		}
	}

	idle := task.CloneEntry(outputOnly)
	idle.Revision++
	idle.State = task.StateCompleted
	idle.Running = false
	idle.Metadata["turn_id"] = "task-1:1"
	if err := store.Upsert(context.Background(), idle); err != nil {
		t.Fatal(err)
	}
	index.Notify(idle)
	for _, subscription := range []DirectorySubscription{first.Subscription, second.Subscription} {
		snapshot := receiveDirectorySnapshot(t, subscription)
		if snapshot.Revision != 2 || len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Running || snapshot.Tasks[0].State != task.StateCompleted {
			t.Fatalf("idle directory snapshot = %#v", snapshot)
		}
	}

	runningAgain := task.CloneEntry(idle)
	runningAgain.Revision++
	runningAgain.State = task.StateRunning
	runningAgain.Running = true
	runningAgain.Metadata["turn_id"] = "task-1:2"
	runningAgain.Metadata["child_activity_id"] = "activity-2"
	if err := store.Upsert(context.Background(), runningAgain); err != nil {
		t.Fatal(err)
	}
	index.Notify(runningAgain)
	for _, subscription := range []DirectorySubscription{first.Subscription, second.Subscription} {
		snapshot := receiveDirectorySnapshot(t, subscription)
		if snapshot.Revision != 3 || len(snapshot.Tasks) != 1 || !snapshot.Tasks[0].Running || snapshot.Tasks[0].ActivityID != "activity-2" || snapshot.Tasks[0].CurrentTurnID != "task-1:2" {
			t.Fatalf("restarted directory snapshot = %#v", snapshot)
		}
	}

	if err := first.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if sessions, observers := directoryIndexCounts(index); sessions != 1 || observers != 1 {
		t.Fatalf("directory after first close = %d Sessions/%d observers, want 1/1", sessions, observers)
	}
	if err := second.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if sessions, observers := directoryIndexCounts(index); sessions != 0 || observers != 0 {
		t.Fatalf("directory after final close = %d Sessions/%d observers, want no retained state", sessions, observers)
	}
}

func TestDirectoryIndexDoesNotRetainUnobservedSessions(t *testing.T) {
	t.Parallel()

	index := NewDirectoryIndex()
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	index.Notify(entry)
	if sessions, observers := directoryIndexCounts(index); sessions != 0 || observers != 0 {
		t.Fatalf("unobserved directory retained %d Sessions/%d observers", sessions, observers)
	}
}

func receiveDirectorySnapshot(t *testing.T, subscription DirectorySubscription) DirectorySnapshot {
	t.Helper()
	select {
	case snapshot, open := <-subscription.Snapshots():
		if !open {
			t.Fatalf("directory subscription closed: %v", subscription.Err())
		}
		return snapshot
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Task directory snapshot")
		return DirectorySnapshot{}
	}
}

func directoryIndexCounts(index *DirectoryIndex) (sessions int, observers int) {
	index.mu.Lock()
	defer index.mu.Unlock()
	for _, state := range index.sessions {
		sessions++
		observers += len(state.watchers)
	}
	return sessions, observers
}

var _ task.Store = (*taskStreamTestStore)(nil)
