package tuiapp

import (
	"fmt"
	"image/color"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

// ---------------------------------------------------------------------------
// Glamour-based narrative rendering
// ---------------------------------------------------------------------------

// glamourRenderNarrative renders markdown text through Glamour for styled
// terminal output. Returns the rendered multi-line string. On any error
// the caller should fall back to inline rendering.
func glamourRenderNarrative(raw string, width int, theme tuikit.Theme, roleStyle tuikit.LineStyle) string {
	if raw = strings.TrimSpace(raw); raw == "" {
		return ""
	}
	raw = normalizeGlamourMarkdown(raw)
	if width <= 0 {
		width = 80
	}
	renderer := getGlamourRenderer(width, theme, roleStyle)
	if renderer == nil {
		return ""
	}
	rendered, err := renderer.Render(raw)
	if err != nil {
		return ""
	}
	// Glamour appends trailing newlines; trim them.
	return strings.TrimRight(rendered, "\n")
}

// glamourNarrativeRows renders a finalized narrative block into RenderedRows
// via Glamour. The Plain text is derived by stripping ANSI from the styled
// output, ensuring perfect line-by-line correspondence for selection/copy.
// rolePrefix (e.g. "* ") is prepended to the first line of both channels.
func glamourNarrativeRows(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme) []RenderedRow {
	// Compute available width after accounting for the role prefix.
	prefixWidth := maxInt(graphemeWidth(rolePrefix), 0)
	glamourWidth := maxInt(1, width-prefixWidth)
	return glamourNarrativeRowsWithWrapWidth(blockID, raw, rolePrefix, roleStyle, glamourWidth, theme)
}

func glamourNarrativeRowsWithWrapWidth(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, wrapWidth int, theme tuikit.Theme) []RenderedRow {
	rendered := glamourRenderNarrative(raw, wrapWidth, theme, roleStyle)
	if rendered == "" {
		return nil
	}

	styledLines := strings.Split(rendered, "\n")

	// Trim leading/trailing blank lines that glamour may add.
	for len(styledLines) > 0 && strings.TrimSpace(xansi.Strip(styledLines[0])) == "" {
		styledLines = styledLines[1:]
	}
	for len(styledLines) > 0 && strings.TrimSpace(xansi.Strip(styledLines[len(styledLines)-1])) == "" {
		styledLines = styledLines[:len(styledLines)-1]
	}
	if len(styledLines) == 0 {
		return nil
	}

	rows := make([]RenderedRow, 0, len(styledLines))
	styledRolePrefix := ""
	if rolePrefix != "" {
		styledRolePrefix = tuikit.ColorizeLogLine(rolePrefix, roleStyle, theme)
	}

	for i, sl := range styledLines {
		plain := xansi.Strip(sl)
		styled := sl
		if i == 0 && rolePrefix != "" {
			plain = rolePrefix + plain
			styled = styledRolePrefix + styled
		}
		rows = append(rows, RenderedRow{
			Styled:     styled,
			Plain:      plain,
			BlockID:    blockID,
			PreWrapped: true,
		})
	}

	return rows
}

// ---------------------------------------------------------------------------
// Glamour renderer cache (small width/theme/role LRU)
// ---------------------------------------------------------------------------

const glamourRendererCacheMaxEntries = 8

type glamourRendererKey struct {
	width    int
	themeKey string
	role     tuikit.LineStyle
}

var glamourCache struct {
	sync.Mutex
	entries map[glamourRendererKey]*glamour.TermRenderer
	order   []glamourRendererKey
}

type streamingNarrativeCacheEntry struct {
	width           int
	themeKey        string
	role            tuikit.LineStyle
	rendererVersion string
	stableRaw       string
	rolePrefix      string
	renderedRows    []RenderedRow
}

var glamourStreamingCache struct {
	sync.Mutex
	entries map[string]streamingNarrativeCacheEntry
}

// clearGlamourCache invalidates the cached glamour renderer so that the next
// call to getGlamourRenderer creates a fresh one. Call this when the theme or
// color profile changes (e.g. from applyTheme).
func clearGlamourCache() {
	glamourCache.Lock()
	glamourCache.entries = nil
	glamourCache.order = nil
	glamourCache.Unlock()
	glamourStreamingCache.Lock()
	glamourStreamingCache.entries = nil
	glamourStreamingCache.Unlock()
}

func getGlamourRenderer(width int, theme tuikit.Theme, roleStyle tuikit.LineStyle) *glamour.TermRenderer {
	glamourCache.Lock()
	defer glamourCache.Unlock()

	themeKey := themeRenderCacheKey(theme)
	key := glamourRendererKey{width: width, themeKey: themeKey, role: roleStyle}
	if renderer := glamourCache.entries[key]; renderer != nil {
		touchGlamourRendererCacheKey(key)
		return renderer
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(narrativeStyleConfig(theme, roleStyle)),
		glamour.WithWordWrap(width),
		glamour.WithTableWrap(true),
		glamour.WithInlineTableLinks(true),
	)
	if err != nil {
		return nil
	}

	storeGlamourRenderer(key, renderer)
	return renderer
}

func touchGlamourRendererCacheKey(key glamourRendererKey) {
	for i, item := range glamourCache.order {
		if item == key {
			copy(glamourCache.order[i:], glamourCache.order[i+1:])
			glamourCache.order[len(glamourCache.order)-1] = key
			return
		}
	}
	glamourCache.order = append(glamourCache.order, key)
}

func storeGlamourRenderer(key glamourRendererKey, renderer *glamour.TermRenderer) {
	if glamourCache.entries == nil {
		glamourCache.entries = make(map[glamourRendererKey]*glamour.TermRenderer, glamourRendererCacheMaxEntries)
	}
	if old := glamourCache.entries[key]; old != nil {
		glamourCache.entries[key] = renderer
		touchGlamourRendererCacheKey(key)
		return
	}
	if len(glamourCache.order) >= glamourRendererCacheMaxEntries {
		evict := glamourCache.order[0]
		copy(glamourCache.order, glamourCache.order[1:])
		glamourCache.order = glamourCache.order[:len(glamourCache.order)-1]
		delete(glamourCache.entries, evict)
	}
	glamourCache.entries[key] = renderer
	glamourCache.order = append(glamourCache.order, key)
}

// ---------------------------------------------------------------------------
// Glamour style config derived from current theme
// ---------------------------------------------------------------------------

func narrativeStyleConfig(theme tuikit.Theme, roleStyle tuikit.LineStyle) gansi.StyleConfig {
	style := styles.DarkStyleConfig
	if !theme.IsDark {
		style = styles.LightStyleConfig
	}

	// No document-level margin; our layout handles outer spacing.
	zero := uint(0)
	style.Document.Margin = &zero
	bodyHex := styleForegroundToAnsiPtr(theme.TextStyle())
	linkHex := styleForegroundToAnsiPtr(theme.MarkdownLinkStyle())
	style.Document.Color = bodyHex
	style.Text.Color = bodyHex
	style.Paragraph.Color = bodyHex

	// ---------------------------------------------------------------
	// Headings — crush-style: H1 gets background pill, H2+ keep
	// markdown prefix for scannability.
	// ---------------------------------------------------------------
	headingHex := styleForegroundToAnsiPtr(theme.MarkdownHeadingStyle())

	style.Heading.BlockSuffix = "\n"
	style.Heading.Color = headingHex
	style.Heading.Bold = boolPtr(true)

	style.H1.Prefix = " "
	style.H1.Suffix = " "
	style.H1.Color = styleForegroundToAnsiPtr(theme.TextStyle())
	style.H1.BackgroundColor = styleBackgroundToAnsiPtr(theme.MarkdownCodeBlockStyle())
	style.H1.Bold = boolPtr(true)
	style.H1.Underline = boolPtr(false)

	style.H2.Prefix = ""
	style.H2.Color = headingHex
	style.H2.Bold = boolPtr(true)

	style.H3.Prefix = ""
	style.H3.Color = headingHex
	style.H3.Bold = boolPtr(true)

	style.H4.Prefix = ""
	style.H4.Color = headingHex
	style.H5.Prefix = ""
	style.H5.Color = headingHex
	style.H6.Prefix = ""
	style.H6.Color = styleForegroundToAnsiPtr(theme.TextStyle())

	// ---------------------------------------------------------------
	// Lists — bullet marker "• " for unordered, ". " for ordered
	// ---------------------------------------------------------------
	style.Item.BlockPrefix = "• "
	style.Item.Color = bodyHex
	style.Enumeration.BlockPrefix = ". "
	style.Enumeration.Color = bodyHex
	style.List.LevelIndent = 2

	// ---------------------------------------------------------------
	// Strong / Emphasis / Strikethrough
	// ---------------------------------------------------------------
	style.Strong.Color = bodyHex
	style.Strong.Bold = boolPtr(true)
	style.Emph.Color = bodyHex
	style.Emph.Italic = boolPtr(true)
	style.Strikethrough.Color = bodyHex
	style.Strikethrough.CrossedOut = boolPtr(true)

	// ---------------------------------------------------------------
	// Inline code — background highlight with padding (crush style)
	// ---------------------------------------------------------------
	style.Code.Prefix = " "
	style.Code.Suffix = " "
	style.Code.Color = styleForegroundToAnsiPtr(theme.MarkdownInlineCodeStyle())
	style.Code.BackgroundColor = styleBackgroundToAnsiPtr(theme.MarkdownInlineCodeStyle())

	// ---------------------------------------------------------------
	// Code blocks — Chroma syntax highlighting
	// ---------------------------------------------------------------
	cbIndent := uint(0)
	cbMargin := uint(0)
	style.CodeBlock.Margin = &cbMargin
	style.CodeBlock.Indent = &cbIndent
	style.CodeBlock.Color = styleForegroundToAnsiPtr(theme.MarkdownCodeBlockStyle())
	style.CodeBlock.BackgroundColor = styleBackgroundToAnsiPtr(theme.MarkdownCodeBlockStyle())
	if style.CodeBlock.Chroma == nil {
		style.CodeBlock.Chroma = &gansi.Chroma{}
	}
	style.CodeBlock.Chroma.Text.Color = styleForegroundToAnsiPtr(theme.MarkdownCodeBlockStyle())
	style.CodeBlock.Chroma.Background.BackgroundColor = styleBackgroundToAnsiPtr(theme.MarkdownCodeBlockStyle())
	style.CodeBlock.Chroma.Background.Color = styleForegroundToAnsiPtr(theme.MarkdownCodeBlockStyle())
	style.CodeBlock.Chroma.Comment.Color = styleForegroundToAnsiPtr(theme.MutedTextStyle())
	style.CodeBlock.Chroma.Keyword.Color = headingHex
	style.CodeBlock.Chroma.KeywordType.Color = colorToAnsiPtr(theme.Success)
	style.CodeBlock.Chroma.NameFunction.Color = colorToAnsiPtr(theme.Success)
	style.CodeBlock.Chroma.LiteralString.Color = colorToAnsiPtr(theme.Warning)
	style.CodeBlock.Chroma.LiteralNumber.Color = colorToAnsiPtr(theme.Warning)
	style.CodeBlock.Chroma.Operator.Color = colorToAnsiPtr(theme.Error)

	// ---------------------------------------------------------------
	// Blockquotes
	// ---------------------------------------------------------------
	bqIndent := uint(2)
	bqToken := "│ "
	style.BlockQuote.Indent = &bqIndent
	style.BlockQuote.IndentToken = &bqToken
	style.BlockQuote.Color = styleForegroundToAnsiPtr(theme.MarkdownQuoteStyle())

	// ---------------------------------------------------------------
	// Links — colored + underline, link text bold
	// ---------------------------------------------------------------
	style.Link.Color = linkHex
	style.Link.Underline = boolPtr(true)
	style.LinkText.Color = linkHex
	style.LinkText.Bold = boolPtr(true)

	// ---------------------------------------------------------------
	// Horizontal rule
	// ---------------------------------------------------------------
	style.HorizontalRule.Color = styleForegroundToAnsiPtr(theme.MarkdownRuleStyle())
	style.HorizontalRule.Format = "\n────────\n"

	// ---------------------------------------------------------------
	// Tables — keep compact box-drawing separators that fit the transcript.
	// ---------------------------------------------------------------
	tableMargin := uint(0)
	style.Table.Margin = &tableMargin
	style.Table.Color = bodyHex
	style.Table.CenterSeparator = stringPtr("┼")
	style.Table.ColumnSeparator = stringPtr("│")
	style.Table.RowSeparator = stringPtr("─")

	// ---------------------------------------------------------------
	// Task list
	// ---------------------------------------------------------------
	style.Task.Color = bodyHex
	style.Task.Ticked = "[✓] "
	style.Task.Unticked = "[ ] "

	if roleStyle == tuikit.LineStyleReasoning {
		reasoningHex := colorToAnsiPtr(theme.ReasoningFg)
		mutedHex := colorToAnsiPtr(theme.MutedText)
		style.Document.Color = reasoningHex
		style.Text.Color = reasoningHex
		style.Paragraph.Color = reasoningHex
		style.Heading.Color = reasoningHex
		style.H1.Color = reasoningHex
		style.H2.Color = reasoningHex
		style.H3.Color = reasoningHex
		style.H4.Color = mutedHex
		style.H5.Color = mutedHex
		style.H6.Color = mutedHex
		style.Item.Color = reasoningHex
		style.Enumeration.Color = reasoningHex
		style.Task.Color = reasoningHex
		style.BlockQuote.Color = reasoningHex
		style.Link.Color = reasoningHex
		style.LinkText.Color = reasoningHex
		style.Table.Color = reasoningHex
		style.Code.Color = reasoningHex
		style.CodeBlock.Color = reasoningHex
		style.CodeBlock.Chroma.Text.Color = reasoningHex
		style.CodeBlock.Chroma.Comment.Color = mutedHex
		style.CodeBlock.Chroma.Keyword.Color = reasoningHex
		style.CodeBlock.Chroma.KeywordType.Color = reasoningHex
		style.CodeBlock.Chroma.NameFunction.Color = reasoningHex
		style.CodeBlock.Chroma.LiteralString.Color = reasoningHex
		style.CodeBlock.Chroma.LiteralNumber.Color = reasoningHex
		style.CodeBlock.Chroma.Operator.Color = reasoningHex
		style.HorizontalRule.Color = mutedHex
	}

	normalizeNarrativeChromaStyle(style.CodeBlock.Chroma, theme, roleStyle)

	return style
}

func normalizeNarrativeChromaStyle(chroma *gansi.Chroma, theme tuikit.Theme, roleStyle tuikit.LineStyle) {
	if chroma == nil {
		return
	}
	codeFg := styleForegroundToAnsiPtr(theme.MarkdownCodeBlockStyle())
	codeBg := styleBackgroundToAnsiPtr(theme.MarkdownCodeBlockStyle())
	textFg := styleForegroundToAnsiPtr(theme.TextStyle())
	mutedFg := styleForegroundToAnsiPtr(theme.MutedTextStyle())
	headingFg := styleForegroundToAnsiPtr(theme.MarkdownHeadingStyle())
	linkFg := styleForegroundToAnsiPtr(theme.MarkdownLinkStyle())
	successFg := colorToAnsiPtr(theme.Success)
	warningFg := colorToAnsiPtr(theme.Warning)
	errorFg := colorToAnsiPtr(theme.Error)
	diffAddFg := colorToAnsiPtr(theme.DiffAddFg)
	diffRemoveFg := colorToAnsiPtr(theme.DiffRemoveFg)

	if roleStyle == tuikit.LineStyleReasoning {
		reasoningFg := colorToAnsiPtr(theme.ReasoningFg)
		codeFg = firstAnsiPtr(reasoningFg, codeFg)
		textFg = firstAnsiPtr(reasoningFg, textFg)
		headingFg = firstAnsiPtr(reasoningFg, headingFg)
		linkFg = firstAnsiPtr(reasoningFg, linkFg)
		successFg = firstAnsiPtr(reasoningFg, successFg)
		warningFg = firstAnsiPtr(reasoningFg, warningFg)
		errorFg = firstAnsiPtr(reasoningFg, errorFg)
		diffAddFg = firstAnsiPtr(reasoningFg, diffAddFg)
		diffRemoveFg = firstAnsiPtr(reasoningFg, diffRemoveFg)
	}

	all := []*gansi.StylePrimitive{
		&chroma.Text,
		&chroma.Error,
		&chroma.Comment,
		&chroma.CommentPreproc,
		&chroma.Keyword,
		&chroma.KeywordReserved,
		&chroma.KeywordNamespace,
		&chroma.KeywordType,
		&chroma.Operator,
		&chroma.Punctuation,
		&chroma.Name,
		&chroma.NameBuiltin,
		&chroma.NameTag,
		&chroma.NameAttribute,
		&chroma.NameClass,
		&chroma.NameConstant,
		&chroma.NameDecorator,
		&chroma.NameException,
		&chroma.NameFunction,
		&chroma.NameOther,
		&chroma.Literal,
		&chroma.LiteralNumber,
		&chroma.LiteralDate,
		&chroma.LiteralString,
		&chroma.LiteralStringEscape,
		&chroma.GenericDeleted,
		&chroma.GenericEmph,
		&chroma.GenericInserted,
		&chroma.GenericStrong,
		&chroma.GenericSubheading,
		&chroma.Background,
	}
	for _, primitive := range all {
		primitive.BackgroundColor = codeBg
	}

	chroma.Text.Color = firstAnsiPtr(codeFg, textFg)
	chroma.Background.Color = firstAnsiPtr(codeFg, textFg)
	chroma.Comment.Color = firstAnsiPtr(mutedFg, codeFg, textFg)
	chroma.CommentPreproc.Color = firstAnsiPtr(mutedFg, codeFg, textFg)
	chroma.Keyword.Color = firstAnsiPtr(headingFg, codeFg, textFg)
	chroma.KeywordReserved.Color = firstAnsiPtr(headingFg, codeFg, textFg)
	chroma.KeywordNamespace.Color = firstAnsiPtr(headingFg, codeFg, textFg)
	chroma.KeywordType.Color = firstAnsiPtr(successFg, headingFg, codeFg, textFg)
	chroma.Operator.Color = firstAnsiPtr(errorFg, headingFg, codeFg, textFg)
	chroma.Punctuation.Color = firstAnsiPtr(mutedFg, codeFg, textFg)
	chroma.Name.Color = firstAnsiPtr(codeFg, textFg)
	chroma.NameBuiltin.Color = firstAnsiPtr(linkFg, codeFg, textFg)
	chroma.NameTag.Color = firstAnsiPtr(headingFg, codeFg, textFg)
	chroma.NameAttribute.Color = firstAnsiPtr(linkFg, codeFg, textFg)
	chroma.NameClass.Color = firstAnsiPtr(successFg, codeFg, textFg)
	chroma.NameConstant.Color = firstAnsiPtr(warningFg, codeFg, textFg)
	chroma.NameDecorator.Color = firstAnsiPtr(linkFg, codeFg, textFg)
	chroma.NameException.Color = firstAnsiPtr(errorFg, codeFg, textFg)
	chroma.NameFunction.Color = firstAnsiPtr(successFg, codeFg, textFg)
	chroma.NameOther.Color = firstAnsiPtr(codeFg, textFg)
	chroma.Literal.Color = firstAnsiPtr(warningFg, codeFg, textFg)
	chroma.LiteralNumber.Color = firstAnsiPtr(warningFg, codeFg, textFg)
	chroma.LiteralDate.Color = firstAnsiPtr(warningFg, codeFg, textFg)
	chroma.LiteralString.Color = firstAnsiPtr(warningFg, codeFg, textFg)
	chroma.LiteralStringEscape.Color = firstAnsiPtr(headingFg, warningFg, codeFg, textFg)
	chroma.Error.Color = firstAnsiPtr(errorFg, codeFg, textFg)
	chroma.GenericDeleted.Color = firstAnsiPtr(diffRemoveFg, errorFg, codeFg, textFg)
	chroma.GenericInserted.Color = firstAnsiPtr(diffAddFg, successFg, codeFg, textFg)
	chroma.GenericSubheading.Color = firstAnsiPtr(headingFg, codeFg, textFg)
}

func firstAnsiPtr(values ...*string) *string {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// colorToAnsiPtr converts an image/color.Color to a hex "#rrggbb" pointer
// suitable for glamour's StyleConfig fields. Returns nil for nil input.
func colorToAnsiPtr(c color.Color) *string {
	if c == nil {
		return nil
	}
	if _, ok := c.(lipgloss.NoColor); ok {
		return nil
	}
	r, g, b, _ := c.RGBA()
	s := fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	return &s
}

func styleForegroundToAnsiPtr(style lipgloss.Style) *string {
	return colorToAnsiPtr(style.GetForeground())
}

func styleBackgroundToAnsiPtr(style lipgloss.Style) *string {
	return colorToAnsiPtr(style.GetBackground())
}

func boolPtr(v bool) *bool { return &v }

func stringPtr(v string) *string { return &v }

func themeRenderCacheKey(theme tuikit.Theme) string {
	parts := []string{
		theme.Name,
		fmt.Sprintf("%t", theme.IsDark),
		fmt.Sprintf("%t", theme.NoColor),
		fmt.Sprint(theme.Profile),
		themeColorCacheKey(theme.TextPrimary),
		themeColorCacheKey(theme.SecondaryText),
		themeColorCacheKey(theme.MutedText),
		themeColorCacheKey(theme.AssistantFg),
		themeColorCacheKey(theme.ReasoningFg),
		themeColorCacheKey(theme.UserFg),
		themeColorCacheKey(theme.UserPrefixFg),
		themeColorCacheKey(theme.ToolFg),
		themeColorCacheKey(theme.Accent),
		themeColorCacheKey(theme.Focus),
		themeColorCacheKey(theme.LinkFg),
		themeColorCacheKey(theme.CodeFg),
		themeColorCacheKey(theme.CodeBg),
		themeColorCacheKey(theme.CodeBlockFg),
		themeColorCacheKey(theme.CodeBlockBg),
		themeColorCacheKey(theme.TableHeaderBg),
		themeColorCacheKey(theme.TableBorder),
		themeColorCacheKey(theme.Success),
		themeColorCacheKey(theme.Warning),
		themeColorCacheKey(theme.Error),
	}
	return strings.Join(parts, "|")
}

func themeColorCacheKey(c color.Color) string {
	if ansiColor := colorToAnsiPtr(c); ansiColor != nil {
		return *ansiColor
	}
	return ""
}

func normalizeGlamourMarkdown(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = normalizeTerminalMarkdown(raw)
	return normalizeIndentedCodeFences(raw)
}

func normalizeIndentedCodeFences(raw string) string {
	if raw == "" {
		return raw
	}
	lines := strings.Split(raw, "\n")
	for i := 0; i < len(lines); i++ {
		indent, fence, ok := parseFenceLine(lines[i])
		if !ok || indent < 4 {
			continue
		}
		end := findClosingFenceLine(lines, i+1, fence)
		if end < 0 {
			continue
		}
		minIndent := indent
		for j := i; j <= end; j++ {
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			if lead := leadingIndentWidth(lines[j]); lead < minIndent {
				minIndent = lead
			}
		}
		if minIndent <= 0 {
			continue
		}
		for j := i; j <= end; j++ {
			lines[j] = trimLeadingIndent(lines[j], minIndent)
		}
		i = end
	}
	return strings.Join(lines, "\n")
}

func parseFenceLine(line string) (indent int, fence string, ok bool) {
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	indent = len(line) - len(trimmed)
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return indent, "```", true
	case strings.HasPrefix(trimmed, "~~~"):
		return indent, "~~~", true
	default:
		return 0, "", false
	}
}

func findClosingFenceLine(lines []string, start int, fence string) int {
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimLeftFunc(lines[i], unicode.IsSpace)
		if strings.HasPrefix(trimmed, fence) {
			return i
		}
	}
	return -1
}

