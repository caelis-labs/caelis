package task

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
	sandboxhost "github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/host"
	toolshell "github.com/OnslaughtSnail/caelis/internal/adapters/tools/shell"
)

func TestTaskToolWaitsForAsyncRunCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("async shell test uses POSIX sleep")
	}
	rt := newRuntime(t)
	runTool, err := toolshell.NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	taskTool, err := New(rt)
	if err != nil {
		t.Fatal(err)
	}
	start := callTool(t, runTool, map[string]any{
		"command":       "printf start; sleep 0.1; printf done",
		"yield_time_ms": 10,
	})
	startPayload := resultPayload(t, start)
	taskID, _ := startPayload["task_id"].(string)
	if taskID == "" || startPayload["state"] != "running" {
		t.Fatalf("start payload = %#v, want running task", startPayload)
	}

	wait := callTool(t, taskTool, map[string]any{
		"action":        "wait",
		"task_id":       taskID,
		"yield_time_ms": 500,
	})
	waitPayload := resultPayload(t, wait)
	if waitPayload["state"] != "completed" || waitPayload["exit_code"] != float64(0) {
		t.Fatalf("wait payload = %#v, want completed task", waitPayload)
	}
	if stdout, _ := waitPayload["stdout"].(string); !strings.Contains(stdout, "done") {
		t.Fatalf("stdout = %q, want final output", stdout)
	}
}

func TestTaskToolWritesAndCancelsAsyncCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("async stdin test uses POSIX cat")
	}
	rt := newRuntime(t)
	runTool, err := toolshell.NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	taskTool, err := New(rt)
	if err != nil {
		t.Fatal(err)
	}
	start := callTool(t, runTool, map[string]any{
		"command":       "cat",
		"yield_time_ms": 10,
	})
	taskID, _ := resultPayload(t, start)["task_id"].(string)
	if taskID == "" {
		t.Fatalf("start payload = %#v, missing task id", resultPayload(t, start))
	}

	wrote := callTool(t, taskTool, map[string]any{
		"action":        "write",
		"task_id":       taskID,
		"input":         "hello\n",
		"yield_time_ms": 100,
	})
	if stdout, _ := resultPayload(t, wrote)["stdout"].(string); !strings.Contains(stdout, "hello") {
		t.Fatalf("write stdout = %q, want echoed input", stdout)
	}

	cancelled := callTool(t, taskTool, map[string]any{
		"action":  "cancel",
		"task_id": taskID,
	})
	if resultPayload(t, cancelled)["state"] != "cancelled" {
		t.Fatalf("cancel payload = %#v, want cancelled", resultPayload(t, cancelled))
	}
}

func TestTaskToolListsAndTailsAsyncCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("async shell test uses POSIX sleep")
	}
	rt := newRuntime(t)
	runTool, err := toolshell.NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	taskTool, err := New(rt)
	if err != nil {
		t.Fatal(err)
	}
	start := callTool(t, runTool, map[string]any{
		"command":       "printf one; sleep 0.2; printf two",
		"yield_time_ms": 50,
	})
	startPayload := resultPayload(t, start)
	taskID, _ := startPayload["task_id"].(string)
	if taskID == "" {
		t.Fatalf("start payload = %#v, missing task id", startPayload)
	}
	if stdout, _ := startPayload["stdout"].(string); !strings.Contains(stdout, "one") {
		t.Fatalf("start stdout = %q, want first chunk", stdout)
	}

	list := callTool(t, taskTool, map[string]any{
		"action": "list",
		"limit":  10,
	})
	listPayload := resultPayload(t, list)
	if listPayload["count"] != float64(1) {
		t.Fatalf("list payload = %#v, want one task", listPayload)
	}
	tasks, ok := listPayload["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("tasks = %#v, want one task", listPayload["tasks"])
	}
	taskEntry, ok := tasks[0].(map[string]any)
	if !ok || taskEntry["task_id"] != taskID {
		t.Fatalf("task entry = %#v, want task id %q", taskEntry, taskID)
	}

	stdoutCursor, _ := startPayload["stdout_cursor"].(float64)
	time.Sleep(250 * time.Millisecond)
	tail := callTool(t, taskTool, map[string]any{
		"action":        "tail",
		"task_id":       taskID,
		"stdout_cursor": int64(stdoutCursor),
	})
	tailPayload := resultPayload(t, tail)
	if stdout, _ := tailPayload["stdout"].(string); strings.Contains(stdout, "one") || !strings.Contains(stdout, "two") {
		t.Fatalf("tail stdout = %q, want only data after cursor", stdout)
	}
}

