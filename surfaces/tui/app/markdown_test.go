package tuiapp

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestGlamourNarrativePreservesDollarDelimitedSourceText(t *testing.T) {
	raw := "```sh\nprintf '%s\\n' '$PATH$'\n```"
	rendered := glamourRenderNarrative(raw, 80, tuikit.DefaultTheme(), tuikit.LineStyleAssistant)
	if plain := ansi.Strip(rendered); !strings.Contains(plain, "$PATH$") {
		t.Fatalf("Glamour render changed code source containing dollars: %q", plain)
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

func TestNarrativeInlineCodeUsesCompactStyle(t *testing.T) {
	raw := "Use `shell` now."
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	style := narrativeStyleConfig(theme, tuikit.LineStyleAssistant)

	if style.Code.Prefix != "" || style.Code.Suffix != "" {
		t.Fatalf("inline code padding prefix=%q suffix=%q, want none", style.Code.Prefix, style.Code.Suffix)
	}
	if got, want := ptrString(style.Code.Color), ptrString(styleForegroundToAnsiPtr(theme.MarkdownInlineCodeStyle())); got != want {
		t.Fatalf("inline code foreground = %q, want %q", got, want)
	}
	if got := ptrString(style.Code.BackgroundColor); got != "" {
		t.Fatalf("inline code background = %q, want none", got)
	}

	rendered := glamourRenderNarrative(raw, 80, theme, tuikit.LineStyleAssistant)
	plain := ansi.Strip(rendered)
	if !strings.Contains(plain, "Use shell now.") {
		t.Fatalf("rendered inline code should not add extra spaces\nplain=%q\nstyled=%q", plain, rendered)
	}
	if strings.Contains(plain, "Use  shell  now.") {
		t.Fatalf("rendered inline code added padding spaces\nplain=%q\nstyled=%q", plain, rendered)
	}
}

func TestFinalAssistantInlineStrongStaysOnOneLogicalLine(t *testing.T) {
	t.Parallel()

	const raw = "原来如此！**Task read** 可查看单个任务；这里用 **Task wait** 批量等待三个子代理完成："
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	rows := renderParticipantTurnNarrativeRowsWithBuffer(
		"block-1",
		raw,
		nil,
		tuikit.LineStyleAssistant,
		180,
		BlockRenderContext{
			Width:     180,
			TermWidth: 180,
			Theme:     model.theme,
			ThemeKey:  themeRenderCacheKey(model.theme),
		},
		false,
	)
	plain := renderedPlainRows(rows)
	if len(plain) != 1 || strings.TrimRight(plain[0], " ") != "· 原来如此！Task read 可查看单个任务；这里用 Task wait 批量等待三个子代理完成：" {
		t.Fatalf("rendered rows = %#v, want one assistant row", plain)
	}
}

func TestNarrativeInlineCodeStyleScopesAfterCJKText(t *testing.T) {
	raw := "- **事实优先**：比起猜测，我更倾向于读取仓库中的真实代码。在编辑之前先读或搜索，用 `shell` 验证结果。"
	theme := tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor)
	inlineFG := sgrForegroundCode(t, theme.MarkdownInlineCodeStyle().GetForeground())

	rendered := glamourRenderNarrative(raw, 180, theme, tuikit.LineStyleAssistant)
	assertInlineCodeForegroundScope(t, rendered, inlineFG, "shell")

	rows := renderPlainStreamingNarrativeTailRows("block-1", raw, "· ", tuikit.LineStyleAssistant, 180, theme, true)
	if plain := joinRenderedPlain(rows); plain != "· "+raw {
		t.Fatalf("streaming tail plain = %q, want raw Markdown source", plain)
	}
	if styled := textWithSGRForeground(joinRenderedStyled(rows), inlineFG); strings.Contains(styled, "shell") {
		t.Fatalf("raw streaming tail unexpectedly used inline-code styling: %q", joinRenderedStyled(rows))
	}
}

func TestFinalMarkdownDoesNotDecorateProseWithInlineCodeColor(t *testing.T) {
	raw := markdownInlineCodeScopeFixture()
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)

	rendered := glamourRenderNarrative(raw, 160, theme, tuikit.LineStyleAssistant)
	assertMarkdownInlineCodeForegroundScope(t, rendered, sgrForegroundCode(t, theme.MarkdownInlineCodeStyle().GetForeground()), []string{"git status", "console.log(\"hello\")"})
}

