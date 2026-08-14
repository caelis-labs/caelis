//go:build windows

package runtime

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
)

func TestRuntimeCommandTTYDefaultTaskWriteSubmitsWindowsLine(t *testing.T) {
	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	hostRuntime := hostRuntimeForTest(t, activeSession.CWD)

	runResult := callRuntimeRunCommandTool(t, runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, hostRuntime),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"command":       "$value = [Console]::ReadLine(); [Console]::Write(\"got:$value\")",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
		"tty":           true,
	})
	running := testToolResultPayload(t, runResult)
	if running["state"] != string(taskapi.StateRunning) || running["supports_input"] != true {
		t.Fatalf("RunCommand payload = %#v, want running input-capable task", running)
	}
	handle, _ := running["handle"].(string)
	writeResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action": "write",
		"handle": handle,
		"input":  "demo",
	})
	completed := testToolResultPayload(t, writeResult)
	if completed["state"] != string(taskapi.StateCompleted) {
		t.Fatalf("Task write payload = %#v, want completed", completed)
	}
	if got, _ := completed["result"].(string); !strings.Contains(got, "got:demo") {
		t.Fatalf("Task write result = %q, want Windows line submission", got)
	}
}
