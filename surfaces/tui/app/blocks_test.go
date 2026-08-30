package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestMainACPTurnAssistantRenderSuppressesDefaultAssistantLabel(t *testing.T) {
	block := NewMainACPTurnBlock("turn-1")
	block.ReplaceFinalStreamEvent(SEAssistant, "hello", narrativeSourceIdentity{})
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

func TestMainACPTurnReasoningRenderSuppressesDefaultAssistantLabel(t *testing.T) {
	block := NewMainACPTurnBlock("turn-1")
	block.ReplaceFinalStreamEvent(SEReasoning, "thinking", narrativeSourceIdentity{})
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

func TestACPTranscriptDecorativeColumnsAreExcludedFromSelection(t *testing.T) {
	block := NewMainACPTurnBlock("turn-selection-gutters")
	block.AppendStreamEvent(SEReasoning, "检查范围", newNarrativeSourceIdentity("reasoning-1", "", ""))
	block.AppendStreamEvent(SEAssistant, "审查结论", newNarrativeSourceIdentity("answer-1", "", ""))
	block.UpdateToolWithMeta("call-1", "Read", "file.go", "", false, false, ToolUpdateMeta{})
	rows := block.Render(BlockRenderContext{
		Width:     80,
		TermWidth: 100,
		Theme:     tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY),
	})

	wantPrefixes := map[string]bool{"› ": false, "· ": false, "• ": false}
	for _, row := range rows {
		for prefix := range wantPrefixes {
			if !strings.HasPrefix(row.Plain, prefix) {
				continue
			}
			wantPrefixes[prefix] = true
			if row.selectionIndent != displayColumns(prefix) {
				t.Fatalf("row %q selection indent = %d, want %d", row.Plain, row.selectionIndent, displayColumns(prefix))
			}
			copied := selectionTextFromLinesWithIndents(
				[]string{row.Plain},
				[]int{row.selectionIndent},
				textSelectionPoint{line: 0, col: 0},
				textSelectionPoint{line: 0, col: displayColumns(row.Plain)},
			)
			if strings.HasPrefix(copied, prefix) || strings.TrimSpace(copied) == "" {
				t.Fatalf("copied row = %q, want non-empty marker-free content from %q", copied, row.Plain)
			}
		}
	}
	for prefix, seen := range wantPrefixes {
		if !seen {
			t.Fatalf("rendered transcript missing decorative prefix %q: %#v", prefix, rows)
		}
	}
}

func TestAppendSubagentStreamDeltaPreservesOverlappingDelta(t *testing.T) {
	got := appendSubagentStreamDelta("abcabc", "abcXYZ")
	if got != "abcabcabcXYZ" {
		t.Fatalf("merged chunk = %q, want exact appended delta", got)
	}
}

func TestAppendSubagentStreamDeltaPreservesPrefixLikeDelta(t *testing.T) {
	got := appendSubagentStreamDelta("你好", "你好，世界")
	if got != "你好你好，世界" {
		t.Fatalf("merged prefix-like chunk = %q, want exact appended delta", got)
	}
}

func TestMergeSubagentNarrativeChunkPreservesACPDeltaAndMessageBoundary(t *testing.T) {
	combined, current := mergeSubagentNarrativeChunk("", "", "", "ha", "message-1")
	combined, current = mergeSubagentNarrativeChunk(combined, "message-1", current, "ha", "message-1")
	if combined != "haha" || current != "haha" {
		t.Fatalf("same-message delta merge = (%q, %q), want repeated bytes preserved", combined, current)
	}
	combined, current = mergeSubagentNarrativeChunk(combined, "message-1", current, "haha", "message-2")
	if combined != "haha\n\nhaha" || current != "haha" {
		t.Fatalf("different-message delta merge = (%q, %q), want a visible message boundary", combined, current)
	}
}

func TestMergeSubagentNarrativeChunkKeepsMarkdownBlocksSeparate(t *testing.T) {
	combined, current := mergeSubagentNarrativeChunk("", "", "", "> **任务 3 结果**：完成。\n---", "message-1")
	combined, current = mergeSubagentNarrativeChunk(combined, "message-1", current, "### ✅ 任务 4：创建文件", "message-2")
	if combined != "> **任务 3 结果**：完成。\n---\n\n### ✅ 任务 4：创建文件" || current != "### ✅ 任务 4：创建文件" {
		t.Fatalf("different-message Markdown merge = (%q, %q), want separate blocks", combined, current)
	}
}

func TestMergeSubagentNarrativeChunkAccumulatesBlankMessageID(t *testing.T) {
	combined, current := mergeSubagentNarrativeChunk("", "", "", "当前", "")
	combined, current = mergeSubagentNarrativeChunk(combined, "", current, "当前", "")
	combined, current = mergeSubagentNarrativeChunk(combined, "", current, "。", "")
	if combined != "当前当前。" || current != "当前当前。" {
		t.Fatalf("blank-message delta merge = (%q, %q), want every chunk accumulated", combined, current)
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

func TestMergeCommandStreamChunkDropsCumulativePrefixLines(t *testing.T) {
	existing := "🚀 异步 BASH 启动\n  第 1 秒...\n  第 2 秒...\n  第 3 秒...\n"
	incoming := "🚀 异步 BASH 启动\n  第 1 秒...\n  第 2 秒...\n  第 4 秒...\n  第 5 秒...\n✅ 异步 BASH 完成\n"
	want := existing + "  第 4 秒...\n  第 5 秒...\n✅ 异步 BASH 完成\n"
	if got := mergeCommandStreamChunk(existing, incoming); got != want {
		t.Fatalf("merged cumulative command chunk = %q, want %q", got, want)
	}
}

func TestRUNCommandOverlappingRunningTailDoesNotDuplicateOutput(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	first := "步骤 1/5 - 21:53:13\n步骤 2/5 - 21:53:14\n步骤 3/5 - 21:53:15\n步骤 4/5 - 21:53:16\n"
	tail := "步骤 4/5 - 21:53:16\n步骤 5/5 - 21:53:17\n"
	block.UpdateToolWithMeta("command-1", "RunCommand", "for i in 1 2 3 4 5", first, false, false, ToolUpdateMeta{TaskHandle: "task-1"})
	block.UpdateToolWithMeta("command-1", "RunCommand", "for i in 1 2 3 4 5", tail, false, false, ToolUpdateMeta{TaskHandle: "task-1"})

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one RunCommand event", block.Events)
	}
	want := first + "步骤 5/5 - 21:53:17\n"
	if got := block.Events[0].Output; got != want {
		t.Fatalf("RunCommand output = %q, want %q", got, want)
	}
}

func TestRUNCommandSplitNewlineStreamChunkPreserved(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("command-1", "RunCommand", "for i in 1 2", "Step 1/2", false, false, ToolUpdateMeta{TaskHandle: "task-1"})
	block.UpdateToolWithMeta("command-1", "RunCommand", "for i in 1 2", "\n", false, false, ToolUpdateMeta{TaskHandle: "task-1"})
	block.UpdateToolWithMeta("command-1", "RunCommand", "for i in 1 2", "Step 2/2\n", false, false, ToolUpdateMeta{TaskHandle: "task-1"})

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one RunCommand event", block.Events)
	}
	if got, want := block.Events[0].Output, "Step 1/2\nStep 2/2\n"; got != want {
		t.Fatalf("RunCommand output = %q, want %q", got, want)
	}
}

func TestToolPanelClickExpandsHiddenSummaryBeforeCollapse(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	output := strings.Join([]string{
		"Step 1/6",
		"Step 2/6",
		"Step 3/6",
		"Step 4/6",
		"Step 5/6",
		"Step 6/6",
	}, "\n")
	block.UpdateToolWithMeta("command-1", "RunCommand", "for i in 1 2 3 4 5 6", output, true, false, ToolUpdateMeta{})
	block.setToolPanelExpanded("command-1", true)

	if !block.toggleToolPanelClick("command-1") {
		t.Fatal("toggleToolPanelClick() = false, want hidden summary to expand")
	}
	if !block.toolPanelExpanded("command-1") {
		t.Fatal("tool panel collapsed, want it to stay expanded while showing full output")
	}
	if !block.toolPanelFullOutput("command-1") {
		t.Fatal("tool panel full output = false, want first click on hidden summary to show full output")
	}
}

func TestTerminalToolSummaryRowsCarryClickTokenAndExpandToFullOutput(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*Model)
	block := NewMainACPTurnBlock("session-1")
	output := strings.Join([]string{
		"{",
		`  "status": "success",`,
		`  "checks": [`,
		`    {"name": "archive", "ok": true},`,
		`    {"name": "schema", "ok": true},`,
		`    {"name": "metadata", "ok": true},`,
		`    {"name": "index", "ok": true},`,
		`    {"name": "documents", "ok": true},`,
		`    {"name": "checksum", "ok": true},`,
		`    {"name": "upload", "ok": true}`,
		"  ]",
		"}",
	}, "\n")
	block.UpdateToolWithMeta("command-1", "RunCommand", "/home/xueyongzhi/go/bin/cmpctl dict archive preflight --output json", output, true, false, ToolUpdateMeta{})
	model.doc.Append(block)
	model.syncViewportContent()

	var token string
	contentLine := -1
	for i, line := range model.viewportPlainLines {
		if !strings.Contains(line, "... +") {
			continue
		}
		if i >= len(model.viewportClickTokens) {
			t.Fatalf("summary line %d has no matching click token entry", i)
		}
		token = model.viewportClickTokens[i]
		contentLine = i
		break
	}
	if token == "" {
		t.Fatalf("collapsed terminal summary line has no click token\nplain rows: %#v\ntokens: %#v", model.viewportPlainLines, model.viewportClickTokens)
	}
	mouse := tea.Mouse{
		Button: tea.MouseLeft,
		X:      model.mainColumnX() + tuikit.GutterNarrative + 2,
		Y:      contentLine - model.viewportVisibleOffset(),
	}
	_ = model.handleViewportMousePress(mouse)
	_ = model.handleViewportMouseRelease(mouse)
	if !block.toolPanelFullOutput("command-1") {
		t.Fatal("tool panel full output = false after clicking summary token")
	}
	foundMiddleLine := false
	for _, line := range model.viewportPlainLines {
		if strings.Contains(line, `{"name": "metadata", "ok": true}`) {
			foundMiddleLine = true
			break
		}
	}
	if !foundMiddleLine {
		t.Fatalf("expanded viewport missing middle JSON row\nplain rows: %#v", model.viewportPlainLines)
	}
}

