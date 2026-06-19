package tuiapp

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTerminalToolPanelShowsTailWithoutInternalScroll(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme}

	for _, toolName := range []string{"RUN_COMMAND", "SPAWN"} {
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
	block.UpdateTool("command-1", "RUN_COMMAND", "run long task", strings.Join(lines, "\n"), false, false)
	block.UpdateTool("command-1", "RUN_COMMAND", "run long task", strings.Join(lines, "\n"), true, false)
	block.SetStatus("completed", "", "", nowForToolPanelTest())

	rows := block.Render(ctx)
	plain := renderedPlainRows(rows)
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "• Ran run long task") || !strings.Contains(joined, "line 01") || !strings.Contains(joined, "line 12") {
		t.Fatalf("rendered rows = %q, want completed RUN_COMMAND output still expanded", joined)
	}
	for _, want := range []string{"line 01", "line 02", "... +8 lines", "line 11", "line 12"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered rows missing %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "line 03") || strings.Contains(joined, "line 10") {
		t.Fatalf("completed terminal output should keep first two and last two lines, got\n%s", joined)
	}

	if !block.toggleToolPanelClick("command-1") {
		t.Fatal("expected completed terminal summary click to expand full output")
	}
	rows = block.Render(ctx)
	plain = renderedPlainRows(rows)
	joined = strings.Join(plain, "\n")
	if strings.Contains(joined, "... +") {
		t.Fatalf("expanded terminal output should remove hidden marker, got\n%s", joined)
	}
	for _, want := range []string{"line 03", "line 10"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded terminal output missing %q\n%s", want, joined)
		}
	}
	if !rowsContainClickToken(rows, acpToolPanelClickToken("command-1")) {
		t.Fatalf("expanded terminal output should expose a collapse click token: %#v", plain)
	}
	if !block.toggleToolPanelClick("command-1") {
		t.Fatal("expected expanded terminal output click to collapse back to summary")
	}
	rows = block.Render(ctx)
	plain = renderedPlainRows(rows)
	joined = strings.Join(plain, "\n")
	if !strings.Contains(joined, "... +8 lines") || strings.Contains(joined, "line 03") || strings.Contains(joined, "line 10") {
		t.Fatalf("terminal output should collapse back to summary, got\n%s", joined)
	}
}

func TestTerminalToolPanelPreservesLineBreaks(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 110, TermWidth: 110, Theme: model.theme}
	block := NewMainACPTurnBlock("session-1")
	output := strings.Join([]string{"calculator", "demo-caelis.exe", "go.mod", "main.go"}, "\n")

	block.UpdateTool("command-1", "RUN_COMMAND", "Get-ChildItem -Name", output, true, false)

	rows := block.Render(ctx)
	plain := renderedPlainRows(rows)
	joined := strings.Join(plain, "\n")
	if strings.Contains(joined, "calculatordemo-caelis.exego.modmain.go") {
		t.Fatalf("terminal panel collapsed line breaks, got\n%s", joined)
	}
	for _, name := range []string{"calculator", "demo-caelis.exe", "go.mod", "main.go"} {
		if indexOfRowContaining(plain, name) < 0 {
			t.Fatalf("terminal panel missing %q in rows %#v", name, plain)
		}
	}
	if indexOfRowContaining(plain, "calculator") == indexOfRowContaining(plain, "demo-caelis.exe") {
		t.Fatalf("terminal panel rendered separate filenames on the same row: %#v", plain)
	}
}

