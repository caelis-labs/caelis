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

func TestMergeCommandStreamChunkDropsRepeatedLineOverlap(t *testing.T) {
	existing := "步骤 1/5 - 21:53:13\n步骤 2/5 - 21:53:14\n步骤 3/5 - 21:53:15\n步骤 4/5 - 21:53:16\n"
	incoming := "步骤 4/5 - 21:53:16\n步骤 5/5 - 21:53:17\n"
	want := existing + "步骤 5/5 - 21:53:17\n"
	if got := mergeCommandStreamChunk(existing, incoming); got != want {
		t.Fatalf("merged command chunk = %q, want %q", got, want)
	}
}

func TestMergeCommandStreamChunkKeepsPrefixLikeDelta(t *testing.T) {
	got := mergeCommandStreamChunk("abc", "abcdef")
	if got != "abcabcdef" {
		t.Fatalf("merged command chunk = %q, want exact appended delta", got)
	}
}

func TestRUNCommandOverlappingRunningTailDoesNotDuplicateOutput(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	first := "步骤 1/5 - 21:53:13\n步骤 2/5 - 21:53:14\n步骤 3/5 - 21:53:15\n步骤 4/5 - 21:53:16\n"
	tail := "步骤 4/5 - 21:53:16\n步骤 5/5 - 21:53:17\n"
	block.UpdateToolWithMeta("command-1", "RUN_COMMAND", "for i in 1 2 3 4 5", first, false, false, ToolUpdateMeta{TaskID: "task-1"})
	block.UpdateToolWithMeta("command-1", "RUN_COMMAND", "for i in 1 2 3 4 5", tail, false, false, ToolUpdateMeta{TaskID: "task-1"})

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one RUN_COMMAND event", block.Events)
	}
	want := first + "步骤 5/5 - 21:53:17\n"
	if got := block.Events[0].Output; got != want {
		t.Fatalf("RUN_COMMAND output = %q, want %q", got, want)
	}
}

func TestMainACPFinalCumulativeSuffixKeepsPreToolTextInPlace(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.AppendStreamChunk(SEAssistant, "Before tool.")
	block.UpdateToolWithMeta("command-1", "RUN_COMMAND", "pwd", "ok", true, false, ToolUpdateMeta{})
	block.AppendStreamChunk(SEAssistant, "After")

	block.ReplaceFinalStreamChunk(SEAssistant, "Before tool.\n\nAfter tool done.")

	if len(block.Events) != 3 {
		t.Fatalf("events = %#v, want pre-tool assistant, tool, post-tool assistant", block.Events)
	}
	if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "Before tool." {
		t.Fatalf("pre-tool event = %#v, want original assistant text", block.Events[0])
	}
	if block.Events[1].Kind != SEToolCall || block.Events[1].Name != "RUN_COMMAND" {
		t.Fatalf("tool event = %#v, want RUN_COMMAND between assistant chunks", block.Events[1])
	}
	if block.Events[2].Kind != SEAssistant || block.Events[2].Text != "After tool done." {
		t.Fatalf("post-tool event = %#v, want only final suffix after prior text", block.Events[2])
	}
}

func TestMainACPClearActiveBuffersDropsSpeculativeNarrativeText(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.AppendStreamChunk(SEReasoning, "failed thought")
	block.AppendStreamChunk(SEAssistant, "failed answer")

	block.ClearActiveBuffers()
	block.AppendStreamChunk(SEAssistant, "retry answer")

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want only retry narrative after reset", block.Events)
	}
	if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "retry answer" {
		t.Fatalf("retry event = %#v, want clean assistant retry text", block.Events[0])
	}
}

