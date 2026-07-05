package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestTaskStoreUpsertCompletedTaskKeepsCanonicalResultInIndex(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	entry := &task.Entry{
		TaskID:    "task-1",
		Kind:      task.KindCommand,
		Session:   taskSessionRef("sess-1"),
		Title:     "RUN_COMMAND echo hi",
		State:     task.StateCompleted,
		Running:   false,
		CreatedAt: time.Unix(10, 0),
		UpdatedAt: time.Unix(20, 0),
		Result: map[string]any{
			"stdout":         "hello\n",
			"stderr":         "warn\n",
			"output":         "transient output\n",
			"text":           "transient text\n",
			"latest_output":  "transient latest\n",
			"output_preview": "transient preview\n",
			"result":         "hello\nwarn\n",
			"exit_code":      0,
			"state":          "completed",
		},
		Terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendHost,
			SessionID:  "exec-1",
			TerminalID: "term-1",
		},
	}
	if err := store.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	listed, err := store.ListSession(context.Background(), taskSessionRef("sess-1"))
	if err != nil {
		t.Fatalf("ListSession() error = %v", err)
	}
	if got, want := len(listed), 1; got != want {
		t.Fatalf("len(listed) = %d, want %d", got, want)
	}
	if _, ok := listed[0].Result["stdout"]; ok {
		t.Fatalf("ListSession result unexpectedly contains stdout: %#v", listed[0].Result)
	}
	if _, ok := listed[0].Result["stderr"]; ok {
		t.Fatalf("ListSession result unexpectedly contains stderr: %#v", listed[0].Result)
	}
	if got, _ := listed[0].Result["result"].(string); got != "hello\nwarn\n" {
		t.Fatalf("ListSession result = %q, want canonical result", got)
	}
	assertNoTransientTaskResultKeys(t, listed[0].Result)

	got, err := store.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	assertNoTransientTaskResultKeys(t, got.Result)
	if gotResult, _ := got.Result["result"].(string); gotResult != "hello\nwarn\n" {
		t.Fatalf("Get() result = %q, want canonical result", gotResult)
	}
	if _, err := os.Stat(filepath.Join(root, "sess-1.index.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy task index should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks.lookup.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy task lookup should not exist, stat err = %v", err)
	}

	entry.UpdatedAt = time.Unix(30, 0)
	entry.Result = map[string]any{
		"stdout": "hello again\n",
		"stderr": "warn again\n",
		"result": "hello again\nwarn again\n",
		"state":  "completed",
	}
	if err := store.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert(repeated final) error = %v", err)
	}
	got, err = store.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get(after repeated upsert) error = %v", err)
	}
	if gotResult, _ := got.Result["result"].(string); gotResult != "hello again\nwarn again\n" {
		t.Fatalf("Get(after repeated upsert) result = %q, want canonical result", gotResult)
	}
}

func TestTaskStoreUpsertCompletedTaskDropsWhitespaceOnlyStreams(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	entry := &task.Entry{
		TaskID:    "task-blank",
		Kind:      task.KindCommand,
		Session:   taskSessionRef("sess-blank"),
		State:     task.StateCompleted,
		Running:   false,
		CreatedAt: time.Unix(10, 0),
		UpdatedAt: time.Unix(20, 0),
		Result: map[string]any{
			"stdout": "\n\n",
			"stderr": "   ",
			"state":  "completed",
		},
		Terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendHost,
			SessionID:  "exec-blank",
			TerminalID: "term-blank",
		},
	}
	if err := store.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	listed, err := store.ListSession(context.Background(), taskSessionRef("sess-blank"))
	if err != nil {
		t.Fatalf("ListSession() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("len(listed) = %d, want 1", len(listed))
	}
	if _, ok := listed[0].Result["stdout"]; ok {
		t.Fatalf("ListSession result unexpectedly contains stdout: %#v", listed[0].Result)
	}
	if _, ok := listed[0].Result["stderr"]; ok {
		t.Fatalf("ListSession result unexpectedly contains stderr: %#v", listed[0].Result)
	}
}

func TestTaskStoreUpsertRunningTaskKeepsSQLiteOnly(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	entry := &task.Entry{
		TaskID:  "task-running",
		Kind:    task.KindCommand,
		Session: taskSessionRef("sess-2"),
		Title:   "RUN_COMMAND sleep 1",
		State:   task.StateRunning,
		Running: true,
		Result: map[string]any{
			"task_id":        "task-running",
			"state":          "running",
			"latest_output":  "live output\n",
			"output_preview": "preview\n",
			"output":         "stream output\n",
			"text":           "stream text\n",
		},
		Terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendHost,
			SessionID:  "exec-running",
			TerminalID: "term-running",
		},
	}
	if err := store.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sess-2.index.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy task index should not exist for running task, stat err = %v", err)
	}
	got, err := store.Get(context.Background(), "task-running")
	if err != nil {
		t.Fatalf("Get(running) error = %v", err)
	}
	assertNoTransientTaskResultKeys(t, got.Result)
	if gotState, _ := got.Result["state"].(string); gotState != "running" {
		t.Fatalf("Get(running) state = %q, want running", gotState)
	}
}