func TestStreamingTailMarkdownDoesNotDecorateProseWithInlineCodeColor(t *testing.T) {
	raw := markdownInlineCodeScopeFixture()
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)

	rows := renderPlainStreamingNarrativeTailRows("block-1", raw, "", tuikit.LineStyleAssistant, 160, theme, true)
	if got := joinRenderedPlain(rows); got != raw {
		t.Fatalf("streaming tail changed Markdown source\n got=%q\nwant=%q", got, raw)
	}
	if got := textWithSGRForeground(joinRenderedStyled(rows), sgrForegroundCode(t, theme.MarkdownInlineCodeStyle().GetForeground())); strings.TrimSpace(got) != "" {
		t.Fatalf("raw streaming tail unexpectedly used inline-code styling: %q", got)
	}
}

func TestNarrativeInlineCodeStyleScopesToolNamesInCJKLists(t *testing.T) {
	raw := strings.Join([]string{
		"我的工作方式：",
		"",
		"- **事实优先**：优先从仓库/文件中获取真相，而不是靠猜测。读取搜索之后再编辑，用 `Shell` 验证来确认结果。",
		"",
		"我能做什么：",
		"",
		"- 执行 `Shell` 命令（默认沙箱模式，需要时可申请提权）",
		"- 通过 `Spawn` 委托子任务",
	}, "\n")
	theme := tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor)
	inlineFG := sgrForegroundCode(t, theme.MarkdownInlineCodeStyle().GetForeground())

	rendered := glamourRenderNarrative(raw, 180, theme, tuikit.LineStyleAssistant)
	assertInlineCodeForegroundScope(t, firstStyledLineContaining(rendered, "验证"), inlineFG, "Shell")
	assertInlineCodeForegroundScope(t, firstStyledLineContaining(rendered, "命令"), inlineFG, "Shell")
	assertInlineCodeForegroundScope(t, firstStyledLineContaining(rendered, "委托"), inlineFG, "Spawn")

	ctx := BlockRenderContext{Width: 180, Theme: theme, ThemeKey: themeRenderCacheKey(theme)}
	stream := RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           TextAssistant,
		Mode:           RenderStream,
		MarkdownPolicy: MarkdownStableTail,
		Raw:            raw,
		Prefix:         "· ",
		BlockID:        "block-stream-raw",
		LineStyle:      tuikit.LineStyleAssistant,
	}).Rows
	if got := joinRenderedPlain(stream); got != "· "+raw {
		t.Fatalf("streaming CJK list changed raw Markdown\n got=%q\nwant=%q", got, "· "+raw)
	}
}

func TestNarrativeInlineCodeStyleScopesShortCJKListAcronym(t *testing.T) {
	raws := []string{
		"- 无新增 `API` 依赖",
		"- 无新增`API`依赖",
		"• 无新增 `API` 依赖",
		"• 无新增`API`依赖",
	}
	for _, dark := range []bool{false, true} {
		theme := tuikit.ResolveThemeWithState(dark, false, colorprofile.TrueColor)
		inlineFG := sgrForegroundCode(t, theme.MarkdownInlineCodeStyle().GetForeground())
		for _, raw := range raws {
			rendered := glamourRenderNarrative(raw, 80, theme, tuikit.LineStyleAssistant)
			assertInlineCodeForegroundScope(t, firstStyledLineContaining(rendered, "API"), inlineFG, "API")
			rows := renderPlainStreamingNarrativeTailRows("block-api", raw, "", tuikit.LineStyleAssistant, 80, theme, true)
			if got := joinRenderedPlain(rows); got != raw {
				t.Fatalf("streaming acronym row = %q, want raw %q", got, raw)
			}
		}
	}
}