func TestExplorationSummaryDisplaysSkillName(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.Status = "completed"
	block.UpdateToolWithMeta("skill-1", "Skill", "superpowers:brainstorming", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("list-1", "LIST", "demo 9 entries", "", true, false, ToolUpdateMeta{ToolKind: "search"})

	rows := block.Render(BlockRenderContext{
		Width:     100,
		TermWidth: 100,
		Theme:     tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY),
	})
	plain := joinRenderedPlain(rows)
	if !strings.Contains(plain, "Skill superpowers:brainstorming") {
		t.Fatalf("rendered rows missing skill summary\nplain:\n%s", plain)
	}
	if strings.Contains(plain, "Load skill") {
		t.Fatalf("rendered rows still use Load skill\nplain:\n%s", plain)
	}
}

func TestExplorationSummaryKeepsWebFetchVerbAndFullURL(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.Status = "completed"
	block.UpdateToolWithMeta("fetch-1", surfaceToolWebFetch, "https://example.com/a/b.md", "", true, false, ToolUpdateMeta{ToolKind: "search"})
	block.UpdateToolWithMeta("search-1", surfaceToolWebSearch, `surface projection`, "", true, false, ToolUpdateMeta{ToolKind: "search"})

	rows := block.Render(BlockRenderContext{
		Width:     100,
		TermWidth: 100,
		Theme:     tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY),
	})
	plain := joinRenderedPlain(rows)
	if !strings.Contains(plain, "Fetch https://example.com/a/b.md") {
		t.Fatalf("rendered rows lost WebFetch verb or compacted its URL\nplain:\n%s", plain)
	}
	if !strings.Contains(plain, `Search surface projection`) {
		t.Fatalf("rendered rows merged WebSearch into the Fetch summary\nplain:\n%s", plain)
	}
	if strings.Contains(plain, "Search b.md") {
		t.Fatalf("rendered rows treated the WebFetch URL as a filesystem path\nplain:\n%s", plain)
	}
}