func leadingIndentWidth(line string) int {
	count := 0
	for _, r := range line {
		if r != ' ' && r != '\t' {
			break
		}
		count++
	}
	return count
}

func trimLeadingIndent(line string, width int) string {
	if width <= 0 || line == "" {
		return line
	}
	i := 0
	for i < len(line) && width > 0 {
		if line[i] != ' ' && line[i] != '\t' {
			break
		}
		i++
		width--
	}
	return line[i:]
}

// ---------------------------------------------------------------------------
// Streaming-safe glamour rendering
// ---------------------------------------------------------------------------

// glamourStreamingNarrativeRows renders an active narrative block with a
// stable-prefix/full-Glamour plus unstable-tail/lightweight split. The tail
// deliberately avoids Chroma because incomplete code and markdown can be
// reclassified while tokens are still arriving.
func glamourStreamingNarrativeRows(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme) []RenderedRow {
	rows, _, _ := glamourStreamingNarrativeRowsObserved(blockID, raw, rolePrefix, roleStyle, width, theme, nil)
	return rows
}

func glamourStreamingNarrativeRowsObserved(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme, observeGlamour func()) ([]RenderedRow, int, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, 0, false
	}
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	stableRaw, tailRaw := splitStableStreamingMarkdown(raw)
	prefixWidth := maxInt(graphemeWidth(rolePrefix), 0)
	glamourWidth := maxInt(1, width-prefixWidth)
	if strings.TrimSpace(stableRaw) == "" {
		return renderStreamingNarrativeTailRows(blockID, raw, rolePrefix, roleStyle, glamourWidth, theme), 0, false
	}
	prefixRows, glamourCalls, cacheHit := cachedStreamingNarrativePrefixRows(blockID, stableRaw, rolePrefix, roleStyle, width, theme, observeGlamour)
	if len(prefixRows) == 0 {
		return renderStreamingNarrativeTailRows(blockID, raw, rolePrefix, roleStyle, glamourWidth, theme), glamourCalls, cacheHit
	}
	tailRows := renderStreamingNarrativeTailRows(blockID, tailRaw, "", roleStyle, glamourWidth, theme)
	if len(tailRows) == 0 {
		return prefixRows, glamourCalls, cacheHit
	}
	separatorRows := 0
	if hasStreamingParagraphBoundary(stableRaw) {
		separatorRows = 1
	}
	rows := make([]RenderedRow, 0, len(prefixRows)+separatorRows+len(tailRows))
	rows = append(rows, prefixRows...)
	if separatorRows > 0 {
		separator := strings.Repeat(" ", glamourWidth)
		rows = append(rows, RenderedRow{Styled: separator, Plain: separator, BlockID: blockID, PreWrapped: true})
	}
	rows = append(rows, tailRows...)
	return rows, glamourCalls, cacheHit
}