func TestGlamourListStrongDoesNotStealToolCodeColor(t *testing.T) {
	raw := strings.Join([]string{
		"可用工具",
		"",
		"- **读/写/编辑文件**（`Read`, `Write`, `Patch`）",
		"- **搜索文件内容与路径**（`Grep`, `Glob`, `LIST`）",
		"- **执行 `Shell` 命令**（`RunCommand`）",
		"- **管理多步骤任务**（`Plan`, `Task`, `Spawn`）",
	}, "\n")
	theme := tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor)
	rendered := glamourRenderNarrative(raw, 180, theme, tuikit.LineStyleAssistant)
	if rendered == "" {
		t.Fatal("expected rendered markdown")
	}

	bodyFG := sgrForegroundCode(t, theme.TextStyle().GetForeground())
	bodyText := textWithSGRForeground(rendered, bodyFG)
	for _, want := range []string{"读/写/编辑文件", "搜索文件内容与路径", "执行", "命令", "管理多步骤任务"} {
		if !strings.Contains(bodyText, want) {
			t.Fatalf("strong list label %q should keep body foreground\nbodyText=%q\nstyled=%q", want, bodyText, rendered)
		}
	}

	codeFG := sgrForegroundCode(t, theme.MarkdownInlineCodeStyle().GetForeground())
	codeText := textWithSGRForeground(rendered, codeFG)
	for _, want := range []string{"Read", "Write", "Patch", "Grep", "Glob", "LIST", "Shell", "RunCommand", "Plan", "Task", "Spawn"} {
		if !strings.Contains(codeText, want) {
			t.Fatalf("inline code %q should use code foreground\ncodeText=%q\nstyled=%q", want, codeText, rendered)
		}
	}
	for _, notWant := range []string{"读/写/编辑文件", "搜索文件内容与路径", "执行", "命令", "管理多步骤任务"} {
		if strings.Contains(codeText, notWant) {
			t.Fatalf("strong list label %q should not use inline-code foreground\ncodeText=%q\nstyled=%q", notWant, codeText, rendered)
		}
	}
}

func TestNarrativeViewportWrapPreservesRenderedInlineCodeANSI(t *testing.T) {
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	raw := "前缀前缀前缀前缀前缀前缀，用 `Shell` 验证结果。"
	styled := renderInlineMarkdown(raw, m.theme.TextStyle(), m.theme)
	wrapped := hardWrapDisplayLine(styled, 24)

	assertInlineCodeForegroundScope(t,
		firstStyledLineContaining(wrapped, "Shell"),
		sgrForegroundCode(t, m.theme.MarkdownInlineCodeStyle().GetForeground()),
		"Shell")
}

func TestInlineMarkdownSpanWrappingInvariants(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor)
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "cjk around inline code",
			raw:  "前缀 `shell` 后缀",
			want: "前缀 shell 后缀",
		},
		{
			name: "emoji and combining mark",
			raw:  "Fix e\u0301moji 👩‍💻 with `go test`",
			want: "Fix e\u0301moji 👩‍💻 with go test",
		},
		{
			name: "escaped markers",
			raw:  "\\*literal\\* and \\`tick\\`",
			want: "*literal* and `tick`",
		},
		{
			name: "windows glob keeps path separator",
			raw:  `Path D:\repo\*.go`,
			want: `Path D:\repo\*.go`,
		},
		{
			name: "windows path keeps punctuation separators",
			raw:  `Path D:\repo\.config\[cache]\#index.go`,
			want: `Path D:\repo\.config\[cache]\#index.go`,
		},
		{
			name: "multi backtick code span",
			raw:  "Use ``a ` b`` now",
			want: "Use a ` b now",
		},
		{
			name: "nested strong with code",
			raw:  "**bold `code`** done",
			want: "bold code done",
		},
		{
			name: "unclosed streaming marker stays literal",
			raw:  "**partial `code",
			want: "**partial `code",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			styled, plain := renderInlineMarkdownWrappedSegments(tt.raw, theme.TextStyle(), theme, 80)
			if got := strings.Join(plain, ""); got != tt.want {
				t.Fatalf("plain = %q, want %q", got, tt.want)
			}
			if len(styled) != len(plain) {
				t.Fatalf("styled/plain segment count mismatch: %d/%d", len(styled), len(plain))
			}
			for i := range styled {
				if got := ansi.Strip(styled[i]); got != plain[i] {
					t.Fatalf("segment %d strip(styled) = %q, plain = %q\nstyled=%q", i, got, plain[i], styled[i])
				}
			}
			wrappedStyled, wrappedPlain := renderInlineMarkdownWrappedSegments(tt.raw, theme.TextStyle(), theme, 12)
			if len(wrappedStyled) != len(wrappedPlain) {
				t.Fatalf("wrapped styled/plain segment count mismatch: %d/%d", len(wrappedStyled), len(wrappedPlain))
			}
			for i := range wrappedStyled {
				if got := ansi.Strip(wrappedStyled[i]); got != wrappedPlain[i] {
					t.Fatalf("wrapped segment %d strip(styled) = %q, plain = %q\nstyled=%q", i, got, wrappedPlain[i], wrappedStyled[i])
				}
				if width := graphemeWidth(wrappedPlain[i]); width > 12 {
					t.Fatalf("wrapped segment %d width = %d, want <= 12: %q", i, width, wrappedPlain[i])
				}
			}
		})
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