func TestExplorationSummaryPreservesStructuredQueryWrappers(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.Status = "completed"
	block.UpdateToolWithMeta("search-quoted", surfaceToolGrep, `"needle"`, "", true, false, ToolUpdateMeta{ToolKind: "search"})
	block.UpdateToolWithMeta("search-backtick", surfaceToolGrep, "`file`", "", true, false, ToolUpdateMeta{ToolKind: "search"})
	block.UpdateToolWithMeta("search-slash", surfaceToolGrep, `site:example.com/docs`, "", true, false, ToolUpdateMeta{ToolKind: "search"})

	plain := joinRenderedPlain(block.Render(BlockRenderContext{
		Width:     100,
		TermWidth: 100,
		Theme:     tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY),
	}))
	if !strings.Contains(plain, `Search "needle", `+"`file`"+`, site:example.com/docs`) {
		t.Fatalf("rendered rows rewrote structured search arguments\nplain:\n%s", plain)
	}
}

func TestAnonymousCompletedToolWithoutPresentationFieldsHasNoHeader(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	rows, _ := renderACPToolLifecycleRows("block-1", []SubagentEvent{{
		Kind: SEToolCall, CallID: "tool-1", Args: "echo hidden", Output: "hidden output", Done: true,
	}}, 0, 80, model.blockRenderContext(80), acpTranscriptRenderOptions{ToolOutputPanels: true})
	if len(rows) != 0 {
		t.Fatalf("anonymous completed tool rows = %#v, want sparse content retained without a synthetic Tool header", rows)
	}
}

