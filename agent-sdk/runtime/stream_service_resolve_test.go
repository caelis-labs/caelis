package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func TestStreamResolveReaderUsesDurableTaskKindOnce(t *testing.T) {
	t.Parallel()

	store := &streamResolveTaskStore{entry: &taskapi.Entry{
		TaskID:    "task-1",
		Kind:      taskapi.KindSubagent,
		Session:   session.SessionRef{SessionID: "session-1"},
		State:     taskapi.StateCompleted,
		CreatedAt: time.Unix(100, 0),
		UpdatedAt: time.Unix(200, 0),
		Spec: map[string]any{
			"session_id":  "child-session-1",
			"terminal_id": "task-task-1",
		},
		Result: map[string]any{"final_message": "child complete"},
	}}
	service := newStreamService(newTaskRuntime(&Runtime{clock: time.Now}, store))

	read, _, err := service.resolveReader(context.Background(), stream.Ref{
		SessionID: "session-1",
		TaskID:    "task-1",
	})
	if err != nil {
		t.Fatalf("resolveReader() error = %v", err)
	}
	snapshot, err := read(context.Background(), stream.Cursor{})
	if err != nil {
		t.Fatalf("resolved reader error = %v", err)
	}
	if snapshot.State != string(taskapi.StateCompleted) || snapshot.FinalText != "child complete" {
		t.Fatalf("subagent snapshot = %#v, want completed durable result", snapshot)
	}
	if calls := store.getCalls.Load(); calls != 1 {
		t.Fatalf("Task store Get calls = %d, want one kind-directed lookup", calls)
	}
}

func TestStreamResolveReaderPreservesDurableLookupErrorWithoutFallback(t *testing.T) {
	t.Parallel()

	storeCause := errors.New("task index read failed")
	store := &streamResolveTaskStore{
		err: errorcode.Wrap(errorcode.Unavailable, "task index unavailable", storeCause),
	}
	service := newStreamService(newTaskRuntime(&Runtime{clock: time.Now}, store))

	_, _, err := service.resolveReader(context.Background(), stream.Ref{
		SessionID: "session-1",
		TaskID:    "task-1",
	})
	if !errors.Is(err, storeCause) {
		t.Fatalf("resolveReader() error = %v, want original store cause", err)
	}
	if !errorcode.Is(err, errorcode.Unavailable) {
		t.Fatalf("resolveReader() error code = %q, want unavailable", errorcode.CodeOf(err))
	}
	if calls := store.getCalls.Load(); calls != 1 {
		t.Fatalf("Task store Get calls = %d, want no command-to-subagent fallback", calls)
	}
}

func TestStreamTerminalControlRejectsSubagentTask(t *testing.T) {
	t.Parallel()

	const (
		sessionID = "session-1"
		taskID    = "subagent-task-1"
	)
	subagent := &subagentTask{
		ref:        taskapi.Ref{TaskID: taskID},
		sessionRef: session.SessionRef{SessionID: sessionID},
		state:      taskapi.StateRunning,
		running:    true,
	}
	tasks := newTaskRuntime(nil, nil)
	tasks.subagents[taskID] = subagent
	service := newStreamService(tasks)
	ref := stream.Ref{SessionID: sessionID, TaskID: taskID}

	for name, control := range map[string]func(context.Context, stream.Ref) error{
		"kill":    service.Kill,
		"release": service.Release,
	} {
		t.Run(name, func(t *testing.T) {
			err := control(context.Background(), ref)
			if err == nil || !strings.Contains(err.Error(), `task "subagent-task-1" not found`) {
				t.Fatalf("%s(subagent) error = %v, want command-only not found", name, err)
			}
		})
	}

	subagent.mu.Lock()
	state, running := subagent.state, subagent.running
	subagent.mu.Unlock()
	if state != taskapi.StateRunning || !running {
		t.Fatalf("subagent state after terminal control = (%q, %v), want unchanged running task", state, running)
	}
}

type streamResolveTaskStore struct {
	taskapi.Store
	entry    *taskapi.Entry
	err      error
	getCalls atomic.Int64
}

func (s *streamResolveTaskStore) Get(context.Context, string) (*taskapi.Entry, error) {
	s.getCalls.Add(1)
	return taskapi.CloneEntry(s.entry), s.err
}