func TestGlamourNarrativeRendersGFMMarkdownTable(t *testing.T) {
	raw := "工具调用演示总结：\n\n| 工具 | 用途 | 演示内容 |\n|------|------|----------|\n| Command | 执行 shell 命令 | ls 列出文件 |\n| Glob | 文件名模式匹配 | 搜索 *.py 文件 |"

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

func TestNarrativeBlockquoteKeepsRawStreamAndGlamourFinal(t *testing.T) {
	raw := "> **RunCommand** 运行了 pytest 测试套件。可以看到 36 个测试用例中有些导入错误（`caelis_demo` 模块不存在）。这展示了我执行测试和诊断问题的能力。"
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	ctx := BlockRenderContext{Width: 220, TermWidth: 220, Theme: theme, ThemeKey: themeRenderCacheKey(theme)}
	body := "│ RunCommand 运行了 pytest 测试套件。可以看到 36 个测试用例中有些导入错误（caelis_demo 模块不存在）。这展示了我执行测试和诊断问题的能力。"

	stream := RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           TextAssistant,
		Mode:           RenderStream,
		MarkdownPolicy: MarkdownStableTail,
		Raw:            raw,
		Prefix:         "· ",
		BlockID:        "blockquote-stream",
		LineStyle:      tuikit.LineStyleAssistant,
	}).Rows
	if got := strings.TrimRight(joinRenderedPlain(stream), " "); got != "· "+raw {
		t.Fatalf("streaming blockquote = %q, want raw source", got)
	}

	final := RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           TextAssistant,
		Mode:           RenderFinal,
		MarkdownPolicy: MarkdownFull,
		Raw:            raw,
		Prefix:         "· ",
		BlockID:        "blockquote-final",
		LineStyle:      tuikit.LineStyleAssistant,
	}).Rows
	if got := strings.TrimRight(joinRenderedPlain(final), " "); got != "· "+body {
		t.Fatalf("final blockquote = %q, want Glamour rail %q", got, "· "+body)
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
			if style.CodeBlock.Theme != syntaxCodeBlockTheme(tt.theme) {
				t.Fatalf("code block theme = %q, want %q", style.CodeBlock.Theme, syntaxCodeBlockTheme(tt.theme))
			}
			if style.CodeBlock.Chroma != nil {
				t.Fatalf("expected direct named Chroma theme, got custom Chroma config")
			}
			rendered := glamourRenderNarrative("```diff\n-old\n+new\n```", 80, tt.theme, tt.role)
			if containsForbiddenRedBackground(rendered) {
				t.Fatalf("rendered diff code block contains forbidden red background ANSI:\n%q", rendered)
			}
		})
	}
}

func TestNarrativeCodeBlockUsesCaelisThemeForTerminalBackground(t *testing.T) {
	tests := []struct {
		name  string
		theme tuikit.Theme
		want  string
	}{
		{
			name:  "dark uses caelis dusk",
			theme: tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor),
			want:  "caelis-dusk",
		},
		{
			name:  "light uses caelis dawn",
			theme: tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor),
			want:  "caelis-dawn",
		},
	}

	for _, tt := range tests {
		for _, role := range []tuikit.LineStyle{tuikit.LineStyleAssistant, tuikit.LineStyleReasoning} {
			t.Run(tt.name+"/"+fmt.Sprint(role), func(t *testing.T) {
				style := narrativeStyleConfig(tt.theme, role)
				if style.CodeBlock.Theme != tt.want {
					t.Fatalf("code block Chroma theme = %q, want %q", style.CodeBlock.Theme, tt.want)
				}
				if style.CodeBlock.Chroma != nil {
					t.Fatalf("expected direct Chroma theme, got custom Chroma config")
				}
			})
		}
	}
}

