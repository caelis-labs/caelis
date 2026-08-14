//go:build !windows

package runtime

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
)

func TestRuntimeCommandTTYSupportsExactTaskInputAndPersistsCapability(t *testing.T) {
	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := newFileTaskStoreForTest(t)
	runtime.tasks.store = store
	hostRuntime := hostRuntimeForTest(t, activeSession.CWD)

	runResult := callRuntimeRunCommandTool(t, runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, hostRuntime),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"command":       "test -t 0 && stty raw -echo min 1 time 0 && value=$(dd bs=1 count=1 2>/dev/null) && stty min 0 time 2 && extra=$(dd bs=1 count=1 2>/dev/null | wc -c | tr -d '[:space:]') && printf 'got:%s;extra:%s' \"$value\" \"$extra\"",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
		"tty":           true,
	})
	running := testToolResultPayload(t, runResult)
	if running["state"] != string(taskapi.StateRunning) || running["supports_input"] != true {
		t.Fatalf("RunCommand payload = %#v, want running input-capable task", running)
	}
	handle, _ := running["handle"].(string)
	taskID, _ := testToolResultRuntimeMeta(t, runResult, "task")["task_id"].(string)
	entry, err := store.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if !entry.Running || !entry.SupportsInput {
		t.Fatalf("persisted entry = %#v, want running input capability", entry)
	}
	if tty, _ := entry.Spec["tty"].(bool); !tty {
		t.Fatalf("persisted spec = %#v, want tty=true", entry.Spec)
	}

	writeResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":         "write",
		"handle":         handle,
		"input":          "x",
		"append_newline": false,
	})
	completed := testToolResultPayload(t, writeResult)
	if completed["state"] != string(taskapi.StateCompleted) {
		t.Fatalf("Task write payload = %#v, want completed", completed)
	}
	if got, _ := completed["result"].(string); !strings.Contains(got, "got:x;extra:0") {
		t.Fatalf("Task write result = %q, want one exact raw byte and no appended newline", got)
	}
	if _, exists := completed["supports_input"]; exists {
		t.Fatalf("terminal payload = %#v, want input capability removed", completed)
	}
}

func TestRuntimeTaskWriteAllowsWhitespaceOnlyExactInput(t *testing.T) {
	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	_, err := (runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}).Call(context.Background(), tool.Call{
		ID:   "task-write-exact-whitespace",
		Name: tasktool.ToolName,
		Input: mustJSONRaw(map[string]any{
			"action": "write", "handle": "missing", "input": "\n", "append_newline": false,
		}),
	})
	if err == nil || strings.Contains(err.Error(), "input") {
		t.Fatalf("Task write error = %v, want validation success followed by missing handle", err)
	}
}

func TestRuntimeCommandTTYTaskWritePreservesUTF8Input(t *testing.T) {
	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	runtime.tasks.store = newFileTaskStoreForTest(t)
	hostRuntime := hostRuntimeForTest(t, activeSession.CWD)

	runResult := callRuntimeRunCommandTool(t, runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, hostRuntime),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"command":       `read -p "请输入你的名字: " name && echo "你好, $name! (输入来自 stdin/TTY)"`,
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
		"tty":           true,
	})
	running := testToolResultPayload(t, runResult)
	handle, _ := running["handle"].(string)

	writeResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action": "write",
		"handle": handle,
		"input":  "小明",
	})
	completed := testToolResultPayload(t, writeResult)
	got, _ := completed["result"].(string)
	if !utf8.ValidString(got) || !strings.Contains(got, "你好, 小明! (输入来自 stdin/TTY)") {
		t.Fatalf("Task write result = %q valid=%v, want intact UTF-8 input echo", got, utf8.ValidString(got))
	}
	delta, _ := testToolResultRuntimeMeta(t, writeResult, "task")["output_delta"].(string)
	if !utf8.ValidString(delta) || !strings.Contains(delta, "你好, 小明! (输入来自 stdin/TTY)") {
		t.Fatalf("Task write output_delta = %q valid=%v, want intact UTF-8 input echo", delta, utf8.ValidString(delta))
	}
}