const streamingStableTailMinRunes = 96
const streamingNarrativeRendererVersion = "stream-md-v2"

func renderStreamingNarrativeTailRows(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme) []RenderedRow {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	streamLines := buildStreamingNarrativeTailLines(raw)
	if len(streamLines) == 0 {
		if strings.TrimSpace(raw) != "" {
			return []RenderedRow{{BlockID: blockID, PreWrapped: true}}
		}
		return nil
	}
	if width <= 0 {
		width = 1
	}
	baseStyle := narrativeBodyStyle(roleStyle, theme)
	styledRolePrefix := ""
	if rolePrefix != "" {
		styledRolePrefix = tuikit.ColorizeLogLine(rolePrefix, roleStyle, theme)
	}
	rows := make([]RenderedRow, 0, len(streamLines)+4)
	for idx, line := range streamLines {
		lineStyle := streamingNarrativeTailLineStyle(line.Kind, baseStyle, roleStyle, theme)
		styledSegments, plainSegments := renderStreamingNarrativeTailSegments(line.Raw, lineStyle, line.Kind, theme, width)
		for segIdx, styled := range styledSegments {
			plain := plainSegments[segIdx]
			if idx == 0 && rolePrefix != "" && segIdx == 0 {
				plain = rolePrefix + plain
				styled = styledRolePrefix + styled
			}
			rows = append(rows, RenderedRow{
				Styled:     styled,
				Plain:      plain,
				BlockID:    blockID,
				PreWrapped: true,
			})
		}
	}
	return rows
}

