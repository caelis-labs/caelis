package tuiapp

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestNormalizeTerminalMarkdownSplitsGluedMarkdownTable(t *testing.T) {
	raw := "---## 工具调用演示总结我刚刚同时使用了 7 种工具 来展示能力：| 工具 | 用途 | 演示内容 | |------|------|----------| | Bash | 执行 shell 命令 | ls 列出文件 | | Glob | 文件名模式匹配 | 搜索 *.py 文件 |"

	got := normalizeTerminalMarkdown(raw)

	for _, want := range []string{
		"---\n## 工具调用演示总结",
		"能力：\n| 工具 | 用途 | 演示内容 |",
		"| 工具 | 用途 | 演示内容 |\n|------|------|----------|",
		"| Bash | 执行 shell 命令 | ls 列出文件 |",
		"| Glob | 文件名模式匹配 | 搜索 *.py 文件 |",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("normalized markdown missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestGlamourNarrativeRendererCacheUsesFullThemeKey(t *testing.T) {
	raw := "## Heading\n\nUse `code` and [link](https://example.com)."
	dark := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	light := tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor)

	darkRendered := glamourRenderNarrative(raw, 96, dark, tuikit.LineStyleAssistant)
	lightRendered := glamourRenderNarrative(raw, 96, light, tuikit.LineStyleAssistant)

	if darkRendered == "" || lightRendered == "" {
		t.Fatal("expected both themed glamour renders to produce output")
	}
	if darkRendered == lightRendered {
		t.Fatalf("expected light and dark renders to use different ANSI styling; got identical output %q", darkRendered)
	}
	if ansi.Strip(darkRendered) != ansi.Strip(lightRendered) {
		t.Fatalf("theme should not change rendered markdown text\n dark=%q\nlight=%q", ansi.Strip(darkRendered), ansi.Strip(lightRendered))
	}
}

func TestGlamourNarrativeRendererCacheKeepsRecentKeys(t *testing.T) {
	clearGlamourCache()
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)

	first := getGlamourRenderer(80, theme, tuikit.LineStyleAssistant)
	second := getGlamourRenderer(96, theme, tuikit.LineStyleReasoning)
	again := getGlamourRenderer(80, theme, tuikit.LineStyleAssistant)

	if first == nil || second == nil || again == nil {
		t.Fatal("expected cached glamour renderers")
	}
	if first != again {
		t.Fatal("expected LRU cache to retain the first renderer across another key")
	}
	if first == second {
		t.Fatal("expected different width/role keys to use distinct renderers")
	}
}

func TestGlamourNarrativeRendersNormalizedMarkdownTable(t *testing.T) {
	raw := "工具调用演示总结：| 工具 | 用途 | 演示内容 | |------|------|----------| | Bash | 执行 shell 命令 | ls 列出文件 | | Glob | 文件名模式匹配 | 搜索 *.py 文件 |"

	rendered := glamourRenderNarrative(raw, 96, tuikit.DefaultTheme(), tuikit.LineStyleAssistant)
	plain := ansi.Strip(rendered)

	for _, want := range []string{"工具", "用途", "演示内容", "Bash", "Glob"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered table missing %q\n%s", want, plain)
		}
	}
	if !strings.Contains(plain, "│") {
		t.Fatalf("rendered table should use table separators, got\n%s", plain)
	}
}

func TestNarrativeChromaClearsDefaultErrorBackground(t *testing.T) {
	dark := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	light := tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor)
	tests := []struct {
		name  string
		theme tuikit.Theme
		role  tuikit.LineStyle
	}{
		{name: "dark assistant", theme: dark, role: tuikit.LineStyleAssistant},
		{name: "light assistant", theme: light, role: tuikit.LineStyleAssistant},
		{name: "dark reasoning", theme: dark, role: tuikit.LineStyleReasoning},
		{name: "light reasoning", theme: light, role: tuikit.LineStyleReasoning},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			style := narrativeStyleConfig(tt.theme, tt.role)
			if style.CodeBlock.Chroma == nil {
				t.Fatal("expected narrative style to configure Chroma")
			}
			codeBg := ptrString(styleBackgroundToAnsiPtr(tt.theme.MarkdownCodeBlockStyle()))
			for token, got := range map[string]string{
				"error":            ptrString(style.CodeBlock.Chroma.Error.BackgroundColor),
				"generic_deleted":  ptrString(style.CodeBlock.Chroma.GenericDeleted.BackgroundColor),
				"generic_inserted": ptrString(style.CodeBlock.Chroma.GenericInserted.BackgroundColor),
			} {
				if isDefaultChromaRedBackground(got) {
					t.Fatalf("%s inherited default red background %q", token, got)
				}
				if got != codeBg {
					t.Fatalf("%s background = %q, want code block background %q", token, got, codeBg)
				}
			}
		})
	}
}

