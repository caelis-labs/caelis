package projection

import "testing"

func TestTaskMetadataDisplayPolicy(t *testing.T) {
	meta := map[string]any{"caelis": map[string]any{"runtime": map[string]any{"tool": map[string]any{
		"target_handle": "sidecar",
		"action":        "write",
		"input":         "continue",
		"target_kind":   "subagent",
	}}}}
	if got := ToolTaskHandle(nil, nil, meta); got != "sidecar" {
		t.Fatalf("ToolTaskHandle() = %q", got)
	}
	if got := ToolTaskAction(nil, nil, meta); got != "write" {
		t.Fatalf("ToolTaskAction() = %q", got)
	}
	if got := ToolTaskInput(nil, nil, meta); got != "continue" {
		t.Fatalf("ToolTaskInput() = %q", got)
	}
	if got := ToolTaskTargetKind(nil, nil, meta); got != "subagent" {
		t.Fatalf("ToolTaskTargetKind() = %q", got)
	}
}

func TestTaskOutputDisplayPolicyPreservesTerminalText(t *testing.T) {
	command := map[string]any{
		"stdout": "one\n",
		"stderr": "two\n",
	}
	if got := CommandTaskFinalText("completed", command); got != "one\ntwo\n" {
		t.Fatalf("CommandTaskFinalText() = %q", got)
	}
	if got := CommandTaskOutputText(map[string]any{"stdout": "raw\n"}); got != "raw\n" {
		t.Fatalf("CommandTaskOutputText() = %q", got)
	}
	if got := CommandTaskFinalText("completed", nil); got != "(no output)" {
		t.Fatalf("CommandTaskFinalText(no output) = %q", got)
	}
	if got := SubagentTaskFinalText("completed", map[string]any{"final_message": "done\n"}); got != "done\n" {
		t.Fatalf("SubagentTaskFinalText() = %q", got)
	}
	if got := SubagentTaskFinalText("failed", map[string]any{"error": "failed\n", "result": "ignored"}); got != "failed\n" {
		t.Fatalf("SubagentTaskFinalText(failed) = %q", got)
	}
}