type streamingNarrativeTailLine struct {
	Kind  NarrativeBlockKind
	Raw   string
	Plain string
}

func buildStreamingNarrativeTailLines(raw string) []streamingNarrativeTailLine {
	nls, _ := buildNarrativeRows(raw)
	if len(nls) == 0 {
		return nil
	}
	lines := make([]streamingNarrativeTailLine, 0, len(nls))
	for _, nl := range nls {
		line, ok := streamingNarrativeTailLineTexts(nl)
		if !ok {
			continue
		}
		lines = append(lines, line)
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0].Plain) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1].Plain) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func streamingNarrativeTailLineTexts(line NarrativeLine) (streamingNarrativeTailLine, bool) {
	switch line.Kind {
	case NarrativeCodeFenceDelim:
		return streamingNarrativeTailLine{}, false
	case NarrativeCodeFence:
		raw := strings.TrimRight(line.Text, " \t")
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: raw, Plain: raw}, true
	case NarrativeHeading:
		raw := stripHeadingMarker(line.Text)
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: raw, Plain: simplifyInlineMarkers(raw)}, true
	case NarrativeTableRow:
		plain := formatTablePlainRow(line.Text)
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: plain, Plain: plain}, true
	case NarrativeTableRule:
		plain := formatTableRuleRow(line.Text)
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: plain, Plain: plain}, true
	case NarrativeListItem, NarrativeBlockquote:
		raw := strings.TrimRight(line.Text, " \t")
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: raw, Plain: simplifyInlineMarkers(raw)}, true
	default:
		raw := strings.TrimRight(line.Text, " \t")
		return streamingNarrativeTailLine{Kind: line.Kind, Raw: raw, Plain: simplifyInlineMarkers(raw)}, true
	}
}

