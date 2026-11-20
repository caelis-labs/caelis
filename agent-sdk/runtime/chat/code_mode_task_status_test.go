package chat

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestScriptResultStateDoesNotOwnScriptLifecycle(t *testing.T) {
	for _, outer := range []string{"running", "completed", "cancelled", "failed"} {
		result := map[string]any{"caelis": map[string]any{"runtime": map[string]any{"task": map[string]any{"kind": "code_mode", "state": outer}}}}
		got := toolCallStatus(model.ToolCall{Name: "js_exec"}, result, nil, map[string]any{"state": "running", "handle": "inner-command"}, true)
		if got != outer {
			t.Fatalf("outer=%s status=%s; inner result must not own outer lifecycle", outer, got)
		}
	}
}