func TestSpawnTerminalPanelCleansMessySubagentPreview(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme}

	t.Run("running filters protocol noise and duplicate progress", func(t *testing.T) {
		block := NewMainACPTurnBlock("session-1")
		output := strings.Join([]string{
			`{"type":"session/update","running":true,"terminal_id":"spawn-1"}`,
			"progress: scanning",
			"progress: scanning",
			"ran go test ./surfaces/tui/app",
			"error: retrying failed package",
			"latest status: waiting for file scan",
		}, "\n")
		block.UpdateTool("spawn-noisy", "SPAWN", "helper: inspect", output, false, false)

		plain := renderedPlainRows(block.Render(ctx))
		joined := strings.Join(plain, "\n")
		for _, forbidden := range []string{"session/update", "terminal_id", `{"type"`} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("running SPAWN preview leaked noise %q:\n%s", forbidden, joined)
			}
		}
		if got := countRowsContaining(plain, "progress: scanning"); got != 1 {
			t.Fatalf("running SPAWN preview progress rows = %d, want 1\n%s", got, joined)
		}
		for _, want := range []string{"ran go test ./surfaces/tui/app", "error: retrying failed package", "latest status"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("running SPAWN preview missing %q:\n%s", want, joined)
			}
		}
		if got := len(plain); got > 1+acpTerminalPanelMaxLines {
			t.Fatalf("running SPAWN preview rows = %d, want capped at header + %d\n%s", got, acpTerminalPanelMaxLines, joined)
		}
	})

	t.Run("running compacts duplicate tool lifecycle lines", func(t *testing.T) {
		root := t.TempDir()
		t.Chdir(root)
		block := NewMainACPTurnBlock("session-1")
		readPath := filepath.Join(root, "a.py")
		writePath := filepath.Join(root, "spawn_demo_output.txt")
		output := strings.Join([]string{
			"READ " + readPath,
			"READ " + readPath + " completed",
			"WRITE " + writePath,
			"WRITE " + writePath + " completed",
			"completed",
		}, "\n")
		block.UpdateTool("spawn-tools", "SPAWN", "helper: inspect", output, false, false)

		plain := renderedPlainRows(block.Render(ctx))
		joined := strings.Join(plain, "\n")
		if got := countRowsContaining(plain, "Read a.py"); got != 1 {
			t.Fatalf("running SPAWN preview Read rows = %d, want 1\n%s", got, joined)
		}
		if got := countRowsContaining(plain, "Write spawn_demo_output.txt"); got != 1 {
			t.Fatalf("running SPAWN preview Write rows = %d, want 1\n%s", got, joined)
		}
		for _, forbidden := range []string{root, "READ ", "WRITE ", "completed"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("running SPAWN preview leaked %q:\n%s", forbidden, joined)
			}
		}
	})

	t.Run("final cleans markdown table and fences", func(t *testing.T) {
		block := NewMainACPTurnBlock("session-1")
		output := strings.Join([]string{
			`{"type":"event","task_id":"spawn-1"}`,
			"```markdown",
			"### Done",
			"- `hello.txt` **created**",
			"| File | State |",
			"| --- | --- |",
			"| `hello.txt` | **ok** |",
			"```",
		}, "\n")
		block.UpdateTool("spawn-messy", "SPAWN", "helper: write", output, false, false)
		block.UpdateTool("spawn-messy", "SPAWN", "helper: write", output, true, false)

		plain := renderedPlainRows(block.Render(ctx))
		joined := strings.Join(plain, "\n")
		for _, want := range []string{"Done", "hello.txt created", "File  State", "hello.txt  ok"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("final SPAWN preview missing %q:\n%s", want, joined)
			}
		}
		for _, forbidden := range []string{`{"type"`, "```", "| --- |", "**", "`hello.txt`"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("final SPAWN preview leaked %q:\n%s", forbidden, joined)
			}
		}
		if got := len(plain); got > 1+acpTerminalPanelMaxLines {
			t.Fatalf("final SPAWN preview rows = %d, want capped at header + %d\n%s", got, acpTerminalPanelMaxLines, joined)
		}
	})
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

	block.UpdateToolWithMeta("ws-1", "lookup_weather", `"weather: Shanghai, China"`, output, true, false, ToolUpdateMeta{
		ToolKind: "other",
	})

	rows := block.Render(ctx)
	plain := renderedPlainRows(rows)
	joined := strings.Join(plain, "\n")
	if !rowsContainClickToken(rows, acpToolPanelClickToken("ws-1")) {
		t.Fatalf("summarized generic ACP tool should expose expand click token: %#v", plain)
	}
	if !strings.Contains(joined, `lookup_weather "weather: Shanghai, China"`) {
		t.Fatalf("generic ACP tool should use standard header, got\n%s", joined)
	}
	if strings.Contains(joined, "▾ Searching the Web") || strings.Contains(joined, "{") {
		t.Fatalf("generic ACP tool leaked old expandable/raw-json header, got\n%s", joined)
	}
	for _, want := range []string{"result 01", "result 02", "... +2 lines", "result 05", "result 06"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("generic ACP tool output missing %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "result 03") || strings.Contains(joined, "result 04") {
		t.Fatalf("generic ACP tool final output should be summarized, got\n%s", joined)
	}
	if strings.Contains(joined, "╭") || strings.Contains(joined, "│") {
		t.Fatalf("generic ACP tool should use unified detail rows, got\n%s", joined)
	}

	if !block.toggleToolPanelClick("ws-1") {
		t.Fatal("expected generic ACP summary click to expand full output")
	}
	rows = block.Render(ctx)
	plain = renderedPlainRows(rows)
	joined = strings.Join(plain, "\n")
	if strings.Contains(joined, "... +") || !strings.Contains(joined, "result 03") || !strings.Contains(joined, "result 04") {
		t.Fatalf("expanded generic ACP tool output should show hidden lines, got\n%s", joined)
	}
	if !rowsContainClickToken(rows, acpToolPanelClickToken("ws-1")) {
		t.Fatalf("expanded generic ACP tool should expose a collapse click token: %#v", plain)
	}
	if !block.toggleToolPanelClick("ws-1") {
		t.Fatal("expanded generic ACP tool output should collapse on a second click")
	}
	rows = block.Render(ctx)
	plain = renderedPlainRows(rows)
	joined = strings.Join(plain, "\n")
	if !strings.Contains(joined, "... +2 lines") || strings.Contains(joined, "result 03") || strings.Contains(joined, "result 04") {
		t.Fatalf("generic ACP tool output should collapse back to summary, got\n%s", joined)
	}
}

