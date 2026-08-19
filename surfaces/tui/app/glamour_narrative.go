package tuiapp

import (
	"fmt"
	"image/color"
	"strings"
	"sync"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// ---------------------------------------------------------------------------
// Glamour-based narrative rendering
// ---------------------------------------------------------------------------

const narrativeBlockquoteRail = "│ "

// glamourRenderNarrative renders markdown text through Glamour for styled
// terminal output. Returns the rendered multi-line string. On any error
// the caller should fall back to the last compatible rendered prefix plus raw
// source text for the remaining suffix.
func glamourRenderNarrative(raw string, width int, theme tuikit.Theme, roleStyle tuikit.LineStyle) string {
	raw = normalizeTextRenderRaw(raw)
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	rendered, err := renderGlamourMarkdown(raw, width, theme, roleStyle)
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
	for len(styledLines) > 0 && strings.TrimSpace(tuikit.SanitizeLogText(styledLines[0])) == "" {
		styledLines = styledLines[1:]
	}
	for len(styledLines) > 0 && strings.TrimSpace(tuikit.SanitizeLogText(styledLines[len(styledLines)-1])) == "" {
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

	firstOutputLine := true
	for _, sl := range styledLines {
		for _, segment := range wrapGlamourNarrativeBodyLine(sl, wrapWidth) {
			// Glamour pads several block styles to the configured terminal width.
			// Keep that visual padding in Styled, but exclude it from the copy and
			// mouse-hit plane just like the other styled-row constructors do.
			plain := strings.TrimRight(tuikit.SanitizeLogText(segment), " ")
			styled := segment
			if firstOutputLine && rolePrefix != "" {
				plain = rolePrefix + plain
				styled = styledRolePrefix + styled
			}
			firstOutputLine = false
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

func wrapGlamourNarrativeBodyLine(styled string, width int) []string {
	if width <= 0 || styled == "" {
		return []string{styled}
	}
	if graphemeWidth(tuikit.SanitizeLogText(styled)) <= width {
		return []string{styled}
	}
	segments := strings.Split(hardWrapDisplayLine(styled, width), "\n")
	if len(segments) == 0 {
		return []string{styled}
	}
	return segments
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
	order   []string
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
	glamourStreamingCache.order = nil
	glamourStreamingCache.Unlock()
}

func getGlamourRenderer(width int, theme tuikit.Theme, roleStyle tuikit.LineStyle) *glamour.TermRenderer {
	glamourCache.Lock()
	defer glamourCache.Unlock()
	return getGlamourRendererLocked(width, theme, roleStyle)
}

func getGlamourRendererLocked(width int, theme tuikit.Theme, roleStyle tuikit.LineStyle) *glamour.TermRenderer {
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

func renderGlamourMarkdown(raw string, width int, theme tuikit.Theme, roleStyle tuikit.LineStyle) (string, error) {
	glamourCache.Lock()
	defer glamourCache.Unlock()
	renderer := getGlamourRendererLocked(width, theme, roleStyle)
	if renderer == nil {
		return "", nil
	}
	return renderer.Render(raw)
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
	var style gansi.StyleConfig

	// No document-level margin; our layout handles outer spacing.
	zero := uint(0)
	style.Document.Margin = &zero
	bodyHex := styleForegroundToAnsiPtr(theme.TextStyle())
	linkHex := styleForegroundToAnsiPtr(theme.MarkdownLinkStyle())
	style.Document.Color = bodyHex
	style.Text.Color = bodyHex
	style.Paragraph.Color = bodyHex

	// ---------------------------------------------------------------
	// Headings — light prose style: no decorative background and no
	// markdown prefix once the markdown has been parsed.
	// ---------------------------------------------------------------
	headingHex := styleForegroundToAnsiPtr(theme.MarkdownHeadingStyle())

	style.Heading.BlockSuffix = "\n"
	style.Heading.Color = headingHex
	style.Heading.Bold = boolPtr(true)

	style.H1.Prefix = ""
	style.H1.Suffix = ""
	style.H1.Color = headingHex
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
	// Inline code — foreground-only emphasis keeps short tool names readable
	// without turning prose into visual blocks.
	// ---------------------------------------------------------------
	style.Code.Prefix = ""
	style.Code.Suffix = ""
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
	style.CodeBlock.Theme = syntaxCodeBlockTheme(theme)
	style.CodeBlock.Chroma = nil

	// ---------------------------------------------------------------
	// Blockquotes
	// ---------------------------------------------------------------
	bqIndent := uint(1)
	bqToken := narrativeBlockquoteRail
	style.BlockQuote.Indent = &bqIndent
	style.BlockQuote.IndentToken = &bqToken
	style.BlockQuote.Color = bodyHex

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
		style.Strong.Color = reasoningHex
		style.Emph.Color = reasoningHex
		style.Strikethrough.Color = reasoningHex
		style.BlockQuote.Color = reasoningHex
		style.Link.Color = reasoningHex
		style.LinkText.Color = reasoningHex
		style.Table.Color = reasoningHex
		style.Code.Color = reasoningHex
		style.CodeBlock.Color = reasoningHex
		style.HorizontalRule.Color = mutedHex
	}

	return style
}

func syntaxCodeBlockTheme(theme tuikit.Theme) string {
	if theme.NoColor {
		return ""
	}
	return tuikit.SyntaxPaletteForTheme(theme).ChromaTheme
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
		themeColorCacheKey(theme.UserBg),
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
