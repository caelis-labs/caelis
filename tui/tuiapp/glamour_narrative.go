package tuiapp

import (
	"fmt"
	"image/color"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/glamour"
	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
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
// Glamour renderer cache (width-keyed singleton)
// ---------------------------------------------------------------------------

var glamourCache struct {
	sync.Mutex
	renderer *glamour.TermRenderer
	width    int
	dark     bool
	role     tuikit.LineStyle
}

type streamingNarrativeCacheEntry struct {
	width        int
	dark         bool
	role         tuikit.LineStyle
	stableRaw    string
	rolePrefix   string
	renderedRows []RenderedRow
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
	glamourCache.renderer = nil
	glamourCache.Unlock()
	glamourStreamingCache.Lock()
	glamourStreamingCache.entries = nil
	glamourStreamingCache.Unlock()
}

func getGlamourRenderer(width int, theme tuikit.Theme, roleStyle tuikit.LineStyle) *glamour.TermRenderer {
	glamourCache.Lock()
	defer glamourCache.Unlock()

	if glamourCache.renderer != nil && glamourCache.width == width && glamourCache.dark == theme.IsDark && glamourCache.role == roleStyle {
		return glamourCache.renderer
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(narrativeStyleConfig(theme, roleStyle)),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}

	glamourCache.renderer = renderer
	glamourCache.width = width
	glamourCache.dark = theme.IsDark
	glamourCache.role = roleStyle
	return renderer
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
	style.Document.Color = colorToAnsiPtr(theme.TextPrimary)

	// ---------------------------------------------------------------
	// Headings — crush-style: H1 gets background pill, H2+ keep
	// markdown prefix for scannability.
	// ---------------------------------------------------------------
	accentHex := colorToAnsiPtr(theme.Accent)

	style.Heading.BlockSuffix = "\n"
	style.Heading.Color = accentHex
	style.Heading.Bold = boolPtr(true)

	style.H1.Prefix = " "
	style.H1.Suffix = " "
	style.H1.Color = colorToAnsiPtr(theme.TextPrimary)
	style.H1.BackgroundColor = colorToAnsiPtr(theme.CodeBlockBg)
	style.H1.Bold = boolPtr(true)
	style.H1.Underline = boolPtr(false)

	style.H2.Prefix = ""
	style.H2.Color = accentHex
	style.H2.Bold = boolPtr(true)

	style.H3.Prefix = ""
	style.H3.Color = accentHex
	style.H3.Bold = boolPtr(true)

	style.H4.Prefix = ""
	style.H4.Color = accentHex
	style.H5.Prefix = ""
	style.H5.Color = accentHex
	style.H6.Prefix = ""
	style.H6.Color = colorToAnsiPtr(theme.MutedText)

	// ---------------------------------------------------------------
	// Lists — bullet marker "• " for unordered, ". " for ordered
	// ---------------------------------------------------------------
	style.Item.BlockPrefix = "• "
	style.Enumeration.BlockPrefix = ". "
	style.List.LevelIndent = 2

	// ---------------------------------------------------------------
	// Strong / Emphasis / Strikethrough
	// ---------------------------------------------------------------
	style.Strong.Bold = boolPtr(true)
	style.Emph.Italic = boolPtr(true)
	style.Strikethrough.CrossedOut = boolPtr(true)

	// ---------------------------------------------------------------
	// Inline code — background highlight with padding (crush style)
	// ---------------------------------------------------------------
	style.Code.Prefix = " "
	style.Code.Suffix = " "
	style.Code.Color = colorToAnsiPtr(theme.CodeFg)
	style.Code.BackgroundColor = colorToAnsiPtr(theme.CodeBg)

	// ---------------------------------------------------------------
	// Code blocks — Chroma syntax highlighting
	// ---------------------------------------------------------------
	cbIndent := uint(0)
	cbMargin := uint(0)
	style.CodeBlock.Margin = &cbMargin
	style.CodeBlock.Indent = &cbIndent
	style.CodeBlock.Color = colorToAnsiPtr(theme.CodeBlockFg)
	style.CodeBlock.BackgroundColor = colorToAnsiPtr(theme.CodeBlockBg)
	if style.CodeBlock.Chroma == nil {
		style.CodeBlock.Chroma = &gansi.Chroma{}
	}
	style.CodeBlock.Chroma.Text.Color = colorToAnsiPtr(theme.CodeBlockFg)
	style.CodeBlock.Chroma.Background.BackgroundColor = colorToAnsiPtr(theme.CodeBlockBg)
	style.CodeBlock.Chroma.Background.Color = colorToAnsiPtr(theme.CodeBlockFg)
	style.CodeBlock.Chroma.Comment.Color = colorToAnsiPtr(theme.MutedText)
	style.CodeBlock.Chroma.Keyword.Color = accentHex
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

	// ---------------------------------------------------------------
	// Links — colored + underline, link text bold
	// ---------------------------------------------------------------
	style.Link.Color = colorToAnsiPtr(theme.LinkFg)
	style.Link.Underline = boolPtr(true)
	style.LinkText.Bold = boolPtr(true)

	// ---------------------------------------------------------------
	// Horizontal rule
	// ---------------------------------------------------------------
	style.HorizontalRule.Color = colorToAnsiPtr(theme.MutedText)
	style.HorizontalRule.Format = "\n────────\n"

	// ---------------------------------------------------------------
	// Task list
	// ---------------------------------------------------------------
	style.Task.Ticked = "[✓] "
	style.Task.Unticked = "[ ] "

	if roleStyle == tuikit.LineStyleReasoning {
		reasoningHex := colorToAnsiPtr(theme.ReasoningFg)
		mutedHex := colorToAnsiPtr(theme.MutedText)
		style.Document.Color = reasoningHex
		style.Heading.Color = reasoningHex
		style.H1.Color = reasoningHex
		style.H2.Color = reasoningHex
		style.H3.Color = reasoningHex
		style.H4.Color = mutedHex
		style.H5.Color = mutedHex
		style.H6.Color = mutedHex
		style.Item.Color = reasoningHex
		style.Enumeration.Color = reasoningHex
		style.BlockQuote.Color = reasoningHex
		style.Link.Color = reasoningHex
		style.LinkText.Color = reasoningHex
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

	return style
}

// colorToAnsiPtr converts an image/color.Color to a hex "#rrggbb" pointer
// suitable for glamour's StyleConfig fields. Returns nil for nil input.
func colorToAnsiPtr(c color.Color) *string {
	if c == nil {
		return nil
	}
	r, g, b, _ := c.RGBA()
	s := fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	return &s
}

func boolPtr(v bool) *bool { return &v }

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
		themeColorCacheKey(theme.Accent),
		themeColorCacheKey(theme.LinkFg),
		themeColorCacheKey(theme.CodeFg),
		themeColorCacheKey(theme.CodeBg),
		themeColorCacheKey(theme.CodeBlockFg),
		themeColorCacheKey(theme.CodeBlockBg),
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

// glamourStreamingNarrativeRows renders an active (still-streaming) narrative
// block through glamour. It normalises unclosed markdown constructs (code
// fences, HTML-style tags) so that glamour produces stable output.
// Returns nil when glamour cannot produce usable output, letting the caller
// fall back to the inline renderer.
func glamourStreamingNarrativeRows(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme) []RenderedRow {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	stableRaw, tailRaw := splitStableStreamingMarkdown(raw)
	prefixWidth := maxInt(graphemeWidth(rolePrefix), 0)
	glamourWidth := maxInt(1, width-prefixWidth)
	if strings.TrimSpace(stableRaw) == "" {
		if utf8.RuneCountInString(strings.TrimSpace(raw)) >= streamingLightTailMinRunes {
			if rows := renderStreamingNarrativeTailRows(blockID, raw, rolePrefix, roleStyle, glamourWidth, theme); len(rows) > 0 {
				return rows
			}
		}
		normalized := closeUnclosedCodeFences(raw)
		return glamourNarrativeRows(blockID, normalized, rolePrefix, roleStyle, width, theme)
	}
	prefixRows := cachedStreamingNarrativePrefixRows(blockID, stableRaw, rolePrefix, roleStyle, width, theme)
	if len(prefixRows) == 0 {
		normalized := closeUnclosedCodeFences(raw)
		return glamourNarrativeRows(blockID, normalized, rolePrefix, roleStyle, width, theme)
	}
	tailRows := renderStreamingNarrativeTailRows(blockID, tailRaw, "", roleStyle, glamourWidth, theme)
	if len(tailRows) == 0 {
		return prefixRows
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
	return rows
}

const streamingStableTailMinRunes = 96
const streamingLightTailMinRunes = 160

func renderStreamingNarrativeTailRows(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme) []RenderedRow {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	_, plainRows := buildNarrativeRows(raw)
	if len(plainRows) == 0 {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		plainRows = []string{strings.TrimRight(raw, "\n")}
	}
	if width <= 0 {
		width = 1
	}
	baseStyle := narrativeBodyStyle(roleStyle, theme)
	styledRolePrefix := ""
	if rolePrefix != "" {
		styledRolePrefix = tuikit.ColorizeLogLine(rolePrefix, roleStyle, theme)
	}
	rows := make([]RenderedRow, 0, len(plainRows)+4)
	for idx, plainRow := range plainRows {
		fullPlain := plainRow
		prefixPlain := ""
		prefixStyled := ""
		if idx == 0 && rolePrefix != "" {
			prefixPlain = rolePrefix
			prefixStyled = styledRolePrefix
			fullPlain = rolePrefix + plainRow
		}
		segments := graphemeWordWrap(fullPlain, width)
		if len(segments) == 0 {
			segments = []string{fullPlain}
		}
		for segIdx, segment := range normalizeWrappedPlainSegments(segments) {
			styled := renderInlineMarkdown(segment, baseStyle, theme)
			if prefixPlain != "" && segIdx == 0 && strings.HasPrefix(segment, prefixPlain) {
				body := strings.TrimPrefix(segment, prefixPlain)
				styled = prefixStyled + renderInlineMarkdown(body, baseStyle, theme)
			}
			rows = append(rows, RenderedRow{
				Styled:     styled,
				Plain:      segment,
				BlockID:    blockID,
				PreWrapped: true,
			})
		}
	}
	return rows
}

func cachedStreamingNarrativePrefixRows(blockID, stableRaw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme) []RenderedRow {
	if blockID == "" || strings.TrimSpace(stableRaw) == "" {
		return nil
	}
	glamourStreamingCache.Lock()
	defer glamourStreamingCache.Unlock()
	if glamourStreamingCache.entries == nil {
		glamourStreamingCache.entries = map[string]streamingNarrativeCacheEntry{}
	}
	if entry, ok := glamourStreamingCache.entries[blockID]; ok {
		if entry.width == width && entry.dark == theme.IsDark && entry.role == roleStyle && entry.stableRaw == stableRaw && entry.rolePrefix == rolePrefix {
			return cloneRenderedRows(entry.renderedRows)
		}
	}
	rows := glamourNarrativeRows(blockID, stableRaw, rolePrefix, roleStyle, width, theme)
	if len(rows) == 0 {
		return nil
	}
	glamourStreamingCache.entries[blockID] = streamingNarrativeCacheEntry{
		width:        width,
		dark:         theme.IsDark,
		role:         roleStyle,
		stableRaw:    stableRaw,
		rolePrefix:   rolePrefix,
		renderedRows: cloneRenderedRows(rows),
	}
	return rows
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
