package displaypolicy

import "testing"

func TestSpawnDisplayArgsUsesAgentAndPrompt(t *testing.T) {
	raw := map[string]any{
		"agent":  "codex",
		"prompt": "inspect the transcript",
	}
	if got := SpawnFullDisplayArgs(raw); got != "codex: inspect the transcript" {
		t.Fatalf("SpawnFullDisplayArgs() = %q", got)
	}
}

func TestSpawnDisplayInputForResultMergesPromptFromLifecycleOutput(t *testing.T) {
	input := map[string]any{"agent": "codex"}
	output := map[string]any{"text": `{"prompt":"inspect repo","task_id":"task-1"} running`}
	got := SpawnDisplayInputForResult(input, output)
	if got["agent"] != "codex" || got["prompt"] != "inspect repo" || got["task_id"] != "task-1" {
		t.Fatalf("SpawnDisplayInputForResult() = %#v", got)
	}
	normalized := NormalizeSpawnDisplayRawMap(output)
	if normalized["text"] != "running" {
		t.Fatalf("text remainder = %#v", normalized["text"])
	}
}

func TestDisplayTerminalInitialOutputForSpawn(t *testing.T) {
	got := DisplayTerminalInitialOutput("SPAWN", map[string]any{
		"agent":  "codex",
		"prompt": "explain the patch",
	})
	if got != "SPAWN agent=codex\nexplain the patch\n" {
		t.Fatalf("DisplayTerminalInitialOutput() = %q", got)
	}
}

func TestCleanSubagentFinalOutput(t *testing.T) {
	raw := "### Done\n- `hello.txt` **created**\n| File | State |\n| --- | --- |\n| `hello.txt` | **ok** |"
	got := CleanSubagentFinalOutput(raw)
	want := "Done\nhello.txt created\nFile  State\nhello.txt  ok"
	if got != want {
		t.Fatalf("CleanSubagentFinalOutput() = %q, want %q", got, want)
	}
}

func TestTaskMetadataDisplayPolicy(t *testing.T) {
	meta := map[string]any{"caelis": map[string]any{"runtime": map[string]any{"tool": map[string]any{
		"target_id":   "sidecar",
		"action":      "write",
		"input":       "continue",
		"target_kind": "subagent",
	}}}}
	if got := ToolTaskID(nil, nil, meta); got != "sidecar" {
		t.Fatalf("ToolTaskID() = %q", got)
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