func TestAnonymousToolPatchMaterializesAfterPresentationIdentityArrives(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	index := map[string]int{}
	events, _, _ := applyToolEventUpdate(nil, toolEventUpdate{
		CallID: "shell-1", Args: "printf ready", Output: "ready\n",
	}, index)
	rows, _ := renderACPToolLifecycleRows("block-1", events, 0, 80, model.blockRenderContext(80), acpTranscriptRenderOptions{ToolOutputPanels: true})
	if len(rows) != 0 {
		t.Fatalf("anonymous sparse update rows = %#v, want cached but invisible until ACP identity arrives", rows)
	}

	events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
		CallID: "shell-1", Meta: ToolUpdateMeta{ToolKind: "execute"},
	}, index)
	rows, _ = renderACPToolLifecycleRows("block-1", events, 0, 80, model.blockRenderContext(80), acpTranscriptRenderOptions{ToolOutputPanels: true})
	plain := joinRenderedPlain(rows)
	if !strings.Contains(plain, "Ran") || !strings.Contains(plain, "ready") || strings.Contains(plain, "Tool") {
		t.Fatalf("materialized sparse update = %q, want standard execute identity with retained output", plain)
	}
}

func TestToolOnlyExploredGroupWithoutHiddenContentHasNoClickToken(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*Model)

	block := NewMainACPTurnBlock("session-1")
	block.Status = "completed"
	block.UpdateToolWithMeta("skill-1", "Skill", "superpowers:brainstorming", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("list-1", "LIST", "demo 9 entries", "", true, false, ToolUpdateMeta{ToolKind: "search"})
	model.doc.Append(block)
	model.syncViewportContent()

	found := false
	for i, line := range model.viewportPlainLines {
		if !strings.Contains(line, "Skill superpowers:brainstorming") && !strings.Contains(line, "List demo 9 entries") {
			continue
		}
		found = true
		if i >= len(model.viewportClickTokens) {
			t.Fatalf("line %d has no matching click token entry", i)
		}
		if token := strings.TrimSpace(model.viewportClickTokens[i]); token != "" {
			t.Fatalf("tool-only explored row %q has click token %q", line, token)
		}
	}
	if !found {
		t.Fatalf("viewport missing explored summary rows\nplain rows: %#v", model.viewportPlainLines)
	}
	if len(block.ExpandedExplore) != 0 {
		t.Fatalf("ExpandedExplore = %#v, want no expansion state", block.ExpandedExplore)
	}
}

func TestAnonymousSyntheticFinalToolUpdateDoesNotAppendGenericFailedRow(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")

	block.UpdateToolWithMeta("command-1", "", "", "failed", true, true, ToolUpdateMeta{
		OutputSynthetic: true,
	})

	if len(block.Events) != 0 {
		t.Fatalf("anonymous synthetic final update appended events: %#v", block.Events)
	}
}

