package tuiapp

import (
	"fmt"
	"image/color"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestNormalizeTerminalMarkdownSplitsGluedMarkdownTable(t *testing.T) {
	raw := "---## 工具调用演示总结我刚刚同时使用了 7 种工具 来展示能力：| 工具 | 用途 | 演示内容 | |------|------|----------| | Command | 执行 shell 命令 | ls 列出文件 | | Glob | 文件名模式匹配 | 搜索 *.py 文件 |"

	got := normalizeTerminalMarkdown(raw)

	for _, want := range []string{
		"---\n## 工具调用演示总结",
		"能力：\n| 工具 | 用途 | 演示内容 |",
		"| 工具 | 用途 | 演示内容 |\n|------|------|----------|",
		"| Command | 执行 shell 命令 | ls 列出文件 |",
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

func TestNarrativeInlineCodeStyleScopesAfterCJKText(t *testing.T) {
	raw := "- **事实优先**：比起猜测，我更倾向于读取仓库中的真实代码。在编辑之前先读或搜索，用 `shell` 验证结果。"
	theme := tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor)
	inlineBG := sgrBackgroundCode(t, theme.MarkdownInlineCodeStyle().GetBackground())

	t.Run("glamour finalized", func(t *testing.T) {
		rendered := glamourRenderNarrative(raw, 180, theme, tuikit.LineStyleAssistant)
		assertInlineCodeBackgroundScope(t, rendered, inlineBG, "shell")
	})

	t.Run("streaming tail", func(t *testing.T) {
		rows := renderStreamingNarrativeTailRows("block-1", raw, "", tuikit.LineStyleAssistant, 180, theme)
		assertInlineCodeBackgroundScope(t, joinRenderedStyled(rows), inlineBG, "shell")
	})

	t.Run("main transcript active stream", func(t *testing.T) {
		m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
		m.viewport.SetWidth(180)
		m.viewport.SetHeight(20)
		_, _ = m.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: []TranscriptEvent{{
			Kind:          TranscriptEventNarrative,
			NarrativeKind: TranscriptNarrativeAssistant,
			Scope:         ACPProjectionMain,
			ScopeID:       "session-1",
			Actor:         "assistant",
			Text:          raw,
			Final:         false,
		}}})
		m.syncViewportContent()
		assertInlineCodeBackgroundScope(t, strings.Join(m.viewportStyledLines, "\n"), sgrBackgroundCode(t, m.theme.MarkdownInlineCodeStyle().GetBackground()), "shell")
	})

	t.Run("legacy stream line", func(t *testing.T) {
		m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
		m.viewport.SetWidth(180)
		m.viewport.SetHeight(20)
		m.streamLine = raw
		m.lastCommittedStyle = tuikit.LineStyleAssistant
		ctx := m.blockRenderContext(180)
		styled, _, _ := m.renderStreamViewportLines(ctx)
		assertInlineCodeBackgroundScope(t, strings.Join(styled, "\n"), sgrBackgroundCode(t, m.theme.MarkdownInlineCodeStyle().GetBackground()), "shell")
	})

	t.Run("legacy stream line wrapped inline code", func(t *testing.T) {
		m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
		m.viewport.SetWidth(24)
		m.viewport.SetHeight(20)
		m.streamLine = "前缀前缀前缀前缀前缀前缀，用 `shell command` 验证结果。"
		m.lastCommittedStyle = tuikit.LineStyleAssistant
		ctx := m.blockRenderContext(24)
		styled, _, _ := m.renderStreamViewportLines(ctx)
		assertInlineCodeBackgroundScope(t, strings.Join(styled, "\n"), sgrBackgroundCode(t, m.theme.MarkdownInlineCodeStyle().GetBackground()), "shell command")
	})
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
	raw := "工具调用演示总结：| 工具 | 用途 | 演示内容 | |------|------|----------| | Command | 执行 shell 命令 | ls 列出文件 | | Glob | 文件名模式匹配 | 搜索 *.py 文件 |"

	rendered := glamourRenderNarrative(raw, 96, tuikit.DefaultTheme(), tuikit.LineStyleAssistant)
	plain := ansi.Strip(rendered)

	for _, want := range []string{"工具", "用途", "演示内容", "Command", "Glob"} {
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

func TestNarrativeStyleConfigPinsSemanticBodyColors(t *testing.T) {
	dark := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	light := tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor)
	tests := []struct {
		name  string
		theme tuikit.Theme
		role  tuikit.LineStyle
		body  string
		link  string
	}{
		{
			name:  "dark assistant",
			theme: dark,
			role:  tuikit.LineStyleAssistant,
			body:  ptrString(styleForegroundToAnsiPtr(dark.TextStyle())),
			link:  ptrString(styleForegroundToAnsiPtr(dark.MarkdownLinkStyle())),
		},
		{
			name:  "light assistant",
			theme: light,
			role:  tuikit.LineStyleAssistant,
			body:  ptrString(styleForegroundToAnsiPtr(light.TextStyle())),
			link:  ptrString(styleForegroundToAnsiPtr(light.MarkdownLinkStyle())),
		},
		{
			name:  "dark reasoning",
			theme: dark,
			role:  tuikit.LineStyleReasoning,
			body:  ptrString(colorToAnsiPtr(dark.ReasoningFg)),
			link:  ptrString(colorToAnsiPtr(dark.ReasoningFg)),
		},
		{
			name:  "light reasoning",
			theme: light,
			role:  tuikit.LineStyleReasoning,
			body:  ptrString(colorToAnsiPtr(light.ReasoningFg)),
			link:  ptrString(colorToAnsiPtr(light.ReasoningFg)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			style := narrativeStyleConfig(tt.theme, tt.role)
			for field, got := range map[string]string{
				"document":    ptrString(style.Document.Color),
				"text":        ptrString(style.Text.Color),
				"paragraph":   ptrString(style.Paragraph.Color),
				"item":        ptrString(style.Item.Color),
				"enumeration": ptrString(style.Enumeration.Color),
				"task":        ptrString(style.Task.Color),
				"table":       ptrString(style.Table.Color),
			} {
				if got != tt.body {
					t.Fatalf("%s color = %q, want body %q", field, got, tt.body)
				}
			}
			if got := ptrString(style.LinkText.Color); got != tt.link {
				t.Fatalf("link text color = %q, want %q", got, tt.link)
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

func sgrBackgroundCode(t *testing.T, c color.Color) string {
	t.Helper()
	if c == nil {
		t.Fatal("expected inline code style to have a background color")
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("48;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

func assertInlineCodeBackgroundScope(t *testing.T, styled, inlineBG, want string) {
	t.Helper()
	plain := ansi.Strip(styled)
	bgText := textWithSGRBackground(styled, inlineBG)
	if !strings.Contains(plain, want) {
		t.Fatalf("rendered text missing %q\nplain=%q\nstyled=%q", want, plain, styled)
	}
	if normalizeInlineStyleText(bgText) != want {
		t.Fatalf("inline code background covered %q, want only %q\nplain=%q\nstyled=%q", bgText, want, plain, styled)
	}
}

func normalizeInlineStyleText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func textWithSGRBackground(styled, inlineBG string) string {
	var out strings.Builder
	active := false
	for i := 0; i < len(styled); {
		if styled[i] == '\x1b' && i+1 < len(styled) && styled[i+1] == '[' {
			end := i + 2
			for end < len(styled) && styled[end] != 'm' {
				end++
			}
			if end < len(styled) {
				params := styled[i+2 : end]
				if resetsSGRBackground(params) {
					active = false
				}
				if strings.Contains(params, inlineBG) {
					active = true
				}
				i = end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(styled[i:])
		if active {
			out.WriteRune(r)
		}
		i += size
	}
	return out.String()
}

func resetsSGRBackground(params string) bool {
	if params == "" {
		return true
	}
	for _, part := range strings.Split(params, ";") {
		switch part {
		case "0", "49":
			return true
		}
	}
	return false
}
