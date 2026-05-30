package task

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

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

func newRuntime(t *testing.T) sandbox.Runtime {
	t.Helper()
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
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