func TestAnonymousSyntheticFinalToolUpdatePreservesStreamedOutput(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	output := "{\n  \"status\": \"error\"\n}\n"
	block.UpdateToolWithMeta("command-1", "", "", output, false, false, ToolUpdateMeta{})

	block.UpdateToolWithMeta("command-1", "", "", "failed", true, true, ToolUpdateMeta{
		OutputSynthetic: true,
	})

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one merged event", block.Events)
	}
	event := block.Events[0]
	if !event.Done || !event.Err {
		t.Fatalf("event = %#v, want failed final event", event)
	}
	if event.Output != strings.TrimSpace(output) {
		t.Fatalf("event output = %q, want streamed output %q", event.Output, strings.TrimSpace(output))
	}
}

func TestTerminalToolFinalRawResultDoesNotReplaceTerminalOutput(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	terminalOutput := "preflight ok\n"
	rawResult := "{\n  \"status\": \"success\",\n  \"details\": [\"final raw result\"]\n}"
	block.UpdateToolWithMeta("command-1", "", "cmpctl dict archive preflight --output json", terminalOutput, false, false, ToolUpdateMeta{
		ToolKind:       "execute",
		Terminal:       true,
		OutputTerminal: true,
	})

	block.UpdateToolWithMeta("command-1", "", "", rawResult, true, false, ToolUpdateMeta{ToolKind: "execute"})

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one merged terminal event", block.Events)
	}
	event := block.Events[0]
	if !event.Done || event.Err {
		t.Fatalf("event = %#v, want successful final event", event)
	}
	if !event.Terminal {
		t.Fatalf("event = %#v, want terminal event", event)
	}
	if event.Output != terminalOutput {
		t.Fatalf("event output = %q, want terminal output %q", event.Output, terminalOutput)
	}
}

func TestTerminalToolFinalOutputReplacesStreamedOutput(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("command-1", "", "printf streaming", "partial\n", false, false, ToolUpdateMeta{
		ToolKind:       "execute",
		Terminal:       true,
		OutputTerminal: true,
	})

	block.UpdateToolWithMeta("command-1", "", "", "partial\ncomplete\n", true, false, ToolUpdateMeta{
		ToolKind:       "execute",
		Terminal:       true,
		OutputTerminal: true,
	})

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one merged terminal event", block.Events)
	}
	event := block.Events[0]
	if !event.Done || !event.Terminal {
		t.Fatalf("event = %#v, want completed terminal event", event)
	}
	if got, want := event.Output, "partial\ncomplete\n"; got != want {
		t.Fatalf("event output = %q, want final terminal output %q", got, want)
	}
}

func TestMainACPIdentifiedFinalStaysWithPreToolMessage(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.AppendStreamEvent(SEAssistant, "Before tool.", narrativeTestSource())
	block.UpdateToolWithMeta("command-1", "RunCommand", "pwd", "ok", true, false, ToolUpdateMeta{})
	block.AppendStreamEvent(SEAssistant, "After", narrativeTestSource())

	block.ReplaceFinalStreamEvent(SEAssistant, "Before tool.\n\nAfter tool done.", narrativeTestSource())

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want one identified assistant message followed by tool", block.Events)
	}
	if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "Before tool.\n\nAfter tool done." {
		t.Fatalf("assistant event = %#v, want canonical final on its original owner", block.Events[0])
	}
	if block.Events[1].Kind != SEToolCall || block.Events[1].Name != "RunCommand" {
		t.Fatalf("tool event = %#v, want RunCommand after its owning assistant message", block.Events[1])
	}
}

func TestMainACPAppendStreamEventPreservesPendingPrefixWithoutGhostEvent(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")

	block.AppendStreamEvent(SEAssistant, "    ", narrativeTestSource())
	if len(block.Events) != 0 {
		t.Fatalf("events = %#v, want no event until pending prefix has renderable content", block.Events)
	}
	block.AppendStreamEvent(SEAssistant, "fmt.Println()", narrativeTestSource())
	block.AppendStreamEvent(SEAssistant, " ", narrativeTestSource())
	block.AppendStreamEvent(SEAssistant, "tail", narrativeTestSource())

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one assistant event", block.Events)
	}
	if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "    fmt.Println() tail" {
		t.Fatalf("assistant event = %#v, want preserved boundary whitespace without initial ghost", block.Events[0])
	}
}