func TestTaskStoreGetIgnoresLegacyTaskJSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	entry := &task.Entry{
		TaskID:    "task-target",
		Kind:      task.KindCommand,
		Session:   taskSessionRef("session-target"),
		Title:     "RUN_COMMAND echo target",
		State:     task.StateCompleted,
		Running:   false,
		UpdatedAt: time.Unix(20, 0),
		Result:    map[string]any{"state": "completed"},
	}
	if err := store.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "aaa-decoy.index.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile(decoy) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tasks.lookup.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile(lookup) error = %v", err)
	}

	got, err := store.Get(context.Background(), "task-target")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Session.SessionID != "session-target" {
		t.Fatalf("Get() session = %q, want session-target", got.Session.SessionID)
	}
}

func TestTaskStoreNilReceiverReturnsErrors(t *testing.T) {
	t.Parallel()

	var store *TaskStore
	if err := store.Upsert(context.Background(), &task.Entry{TaskID: "task-1"}); err == nil {
		t.Fatal("Upsert(nil receiver) succeeded, want error")
	}
	if _, err := store.Get(context.Background(), "task-1"); err == nil {
		t.Fatal("Get(nil receiver) succeeded, want error")
	}
	if _, err := store.ListSession(context.Background(), taskSessionRef("sess-1")); err == nil {
		t.Fatal("ListSession(nil receiver) succeeded, want error")
	}
	if _, err := store.GetSessionTaskByHandle(context.Background(), taskSessionRef("sess-1"), task.KindSubagent, "reviewer"); err == nil {
		t.Fatal("GetSessionTaskByHandle(nil receiver) succeeded, want error")
	}
}

func TestTaskStoreGetSessionTaskByHandleUsesIndexedHandle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	ref := taskSessionRef("sess-handle")
	if err := store.Upsert(context.Background(), &task.Entry{
		TaskID:  "task-command-decoy",
		Kind:    task.KindCommand,
		Session: ref,
		State:   task.StateCompleted,
		Spec:    map[string]any{"handle": "reviewer"},
		Result:  map[string]any{"state": "completed"},
	}); err != nil {
		t.Fatalf("Upsert(command decoy) error = %v", err)
	}
	if err := store.Upsert(context.Background(), &task.Entry{
		TaskID:  "task-other-session",
		Kind:    task.KindSubagent,
		Session: taskSessionRef("sess-other"),
		State:   task.StateCompleted,
		Spec:    map[string]any{"handle": "reviewer"},
		Result:  map[string]any{"state": "completed"},
	}); err != nil {
		t.Fatalf("Upsert(other session) error = %v", err)
	}
	if err := store.Upsert(context.Background(), &task.Entry{
		TaskID:  "task-target",
		Kind:    task.KindSubagent,
		Session: ref,
		State:   task.StateCompleted,
		Spec:    map[string]any{"handle": "Reviewer"},
		Result:  map[string]any{"state": "completed"},
	}); err != nil {
		t.Fatalf("Upsert(target) error = %v", err)
	}

	got, err := store.GetSessionTaskByHandle(context.Background(), ref, task.KindSubagent, "@REVIEWER")
	if err != nil {
		t.Fatalf("GetSessionTaskByHandle() error = %v", err)
	}
	if got.TaskID != "task-target" {
		t.Fatalf("GetSessionTaskByHandle() task = %q, want task-target", got.TaskID)
	}
}

func TestTaskStoreGetSessionTaskByHandleRejectsAmbiguousHandle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	ref := taskSessionRef("sess-ambiguous")
	for _, id := range []string{"task-a", "task-b"} {
		if err := store.Upsert(context.Background(), &task.Entry{
			TaskID:  id,
			Kind:    task.KindSubagent,
			Session: ref,
			State:   task.StateCompleted,
			Spec:    map[string]any{"handle": "reviewer"},
			Result:  map[string]any{"state": "completed"},
		}); err != nil {
			t.Fatalf("Upsert(%s) error = %v", id, err)
		}
	}

	_, err := store.GetSessionTaskByHandle(context.Background(), ref, task.KindSubagent, "reviewer")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("GetSessionTaskByHandle() error = %v, want ambiguous", err)
	}
}

func TestTaskStoreConcurrentUpsertKeepsAllTasks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewTaskStore(NewStore(Config{RootDir: root, Clock: fixedTaskClock}))
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry := &task.Entry{
				TaskID:    "task-concurrent-" + time.Unix(int64(i), 0).Format("150405"),
				Kind:      task.KindCommand,
				Session:   taskSessionRef("sess-concurrent"),
				Title:     "RUN_COMMAND concurrent",
				State:     task.StateCompleted,
				Running:   false,
				UpdatedAt: time.Unix(int64(i), 0),
				Result: map[string]any{
					"stdout": "out\n",
					"state":  "completed",
				},
			}
			if err := store.Upsert(context.Background(), entry); err != nil {
				t.Errorf("Upsert(%d) error = %v", i, err)
			}
		}()
	}
	wg.Wait()

	listed, err := store.ListSession(context.Background(), taskSessionRef("sess-concurrent"))
	if err != nil {
		t.Fatalf("ListSession() error = %v", err)
	}
	if len(listed) != 24 {
		t.Fatalf("len(listed) = %d, want 24", len(listed))
	}
}

func taskSessionRef(sessionID string) session.SessionRef {
	return session.SessionRef{AppName: "caelis", UserID: "user-1", SessionID: sessionID}
}

func fixedTaskClock() time.Time { return time.Unix(100, 0).UTC() }

func assertNoTransientTaskResultKeys(t *testing.T, result map[string]any) {
	t.Helper()
	for _, key := range task.TransientResultKeys() {
		if value, ok := result[key]; ok {
			t.Fatalf("task result unexpectedly contains transient %q: %#v", key, value)
		}
	}
}
