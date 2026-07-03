package local

import (
	"context"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/ports/sandbox"
	"github.com/caelis-labs/caelis/ports/session"
	taskapi "github.com/caelis-labs/caelis/ports/task"
)

func TestStartCommandDefersCompletedCommandResultPersistenceToAgentLoop(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	completed := false
	fakeSession := &yieldProbeSandboxSession{
		statusRunning: &completed,
		stdout:        "out\n",
		stderr:        "err\n",
		result: sandbox.CommandResult{
			Stdout:   "out\n",
			Stderr:   "err\n",
			ExitCode: 0,
		},
	}
	fake := &yieldProbeSandboxRuntime{session: fakeSession}
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore

	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, fake, taskapi.CommandStartRequest{
		Command: "echo out; echo err >&2",
		Workdir: activeSession.CWD,
		Yield:   0,
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	if got, _ := snapshot.Result["result"].(string); got != "out\nerr\n" {
		t.Fatalf("snapshot result = %q, want merged terminal summary", got)
	}

	entry, err := taskStore.Get(context.Background(), snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get() error = %v", err)
	}
	if _, exists := entry.Result["result"]; exists {
		t.Fatalf("stored result unexpectedly contains pre-canonical command output: %#v", entry.Result)
	}
	if _, exists := entry.Result["stdout"]; exists {
		t.Fatalf("stored result unexpectedly contains stdout: %#v", entry.Result)
	}
	if _, exists := entry.Result["stderr"]; exists {
		t.Fatalf("stored result unexpectedly contains stderr: %#v", entry.Result)
	}
	if _, exists := entry.Metadata["output_cursor"]; exists {
		t.Fatalf("stored metadata unexpectedly contains pre-canonical output_cursor: %#v", entry.Metadata)
	}
	listed, err := taskStore.ListSession(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("task store ListSession() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed tasks = %d, want 1", len(listed))
	}
	if _, exists := listed[0].Result["stdout"]; exists {
		t.Fatalf("listed result unexpectedly contains stdout: %#v", listed[0].Result)
	}
	if _, exists := listed[0].Result["stderr"]; exists {
		t.Fatalf("listed result unexpectedly contains stderr: %#v", listed[0].Result)
	}
}

func TestTaskRuntimeSyncCanonicalToolResultPersistsCommandResult(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	completed := false
	fakeSession := &yieldProbeSandboxSession{
		statusRunning: &completed,
		stdout:        "raw full output\n",
		result: sandbox.CommandResult{
			Stdout:   "raw full output\n",
			ExitCode: 0,
		},
	}
	fake := &yieldProbeSandboxRuntime{session: fakeSession}
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore

	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, fake, taskapi.CommandStartRequest{
		Command: "printf raw",
		Workdir: activeSession.CWD,
		Yield:   0,
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	canonicalText := "canonical truncated output\n"
	eventTime := time.Unix(123, 0).UTC()
	err = runtime.tasks.syncCanonicalToolResult(context.Background(), activeSession.SessionRef, &session.Event{
		Type: session.EventTypeToolResult,
		Time: eventTime,
		Tool: &session.EventTool{
			Name:   "RUN_COMMAND",
			Status: "completed",
			Output: map[string]any{
				"task_id":   snapshot.Ref.TaskID,
				"state":     string(taskapi.StateCompleted),
				"result":    canonicalText,
				"exit_code": 0,
			},
		},
	})
	if err != nil {
		t.Fatalf("syncCanonicalToolResult() error = %v", err)
	}

	entry, err := taskStore.Get(context.Background(), snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get() error = %v", err)
	}
	if got, _ := entry.Result["result"].(string); got != canonicalText {
		t.Fatalf("stored result = %q, want canonical result", got)
	}
	if got, ok := taskInt64Value(entry.Metadata["output_cursor"]); !ok || got != int64(len([]byte(canonicalText))) {
		t.Fatalf("stored output_cursor = %#v ok=%v, want canonical result byte length", entry.Metadata["output_cursor"], ok)
	}
	if !entry.UpdatedAt.Equal(eventTime) {
		t.Fatalf("stored UpdatedAt = %v, want canonical event time %v", entry.UpdatedAt, eventTime)
	}
}

func TestTaskRuntimeSyncCanonicalToolResultPersistsBatchTaskResults(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore
	for _, id := range []string{"task-a", "task-b", "task-error"} {
		if err := taskStore.Upsert(context.Background(), &taskapi.Entry{
			TaskID:  id,
			Kind:    taskapi.KindCommand,
			Session: activeSession.SessionRef,
			State:   taskapi.StateCompleted,
			Result:  map[string]any{"state": string(taskapi.StateCompleted)},
			Terminal: sandbox.TerminalRef{
				Backend:    sandbox.BackendHost,
				SessionID:  id + "-session",
				TerminalID: id + "-terminal",
			},
		}); err != nil {
			t.Fatalf("Upsert(%s) error = %v", id, err)
		}
	}

	err := runtime.tasks.syncCanonicalToolResult(context.Background(), activeSession.SessionRef, &session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			Name:   "TASK",
			Status: "completed",
			Output: map[string]any{
				"action": "wait",
				"count":  3,
				"tasks": []any{
					map[string]any{"task_id": "task-a", "state": string(taskapi.StateCompleted), "result": "canonical a\n", "exit_code": 0},
					map[string]any{"task_id": "task-b", "state": string(taskapi.StateFailed), "result": "canonical b\n", "exit_code": 1},
					map[string]any{"task_id": "task-error", "error": "not updated"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("syncCanonicalToolResult(batch) error = %v", err)
	}

	for _, tc := range []struct {
		id     string
		result string
		state  taskapi.State
	}{
		{id: "task-a", result: "canonical a\n", state: taskapi.StateCompleted},
		{id: "task-b", result: "canonical b\n", state: taskapi.StateFailed},
	} {
		entry, err := taskStore.Get(context.Background(), tc.id)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", tc.id, err)
		}
		if got, _ := entry.Result["result"].(string); got != tc.result {
			t.Fatalf("Get(%s) result = %q, want %q", tc.id, got, tc.result)
		}
		if entry.State != tc.state {
			t.Fatalf("Get(%s) state = %q, want %q", tc.id, entry.State, tc.state)
		}
	}
	entry, err := taskStore.Get(context.Background(), "task-error")
	if err != nil {
		t.Fatalf("Get(task-error) error = %v", err)
	}
	if _, exists := entry.Result["error"]; exists {
		t.Fatalf("batch item without state unexpectedly overwrote task-error: %#v", entry.Result)
	}
}

func TestLookupCommandBackfillsCanonicalResultFromSessionHistory(t *testing.T) {
	t.Parallel()

	sessions, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore
	entry := &taskapi.Entry{
		TaskID:  "task-backfill",
		Kind:    taskapi.KindCommand,
		Session: activeSession.SessionRef,
		State:   taskapi.StateCompleted,
		Result:  map[string]any{"state": string(taskapi.StateCompleted), "exit_code": 0},
		Terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendHost,
			SessionID:  "term-backfill-session",
			TerminalID: "term-backfill",
		},
	}
	if err := taskStore.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type: session.EventTypeToolResult,
			Tool: &session.EventTool{
				Name:   "RUN_COMMAND",
				Status: "completed",
				Output: map[string]any{
					"task_id":   "task-backfill",
					"state":     string(taskapi.StateCompleted),
					"result":    "canonical from history\n",
					"exit_code": 0,
				},
			},
		},
	}); err != nil {
		t.Fatalf("AppendEvent(tool result) error = %v", err)
	}

	task, err := runtime.tasks.lookupCommand(context.Background(), activeSession.SessionRef, "task-backfill")
	if err != nil {
		t.Fatalf("lookupCommand() error = %v", err)
	}
	if got, _ := task.result["result"].(string); got != "canonical from history\n" {
		t.Fatalf("rehydrated task result = %q, want canonical history result", got)
	}
	stored, err := taskStore.Get(context.Background(), "task-backfill")
	if err != nil {
		t.Fatalf("Get(after backfill) error = %v", err)
	}
	if got, _ := stored.Result["result"].(string); got != "canonical from history\n" {
		t.Fatalf("stored result after backfill = %q, want canonical history result", got)
	}
}
