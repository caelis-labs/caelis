package runtime

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestCommandTaskSuccessfulDiagnosticTextIsOnlyOutput(t *testing.T) {
	const text = "DecodePermissionRequest\nexample: GOCACHE: read-only file system\n"
	_, active, runtime := newRuntimeRunCommandToolTestHarness(t)
	running := false
	handle := &yieldProbeSandboxSession{
		statusRunning: &running,
		stdout:        text,
		result: sandbox.CommandResult{
			Stdout: text, ExitCode: 0, Route: sandbox.RouteSandbox, Backend: sandbox.BackendSeatbelt,
		},
	}
	backend := newCommandStartProbe(handle, nil)
	snapshot, err := runtime.tasks.StartCommand(t.Context(), active, active.SessionRef, backend, task.CommandStartRequest{
		Command: "cat diagnostics.txt", Workdir: active.CWD, ParentCall: "quoted-diagnostics",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != task.StateCompleted {
		t.Fatalf("state = %s, want completed", snapshot.State)
	}
	for _, key := range []string{"error", "error_code", "system_hint"} {
		if value, exists := snapshot.Result[key]; exists {
			t.Fatalf("quoted diagnostics produced %s = %#v", key, value)
		}
	}
	if got, _ := snapshot.Result["result"].(string); !strings.Contains(got, text) {
		t.Fatalf("output lost quoted diagnostics: %q", got)
	}
}