func TestParticipantAppendStreamEventPreservesPendingPrefixWithoutGhostEvent(t *testing.T) {
	block := NewParticipantTurnBlock("session-1", "@agent")

	block.AppendStreamEvent(SEAssistant, "    ", narrativeTestSource())
	if len(block.Events) != 0 {
		t.Fatalf("events = %#v, want no event until pending prefix has renderable content", block.Events)
	}
	block.AppendStreamEvent(SEAssistant, "answer", narrativeTestSource())

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one assistant event", block.Events)
	}
	if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "    answer" {
		t.Fatalf("assistant event = %#v, want preserved pending prefix", block.Events[0])
	}
}

func TestMainACPReasoningAppendStreamEventPreservesPendingPrefixWithoutGhostEvent(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")

	block.AppendStreamEvent(SEReasoning, "  ", narrativeTestSource())
	if len(block.Events) != 0 {
		t.Fatalf("events = %#v, want no event until pending reasoning prefix has renderable content", block.Events)
	}
	block.AppendStreamEvent(SEReasoning, "thinking", narrativeTestSource())

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one reasoning event", block.Events)
	}
	if block.Events[0].Kind != SEReasoning || block.Events[0].Text != "  thinking" {
		t.Fatalf("reasoning event = %#v, want preserved pending prefix", block.Events[0])
	}
}

func TestMainACPAppendStreamEventClearsAnonymousPendingPrefixAcrossToolBoundary(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")

	block.AppendStreamEvent(SEAssistant, "\n", narrativeSourceIdentity{})
	block.UpdateToolWithMeta("command-1", "RunCommand", "pwd", "ok", true, false, ToolUpdateMeta{})
	block.AppendStreamEvent(SEAssistant, "after", narrativeSourceIdentity{})

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want tool plus assistant event", block.Events)
	}
	if block.Events[1].Kind != SEAssistant || block.Events[1].Text != "after" {
		t.Fatalf("assistant event = %#v, want pending prefix cleared across tool barrier", block.Events[1])
	}
}

func TestMainACPClearActiveBuffersClearsPendingPrefix(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")

	block.AppendStreamEvent(SEAssistant, "    ", narrativeTestSource())
	block.ClearActiveBuffers()
	block.AppendStreamEvent(SEAssistant, "answer", narrativeTestSource())

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one assistant event", block.Events)
	}
	if block.Events[0].Text != "answer" {
		t.Fatalf("assistant text = %q, want pending prefix cleared", block.Events[0].Text)
	}
}

func TestMainACPFinalStreamEventMergesPendingPrefixWithoutDuplication(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.AppendStreamEvent(SEAssistant, "    ", narrativeTestSource())
	block.ReplaceFinalStreamEvent(SEAssistant, "    fmt.Println()", narrativeTestSource())

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one assistant event", block.Events)
	}
	if block.Events[0].Text != "    fmt.Println()" {
		t.Fatalf("assistant text = %q, want final text without duplicated pending prefix", block.Events[0].Text)
	}

	block = NewMainACPTurnBlock("session-1")
	block.AppendStreamEvent(SEAssistant, "    ", narrativeTestSource())
	block.ReplaceFinalStreamEvent(SEAssistant, "fmt.Println()", narrativeTestSource())

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one assistant event", block.Events)
	}
	if block.Events[0].Text != "    fmt.Println()" {
		t.Fatalf("assistant text = %q, want pending prefix prepended to suffix-only final text", block.Events[0].Text)
	}
}

func TestVisibleNarrativeEventsFiltersReplayedWhitespaceOnlyNarrative(t *testing.T) {
	events := []SubagentEvent{
		{Kind: SEAssistant, Text: "\n"},
		{Kind: SEToolCall, Name: "RunCommand", Args: "pwd"},
	}

	visible := visibleNarrativeEvents(events, "running")

	if len(visible) != 1 || visible[0].Kind != SEToolCall {
		t.Fatalf("visible events = %#v, want only tool event", visible)
	}
}

func TestMainACPClearActiveBuffersDropsSpeculativeNarrativeText(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.AppendStreamEvent(SEReasoning, "failed thought", narrativeTestSource())
	block.AppendStreamEvent(SEAssistant, "failed answer", narrativeTestSource())

	block.ClearActiveBuffers()
	block.AppendStreamEvent(SEAssistant, "retry answer", narrativeTestSource())

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want only retry narrative after reset", block.Events)
	}
	if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "retry answer" {
		t.Fatalf("retry event = %#v, want clean assistant retry text", block.Events[0])
	}
}

