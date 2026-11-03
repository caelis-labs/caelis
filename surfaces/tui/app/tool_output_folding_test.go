package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestMCPOutputFoldsVisualRowsAndDisclosesOriginal(t *testing.T) {
	for _, width := range []int{24, 80} {
		block := NewMainACPTurnBlock("mcp-output")
		output := `{"value":"` + strings.Repeat("桌面状态", 120) + `MIDDLE_SENTINEL` + strings.Repeat("field", 120) + `"}`
		block.UpdateToolWithMeta("mcp-1", "service__lookup", "query", output, true, false, ToolUpdateMeta{})
		ctx := BlockRenderContext{Width: width, TermWidth: width, Theme: tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)}
		rows := block.Render(ctx)
		text := strings.Join(explorationRenderedPlainRows(rows), "\n")
		if len(rows) > acpTerminalPanelMaxLines+2 || !strings.Contains(text, "... +") || strings.Contains(text, "MIDDLE_SENTINEL") {
			t.Fatalf("width %d leaked wrapped output (%d rows): %s", width, len(rows), text)
		}
		clickable := false
		for _, row := range rows {
			if row.ClickToken == acpToolPanelClickToken("mcp-1") {
				clickable = true
			}
		}
		if !clickable || !block.toggleToolPanelClick("mcp-1") {
			t.Fatal("wrapped preview has no working disclosure")
		}
		var pieces []string
		for _, row := range block.Render(ctx) {
			pieces = append(pieces, strings.TrimSpace(row.Plain))
		}
		full := strings.Join(pieces, "")
		if !strings.Contains(full, "MIDDLE_SENTINEL") || block.Events[0].Output != output {
			t.Fatal("expansion lost original output or changed transcript data")
		}
		if !block.toggleToolPanelClick("mcp-1") || block.toolPanelFullOutput("mcp-1") {
			t.Fatal("full output did not return to the wrapped preview")
		}
	}
}

func TestMCPArgumentDisclosureRetainsAppAndScript(t *testing.T) {
	raw := map[string]any{"app": "fixture.app", "source": "await app.look();\nreturn 'script tail';"}
	args, full := toolDisplayArguments("desktop_world__exec", "other", raw)
	ev := SubagentEvent{Kind: SEToolCall, CallID: "exec-1", Name: "desktop_world__exec", Args: args, FullArgs: full, Output: "completed", Done: true}
	ctx := BlockRenderContext{Width: 40, Theme: tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)}
	text := strings.Join(explorationRenderedPlainRows(renderACPStandardToolLifecycleRows("block", ev, ev.CallID, ev.Output, 40, ctx, false, true, true)), "")
	if !strings.Contains(text, "fixture.app") || !strings.Contains(text, "script tail") || !strings.Contains(text, "desktop_world__exec") {
		t.Fatalf("full invocation parameters missing: %s", text)
	}
	rows := renderACPStandardToolLifecycleRows("block", ev, ev.CallID, ev.Output, 40, ctx, false, true, true)
	if len(rows) == 0 || rows[0].ClickToken != acpToolPanelClickToken(ev.CallID) {
		t.Fatal("expanded arguments lost the collapse affordance")
	}
}