func TestMutationToolPanelClickTogglesDetails(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 100, TermWidth: 100, Theme: model.theme}
	block := NewMainACPTurnBlock("session-1")
	diff := strings.Join([]string{
		"diff / hunk",
		"@@ -1,1 +1,1 @@",
		"-old",
		"+new",
	}, "\n")
	block.UpdateTool("patch-1", "PATCH", "hooks.go", diff, true, false)

	rows := block.Render(ctx)
	joined := strings.Join(renderedPlainRows(rows), "\n")
	if !rowsContainClickToken(rows, acpToolPanelClickToken("patch-1")) {
		t.Fatalf("expanded PATCH panel should expose a collapse click token: %#v", renderedPlainRows(rows))
	}
	if !strings.Contains(joined, "-old") || !strings.Contains(joined, "+new") {
		t.Fatalf("expanded PATCH panel missing diff body:\n%s", joined)
	}

	if !block.toggleToolPanelClick("patch-1") {
		t.Fatal("expected PATCH detail click to collapse")
	}
	rows = block.Render(ctx)
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if strings.Contains(joined, "-old") || strings.Contains(joined, "+new") {
		t.Fatalf("collapsed PATCH panel should hide diff body:\n%s", joined)
	}
	if !rowsContainClickToken(rows, acpToolPanelClickToken("patch-1")) {
		t.Fatalf("collapsed PATCH panel should expose an expand click token: %#v", renderedPlainRows(rows))
	}

	if !block.toggleToolPanelClick("patch-1") {
		t.Fatal("expected PATCH header click to expand")
	}
	rows = block.Render(ctx)
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "-old") || !strings.Contains(joined, "+new") {
		t.Fatalf("re-expanded PATCH panel missing diff body:\n%s", joined)
	}
}

