package shell

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestRunCommandSuccessfulDiagnosticTextIsOnlyOutput(t *testing.T) {
	const text = "DecodePermissionRequest\nexample: GOCACHE: read-only file system\n"
	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		Stdout: text, ExitCode: 0, Route: sandbox.RouteSandbox, Backend: sandbox.BackendSeatbelt,
	}}
	run, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatal(err)
	}
	result, err := run.Call(t.Context(), tool.Call{Name: RunCommandToolName, Input: json.RawMessage(`{"command":"cat diagnostics.txt"}`)})
	if err != nil || result.IsError {
		t.Fatalf("successful command result = %#v, error = %v", result, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"error", "error_code", "system_hint"} {
		if value, exists := payload[key]; exists {
			t.Fatalf("quoted diagnostics produced %s = %#v", key, value)
		}
	}
	if got, _ := payload["result"].(string); !strings.Contains(got, text) {
		t.Fatalf("output lost quoted diagnostics: %q", got)
	}
}
