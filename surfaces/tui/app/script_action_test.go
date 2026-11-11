package tuiapp

import (
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"strings"
	"testing"
)

func TestScriptActionTitlesAndNoDetails(t *testing.T) {
	for _, done := range []bool{false, true} {
		m, b := groupedCanonicalModel(t, unifiedScriptEvents("completed"), "main", true)
		root := b.(*MainACPTurnBlock)
		root.Events[0].Done = done
		rows := root.Render(m.blockRenderContext(60))
		want := "• Running script"
		if done {
			want = "• Ran script"
		}
		if rows[0].Plain != want {
			t.Fatalf("title %q want %q", rows[0].Plain, want)
		}
		for _, row := range rows {
			if strings.Contains(row.Plain, "details") || strings.HasPrefix(row.ClickToken, "tool_details:") || strings.HasPrefix(row.ClickTokenAlt, "tool_details:") {
				t.Fatal("retired details entry survived")
			}
		}
		if m.tryToggleFoldToken(b.BlockID(), "tool_details:script1") {
			t.Fatal("hidden details route survived")
		}
	}
}

func TestScriptActionNameContrastAndWholeTitle(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	m := NewModel(Config{})
	ctx := m.blockRenderContext(60)
	ctx.Theme = tuikit.ResolveThemeFromOptions(false, colorprofile.TrueColor)
	for _, title := range []string{"Running script", "Ran script"} {
		row := renderACPTranscriptHeaderRowMarked("b", "• "+title, 60, ctx, "", acpHeaderMarkDefault, false)
		_, styled, _ := wrapACPTranscriptHeaderForViewport(row.Plain, 60, ctx, acpHeaderMarkDefault, false)
		if !strings.Contains(styled[0], ctx.Theme.Tokens().ToolName.Render(title)) {
			t.Fatalf("whole phrase not highlighted: %q", styled)
		}
	}
	for _, name := range []string{"Read", "desktop_world__exec", "desktop_world.exec"} {
		text := "  └ " + name + " target"
		row := StyledPlainRow("b", text, styleExplorationSummaryRow(text, ctx))
		ev := SubagentEvent{Name: name, Done: true}
		styled := styleScriptToolState(row, ev, ctx)
		if !strings.Contains(styled.Styled, ctx.Theme.Tokens().Accent.Bold(true).Render(name)) {
			t.Fatalf("tool name lacks accent: %q", styled.Styled)
		}
		ev.Err = true
		if got := styleScriptToolState(row, ev, ctx); got.Styled != ctx.Theme.ErrorStyle().Render(text) {
			t.Fatal("failure color did not take priority")
		}
	}
}
