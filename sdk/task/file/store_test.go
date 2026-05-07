package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktask "github.com/OnslaughtSnail/caelis/sdk/task"
)

func TestStoreUpsertCompletedTaskSplitsIndexAndBlob(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{RootDir: root, Clock: fixedClock})
	entry := &sdktask.Entry{
		TaskID:    "task-1",
		Kind:      sdktask.KindBash,
		Session:   sessionRef("sess-1"),
		Title:     "BASH echo hi",
		State:     sdktask.StateCompleted,
		Running:   false,
		CreatedAt: time.Unix(10, 0),
		UpdatedAt: time.Unix(20, 0),
		Result: map[string]any{
			"stdout":    "hello\n",
			"stderr":    "warn\n",
			"exit_code": 0,
			"state":     "completed",
		},
		Terminal: sdksandbox.TerminalRef{
			Backend:    sdksandbox.BackendHost,
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
		t.Fatalf("ListSession result unexpectedly hydrated stdout: %#v", listed[0].Result)
	}
	if got := listed[0].Result["stdout_blob"]; got == nil {
		t.Fatalf("stdout_blob missing from index result: %#v", listed[0].Result)
	}

	got, err := store.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if gotStdout, _ := got.Result["stdout"].(string); gotStdout != "hello\n" {
		t.Fatalf("hydrated stdout = %q, want %q", gotStdout, "hello\n")
	}
	if gotStderr, _ := got.Result["stderr"].(string); gotStderr != "warn\n" {
		t.Fatalf("hydrated stderr = %q, want %q", gotStderr, "warn\n")
	}

	index := readJSONFile(t, filepath.Join(root, "sess-1.index.json"))
	if index["kind"] != indexKind {
		t.Fatalf("index kind = %#v, want %q", index["kind"], indexKind)
	}
	blobs := readLines(t, filepath.Join(root, "sess-1.blobs.jsonl"))
	if got, want := len(blobs), 2; got != want {
		t.Fatalf("blob line count = %d, want %d", got, want)
	}

	entry.UpdatedAt = time.Unix(30, 0)
	entry.Result = map[string]any{
		"stdout": "hello again\n",
		"stderr": "warn again\n",
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
	if gotStdout, _ := got.Result["stdout"].(string); gotStdout != "hello again\n" {
		t.Fatalf("hydrated repeated stdout = %q, want %q", gotStdout, "hello again\n")
	}
	if gotStderr, _ := got.Result["stderr"].(string); gotStderr != "warn again\n" {
		t.Fatalf("hydrated repeated stderr = %q, want %q", gotStderr, "warn again\n")
	}
	blobs = readLines(t, filepath.Join(root, "sess-1.blobs.jsonl"))
	if got, want := len(blobs), 2; got != want {
		t.Fatalf("blob line count after repeated upsert = %d, want %d", got, want)
	}
}

func TestStoreUpsertRunningTaskKeepsIndexOnly(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewStore(Config{RootDir: root, Clock: fixedClock})
	entry := &sdktask.Entry{
		TaskID:  "task-running",
		Kind:    sdktask.KindBash,
		Session: sessionRef("sess-2"),
		Title:   "BASH sleep 1",
		State:   sdktask.StateRunning,
		Running: true,
		Result: map[string]any{
			"task_id": "task-running",
			"state":   "running",
		},
		Terminal: sdksandbox.TerminalRef{
			Backend:    sdksandbox.BackendHost,
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
			entry := &sdktask.Entry{
				TaskID:    "task-concurrent-" + time.Unix(int64(i), 0).Format("150405"),
				Kind:      sdktask.KindBash,
				Session:   sessionRef("sess-concurrent"),
				Title:     "BASH concurrent",
				State:     sdktask.StateCompleted,
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

func sessionRef(sessionID string) sdksession.SessionRef {
	return sdksession.SessionRef{AppName: "caelis", UserID: "user-1", SessionID: sessionID}
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

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