func TestParticipantFinalCumulativeSuffixKeepsPreToolTextInPlace(t *testing.T) {
	block := NewParticipantTurnBlock("session-1", "@self")
	block.AppendStreamChunk(SEAssistant, "Before tool.")
	block.UpdateToolWithMeta("command-1", "RUN_COMMAND", "pwd", "ok", true, false, ToolUpdateMeta{})
	block.AppendStreamChunk(SEAssistant, "After")

	block.ReplaceFinalStreamChunk(SEAssistant, "Before tool.\n\nAfter tool done.")

	if len(block.Events) != 3 {
		t.Fatalf("events = %#v, want pre-tool assistant, tool, post-tool assistant", block.Events)
	}
	if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "Before tool." {
		t.Fatalf("pre-tool event = %#v, want original assistant text", block.Events[0])
	}
	if block.Events[1].Kind != SEToolCall || block.Events[1].Name != "RUN_COMMAND" {
		t.Fatalf("tool event = %#v, want RUN_COMMAND between assistant chunks", block.Events[1])
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

func TestTaskResultReplacesSelfTaskIDWithVisibleHandle(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("task-wait-1", "TASK", "Wait self 3s", "", false, false, ToolUpdateMeta{TaskID: "self"})
	block.UpdateToolWithMeta("task-wait-1", "TASK", "Wait jeff 3s", "still running", false, false, ToolUpdateMeta{TaskID: "jeff"})

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one merged TASK event", block.Events)
	}
	if got := block.Events[0].TaskID; got != "jeff" {
		t.Fatalf("TaskID = %q, want visible handle", got)
	}
	if got := block.Events[0].Args; got != "Wait jeff 3s" {
		t.Fatalf("Args = %q, want visible handle", got)
	}
}

func TestToolEventIndexSurvivesStaleShiftAndUpdatesOpenTool(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("command-1", "RUN_COMMAND", "go test", "first", false, false, ToolUpdateMeta{TaskID: "task-1"})
	if got := block.toolEventIndex["command-1"]; got != 0 {
		t.Fatalf("initial tool index = %d, want 0", got)
	}

	block.Events = append([]SubagentEvent{{Kind: SEAssistant, Text: "shift"}}, block.Events...)
	block.UpdateToolWithMeta("command-1", "RUN_COMMAND", "go test", " second", false, false, ToolUpdateMeta{TaskID: "task-1"})

	if got := block.toolEventIndex["command-1"]; got != 1 {
		t.Fatalf("refreshed tool index = %d, want 1", got)
	}
	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want shifted assistant plus one tool event", block.Events)
	}
	if got := block.Events[1].Output; got != "first second" {
		t.Fatalf("tool output = %q, want merged output after stale-index fallback", got)
	}
}

func TestTaskWaitResultDoesNotCompleteLinkedRunCommandTool(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("command-1", "RUN_COMMAND", "go test", "", false, false, ToolUpdateMeta{TaskID: "task-1"})
	block.UpdateToolWithMeta("task-wait-1", "TASK", "Wait task-1", "final answer", true, false, ToolUpdateMeta{TaskID: "task-1"})

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want RUN_COMMAND event plus TASK control event", block.Events)
	}
	ev := block.Events[0]
	if ev.Done || ev.Err || ev.Output != "" {
		t.Fatalf("linked event = %#v, want RUN_COMMAND unchanged until its own stream final", ev)
	}
	if block.Events[1].Name != "TASK" || block.Events[1].Output != "final answer" {
		t.Fatalf("task control event = %#v, want TASK result kept separate", block.Events[1])
	}

	block.UpdateToolWithMeta("command-1", "RUN_COMMAND", "", "late running output", false, false, ToolUpdateMeta{TaskID: "task-1"})
	if got := block.Events[0].Output; got != "late running output" {
		t.Fatalf("late running update output = %q, want RUN_COMMAND stream to update original panel", got)
	}
}

func TestTaskCancelShowsLinkedCommandWithoutCompletingCommand(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	command := `echo "启动一个长任务" && sleep 30 && echo "这行不会输出"`
	block.UpdateToolWithMeta("command-1", "RUN_COMMAND", command, "启动一个长任务\n", false, false, ToolUpdateMeta{TaskID: "task-1"})
	block.UpdateToolWithMeta("task-cancel-1", "TASK", "Cancel", "", true, false, ToolUpdateMeta{
		TaskID:     "task-1",
		TaskAction: "cancel",
	})

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want linked RUN_COMMAND event plus TASK cancel row", block.Events)
	}
	if ev := block.Events[0]; ev.Done || ev.Output != "启动一个长任务\n" {
		t.Fatalf("linked command event = %#v, want TASK cancel to leave RUN_COMMAND open until stream final", ev)
	}
	if got := block.Events[1].Args; got != "Cancel "+command {
		t.Fatalf("cancel args = %q, want linked command", got)
	}

	block.UpdateToolWithMeta("command-1", "RUN_COMMAND", command, "启动一个长任务\n", true, false, ToolUpdateMeta{TaskID: "task-1"})
	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want final RUN_COMMAND update to replace existing event", block.Events)
	}
	if got := block.Events[0].Output; strings.TrimSpace(got) != "启动一个长任务" {
		t.Fatalf("command output = %q, want final output on original event", got)
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
