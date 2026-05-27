package tuiapp

import (
	"fmt"
	"strings"
	"testing"
)

func TestToolPanelRenderCacheReusesUnchangedTerminalBody(t *testing.T) {
	m := newPerfTestModel()
	ctx := BlockRenderContext{Width: 96, TermWidth: 96, Theme: m.theme}
	block := NewMainACPTurnBlock("session-1")

	block.UpdateToolWithMeta("call-1", "RUN_COMMAND", "go test", strings.Join(numberedToolLines(24), "\n"), false, false, ToolUpdateMeta{TaskID: "task-1"})
	_ = block.Render(ctx)
	cache := block.toolPanelRenderCache["call-1"]
	if cache.bodyRenders != 1 {
		t.Fatalf("initial body renders = %d, want 1", cache.bodyRenders)
	}

	_ = block.Render(ctx)
	cache = block.toolPanelRenderCache["call-1"]
	if cache.bodyRenders != 1 {
		t.Fatalf("unchanged body renders = %d, want cache reuse at 1", cache.bodyRenders)
	}
}

func TestTerminalToolPanelCacheKeyUsesBoundedTail(t *testing.T) {
	m := newPerfTestModel()
	ctx := BlockRenderContext{Width: 96, TermWidth: 96, Theme: m.theme}
	block := NewMainACPTurnBlock("session-1")

	longOutput := strings.Join(numberedToolLines(500), "\n")
	block.UpdateToolWithMeta("call-1", "RUN_COMMAND", "go test", longOutput, false, false, ToolUpdateMeta{TaskID: "task-1"})
	_ = block.Render(ctx)
	cache := block.toolPanelRenderCache["call-1"]
	if cache.lastInputBytes >= len(longOutput) {
		t.Fatalf("terminal panel cache consumed %d bytes, want bounded tail below full %d", cache.lastInputBytes, len(longOutput))
	}
	if cache.lastInputBytes == 0 {
		t.Fatal("terminal panel cache did not record bounded input bytes")
	}
}

func TestGenericToolPanelCacheKeyUsesBoundedText(t *testing.T) {
	m := newPerfTestModel()
	ctx := BlockRenderContext{Width: maxGenericToolPanelCacheBytes, TermWidth: maxGenericToolPanelCacheBytes, Theme: m.theme}
	cacheByCall := map[string]toolOutputRenderCache{}

	longOutput := strings.Repeat("x", maxGenericToolPanelCacheBytes*2)
	_ = renderCachedToolPanelRows(&cacheByCall, toolPanelRenderRequest{
		BlockID:  "block-1",
		CallID:   "call-1",
		ToolName: "READ",
		Text:     longOutput,
		Width:    ctx.Width,
		Ctx:      ctx,
	}, defaultToolPanelScrollState())
	cache := cacheByCall["call-1"]
	if cache.lastInputBytes >= len(longOutput) {
		t.Fatalf("generic panel cache consumed %d bytes, want bounded input below full %d", cache.lastInputBytes, len(longOutput))
	}
	if cache.lastInputBytes == 0 {
		t.Fatal("generic panel cache did not record bounded input bytes")
	}
}

func TestParticipantToolPanelRenderCachePreservesHeaderToken(t *testing.T) {
	m := newPerfTestModel()
	ctx := BlockRenderContext{Width: 96, TermWidth: 96, Theme: m.theme}
	block := NewParticipantTurnBlock("session-1", "worker")

	block.UpdateToolWithMeta("call-1", "RUN_COMMAND", "go test", strings.Join(numberedToolLines(6), "\n"), true, false, ToolUpdateMeta{TaskID: "task-1"})
	rows := block.Render(ctx)
	if !rowsContainClickToken(rows, acpToolPanelClickToken("call-1")) {
		t.Fatalf("initial rows missing panel click token: %#v", renderedPlainRows(rows))
	}
	if cache := block.toolPanelRenderCache["call-1"]; cache.bodyRenders != 1 {
		t.Fatalf("initial participant body renders = %d, want 1", cache.bodyRenders)
	}

	rows = block.Render(ctx)
	if !rowsContainClickToken(rows, acpToolPanelClickToken("call-1")) {
		t.Fatalf("cached rows missing panel click token: %#v", renderedPlainRows(rows))
	}
	if cache := block.toolPanelRenderCache["call-1"]; cache.bodyRenders != 1 {
		t.Fatalf("cached participant body renders = %d, want 1", cache.bodyRenders)
	}
}

func TestDiffToolPanelRowsAreNumberedAndPreWrapped(t *testing.T) {
	m := newPerfTestModel()
	ctx := BlockRenderContext{Width: 72, TermWidth: 72, Theme: m.theme}
	block := NewMainACPTurnBlock("session-1")
	output := strings.Join([]string{
		"demo.go +1 -1",
		"diff / hunk",
		"@@ -10,2 +10,2 @@",
		" context",
		"-old line",
		"+new line",
	}, "\n")

	block.UpdateTool("patch-1", "PATCH", "demo.go +1 -1", output, true, false)
	block.setToolPanelExpanded("patch-1", true)
	rows := block.Render(ctx)

	removeRow, ok := renderedRowContaining(rows, "-old line")
	if !ok {
		t.Fatalf("rendered rows missing removed line: %#v", renderedPlainRows(rows))
	}
	if !removeRow.PreWrapped {
		t.Fatalf("removed diff row was not prewrapped: %#v", removeRow)
	}
	if !strings.Contains(removeRow.Plain, "11") {
		t.Fatalf("removed diff row missing old line number: %q", removeRow.Plain)
	}

	addRow, ok := renderedRowContaining(rows, "+new line")
	if !ok {
		t.Fatalf("rendered rows missing added line: %#v", renderedPlainRows(rows))
	}
	if !addRow.PreWrapped {
		t.Fatalf("added diff row was not prewrapped: %#v", addRow)
	}
	if !strings.Contains(addRow.Plain, "11") {
		t.Fatalf("added diff row missing new line number: %q", addRow.Plain)
	}
	if addRow.Styled == addRow.Plain || !strings.Contains(addRow.Styled, "\x1b[") {
		t.Fatalf("added diff row is not styled: plain=%q styled=%q", addRow.Plain, addRow.Styled)
	}
}

func rowsContainClickToken(rows []RenderedRow, token string) bool {
	for _, row := range rows {
		if row.ClickToken == token {
			return true
		}
	}
	return false
}

func renderedRowContaining(rows []RenderedRow, needle string) (RenderedRow, bool) {
	for _, row := range rows {
		if strings.Contains(row.Plain, needle) {
			return row, true
		}
	}
	return RenderedRow{}, false
}

func numberedToolLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %03d", i+1)
	}
	return lines
}