func wrapStyledStreamingTailLine(styled string, width int) ([]string, []string) {
	if styled == "" {
		return []string{""}, []string{""}
	}
	if width <= 0 {
		width = 1
	}
	plain := strings.TrimRight(xansi.Strip(styled), " ")
	if strings.TrimRight(plain, " ") == "" || graphemeWidth(plain) <= width {
		return []string{styled}, []string{plain}
	}
	wrapped := xansi.Wrap(styled, width, " ")
	styledSegments := strings.Split(wrapped, "\n")
	if len(styledSegments) == 0 {
		return []string{styled}, []string{plain}
	}
	return styledSegments, deriveViewportPlainLines(nil, styledSegments)
}

func streamingNarrativeTailLineStyle(kind NarrativeBlockKind, base lipgloss.Style, roleStyle tuikit.LineStyle, theme tuikit.Theme) lipgloss.Style {
	switch kind {
	case NarrativeCodeFence:
		style := theme.MarkdownCodeBlockStyle()
		if roleStyle == tuikit.LineStyleReasoning {
			style = style.Foreground(theme.ReasoningFg)
		}
		return style
	case NarrativeHeading:
		if roleStyle == tuikit.LineStyleReasoning {
			return theme.ReasoningStyle().Bold(true)
		}
		return theme.MarkdownHeadingStyle().Bold(true)
	case NarrativeBlockquote:
		if roleStyle == tuikit.LineStyleReasoning {
			return theme.ReasoningStyle()
		}
		return theme.MarkdownQuoteStyle()
	default:
		return base
	}
}

