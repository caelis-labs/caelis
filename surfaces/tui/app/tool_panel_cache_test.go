package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestACPToolPanelRenderCacheReusesUnchangedTerminalBody(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, acpToolPanelUpdate("call-1", "go test", strings.Join(numberedToolLines(24), "\n"), schema.ToolStatusCompleted))
	block := requireMainACPTurnBlockForTest(t, model)
	ctx := BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme)}

	rows := block.Render(ctx)
	if !renderedRowsContainPlain(rows, "line 024") {
		t.Fatalf("initial rows missing terminal tail: %#v", renderedPlainRows(rows))
	}
	cache := block.toolPanelRenderCache["call-1"]
	if cache.bodyRenders != 1 {
		t.Fatalf("initial body renders = %d, want 1", cache.bodyRenders)
	}

	rows = block.Render(ctx)
	if !renderedRowsContainPlain(rows, "line 024") {
		t.Fatalf("cached rows missing terminal tail: %#v", renderedPlainRows(rows))
	}
	cache = block.toolPanelRenderCache["call-1"]
	if cache.bodyRenders != 1 {
		t.Fatalf("unchanged body renders = %d, want cache reuse at 1", cache.bodyRenders)
	}

	model = applyACPEnvelopeForTest(t, model, acpToolPanelUpdate("call-1", "go test", strings.Join(numberedToolLines(25), "\n"), schema.ToolStatusCompleted))
	block = requireMainACPTurnBlockForTest(t, model)
	rows = block.Render(ctx)
	if !renderedRowsContainPlain(rows, "line 025") {
		t.Fatalf("updated rows missing terminal tail: %#v", renderedPlainRows(rows))
	}
	cache = block.toolPanelRenderCache["call-1"]
	if cache.bodyRenders != 2 {
		t.Fatalf("changed body renders = %d, want cache miss at 2", cache.bodyRenders)
	}
}

func TestTerminalToolPanelCacheKeyUsesBoundedTail(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	ctx := BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme)}
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
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	ctx := BlockRenderContext{
		Width:     maxGenericToolPanelCacheBytes,
		TermWidth: maxGenericToolPanelCacheBytes,
		Theme:     model.theme,
		ThemeKey:  themeRenderCacheKey(model.theme),
	}
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

func acpToolPanelUpdate(callID string, command string, output string, status string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Final:     status != schema.ToolStatusInProgress,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    callID,
			Title:         stringPtr(command),
			Kind:          stringPtr(schema.ToolKindExecute),
			Status:        stringPtr(status),
			RawInput:      map[string]any{"command": command},
			Content: []schema.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "terminal-1",
				Content:    schema.TextContent{Type: "text", Text: output},
			}},
			Meta: acpToolNameMeta("RUN_COMMAND"),
		},
	}
}

func numberedToolLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %03d", i+1)
	}
	return lines
}

func renderedPlainRows(rows []RenderedRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Plain)
	}
	return out
}

func renderedRowsContainPlain(rows []RenderedRow, needle string) bool {
	for _, row := range rows {
		if strings.Contains(row.Plain, needle) {
			return true
		}
	}
	return false
}
