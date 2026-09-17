package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestCJKInlineCodeDiffSourceKeepsLettersAdjacent(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	raws := []string{
		"用 `diff` 查看变更。",
		"用`diff`查看变更。",
		"- 使用 `diff` 对比文件",
		"- **事实优先**：用 `diff` 验证结果。",
		"**用 `diff` 对比**",
		"*用 `diff` 对比*",
		"这是一个很长的中文句子用来触发换行，其中包含 `diff` 以及后续说明文字。",
	}
	for _, width := range []int{24, 40, 80, 120} {
		for _, raw := range raws {
			t.Run(fmt.Sprintf("%d/%s", width, raw), func(t *testing.T) {
				assertRenderedInlineCodeRun(t, theme, raw, width, "diff")
			})
		}
	}
}

func TestCJKInlineCodeLiteralSpacedDiffSourceIsPreserved(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	raw := "用 `di f f` 查看变更。"
	for _, width := range []int{40, 80} {
		t.Run(fmt.Sprintf("%d", width), func(t *testing.T) {
			assertRenderedInlineCodeRun(t, theme, raw, width, "di f f")
		})
	}
}

func assertRenderedInlineCodeRun(t *testing.T, theme tuikit.Theme, raw string, width int, want string) {
	t.Helper()

	glamour := glamourRenderNarrative(raw, width, theme, tuikit.LineStyleAssistant)
	assertInlineCodeRunText(t, "glamour", raw, width, want, ansi.Strip(glamour), glamour)

	inline := renderInlineSpans(parseInlineMarkdownSpans(raw), theme.TextStyle(), theme)
	assertInlineCodeRunText(t, "inline", raw, width, want, ansi.Strip(inline), inline)

	wrappedStyled, wrappedPlain := renderInlineMarkdownWrappedSegments(raw, theme.TextStyle(), theme, width)
	foundWrap := false
	for i := range wrappedPlain {
		if want == "diff" && (strings.Contains(wrappedPlain[i], "di f f") || strings.Contains(wrappedPlain[i], "d i f f")) {
			t.Fatalf("inline-wrap width=%d raw=%q inserted spaces inside diff\nplain=%q\nstyled=%q", width, raw, wrappedPlain[i], wrappedStyled[i])
		}
		if !strings.Contains(wrappedPlain[i], want) {
			continue
		}
		foundWrap = true
		assertInlineCodeRunText(t, "inline-wrap", raw, width, want, wrappedPlain[i], wrappedStyled[i])
	}
	if strings.Contains(want, " ") && !foundWrap {
		t.Fatalf("inline-wrap width=%d raw=%q missing %q in %#v", width, raw, want, wrappedPlain)
	}

	rows := renderParticipantTurnNarrativeRowsWithBuffer(
		"inline-diff",
		raw,
		nil,
		tuikit.LineStyleAssistant,
		width,
		BlockRenderContext{Width: width, TermWidth: width, Theme: theme, ThemeKey: themeRenderCacheKey(theme)},
		false,
	)
	if len(rows) == 0 {
		t.Fatalf("width=%d raw=%q: expected rendered rows", width, raw)
	}
	foundRow := false
	for _, row := range rows {
		if !strings.Contains(row.Plain, want) {
			continue
		}
		foundRow = true
		assertInlineCodeRunText(t, "rows", raw, width, want, row.Plain, row.Styled)
	}
	if !foundRow {
		t.Fatalf("width=%d raw=%q: rows missing %q: %#v", width, raw, want, renderedPlainRows(rows))
	}
	assertPhysicalInlineCodeRun(t, raw, width, want, rows)
}

func assertInlineCodeRunText(t *testing.T, path, raw string, width int, want, plain, styled string) {
	t.Helper()
	if !strings.Contains(plain, want) {
		t.Fatalf("%s width=%d raw=%q missing %q\nplain=%q\nstyled=%q", path, width, raw, want, plain, styled)
	}
	if want == "diff" && (strings.Contains(plain, "di f f") || strings.Contains(plain, "d i f f")) {
		t.Fatalf("%s width=%d raw=%q inserted spaces inside diff\nplain=%q\nstyled=%q", path, width, raw, plain, styled)
	}
	if !strings.Contains(styled, want) {
		t.Fatalf("%s width=%d raw=%q split %q with ANSI between letters\nplain=%q\nstyled=%q", path, width, raw, want, plain, styled)
	}
}

func assertPhysicalInlineCodeRun(t *testing.T, raw string, width int, want string, rows []RenderedRow) {
	t.Helper()
	height := len(rows)
	styled := make([]string, height)
	for i, row := range rows {
		styled[i] = row.Styled
	}
	frame, _ := normalizeFullscreenFrameWithTopTrim(strings.Join(styled, "\n"), width, height)
	terminal := vt.NewSafeEmulator(width, height)
	t.Cleanup(func() { _ = terminal.Close() })
	if _, err := terminal.Write([]byte(renderFullscreenFramesForTest(t, width, height, frame)[0])); err != nil {
		t.Fatal(err)
	}

	wantRunes := []rune(want)
	screen := ansi.Strip(terminal.Render())
	for y := 0; y < height; y++ {
		for x := 0; x <= width-len(wantRunes); x++ {
			if !physicalRunMatches(terminal, x, y, wantRunes) {
				continue
			}
			for i := range wantRunes {
				cell := terminal.CellAt(x+i, y)
				if cell == nil || cell.Width != 1 {
					got := 0
					if cell != nil {
						got = cell.Width
					}
					t.Fatalf("raw=%q width=%d physical %q cell %d width = %d, want 1; screen=%q", raw, width, want, i, got, screen)
				}
			}
			return
		}
	}
	t.Fatalf("raw=%q width=%d physical screen missing %q cells: %q", raw, width, want, screen)
}

func physicalRunMatches(terminal *vt.SafeEmulator, x, y int, want []rune) bool {
	for i, r := range want {
		cell := terminal.CellAt(x+i, y)
		if cell == nil || cell.Content != string(r) {
			return false
		}
	}
	return true
}
