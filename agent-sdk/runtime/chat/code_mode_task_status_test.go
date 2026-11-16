package chat

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestScriptResultStateDoesNotOwnScriptLifecycle(t *testing.T) {
	for _, outer := range []string{"running", "completed", "cancelled", "failed"} {
		result := tool.Result{Metadata: map[string]any{"caelis": map[string]any{"runtime": map[string]any{"task": map[string]any{"kind": "code_mode", "state": outer}}}}}
		got := toolCallStatus(model.ToolCall{Name: "js_exec"}, result, map[string]any{"state": "running", "handle": "inner-command"}, true)
		if got != outer {
			t.Fatalf("outer=%s status=%s; inner result must not own outer lifecycle", outer, got)
		}
	}
}