func TestCaelisChromaStylesUseSemanticSyntaxPalettes(t *testing.T) {
	themes := []tuikit.Theme{
		tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor),
		tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor),
	}
	for _, theme := range themes {
		t.Run(theme.Name, func(t *testing.T) {
			palette := tuikit.SyntaxPaletteForTheme(theme)
			registered := styles.Get(palette.ChromaTheme)
			if registered.Name != palette.ChromaTheme {
				t.Fatalf("registered Chroma style = %q, want %q", registered.Name, palette.ChromaTheme)
			}
			for _, check := range []struct {
				name  string
				token chroma.TokenType
				want  color.Color
			}{
				{name: "keyword", token: chroma.Keyword, want: palette.Keyword},
				{name: "function", token: chroma.NameFunction, want: palette.Function},
				{name: "string", token: chroma.LiteralString, want: palette.String},
				{name: "number", token: chroma.LiteralNumber, want: palette.Number},
				{name: "operator", token: chroma.Operator, want: palette.Operator},
				{name: "deleted", token: chroma.GenericDeleted, want: palette.Deleted},
				{name: "inserted", token: chroma.GenericInserted, want: palette.Inserted},
			} {
				t.Run(check.name, func(t *testing.T) {
					want := colorToAnsiPtr(check.want)
					if want == nil {
						t.Fatalf("palette %s color is nil", check.name)
					}
					if got := registered.Get(check.token).Colour.String(); got != *want {
						t.Fatalf("%s color = %q, want %q", check.name, got, *want)
					}
				})
			}
			if got := registered.Get(chroma.Background).Background; got.IsSet() {
				t.Fatalf("Chroma background = %q, want parent code-block surface", got)
			}
		})
	}
}