func TestMainACPClearActiveBuffersPreservesCanonicalFinalNarrative(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.ReplaceFinalStreamEvent(SEAssistant, "final answer", narrativeTestSource())

	block.ClearActiveBuffers()

	if len(block.Events) != 1 || block.Events[0].Text != "final answer" {
		t.Fatalf("events = %#v, want canonical final narrative preserved", block.Events)
	}
	if buffer := block.Events[0].ActiveBuffer; buffer == nil || buffer.HasTail() {
		t.Fatalf("final narrative buffer = %#v, want preserved sealed cache", buffer)
	}
}

func TestParticipantIdentifiedFinalStaysWithPreToolMessage(t *testing.T) {
	block := NewParticipantTurnBlock("session-1", "@self")
	block.AppendStreamEvent(SEAssistant, "Before tool.", narrativeTestSource())
	block.UpdateToolWithMeta("command-1", "RunCommand", "pwd", "ok", true, false, ToolUpdateMeta{})
	block.AppendStreamEvent(SEAssistant, "After", narrativeTestSource())

	block.ReplaceFinalStreamEvent(SEAssistant, "Before tool.\n\nAfter tool done.", narrativeTestSource())

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want one identified assistant message followed by tool", block.Events)
	}
	if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "Before tool.\n\nAfter tool done." {
		t.Fatalf("assistant event = %#v, want canonical final on its original owner", block.Events[0])
	}
	if block.Events[1].Kind != SEToolCall || block.Events[1].Name != "RunCommand" {
		t.Fatalf("tool event = %#v, want RunCommand after its owning assistant message", block.Events[1])
	}
}

func TestTaskWaitResultDoesNotCompleteLinkedSpawnTool(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("spawn-1", "Spawn", "inspect files", "", false, false, ToolUpdateMeta{TaskHandle: "jack"})
	block.UpdateToolWithMeta("task-wait-1", "Task", "Wait jack", "final answer", true, false, ToolUpdateMeta{TaskHandle: "jack"})

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want Spawn event plus Task control event", block.Events)
	}
	ev := block.Events[0]
	if ev.Done || ev.Err || ev.Output != "" {
		t.Fatalf("linked event = %#v, want Spawn unchanged until stream final", ev)
	}
	if block.Events[1].Name != "Task" || block.Events[1].Output != "final answer" {
		t.Fatalf("task control event = %#v, want Task result kept separate", block.Events[1])
	}
}

func TestTaskResultReplacesSelfTaskIDWithVisibleHandle(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("task-wait-1", "Task", "Wait self 3s", "", false, false, ToolUpdateMeta{TaskHandle: "self"})
	block.UpdateToolWithMeta("task-wait-1", "Task", "Wait jeff 3s", "still running", false, false, ToolUpdateMeta{TaskHandle: "jeff"})

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one merged Task event", block.Events)
	}
	if got := block.Events[0].TaskHandle; got != "jeff" {
		t.Fatalf("TaskID = %q, want visible handle", got)
	}
	if got := block.Events[0].Args; got != "Wait jeff 3s" {
		t.Fatalf("Args = %q, want visible handle", got)
	}
}

func TestToolEventIndexSurvivesStaleShiftAndUpdatesOpenTool(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("command-1", "RunCommand", "go test", "first", false, false, ToolUpdateMeta{TaskHandle: "task-1"})
	if got := block.toolEventIndex["command-1"]; got != 0 {
		t.Fatalf("initial tool index = %d, want 0", got)
	}

	block.Events = append([]SubagentEvent{{Kind: SEAssistant, Text: "shift"}}, block.Events...)
	block.UpdateToolWithMeta("command-1", "RunCommand", "go test", " second", false, false, ToolUpdateMeta{TaskHandle: "task-1"})

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

func TestToolLifecycleTerminalSnapshotIsMonotonic(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("search-1", "", "ToolCallStatus", "", true, false, ToolUpdateMeta{
		ToolKind: "search", ToolTitle: "Search ToolCallStatus", ToolStatus: "completed", ToolStatusExplicit: true,
	})
	block.UpdateToolWithMeta("search-1", "", "", "", true, false, ToolUpdateMeta{ToolStatus: "completed", ToolStatusExplicit: true})
	block.UpdateToolWithMeta("search-1", "", "", "stale running snapshot", false, false, ToolUpdateMeta{ToolStatus: "in_progress", ToolStatusExplicit: true})

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want repeated finals merged into one tool event", block.Events)
	}
	if event := block.Events[0]; !event.Done || event.Err || event.ToolKind != "search" || event.Title != "Search ToolCallStatus" || event.Output != "" {
		t.Fatalf("settled search = %#v, want retained terminal snapshot with stale progress ignored", event)
	}
	if acpTranscriptEventsHaveRunningTool(block.Events) {
		t.Fatal("late in_progress patch reopened a settled tool")
	}
}