func renderStreamingNarrativeTailSegment(segment string, style lipgloss.Style, kind NarrativeBlockKind, theme tuikit.Theme) string {
	if segment == "" {
		return ""
	}
	if kind == NarrativeCodeFence || kind == NarrativeTableRule {
		return style.Render(segment)
	}
	return renderInlineMarkdown(segment, style, theme)
}

func renderStreamingNarrativeTailSegments(segment string, style lipgloss.Style, kind NarrativeBlockKind, theme tuikit.Theme, width int) ([]string, []string) {
	if segment == "" {
		return []string{""}, []string{""}
	}
	if kind == NarrativeCodeFence || kind == NarrativeTableRule {
		return wrapStyledStreamingTailLine(style.Render(segment), width)
	}
	return renderInlineMarkdownWrappedSegments(segment, style, theme, width)
}

func cachedStreamingNarrativePrefixRows(blockID, stableRaw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme, observeGlamour func()) ([]RenderedRow, int, bool) {
	if blockID == "" || strings.TrimSpace(stableRaw) == "" {
		return nil, 0, false
	}
	glamourStreamingCache.Lock()
	defer glamourStreamingCache.Unlock()
	if glamourStreamingCache.entries == nil {
		glamourStreamingCache.entries = map[string]streamingNarrativeCacheEntry{}
	}
	if entry, ok := glamourStreamingCache.entries[blockID]; ok {
		themeKey := themeRenderCacheKey(theme)
		if entry.width == width && entry.themeKey == themeKey && entry.role == roleStyle && entry.rendererVersion == streamingNarrativeRendererVersion && entry.stableRaw == stableRaw && entry.rolePrefix == rolePrefix {
			return cloneRenderedRows(entry.renderedRows), 0, true
		}
	}
	if observeGlamour != nil {
		observeGlamour()
	}
	rows := glamourNarrativeRows(blockID, stableRaw, rolePrefix, roleStyle, width, theme)
	if len(rows) == 0 {
		return nil, 1, false
	}
	glamourStreamingCache.entries[blockID] = streamingNarrativeCacheEntry{
		width:           width,
		themeKey:        themeRenderCacheKey(theme),
		role:            roleStyle,
		rendererVersion: streamingNarrativeRendererVersion,
		stableRaw:       stableRaw,
		rolePrefix:      rolePrefix,
		renderedRows:    cloneRenderedRows(rows),
	}
	return rows, 1, false
}

