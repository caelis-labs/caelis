package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/task"
)

func TestStoreUpsertCompletedTaskKeepsCanonicalResultInIndex(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{RootDir: root, Clock: fixedClock})
	entry := &task.Entry{
		TaskID:    "task-1",
		Kind:      task.KindCommand,
		Session:   sessionRef("sess-1"),
		Title:     "RUN_COMMAND echo hi",
		State:     task.StateCompleted,
		Running:   false,
		CreatedAt: time.Unix(10, 0),
		UpdatedAt: time.Unix(20, 0),
		Result: map[string]any{
			"stdout":    "hello\n",
			"stderr":    "warn\n",
			"result":    "hello\nwarn\n",
			"exit_code": 0,
			"state":     "completed",
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

	listed, err := store.ListSession(context.Background(), sessionRef("sess-1"))
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

	got, err := store.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if gotStdout, ok := got.Result["stdout"]; ok {
		t.Fatalf("Get() result unexpectedly contains stdout: %#v", gotStdout)
	}
	if gotStderr, ok := got.Result["stderr"]; ok {
		t.Fatalf("Get() result unexpectedly contains stderr: %#v", gotStderr)
	}
	if gotResult, _ := got.Result["result"].(string); gotResult != "hello\nwarn\n" {
		t.Fatalf("Get() result = %q, want canonical result", gotResult)
	}

	index := readJSONFile(t, filepath.Join(root, "sess-1.index.json"))
	if index["kind"] != indexKind {
		t.Fatalf("index kind = %#v, want %q", index["kind"], indexKind)
	}
	if _, err := os.Stat(filepath.Join(root, "sess-1.blobs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("blob file should not exist, stat err = %v", err)
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
	listed, err = store.ListSession(context.Background(), sessionRef("sess-1"))
	if err != nil {
		t.Fatalf("ListSession(after repeated upsert) error = %v", err)
	}
	if got, want := len(listed), 1; got != want {
		t.Fatalf("len(listed after repeated upsert) = %d, want %d", got, want)
	}
	got, err = store.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get(after repeated upsert) error = %v", err)
	}
	if gotResult, _ := got.Result["result"].(string); gotResult != "hello again\nwarn again\n" {
		t.Fatalf("Get(after repeated upsert) result = %q, want canonical result", gotResult)
	}
	if _, err := os.Stat(filepath.Join(root, "sess-1.blobs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("blob file should still not exist after repeated upsert, stat err = %v", err)
	}
}

func TestStoreUpsertCompletedTaskDropsWhitespaceOnlyStreams(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{RootDir: root, Clock: fixedClock})
	entry := &task.Entry{
		TaskID:    "task-blank",
		Kind:      task.KindCommand,
		Session:   sessionRef("sess-blank"),
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
	listed, err := store.ListSession(context.Background(), sessionRef("sess-blank"))
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
	got, err := store.Get(context.Background(), "task-blank")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if _, ok := got.Result["stdout"]; ok {
		t.Fatalf("Get() result unexpectedly contains stdout: %#v", got.Result)
	}
	if _, ok := got.Result["stderr"]; ok {
		t.Fatalf("Get() result unexpectedly contains stderr: %#v", got.Result)
	}
	if _, err := os.Stat(filepath.Join(root, "sess-blank.blobs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("blob file should not exist for blank-only streams, stat err = %v", err)
	}
}

func TestStoreUpsertRunningTaskKeepsIndexOnly(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{RootDir: root, Clock: fixedClock})
	entry := &task.Entry{
		TaskID:  "task-running",
		Kind:    task.KindCommand,
		Session: sessionRef("sess-2"),
		Title:   "RUN_COMMAND sleep 1",
		State:   task.StateRunning,
		Running: true,
		Result: map[string]any{
			"task_id": "task-running",
			"state":   "running",
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
	if _, err := os.Stat(filepath.Join(root, "sess-2.blobs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("blob file should not exist for running task, stat err = %v", err)
	}
}

func TestStoreGetUsesTaskIDLookupIndex(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{RootDir: root, Clock: fixedClock})
	entry := &task.Entry{
		TaskID:    "task-target",
		Kind:      task.KindCommand,
		Session:   sessionRef("session-target"),
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

	got, err := store.Get(context.Background(), "task-target")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Session.SessionID != "session-target" {
		t.Fatalf("Get() session = %q, want session-target", got.Session.SessionID)
	}
}

func TestStoreGetFallsBackWhenTaskLookupIsCorrupt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{RootDir: root, Clock: fixedClock})
	entry := &task.Entry{
		TaskID:    "task-target",
		Kind:      task.KindCommand,
		Session:   sessionRef("session-target"),
		Title:     "RUN_COMMAND echo target",
		State:     task.StateCompleted,
		Running:   false,
		UpdatedAt: time.Unix(20, 0),
		Result:    map[string]any{"state": "completed"},
	}
	if err := store.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
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

func TestStoreConcurrentUpsertKeepsAllTasks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{RootDir: root, Clock: fixedClock})
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry := &task.Entry{
				TaskID:    "task-concurrent-" + time.Unix(int64(i), 0).Format("150405"),
				Kind:      task.KindCommand,
				Session:   sessionRef("sess-concurrent"),
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

	listed, err := store.ListSession(context.Background(), sessionRef("sess-concurrent"))
	if err != nil {
		t.Fatalf("ListSession() error = %v", err)
	}
	if len(listed) != 24 {
		t.Fatalf("len(listed) = %d, want 24", len(listed))
	}
}

func sessionRef(sessionID string) session.SessionRef {
	return session.SessionRef{AppName: "caelis", UserID: "user-1", SessionID: sessionID}
}

func fixedClock() time.Time { return time.Unix(100, 0).UTC() }

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
	}
	return out
}