func TestCompletedToolKeepsEstablishedExactNameAcrossRepeatedFinal(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("session-1")
	block.UpdateTool("spawn-1", "Spawn", "reviewer: inspect", "done", true, false)
	block.UpdateTool("spawn-1", "SPAWN", "reviewer: inspect", "done", true, false)

	if len(block.Events) != 1 || block.Events[0].Name != "Spawn" || !block.Events[0].Done {
		t.Fatalf("events = %#v, want one final with its established exact runtime name", block.Events)
	}
}

func TestTaskWaitResultDoesNotCompleteLinkedRunCommandTool(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("command-1", "RunCommand", "go test", "", false, false, ToolUpdateMeta{TaskHandle: "task-1"})
	block.UpdateToolWithMeta("task-wait-1", "Task", "Wait task-1", "final answer", true, false, ToolUpdateMeta{TaskHandle: "task-1"})

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want RunCommand event plus Task control event", block.Events)
	}
	ev := block.Events[0]
	if ev.Done || ev.Err || ev.Output != "" {
		t.Fatalf("linked event = %#v, want RunCommand unchanged until its own stream final", ev)
	}
	if block.Events[1].Name != "Task" || block.Events[1].Output != "final answer" {
		t.Fatalf("task control event = %#v, want Task result kept separate", block.Events[1])
	}

	block.UpdateToolWithMeta("command-1", "RunCommand", "", "late running output", false, false, ToolUpdateMeta{TaskHandle: "task-1"})
	if got := block.Events[0].Output; got != "late running output" {
		t.Fatalf("late running update output = %q, want RunCommand stream to update original panel", got)
	}
}

func TestTaskCancelShowsLinkedCommandWithoutCompletingCommand(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	command := `echo "启动一个长任务" && sleep 30 && echo "这行不会输出"`
	block.UpdateToolWithMeta("command-1", "RunCommand", command, "启动一个长任务\n", false, false, ToolUpdateMeta{TaskHandle: "task-1"})
	block.UpdateToolWithMeta("task-cancel-1", "Task", "Cancel", "", true, false, ToolUpdateMeta{
		TaskHandle: "task-1",
		TaskAction: "cancel",
	})

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want linked RunCommand event plus Task cancel row", block.Events)
	}
	if ev := block.Events[0]; ev.Done || ev.Output != "启动一个长任务\n" {
		t.Fatalf("linked command event = %#v, want Task cancel to leave RunCommand open until stream final", ev)
	}
	if got := block.Events[1].Args; got != "Cancel "+command {
		t.Fatalf("cancel args = %q, want linked command", got)
	}

	block.UpdateToolWithMeta("command-1", "RunCommand", command, "启动一个长任务\n", true, false, ToolUpdateMeta{TaskHandle: "task-1"})
	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want final RunCommand update to replace existing event", block.Events)
	}
	if got := block.Events[0].Output; strings.TrimSpace(got) != "启动一个长任务" {
		t.Fatalf("command output = %q, want final output on original event", got)
	}
}

func TestCompletedSpawnFinalWithSameCallIDReplacesExistingEvent(t *testing.T) {
	block := NewMainACPTurnBlock("session-1")
	block.UpdateToolWithMeta("spawn-1", "Spawn", "claude: first very long original prompt", "first done", true, false, ToolUpdateMeta{TaskHandle: "amy"})
	block.UpdateToolWithMeta("spawn-1", "Spawn", "claude: ok", "second done", true, false, ToolUpdateMeta{TaskHandle: "amy"})

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one replaced Spawn event", block.Events)
	}
	ev := block.Events[0]
	if !ev.Done || ev.Output != "second done" || ev.Args != "claude: ok" {
		t.Fatalf("spawn event = %#v, want follow-up final replacement", ev)
	}
}
