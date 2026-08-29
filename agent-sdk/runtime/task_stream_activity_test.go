package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func TestTaskStreamActivityWatchClosesResolutionWindowAndReclaimsSignal(t *testing.T) {
	t.Parallel()

	tasks := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	watch := tasks.watchTaskStreamActivity("session-1", "task-1")
	tasks.notifyTaskStreamActivity("session-1", "task-1")
	if err := watch.Await(context.Background()); err != nil {
		t.Fatal(err)
	}
	tasks.mu.RLock()
	remaining := len(tasks.streamActivity)
	tasks.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("stream activity signals = %d, want reclaimed after last watcher", remaining)
	}

	// A notification with no observer must not accumulate one entry per Task.
	tasks.notifyTaskStreamActivity("session-1", "task-2")
	tasks.mu.RLock()
	remaining = len(tasks.streamActivity)
	tasks.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("unobserved stream activity signals = %d, want no retained entries", remaining)
	}
}

func TestSubagentTaskPersistsLatestACPContextUsage(t *testing.T) {
	t.Parallel()

	task := &subagentTask{ref: taskapi.Ref{TaskID: "task-1"}, sessionRef: session.SessionRef{SessionID: "parent-1"}}
	task.applyStreamFrames([]stream.Frame{
		{Event: &session.Event{ContextUsage: &session.ContextUsageSnapshot{Size: 200000, Used: 12000}, Invocation: &session.EventInvocation{Provider: "codex", Model: "gpt-test"}}},
		{Event: &session.Event{ContextUsage: &session.ContextUsageSnapshot{Size: 200000, Used: 42000}, Invocation: &session.EventInvocation{Provider: "codex", Model: "gpt-test"}}},
	})
	entry := task.entrySnapshot(time.Unix(1, 0))
	if entry.ContextUsage == nil || entry.ContextUsage.Snapshot.Used != 42000 || entry.ContextUsage.Snapshot.Size != 200000 {
		t.Fatalf("entry context usage = %#v, want latest gauge", entry.ContextUsage)
	}
	if entry.ContextUsage.Invocation.Provider != "codex" || entry.ContextUsage.Invocation.Model != "gpt-test" {
		t.Fatalf("entry invocation = %#v, want codex/gpt-test", entry.ContextUsage.Invocation)
	}
	task.mu.Lock()
	beginObservedSubagentActivityLocked(task)
	entry = task.entrySnapshot(time.Unix(2, 0))
	task.mu.Unlock()
	if entry.ContextUsage != nil {
		t.Fatalf("new activity retained prior context usage = %#v", entry.ContextUsage)
	}
}

func TestStreamSubscribeAwaitErrorReclaimsActivityWatch(t *testing.T) {
	t.Parallel()

	task := &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "session-1", TerminalID: "task-1:1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
	}
	tasks := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	tasks.subagents[task.ref.TaskID] = task
	service := newStreamService(tasks)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		for _, err := range service.Subscribe(ctx, stream.SubscribeRequest{
			Ref:    stream.Ref{SessionID: task.sessionRef.SessionID, TaskID: task.ref.TaskID},
			Follow: true,
		}) {
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	deadline := time.Now().Add(time.Second)
	for {
		tasks.mu.RLock()
		watchers := 0
		for _, signal := range tasks.streamActivity {
			watchers += signal.watchers
		}
		tasks.mu.RUnlock()
		if watchers == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Subscribe did not reserve an activity watcher before await")
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Subscribe error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not return after await cancellation")
	}
	tasks.mu.RLock()
	remaining := len(tasks.streamActivity)
	tasks.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("stream activity signals = %d, want await error to release watcher", remaining)
	}
}
