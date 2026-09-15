package tuiapp

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
)

func TestTerminalLiveTailSettlesWithUnchangedCachedOutput(t *testing.T) {
	t.Setenv("CAELIS_THEME", "auto")
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 100, TermWidth: 100, Theme: m.theme, AnimationsEnabled: true}
	b := NewMainACPTurnBlock("session")
	// One wrapped source line overflows the panel, but produces the same
	// preview text on completion. Only lifecycle can invalidate its colors.
	output := strings.Repeat("working ", 90)
	b.UpdateToolWithMeta("call", "RunCommand", "make check", output, false, false, ToolUpdateMeta{})
	running := b.Render(ctx)
	cache := b.toolPanelRenderCache["call"]
	primary := sgrForegroundCode(t, m.theme.TextStyle().GetForeground())
	if !strings.Contains(textWithSGRForeground(running[len(running)-1].Styled, primary), "working") {
		t.Fatal("running tail did not emphasize newest output")
	}
	b.UpdateToolWithMeta("call", "RunCommand", "make check", output, true, false, ToolUpdateMeta{})
	completed := b.Render(ctx)
	if b.toolPanelRenderCache["call"].bodyRenders <= cache.bodyRenders {
		t.Fatal("completion reused the live-colored cache")
	}
	if strings.Contains(textWithSGRForeground(completed[len(completed)-1].Styled, primary), "working") {
		t.Fatal("completed output retained live emphasis")
	}
	if strings.Join(renderedPlainRows(running), "\n") != strings.Join(renderedPlainRows(completed), "\n") {
		t.Fatal("settling colors changed output text or layout")
	}
}

func TestTerminalLiveTailEligibility(t *testing.T) {
	ctx := BlockRenderContext{Width: 100, AnimationsEnabled: true}
	text := strings.Join(numberedToolLines(8), "\n")
	for _, tc := range []struct {
		name                                             string
		final, full, failed, noMotion, scrollBack, short bool
	}{
		{name: "completed", final: true}, {name: "expanded", full: true},
		{name: "failed", failed: true}, {name: "no-motion", noMotion: true},
		{name: "scroll-back", scrollBack: true}, {name: "short", short: true},
		{name: "following"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := ctx
			local.AnimationsEnabled = !tc.noMotion
			output := text
			if tc.short {
				output = "one\ntwo"
			}
			opts := acpTranscriptRenderOptions{ToolPanelScrollState: func(string) toolPanelScrollState {
				return toolPanelScrollState{FollowTail: !tc.scrollBack}
			}}
			got := toolPanelLiveTail("call", output, 100, local, tc.final, tc.full, tc.failed, opts)
			if got != (tc.name == "following") {
				t.Fatalf("live tail = %v", got)
			}
		})
	}
}

func TestToolFoldCountIsQuieterWithoutChangingShellSyntax(t *testing.T) {
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 120, Theme: m.theme}
	command := "cat file.go | head -5"
	suffix := " ... +240 lines"
	got := styleACPTranscriptHeaderDetail(ctx, "Ran", command+suffix)
	want := styleShellCommandText(ctx, command) + m.theme.ToolOutputMetaStyle().Render(suffix)
	if got != want {
		t.Fatal("fold marker changed command syntax styling")
	}
	rows := renderACPTerminalPanelBody("first\n... +240 lines\nlast", 120, ctx, false, false)
	meta := sgrForegroundCode(t, m.theme.ToolOutputMetaStyle().GetForeground())
	if !strings.Contains(textWithSGRForeground(rows[1], meta), "... +240 lines") {
		t.Fatal("fold marker retained output-body styling")
	}
}
