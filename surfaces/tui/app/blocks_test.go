package tuiapp

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestAssistantBlockRenderSuppressesDefaultAssistantLabel(t *testing.T) {
	block := NewAssistantBlock("assistant")
	block.Raw = "hello"
	rows := block.Render(BlockRenderContext{
		Width:     80,
		TermWidth: 100,
		Theme:     tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY),
	})
	if len(rows) == 0 {
		t.Fatal("Render() returned no rows")
	}
	if strings.Contains(rows[0].Plain, "assistant:") {
		t.Fatalf("assistant row = %q, want no assistant label", rows[0].Plain)
	}
}

func TestReasoningBlockRenderSuppressesDefaultAssistantLabel(t *testing.T) {
	block := NewReasoningBlock("assistant")
	block.Raw = "thinking"
	rows := block.Render(BlockRenderContext{
		Width:     80,
		TermWidth: 100,
		Theme:     tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY),
	})
	if len(rows) == 0 {
		t.Fatal("Render() returned no rows")
	}
	if strings.Contains(rows[0].Plain, "assistant:") {
		t.Fatalf("reasoning row = %q, want no assistant label", rows[0].Plain)
	}
}

func TestMergeSubagentStreamChunkPreservesOverlappingDelta(t *testing.T) {
	got := mergeSubagentStreamChunk("abcabc", "abcXYZ")
	if got != "abcabcabcXYZ" {
		t.Fatalf("merged chunk = %q, want exact appended delta", got)
	}
}

func TestMergeSubagentStreamChunkAcceptsCumulativeReplay(t *testing.T) {
	got := mergeSubagentStreamChunk("你好", "你好，世界")
	if got != "你好，世界" {
		t.Fatalf("merged cumulative chunk = %q, want cumulative replacement", got)
	}
}

func TestMainACPFinalCumulativeSuffixKeepsPreToolTextInPlace(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.AppendStreamChunk(SEAssistant, "Before tool.")
	block.UpdateToolWithMeta("bash-1", "BASH", "pwd", "ok", true, false, ToolUpdateMeta{})
	block.AppendStreamChunk(SEAssistant, "After")

	block.ReplaceFinalStreamChunk(SEAssistant, "Before tool.\n\nAfter tool done.")

	if len(block.Events) != 3 {
		t.Fatalf("events = %#v, want pre-tool assistant, tool, post-tool assistant", block.Events)
	}
	if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "Before tool." {
		t.Fatalf("pre-tool event = %#v, want original assistant text", block.Events[0])
	}
	if block.Events[1].Kind != SEToolCall || block.Events[1].Name != "BASH" {
		t.Fatalf("tool event = %#v, want BASH between assistant chunks", block.Events[1])
	}
	if block.Events[2].Kind != SEAssistant || block.Events[2].Text != "After tool done." {
		t.Fatalf("post-tool event = %#v, want only final suffix after prior text", block.Events[2])
	}
}

func TestParticipantFinalCumulativeSuffixKeepsPreToolTextInPlace(t *testing.T) {
	block := NewParticipantTurnBlock("session-1", "@self")
	block.AppendStreamChunk(SEAssistant, "Before tool.")
	block.UpdateToolWithMeta("bash-1", "BASH", "pwd", "ok", true, false, ToolUpdateMeta{})
	block.AppendStreamChunk(SEAssistant, "After")

	block.ReplaceFinalStreamChunk(SEAssistant, "Before tool.\n\nAfter tool done.")

	if len(block.Events) != 3 {
		t.Fatalf("events = %#v, want pre-tool assistant, tool, post-tool assistant", block.Events)
	}
	if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "Before tool." {
		t.Fatalf("pre-tool event = %#v, want original assistant text", block.Events[0])
	}
	if block.Events[1].Kind != SEToolCall || block.Events[1].Name != "BASH" {
		t.Fatalf("tool event = %#v, want BASH between assistant chunks", block.Events[1])
	}
	if block.Events[2].Kind != SEAssistant || block.Events[2].Text != "After tool done." {
		t.Fatalf("post-tool event = %#v, want only final suffix after prior text", block.Events[2])
	}
}

func TestTaskWaitResultDoesNotCompleteLinkedSpawnTool(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("spawn-1", "SPAWN", "inspect files", "", false, false, ToolUpdateMeta{TaskID: "jack"})
	block.UpdateToolWithMeta("task-wait-1", "TASK", "Wait jack", "final answer", true, false, ToolUpdateMeta{TaskID: "jack"})

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want SPAWN event plus TASK control event", block.Events)
	}
	ev := block.Events[0]
	if ev.Done || ev.Err || ev.Output != "" {
		t.Fatalf("linked event = %#v, want SPAWN unchanged until stream final", ev)
	}
	if block.Events[1].Name != "TASK" || block.Events[1].Output != "final answer" {
		t.Fatalf("task control event = %#v, want TASK result kept separate", block.Events[1])
	}
}

func TestTaskWaitResultStillUpdatesLinkedBashTool(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("bash-1", "BASH", "go test", "", false, false, ToolUpdateMeta{TaskID: "task-1"})
	block.UpdateToolWithMeta("task-wait-1", "TASK", "Wait task-1", "final answer", true, false, ToolUpdateMeta{TaskID: "task-1"})

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want BASH event plus TASK control event", block.Events)
	}
	ev := block.Events[0]
	if !ev.Done || ev.Err || ev.Output != "final answer" {
		t.Fatalf("linked event = %#v, want completed BASH output", ev)
	}

	block.UpdateToolWithMeta("bash-1", "BASH", "", "late running output", false, false, ToolUpdateMeta{TaskID: "task-1"})
	if got := block.Events[0].Output; got != "final answer" {
		t.Fatalf("late running update output = %q, want final answer preserved", got)
	}
}

func TestCompletedSpawnFinalWithSameCallIDReplacesExistingEvent(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("spawn-1", "SPAWN", "claude: first very long original prompt", "first done", true, false, ToolUpdateMeta{TaskID: "amy"})
	block.UpdateToolWithMeta("spawn-1", "SPAWN", "claude: ok", "second done", true, false, ToolUpdateMeta{TaskID: "amy"})

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one replaced SPAWN event", block.Events)
	}
	ev := block.Events[0]
	if !ev.Done || ev.Output != "second done" || ev.Args != "claude: ok" {
		t.Fatalf("spawn event = %#v, want follow-up final replacement", ev)
	}
}
