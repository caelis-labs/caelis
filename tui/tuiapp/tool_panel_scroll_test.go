package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTerminalToolPanelShowsTailWithoutInternalScroll(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme}

	for _, toolName := range []string{"BASH", "SPAWN"} {
		t.Run(toolName, func(t *testing.T) {
			block := NewMainACPTurnBlock("session-1")
			callID := strings.ToLower(toolName) + "-1"
			lines := make([]string, 0, 30)
			for i := 1; i <= 30; i++ {
				if i%5 == 0 {
					lines = append(lines, "")
				}
				lines = append(lines, fmt.Sprintf("Step %02d/30", i))
			}
			block.UpdateTool(callID, toolName, "run long task", strings.Join(lines, "\n"), false, false)

			rows := block.Render(ctx)
			plain := renderedPlainRows(rows)
			if got := countRowsContaining(plain, "Step "); got != acpTerminalPanelMaxLines {
				t.Fatalf("visible terminal rows = %d, want %d\n%s", got, acpTerminalPanelMaxLines, strings.Join(plain, "\n"))
			}
			joined := strings.Join(plain, "\n")
			if strings.Contains(joined, "Step 01/30") {
				t.Fatalf("initial panel should follow tail, got\n%s", joined)
			}
			if !strings.Contains(joined, "Step 30/30") {
				t.Fatalf("initial panel missing tail output, got\n%s", joined)
			}
			if strings.Contains(joined, "Step 22/30") {
				t.Fatalf("panel should keep only the last non-empty rows, got\n%s", joined)
			}

			if block.ScrollToolPanel(callID, -30, ctx) {
				t.Fatal("ScrollToolPanel returned true, want terminal panels to ignore internal scroll")
			}

			rows = block.Render(ctx)
			plain = renderedPlainRows(rows)
			joined = strings.Join(plain, "\n")
			if strings.Contains(joined, "Step 01/30") || !strings.Contains(joined, "Step 30/30") {
				t.Fatalf("scroll attempt should leave tail output visible, got\n%s", joined)
			}
		})
	}
}

func TestCompletedTerminalToolStaysExpandedWhenTurnCompletes(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme}
	block := NewMainACPTurnBlock("session-1")
	lines := make([]string, 0, 12)
	for i := 1; i <= 12; i++ {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}
	block.UpdateTool("bash-1", "BASH", "run long task", strings.Join(lines, "\n"), false, false)
	block.UpdateTool("bash-1", "BASH", "run long task", strings.Join(lines, "\n"), true, false)
	block.SetStatus("completed", "", "", nowForToolPanelTest())

	rows := block.Render(ctx)
	plain := renderedPlainRows(rows)
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "• Ran run long task") || !strings.Contains(joined, "line 01") || !strings.Contains(joined, "line 12") {
		t.Fatalf("rendered rows = %q, want completed BASH output still expanded", joined)
	}
	for _, want := range []string{"line 01", "line 02", "... 8 lines hidden ...", "line 11", "line 12"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows missing %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "line 03") || strings.Contains(joined, "line 10") {
		t.Fatalf("completed terminal output should keep first two and last two lines, got\n%s", joined)
	}

	if !block.toggleToolPanelClick("bash-1") {
		t.Fatal("expected completed terminal summary click to expand full output")
	}
	rows = block.Render(ctx)
	plain = renderedPlainRows(rows)
	joined = strings.Join(plain, "\n")
	if strings.Contains(joined, "lines hidden") {
		t.Fatalf("expanded terminal output should remove hidden marker, got\n%s", joined)
	}
	for _, want := range []string{"line 03", "line 10"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded terminal output missing %q\n%s", want, joined)
		}
	}
}

func TestACPGenericToolUsesStandardPanelTemplateAndSummarizesFinalOutput(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme}
	block := NewParticipantTurnBlock("codex-001", "codex-001")
	output := strings.Join([]string{
		"result 01",
		"result 02",
		"result 03",
		"result 04",
		"result 05",
		"result 06",
	}, "\n")

	block.UpdateToolWithMeta("ws-1", "fetch", `"weather: Shanghai, China"`, output, true, false, ToolUpdateMeta{
		ToolKind:        "fetch",
		DisableGrouping: true,
	})

	rows := block.Render(ctx)
	plain := renderedPlainRows(rows)
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, `• Search "weather: Shanghai, China"`) {
		t.Fatalf("generic ACP tool should use standard header, got\n%s", joined)
	}
	if strings.Contains(joined, "▾ Searching the Web") || strings.Contains(joined, "{") {
		t.Fatalf("generic ACP tool leaked old expandable/raw-json header, got\n%s", joined)
	}
	for _, want := range []string{"result 01", "result 02", "... 2 lines hidden ...", "result 05", "result 06"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("generic ACP tool output missing %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "result 03") || strings.Contains(joined, "result 04") {
		t.Fatalf("generic ACP tool final output should be summarized, got\n%s", joined)
	}

	if !block.toggleToolPanelClick("ws-1") {
		t.Fatal("expected generic ACP summary click to expand full output")
	}
	rows = block.Render(ctx)
	plain = renderedPlainRows(rows)
	joined = strings.Join(plain, "\n")
	if strings.Contains(joined, "lines hidden") || !strings.Contains(joined, "result 03") || !strings.Contains(joined, "result 04") {
		t.Fatalf("expanded generic ACP tool output should show hidden lines, got\n%s", joined)
	}
}

