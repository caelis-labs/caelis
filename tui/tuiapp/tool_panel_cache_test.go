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

	block.UpdateToolWithMeta("call-1", "BASH", "go test", strings.Join(numberedToolLines(24), "\n"), false, false, ToolUpdateMeta{TaskID: "task-1"})
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
	block.UpdateToolWithMeta("call-1", "BASH", "go test", longOutput, false, false, ToolUpdateMeta{TaskID: "task-1"})
	_ = block.Render(ctx)
	cache := block.toolPanelRenderCache["call-1"]
	if cache.lastInputBytes >= len(longOutput) {
		t.Fatalf("terminal panel cache consumed %d bytes, want bounded tail below full %d", cache.lastInputBytes, len(longOutput))
	}
	if cache.lastInputBytes == 0 {
		t.Fatal("terminal panel cache did not record bounded input bytes")
	}
}

func TestParticipantToolPanelRenderCachePreservesHeaderToken(t *testing.T) {
	m := newPerfTestModel()
	ctx := BlockRenderContext{Width: 96, TermWidth: 96, Theme: m.theme}
	block := NewParticipantTurnBlock("session-1", "worker")

	block.UpdateToolWithMeta("call-1", "BASH", "go test", "first\n", false, false, ToolUpdateMeta{TaskID: "task-1"})
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

func rowsContainClickToken(rows []RenderedRow, token string) bool {
	for _, row := range rows {
		if row.ClickToken == token {
			return true
		}
	}
	return false
}

func numberedToolLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %03d", i+1)
	}
	return lines
}