func TestNarrativeCodeBlockRendersCaelisKeywordColors(t *testing.T) {
	tests := []struct {
		name       string
		theme      tuikit.Theme
		foreground string
	}{
		{
			name:       "dusk",
			theme:      tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor),
			foreground: "38;5;146",
		},
		{
			name:       "dawn",
			theme:      tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor),
			foreground: "38;5;61",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rendered := glamourRenderNarrative("```go\npackage main\n```", 80, tt.theme, tuikit.LineStyleAssistant)
			keywordText := normalizeInlineStyleText(textWithSGRForeground(rendered, tt.foreground))
			if !strings.Contains(keywordText, "package") {
				t.Fatalf("keyword foreground text = %q, want package\nrendered=%q", keywordText, rendered)
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

func TestStreamingNarrativeTailShowsRawFenceAndAvoidsRedBackground(t *testing.T) {
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
			rows := RenderText(TextRenderRequest{
				Kind:           TextAssistant,
				Mode:           RenderStream,
				MarkdownPolicy: MarkdownStableTail,
				Raw:            tt.raw,
				Prefix:         "* ",
				Width:          80,
				BlockID:        "block-1",
				Theme:          theme,
				LineStyle:      tuikit.LineStyleAssistant,
			}).Rows
			if len(rows) == 0 {
				t.Fatal("expected streaming rows")
			}
			plain := joinRenderedPlain(rows)
			if !strings.Contains(plain, "```go") {
				t.Fatalf("streaming code fence delimiter should remain visible in raw tail rows:\n%s", plain)
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
	shortRows := renderActiveNarrativeBufferTestRows("block-1", prefix+strings.Repeat("// x\n", 4), "* ", tuikit.LineStyleAssistant, 80, theme)
	longRows := renderActiveNarrativeBufferTestRows("block-1", prefix+strings.Repeat("// x\n", 40), "* ", tuikit.LineStyleAssistant, 80, theme)

	shortStyled := firstStyledRowContaining(shortRows, "fmt.Println")
	longStyled := firstStyledRowContaining(longRows, "fmt.Println")
	if shortStyled == "" || longStyled == "" {
		t.Fatalf("missing shared code row\nshort=%q\nlong=%q", joinRenderedPlain(shortRows), joinRenderedPlain(longRows))
	}
	if shortStyled != longStyled {
		t.Fatalf("shared code row style changed across stream length threshold\nshort=%q\n long=%q", shortStyled, longStyled)
	}
}

func TestStreamingStablePrefixMatchesFinalGlamourRows(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	raw := stablePrefixFinalParityMarkdown()
	stableRaw, tailRaw := splitStableStreamingMarkdown(raw)
	if strings.TrimSpace(stableRaw) == "" || strings.TrimSpace(tailRaw) == "" {
		t.Fatalf("test setup did not produce stable prefix and tail\nstable=%q\ntail=%q", stableRaw, tailRaw)
	}

	ctx := BlockRenderContext{Width: 96, Theme: theme, ThemeKey: themeRenderCacheKey(theme)}
	finalPrefixRows := glamourNarrativeRows("block-1", stableRaw, "· ", tuikit.LineStyleAssistant, 96, theme)
	streamRows := RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           TextAssistant,
		Mode:           RenderStream,
		MarkdownPolicy: MarkdownStableTail,
		Raw:            raw,
		Prefix:         "· ",
		BlockID:        "block-1",
		LineStyle:      tuikit.LineStyleAssistant,
	}).Rows

	assertRenderedRowsPrefixEqual(t, streamRows, finalPrefixRows)
}

func TestFinalizingActiveStreamPreservesCanonicalStablePrefixRows(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	raw := stablePrefixFinalParityMarkdown()
	stableRaw, tailRaw := splitStableStreamingMarkdown(raw)
	if strings.TrimSpace(stableRaw) == "" || strings.TrimSpace(tailRaw) == "" {
		t.Fatalf("test setup did not produce stable prefix and tail\nstable=%q\ntail=%q", stableRaw, tailRaw)
	}

	ctx := BlockRenderContext{Width: 96, Theme: theme, ThemeKey: themeRenderCacheKey(theme)}
	streamRows := RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           TextAssistant,
		Mode:           RenderStream,
		MarkdownPolicy: MarkdownStableTail,
		Raw:            raw,
		Prefix:         "· ",
		BlockID:        "block-1",
		LineStyle:      tuikit.LineStyleAssistant,
	}).Rows
	finalRows := RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           TextAssistant,
		Mode:           RenderFinal,
		MarkdownPolicy: MarkdownFull,
		Raw:            raw,
		Prefix:         "· ",
		BlockID:        "block-1",
		LineStyle:      tuikit.LineStyleAssistant,
	}).Rows
	finalPrefixRows := glamourNarrativeRows("block-1", stableRaw, "· ", tuikit.LineStyleAssistant, 96, theme)

	assertRenderedRowsPrefixEqual(t, streamRows, finalPrefixRows)
	assertRenderedRowsPrefixEqual(t, finalRows, finalPrefixRows)
}

func TestViewportStreamLineUsesCanonicalAssistantRenderer(t *testing.T) {
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	m.viewport.SetWidth(96)
	m.viewport.SetHeight(20)
	m.streamLine = stablePrefixFinalParityMarkdown()
	m.lastCommittedStyle = tuikit.LineStyleAssistant

	ctx := m.blockRenderContext(96)
	styledLines, plainLines, _ := m.renderStreamViewportLines(ctx)
	canonical := RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           TextAssistant,
		Mode:           RenderStream,
		MarkdownPolicy: MarkdownStableTail,
		Raw:            m.streamLine,
		Width:          96,
		BlockID:        "",
		LineStyle:      tuikit.LineStyleAssistant,
	}).Rows

	assertRenderedLineSlicesEqualRows(t, styledLines, plainLines, canonical)
}

func renderActiveNarrativeBufferTestRows(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme) []RenderedRow {
	buffer := &activeNarrativeBuffer{}
	buffer.SetText(raw)
	return buffer.RenderRows(blockID, rolePrefix, roleStyle, BlockRenderContext{
		Width: width,
		Theme: theme,
	})
}

