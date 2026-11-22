package tuiapp

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func scriptPreviewEvents(code, output, completion string) []*session.Event {
	return []*session.Event{
		{Type: session.EventTypeToolCall, Tool: &session.EventTool{ID: "preview", Name: "js_exec", Status: "started", Input: map[string]any{"code": code}}},
		{Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "preview", Name: "js_exec", Status: completion, Content: []session.EventToolContent{{Type: "content", Text: output}}}, Meta: map[string]any{"code_mode_script_completion": completion}},
	}
}

func TestScriptOutputPreviewUsesWrappedHeadAndTail(t *testing.T) {
	outputs := []string{
		`{"data":"` + strings.Repeat("桌面状态", 35) + "MIDDLE_SENTINEL" + strings.Repeat("abcdefgh", 35) + `"}`,
		"first\nsecond\nthird\nfourth\nMIDDLE_SENTINEL\nsixth\nseventh\neighth\nninth",
		"short result",
	}
	for _, scope := range []string{"main", "child"} {
		for _, replay := range []bool{false, true} {
			for _, completion := range []string{"completed", "failed"} {
				for _, width := range []int{24, 36, 80, 120} {
					for n, output := range outputs {
						t.Run(fmt.Sprintf("%s/replay=%v/%s/%d/%d", scope, replay, completion, width, n), func(t *testing.T) {
							m, b := groupedCanonicalModel(t, scriptPreviewEvents("return 'result';", output, completion), scope, replay)
							ctx := m.blockRenderContext(width)
							rows := b.Render(ctx)
							wrapped := strings.Split(ansi.Hardwrap(output, width-4, true), "\n")
							want := slices.Clone(wrapped)
							if len(wrapped) > 5 {
								want = []string{wrapped[0], wrapped[1], fmt.Sprintf("... +%d lines", len(wrapped)-4), wrapped[len(wrapped)-2], wrapped[len(wrapped)-1]}
							}
							if len(rows) != len(want)+1 {
								t.Fatalf("got %d rows, want header + %d visual rows:\n%s", len(rows), len(want), joinRenderedPlain(rows))
							}
							for i, row := range rows[1:] {
								prefix := "    "
								if i == 0 {
									prefix = "  └ "
								}
								if strings.TrimRight(row.Plain, " ") != prefix+want[i] || ansi.Strip(row.Styled) != row.Plain || ansi.StringWidth(row.Styled) > width || row.ClickToken != acpToolPanelClickToken("preview") {
									t.Fatalf("output row %d = %#v, want %q", i, row, prefix+want[i])
								}
							}
							if (rows[0].acpHeaderMarkTone == acpHeaderMarkDanger) != (completion == "failed") {
								t.Fatal("preview changed execution mark")
							}
							clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("preview"))
							full := b.Render(ctx)
							var parts []string
							for _, row := range full[1:] {
								parts = append(parts, strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(row.Plain, "  └ "), "    "), " "))
							}
							if strings.Join(parts, "") != strings.Join(wrapped, "") || scriptBlockEvents(b)[0].Output != output {
								t.Fatal("disclosure changed or lost original result")
							}
							clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("preview"))
							if got := joinRenderedPlain(b.Render(ctx)); got != joinRenderedPlain(rows) {
								t.Fatalf("collapse did not restore preview: %s", got)
							}
						})
					}
				}
			}
		}
	}
}

func TestScriptOutputPreviewPhysicalMouse(t *testing.T) {
	for _, width := range []int{24, 80} {
		m, b := groupedCanonicalModel(t, scriptPreviewEvents("return 42;", "head1\nhead2\nmiddle1\nmiddle2\nmiddle3\ntail1\ntail2", "completed"), "main", false)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 35})
		m.syncViewportContent()
		frames := []string{m.View().Content}
		for _, full := range []bool{true, false} {
			found := false
			for line, token := range m.viewportClickTokens {
				if token == acpToolPanelClickToken("preview") {
					clickViewportColumn(t, m, line, 5)
					found = true
					break
				}
			}
			if !found || b.(*MainACPTurnBlock).toolPanelFullOutput("preview") != full {
				t.Fatal("physical output click did not toggle disclosure")
			}
			frames = append(frames, m.View().Content)
		}
		updates := renderFullscreenFramesForTest(t, width, 35, frames...)
		for i, frame := range frames {
			assertPhysicalFullscreenFrame(t, width, 35, frame, updates[:i+1])
		}
	}
}

