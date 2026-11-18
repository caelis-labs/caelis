package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
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
		if !strings.HasPrefix(rows[0].Plain, want) {
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
	for _, tc := range []struct{ name, action string }{
		{"Read", "Read"}, {"RunCommand", "Ran"}, {"desktop_world__exec", "desktop_world__exec"},
	} {
		ev := SubagentEvent{Name: tc.name, Args: "target", Done: true}
		row := renderScriptInvocationRows("b", []SubagentEvent{ev}, 60, ctx, "", true, false)[0]
		if !strings.Contains(row.Styled, toolActionStyle(ctx, tc.action).Render(tc.action)) {
			t.Fatalf("tool action lacks the standard header style: %q", row.Styled)
		}
		ev.Err = true
		failed := renderScriptInvocationRows("b", []SubagentEvent{ev}, 60, ctx, "", true, false)[0]
		if failed.Styled != ctx.Theme.ErrorStyle().Render(row.Plain) {
			t.Fatal("failure color did not take priority")
		}
	}
}