func TestTerminalToolPanelCapsWrappedRows(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 28, TermWidth: 28, Theme: model.theme}
	longLine := strings.Repeat("0123456789", 20)

	body := renderACPTerminalPanelBody(longLine, 28, ctx, false)
	if got := len(body); got != acpTerminalPanelMaxLines {
		t.Fatalf("wrapped terminal rows = %d, want %d\n%s", got, acpTerminalPanelMaxLines, strings.Join(body, "\n"))
	}
}

func TestSubagentPanelShowsLiveTailAndCompletedFinalAnswer(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme}
	panel := NewSubagentPanelBlock("spawn-1", "", "helper", "spawn-call-1")

	for i := 1; i <= 20; i++ {
		panel.Events = append(panel.Events, SubagentEvent{Kind: SEReasoning, Text: fmt.Sprintf("progress %02d", i)})
	}

	rows := panel.Render(ctx)
	plain := renderedPlainRows(rows)
	joined := strings.Join(plain, "\n")
	if got := countRowsContaining(plain, "progress "); got != subagentOutputPreviewLines {
		t.Fatalf("visible subagent rows = %d, want %d\n%s", got, subagentOutputPreviewLines, joined)
	}
	if strings.Contains(joined, "progress 01") || strings.Contains(joined, "progress 08") {
		t.Fatalf("live subagent panel should hide old progress, got\n%s", joined)
	}
	if !strings.Contains(joined, "progress 20") {
		t.Fatalf("live subagent panel missing newest progress, got\n%s", joined)
	}
	if panel.Scroll(-30, ctx) {
		t.Fatal("SubagentPanelBlock.Scroll returned true, want subagent panels to ignore internal scroll")
	}

	panel.Events = append(panel.Events, SubagentEvent{Kind: SEAssistant, Text: "final child answer\nfinal line 2"})
	panel.Status = "completed"
	rows = panel.Render(ctx)
	plain = renderedPlainRows(rows)
	joined = strings.Join(plain, "\n")
	if !strings.Contains(joined, "final child answer") || !strings.Contains(joined, "final line 2") {
		t.Fatalf("completed subagent panel missing final answer, got\n%s", joined)
	}
	if strings.Contains(joined, "progress ") || strings.Contains(joined, "completed") {
		t.Fatalf("completed subagent panel should render only final output, got\n%s", joined)
	}
}

func TestCompletedSubagentPanelPreservesToolOnlyOutput(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme}
	panel := NewSubagentPanelBlock("spawn-1", "", "helper", "spawn-call-1")
	panel.Events = append(panel.Events, SubagentEvent{
		Kind:   SEToolCall,
		CallID: "read-1",
		Name:   "READ",
		Args:   "README.md",
		Output: "README.md 1~20",
		Done:   true,
	})
	panel.Status = "completed"

	rows := panel.Render(ctx)
	plain := renderedPlainRows(rows)
	joined := strings.Join(plain, "\n")
	if strings.Contains(joined, "waiting for subagent output") {
		t.Fatalf("completed tool-only panel rendered placeholder, got\n%s", joined)
	}
	if !strings.Contains(joined, "READ") || !strings.Contains(joined, "README.md") {
		t.Fatalf("completed tool-only panel dropped tool output, got\n%s", joined)
	}
}

func renderedPlainRows(rows []RenderedRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Plain)
	}
	return out
}

func indexOfRowContaining(lines []string, needle string) int {
	for i, line := range lines {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}

func nowForToolPanelTest() time.Time {
	return time.Now()
}

func countRowsContaining(lines []string, needle string) int {
	count := 0
	for _, line := range lines {
		if strings.Contains(line, needle) {
			count++
		}
	}
	return count
}