func stablePrefixFinalParityMarkdown() string {
	stable := strings.Join([]string{
		"# 一级标题：Markdown 格式展示",
		"",
		"二级标题：文本样式",
		"",
		"这是普通文本，这是 **粗体文字**，这是 *斜体文字*，这是 `go test ./...`。",
		"",
		"> 💡 这是一段引用，用来强调重要信息或提示。",
		"",
		"- **事实优先**：读取仓库中的真实代码。",
		"- 使用 `Shell` 验证结果。",
		"",
	}, "\n")
	tail := strings.Repeat("尾部内容仍在 stream 中追加，保持轻量渲染直到段落稳定。", 12)
	return stable + tail
}

func markdownInlineCodeScopeFixture() string {
	return strings.Join([]string{
		"# Markdown 格式大全 — Rich Formatting Demo",
		"",
		"> **作者**: Caelis · **日期**: 2025-01-15 · **版本**: v1.0",
		"",
		"---",
		"",
		"## 1. 文本样式",
		"",
		"| 样式 | 语法 | 示例 |",
		"| --- | --- | --- |",
		"| **粗体** | `**text**` | **这是粗体文字** |",
		"| `行内代码` | `` `text` `` | `console.log(\"hello\")` |",
		"",
		"正文使用 `git status` 查看状态。",
		"",
		"### 无序列表",
		"",
		"- 🍎 苹果",
		"- 红富士",
	}, "\n")
}

func assertMarkdownInlineCodeForegroundScope(t *testing.T, styled string, inlineFG string, wantForegroundText []string) {
	t.Helper()
	foregroundText := normalizeInlineStyleText(textWithSGRForeground(styled, inlineFG))
	for _, want := range wantForegroundText {
		if !strings.Contains(foregroundText, want) {
			t.Fatalf("inline-code foreground text missing expected code span %q\nforeground=%q\nplain=%q\nstyled=%q", want, foregroundText, ansi.Strip(styled), styled)
		}
	}
	for _, notWant := range []string{
		"Markdown 格式大全",
		"作者",
		"日期",
		"版本",
		"1. 文本样式",
		"样式",
		"语法",
		"示例",
		"粗体",
		"这是粗体文字",
		"正文使用",
		"查看状态",
		"无序列表",
		"苹果",
		"红富士",
	} {
		if strings.Contains(foregroundText, notWant) {
			t.Fatalf("markdown prose %q should not receive inline-code foreground\nforeground=%q\nplain=%q\nstyled=%q", notWant, foregroundText, ansi.Strip(styled), styled)
		}
	}
}

func assertRenderedRowsPrefixEqual(t *testing.T, got []RenderedRow, want []RenderedRow) {
	t.Helper()
	if len(want) == 0 {
		t.Fatal("want prefix rows is empty")
	}
	if len(got) < len(want) {
		t.Fatalf("got %d rows, want at least %d\n got plain:\n%s\nwant plain:\n%s", len(got), len(want), joinRenderedPlain(got), joinRenderedPlain(want))
	}
	for i := range want {
		if got[i].Plain != want[i].Plain || got[i].Styled != want[i].Styled {
			t.Fatalf("row %d mismatch\n got plain: %q\nwant plain: %q\n got styled: %q\nwant styled: %q\n got all:\n%s\nwant all:\n%s",
				i, got[i].Plain, want[i].Plain, got[i].Styled, want[i].Styled, joinRenderedPlain(got[:len(want)]), joinRenderedPlain(want))
		}
	}
}

func assertRenderedLineSlicesEqualRows(t *testing.T, styledLines []string, plainLines []string, rows []RenderedRow) {
	t.Helper()
	if len(styledLines) != len(rows) || len(plainLines) != len(rows) {
		t.Fatalf("line count mismatch styled=%d plain=%d rows=%d\nstyled=%q\nplain=%q\nrows=%q",
			len(styledLines), len(plainLines), len(rows), strings.Join(styledLines, "\n"), strings.Join(plainLines, "\n"), joinRenderedPlain(rows))
	}
	for i := range rows {
		if styledLines[i] != rows[i].Styled || plainLines[i] != rows[i].Plain {
			t.Fatalf("line %d mismatch\n got plain: %q\nwant plain: %q\n got styled: %q\nwant styled: %q",
				i, plainLines[i], rows[i].Plain, styledLines[i], rows[i].Styled)
		}
	}
}