func TestTaskToolReadsArchivedAsyncCommandAfterRuntimeRestart(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	rt := newRuntimeWithConfig(t, sandbox.Config{CWD: dir, StateDir: stateDir})
	runTool, err := toolshell.NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	command := "printf archived"
	if runtime.GOOS == "windows" {
		command = "echo archived"
	}
	start := callTool(t, runTool, map[string]any{
		"command":       command,
		"yield_time_ms": 500,
	})
	taskID, _ := resultPayload(t, start)["task_id"].(string)
	if taskID == "" {
		t.Fatalf("start payload = %#v, missing task id", resultPayload(t, start))
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := newRuntimeWithConfig(t, sandbox.Config{CWD: dir, StateDir: stateDir})
	taskTool, err := New(reopened)
	if err != nil {
		t.Fatal(err)
	}
	list := callTool(t, taskTool, map[string]any{
		"action": "list",
		"limit":  10,
	})
	if !taskListContains(resultPayload(t, list), taskID) {
		t.Fatalf("list payload = %#v, want archived task %q", resultPayload(t, list), taskID)
	}
	tail := callTool(t, taskTool, map[string]any{
		"action":  "tail",
		"task_id": taskID,
	})
	if stdout, _ := resultPayload(t, tail)["stdout"].(string); !strings.Contains(stdout, "archived") {
		t.Fatalf("tail stdout = %q, want archived output", stdout)
	}
}

func TestTaskToolUsesResolverWithoutSandboxRuntime(t *testing.T) {
	resolver := &resolverOnly{
		session: &resolverOnlySession{
			snapshot: sandbox.SessionSnapshot{
				Ref:           sandbox.SessionRef{ID: "spawn-1", Backend: sandbox.BackendCustom},
				Command:       "SPAWN reviewer",
				State:         sandbox.SessionCompleted,
				Running:       false,
				SupportsInput: false,
				ExitCode:      0,
				Terminal:      sandbox.TerminalRef{ID: "spawn-spawn-1", SessionID: "spawn-1"},
				Metadata: map[string]any{
					"task_kind": "subagent",
					"agent":     "reviewer",
				},
			},
			stdout: "child done\n",
		},
	}
	taskTool, err := NewWithResolver(nil, resolver)
	if err != nil {
		t.Fatal(err)
	}
	list := callTool(t, taskTool, map[string]any{
		"action": "list",
		"limit":  10,
	})
	listPayload := resultPayload(t, list)
	tasks, ok := listPayload["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("tasks = %#v, want resolver task", listPayload["tasks"])
	}
	taskEntry, ok := tasks[0].(map[string]any)
	if !ok || taskEntry["task_kind"] != "subagent" || taskEntry["agent"] != "reviewer" {
		t.Fatalf("task entry = %#v, want resolver metadata", taskEntry)
	}

	tail := callTool(t, taskTool, map[string]any{
		"action":  "tail",
		"task_id": "spawn-1",
	})
	tailPayload := resultPayload(t, tail)
	if tailPayload["task_kind"] != "subagent" || !strings.Contains(stringValue(tailPayload["stdout"]), "child done") {
		t.Fatalf("tail payload = %#v, want resolver output and metadata", tailPayload)
	}
}

func newRuntime(t *testing.T) sandbox.Runtime {
	t.Helper()
	return newRuntimeWithConfig(t, sandbox.Config{CWD: t.TempDir()})
}

func newRuntimeWithConfig(t *testing.T, cfg sandbox.Config) sandbox.Runtime {
	t.Helper()
	rt, err := sandboxhost.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func callTool(t *testing.T, toolImpl tool.Tool, input map[string]any) tool.Result {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := toolImpl.Call(context.Background(), tool.Call{ID: "call-1", Input: raw})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool result error = %#v", result)
	}
	return result
}

func resultPayload(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	for _, part := range result.Content {
		if part.JSON == nil {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(part.JSON.Value, &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	t.Fatalf("result content = %#v, want json payload", result.Content)
	return nil
}

func taskListContains(payload map[string]any, taskID string) bool {
	tasks, ok := payload["tasks"].([]any)
	if !ok {
		return false
	}
	for _, item := range tasks {
		task, ok := item.(map[string]any)
		if ok && task["task_id"] == taskID {
			return true
		}
	}
	return false
}

func stringValue(value any) string {
	out, _ := value.(string)
	return out
}

type resolverOnly struct {
	session *resolverOnlySession
}

func (r *resolverOnly) OpenTask(_ context.Context, ref sandbox.SessionRef) (sandbox.Session, bool, error) {
	if strings.TrimSpace(ref.ID) != r.session.snapshot.Ref.ID {
		return nil, false, nil
	}
	return r.session, true, nil
}

func (r *resolverOnly) ListTasks(context.Context, sandbox.SessionListQuery) ([]sandbox.SessionSnapshot, error) {
	return []sandbox.SessionSnapshot{r.session.snapshot}, nil
}

type resolverOnlySession struct {
	snapshot sandbox.SessionSnapshot
	stdout   string
}

func (s *resolverOnlySession) Ref() sandbox.SessionRef {
	return s.snapshot.Ref
}

func (s *resolverOnlySession) Snapshot(context.Context) (sandbox.SessionSnapshot, error) {
	return s.snapshot, nil
}

func (s *resolverOnlySession) Read(_ context.Context, cursor sandbox.OutputCursor) (sandbox.OutputSnapshot, error) {
	start := clampResolverCursor(cursor.Stdout, s.stdout)
	return sandbox.OutputSnapshot{
		Stdout: s.stdout[start:],
		Cursor: sandbox.OutputCursor{Stdout: int64(len(s.stdout))},
	}, nil
}

func (*resolverOnlySession) Write(context.Context, []byte) error {
	return nil
}

func (*resolverOnlySession) Cancel(context.Context) error {
	return nil
}

func (*resolverOnlySession) Wait(context.Context) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, nil
}

func (*resolverOnlySession) Close() error {
	return nil
}

func clampResolverCursor(cursor int64, text string) int64 {
	if cursor < 0 {
		return 0
	}
	if cursor > int64(len(text)) {
		return int64(len(text))
	}
	return cursor
}