func TestDefaultCollapsedReadClickTokensMatchDetails(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 100, TermWidth: 100, Theme: model.theme}

	t.Run("empty output is not clickable", func(t *testing.T) {
		block := NewParticipantTurnBlock("codex-001", "codex-001")
		block.UpdateToolWithMeta("read-empty", "READ", "empty.txt", "", true, false, ToolUpdateMeta{ToolKind: "read"})

		rows := block.Render(ctx)
		if rowsContainClickToken(rows, acpToolPanelClickToken("read-empty")) {
			t.Fatalf("empty collapsed READ should not expose a click token: %#v", renderedPlainRows(rows))
		}
		if block.toggleToolPanelClick("read-empty") {
			t.Fatal("empty collapsed READ should not expand")
		}
	})

	t.Run("short expanded output can collapse", func(t *testing.T) {
		block := NewParticipantTurnBlock("codex-001", "codex-001")
		block.UpdateToolWithMeta("read-short", "READ", "file.go", "line one\nline two", true, false, ToolUpdateMeta{ToolKind: "read"})

		rows := block.Render(ctx)
		if !rowsContainClickToken(rows, acpToolPanelClickToken("read-short")) {
			t.Fatalf("collapsed READ should expose an expand click token: %#v", renderedPlainRows(rows))
		}
		if !block.toggleToolPanelClick("read-short") {
			t.Fatal("collapsed READ should expand")
		}
		rows = block.Render(ctx)
		if !rowsContainClickToken(rows, acpToolPanelClickToken("read-short")) {
			t.Fatalf("expanded READ should expose a collapse click token: %#v", renderedPlainRows(rows))
		}
		if !block.toggleToolPanelClick("read-short") {
			t.Fatal("expanded READ should collapse")
		}
		if block.toolPanelExpanded("read-short") {
			t.Fatal("READ should be collapsed after second click")
		}
	})
}

func TestShortToolOutputDoesNotCollapseOnClick(t *testing.T) {
	model := newGatewayEventTestModel()
	ctx := BlockRenderContext{Width: 100, TermWidth: 100, Theme: model.theme}
	block := NewParticipantTurnBlock("codex-001", "codex-001")
	block.UpdateToolWithMeta("custom-1", "lookup_weather", `"Shanghai"`, "sunny\n24C", true, false, ToolUpdateMeta{
		ToolKind: "other",
	})

	rows := block.Render(ctx)
	joined := strings.Join(renderedPlainRows(rows), "\n")
	if rowsContainClickToken(rows, acpToolPanelClickToken("custom-1")) {
		t.Fatalf("short custom tool output should not expose a click token: %#v", renderedPlainRows(rows))
	}
	if !strings.Contains(joined, `• lookup_weather "Shanghai"`) ||
		!strings.Contains(joined, "sunny") ||
		!strings.Contains(joined, "24C") {
		t.Fatalf("expanded custom tool should show standard header and output, got\n%s", joined)
	}

	if block.toggleToolPanelClick("custom-1") {
		t.Fatal("short custom tool output should not be clickable")
	}
	joined = strings.Join(renderedPlainRows(block.Render(ctx)), "\n")
	if !strings.Contains(joined, `• lookup_weather "Shanghai"`) {
		t.Fatalf("custom tool should keep header, got\n%s", joined)
	}
	if !strings.Contains(joined, "sunny") || !strings.Contains(joined, "24C") {
		t.Fatalf("short custom tool output should stay visible, got\n%s", joined)
	}

	terminal := NewMainACPTurnBlock("session-1")
	terminal.UpdateTool("command-1", "RUN_COMMAND", "create table", "CREATE TABLE", true, false)
	if terminal.toggleToolPanelClick("command-1") {
		t.Fatal("single-line terminal output should not be clickable")
	}
	rows = terminal.Render(ctx)
	if rowsContainClickToken(rows, acpToolPanelClickToken("command-1")) {
		t.Fatalf("single-line terminal output should not expose a click token: %#v", renderedPlainRows(rows))
	}
	joined = strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "CREATE TABLE") {
		t.Fatalf("single-line terminal output should stay visible, got\n%s", joined)
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