func TestScriptHeaderJavaScriptSyntaxAndWidth(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	code := "const label = `桌面 value`; /* comment\nCOMMENT_CONTINUATION */\nreturn {label, count: 42};"
	for _, dark := range []bool{false, true} {
		for _, action := range []string{scriptToolLabel} {
			m := NewModel(Config{})
			ctx := m.blockRenderContext(80)
			ctx.Theme = tuikit.ResolveThemeWithState(dark, false, colorprofile.TrueColor)
			palette := tuikit.SyntaxPaletteForTheme(ctx.Theme)
			plain := "• " + action + " " + code
			styled := styleACPTranscriptHeaderWithMark(ctx, plain, acpHeaderMarkDefault, false)
			if ansi.Strip(styled) != plain || !strings.Contains(styled, shellSyntaxStyle(palette.Keyword).Render("return")) || !strings.Contains(styled, shellSyntaxStyle(palette.Number).Render("42")) {
				t.Fatalf("script header lacks JavaScript syntax: %q", styled)
			}
			for _, width := range []int{24, 36, 80} {
				lines, styledLines, ok := wrapACPTranscriptHeaderForViewport(plain, width, ctx, acpHeaderMarkDefault, false)
				if !ok || len(lines) != len(styledLines) {
					t.Fatal("expanded script lost viewport layout")
				}
				for i, line := range styledLines {
					if ansi.Strip(line) != lines[i] || ansi.StringWidth(line) > width {
						t.Fatalf("ANSI width mismatch at %d: %q != %q", width, line, lines[i])
					}
				}
				if width == 80 && !strings.Contains(strings.Join(styledLines, "\n"), shellSyntaxStyle(palette.Comment).Render("COMMENT_CONTINUATION */")) {
					t.Fatalf("wrapping lost multiline JavaScript token context: %q", styledLines)
				}
			}
		}
	}
}

func TestScriptHeaderHighlightSurvivesDisclosure(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	code := "const label = `桌面 value`; /* comment\nCOMMENT_CONTINUATION */\nreturn {label, count: 42, tail: 'last value'};"
	for _, noColor := range []bool{false, true} {
		for _, width := range []int{8, 13, 14, 16, 17, 18, 19, 24, 36, 80, 160} {
			m, b := groupedCanonicalModel(t, scriptPreviewEvents(code, "42", "completed"), "main", false)
			ctx := m.blockRenderContext(width)
			ctx.Theme = tuikit.ResolveThemeWithState(true, noColor, colorprofile.TrueColor)
			compact := b.Render(ctx)
			if strings.Contains(compact[0].Plain, "\n") || ansi.StringWidth(compact[0].Styled) > width {
				t.Fatalf("compact header exceeds %d columns: %q", width, compact[0].Styled)
			}
			if strings.TrimSpace(ansi.Strip(compact[0].Styled)) != compact[0].Plain {
				t.Fatalf("styled preview changed original command folding: %q != %q", compact[0].Styled, compact[0].Plain)
			}
			if !noColor && width == 160 && !strings.Contains(compact[0].Styled, shellSyntaxStyle(tuikit.SyntaxPaletteForTheme(ctx.Theme).Number).Render("42")) {
				t.Fatal("truncated comment changed the retained JavaScript token colors")
			}
			clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("preview"))
			wrapped := m.wrapRenderedRowsForViewport(b, b.Render(ctx), width, ctx)
			for i, styled := range wrapped.styledLines {
				if ansi.StringWidth(styled) > width || strings.TrimSpace(ansi.Strip(styled)) != strings.TrimSpace(wrapped.plainLines[i]) {
					t.Fatalf("expanded header at %d columns: styled width %d, text %q != %q", width, ansi.StringWidth(styled), ansi.Strip(styled), wrapped.plainLines[i])
				}
				if noColor && strings.Contains(styled, "38;") {
					t.Fatal("NoColor emitted syntax colors")
				}
			}
			clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("preview"))
			if got := joinRenderedPlain(b.Render(ctx)); got != joinRenderedPlain(compact) {
				t.Fatal("syntax styling changed argument disclosure")
			}
		}
	}
}