func cloneRenderedRows(rows []RenderedRow) []RenderedRow {
	if len(rows) == 0 {
		return nil
	}
	out := make([]RenderedRow, len(rows))
	copy(out, rows)
	return out
}

func hasStreamingParagraphBoundary(raw string) bool {
	raw = strings.TrimRight(raw, " \t\r")
	newlines := 0
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] != '\n' {
			break
		}
		newlines++
	}
	return newlines >= 2
}

func splitStableStreamingMarkdown(raw string) (stableRaw string, tailRaw string) {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	if strings.TrimSpace(raw) == "" {
		return "", ""
	}
	if utf8.RuneCountInString(raw) < streamingStableTailMinRunes*2 {
		return "", raw
	}
	lines := strings.SplitAfter(raw, "\n")
	if len(lines) < 3 {
		return "", raw
	}
	lastBoundary := 0
	offset := 0
	inFence := false
	fenceChar := byte(0)
	fenceLen := 0
	for idx, seg := range lines {
		line := strings.TrimSuffix(seg, "\n")
		trimmed := strings.TrimSpace(line)
		if len(trimmed) >= 3 {
			ch := trimmed[0]
			if ch == '`' || ch == '~' {
				count := 0
				for count < len(trimmed) && trimmed[count] == ch {
					count++
				}
				if count >= 3 {
					if !inFence {
						inFence = true
						fenceChar = ch
						fenceLen = count
					} else if ch == fenceChar && count >= fenceLen && strings.TrimSpace(trimmed[count:]) == "" {
						inFence = false
					}
				}
			}
		}
		offset += len(seg)
		if inFence || idx >= len(lines)-1 {
			continue
		}
		if strings.TrimSpace(line) != "" {
			continue
		}
		tailCandidate := strings.TrimSpace(raw[offset:])
		if utf8.RuneCountInString(tailCandidate) >= streamingStableTailMinRunes {
			lastBoundary = offset
		}
	}
	if lastBoundary <= 0 || lastBoundary >= len(raw) {
		return "", raw
	}
	stableRaw = raw[:lastBoundary]
	tailRaw = raw[lastBoundary:]
	if strings.TrimSpace(stableRaw) == "" || strings.TrimSpace(tailRaw) == "" {
		return "", raw
	}
	return stableRaw, tailRaw
}

// closeUnclosedCodeFences appends a closing fence marker when the input
// contains an odd number of fence delimiters (i.e. a code block that hasn't
// been closed yet). This prevents glamour from mis-rendering trailing content.
func closeUnclosedCodeFences(raw string) string {
	lines := strings.Split(raw, "\n")
	inFence := false
	fenceChar := byte(0)
	fenceLen := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 3 {
			continue
		}
		ch := trimmed[0]
		if ch != '`' && ch != '~' {
			continue
		}
		count := 0
		for j := 0; j < len(trimmed) && trimmed[j] == ch; j++ {
			count++
		}
		if count < 3 {
			continue
		}
		if !inFence {
			// Opening fence (may have info string after the markers).
			inFence = true
			fenceChar = ch
			fenceLen = count
			continue
		}
		// Potential closing fence: same char, at least as many markers, no
		// non-whitespace after the markers.
		if ch != fenceChar || count < fenceLen {
			continue
		}
		rest := strings.TrimSpace(trimmed[count:])
		if rest == "" {
			inFence = false
		}
	}

	if inFence {
		raw += "\n" + strings.Repeat(string(fenceChar), fenceLen)
	}
	return raw
}