func TestStreamingNarrativeTailHidesFenceDelimiterAndAvoidsRedBackground(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	tests := []struct {
		name string
		raw  string
	}{
		{name: "short", raw: "```go\nfmt.Println(\"short\")\n"},
		{name: "long", raw: "```go\n" + strings.Repeat("fmt.Println(\"stream\")\n", 14)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := glamourStreamingNarrativeRows("block-1", tt.raw, "* ", tuikit.LineStyleAssistant, 80, theme)
			if len(rows) == 0 {
				t.Fatal("expected streaming rows")
			}
			plain := joinRenderedPlain(rows)
			if strings.Contains(plain, "```") {
				t.Fatalf("streaming code fence delimiter should not be displayed in tail rows:\n%s", plain)
			}
			styled := joinRenderedStyled(rows)
			if containsForbiddenRedBackground(styled) {
				t.Fatalf("streaming rows contain forbidden red background ANSI:\n%q", styled)
			}
		})
	}
}

func TestActiveStreamingTailStyleDoesNotJumpAcrossLegacyLengthThreshold(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	prefix := "```go\nfmt.Println(\"stable\")\n"
	shortRows := renderActiveNarrativeTailRows("block-1", prefix+strings.Repeat("// x\n", 4), "* ", tuikit.LineStyleAssistant, 80, theme)
	longRows := renderActiveNarrativeTailRows("block-1", prefix+strings.Repeat("// x\n", 40), "* ", tuikit.LineStyleAssistant, 80, theme)

	shortStyled := firstStyledRowContaining(shortRows, "fmt.Println")
	longStyled := firstStyledRowContaining(longRows, "fmt.Println")
	if shortStyled == "" || longStyled == "" {
		t.Fatalf("missing shared code row\nshort=%q\nlong=%q", joinRenderedPlain(shortRows), joinRenderedPlain(longRows))
	}
	if shortStyled != longStyled {
		t.Fatalf("shared code row style changed across stream length threshold\nshort=%q\n long=%q", shortStyled, longStyled)
	}
}

func TestMainACPTurnActiveMarkdownStreamUsesTailRenderer(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	block := NewMainACPTurnBlock("session-1")
	block.Events = append(block.Events, SubagentEvent{
		Kind: SEAssistant,
		Text: "```go\nfmt.Println(\"gateway\")\n",
	})
	ctx := BlockRenderContext{
		Width:    80,
		Theme:    theme,
		ThemeKey: themeRenderCacheKey(theme),
	}

	rows := block.Render(ctx)
	plain := joinRenderedPlain(rows)
	if strings.Contains(plain, "```") {
		t.Fatalf("Main ACP active stream should hide code fence delimiter:\n%s", plain)
	}
	styled := joinRenderedStyled(rows)
	if containsForbiddenRedBackground(styled) {
		t.Fatalf("Main ACP active stream contains forbidden red background ANSI:\n%q", styled)
	}
}

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func isDefaultChromaRedBackground(value string) bool {
	switch strings.ToLower(value) {
	case "#f05b5b", "#ff5555":
		return true
	default:
		return false
	}
}

func containsForbiddenRedBackground(styled string) bool {
	for _, token := range []string{
		"\x1b[41m",
		"\x1b[48;2;240;91;91m",
		"\x1b[48;2;255;85;85m",
		"48;2;240;91;91",
		"48;2;255;85;85",
	} {
		if strings.Contains(styled, token) {
			return true
		}
	}
	return false
}

func joinRenderedPlain(rows []RenderedRow) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, row.Plain)
	}
	return strings.Join(parts, "\n")
}

func joinRenderedStyled(rows []RenderedRow) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, row.Styled)
	}
	return strings.Join(parts, "\n")
}

func firstStyledRowContaining(rows []RenderedRow, needle string) string {
	for _, row := range rows {
		if strings.Contains(row.Plain, needle) {
			return row.Styled
		}
	}
	return ""
}