func TestMainACPTurnActiveMarkdownStreamUsesRawTail(t *testing.T) {
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
	if !strings.Contains(plain, "```go") {
		t.Fatalf("Main ACP active stream should show raw code fence delimiter:\n%s", plain)
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

func renderedRowsHaveClickToken(rows []RenderedRow, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	for _, row := range rows {
		if row.ClickToken == token {
			return true
		}
	}
	return false
}

func viewportHasClickToken(model *Model, token string) bool {
	token = strings.TrimSpace(token)
	if model == nil || token == "" {
		return false
	}
	for _, got := range model.viewportClickTokens {
		if got == token {
			return true
		}
	}
	return false
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

func firstStyledLineContaining(styled string, needle string) string {
	for _, line := range strings.Split(styled, "\n") {
		if strings.Contains(ansi.Strip(line), needle) {
			return line
		}
	}
	return ""
}

func sgrForegroundCode(t *testing.T, c color.Color) string {
	t.Helper()
	if c == nil {
		t.Fatal("expected style to have a foreground color")
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

func assertInlineCodeForegroundScope(t *testing.T, styled, inlineFG, want string) {
	t.Helper()
	plain := ansi.Strip(styled)
	fgText := textWithSGRForeground(styled, inlineFG)
	if !strings.Contains(plain, want) {
		t.Fatalf("rendered text missing %q\nplain=%q\nstyled=%q", want, plain, styled)
	}
	if normalizeInlineStyleText(fgText) != want {
		t.Fatalf("inline code foreground covered %q, want only %q\nplain=%q\nstyled=%q", fgText, want, plain, styled)
	}
}

func normalizeInlineStyleText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func textWithSGRForeground(styled, foreground string) string {
	var out strings.Builder
	active := false
	target, targetOK := parseSGRColorCode(foreground, "38")
	for i := 0; i < len(styled); {
		if styled[i] == '\x1b' && i+1 < len(styled) && styled[i+1] == '[' {
			end := i + 2
			for end < len(styled) && styled[end] != 'm' {
				end++
			}
			if end < len(styled) {
				params := styled[i+2 : end]
				if resetsSGRForeground(params) {
					active = false
				}
				matchesTarget := strings.Contains(params, foreground) || (targetOK && sgrParamsContainColor(params, "38", target, 1))
				if sgrSetsForeground(params) && !matchesTarget {
					active = false
				}
				if matchesTarget {
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

type sgrRGB struct {
	r int
	g int
	b int
}

func parseSGRColorCode(code, kind string) (sgrRGB, bool) {
	parts := strings.Split(code, ";")
	if len(parts) != 5 || parts[0] != kind || parts[1] != "2" {
		return sgrRGB{}, false
	}
	r, errR := strconv.Atoi(parts[2])
	g, errG := strconv.Atoi(parts[3])
	b, errB := strconv.Atoi(parts[4])
	if errR != nil || errG != nil || errB != nil {
		return sgrRGB{}, false
	}
	return sgrRGB{r: r, g: g, b: b}, true
}

func sgrParamsContainColor(params, kind string, target sgrRGB, tolerance int) bool {
	parts := strings.Split(params, ";")
	for i := 0; i+4 < len(parts); i++ {
		if parts[i] != kind || parts[i+1] != "2" {
			continue
		}
		r, errR := strconv.Atoi(parts[i+2])
		g, errG := strconv.Atoi(parts[i+3])
		b, errB := strconv.Atoi(parts[i+4])
		if errR != nil || errG != nil || errB != nil {
			continue
		}
		if absInt(r-target.r) <= tolerance && absInt(g-target.g) <= tolerance && absInt(b-target.b) <= tolerance {
			return true
		}
	}
	return false
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func resetsSGRForeground(params string) bool {
	if params == "" {
		return true
	}
	for _, part := range strings.Split(params, ";") {
		switch part {
		case "0", "39":
			return true
		}
	}
	return false
}

func sgrSetsForeground(params string) bool {
	parts := strings.Split(params, ";")
	for i, part := range parts {
		if part == "38" && i+1 < len(parts) && (parts[i+1] == "2" || parts[i+1] == "5") {
			return true
		}
		switch part {
		case "30", "31", "32", "33", "34", "35", "36", "37",
			"90", "91", "92", "93", "94", "95", "96", "97":
			return true
		}
	}
	return false
}
