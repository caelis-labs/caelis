package tuiapp

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

type activeNarrativeBuffer struct {
	stablePrefixRaw string
	tailRaw         string
	version         uint64

	cachedVersion    uint64
	cachedWidth      int
	cachedThemeKey   string
	cachedRolePrefix string
	cachedRoleStyle  tuikit.LineStyle
	cachedRows       []RenderedRow
}

func (b *activeNarrativeBuffer) Append(text string) {
	if b == nil || text == "" {
		return
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	combinedTail := b.tailRaw + text
	stableRaw, tailRaw := splitStableStreamingMarkdown(combinedTail)
	if strings.TrimSpace(stableRaw) != "" {
		b.stablePrefixRaw += stableRaw
		b.tailRaw = tailRaw
	} else {
		b.tailRaw = combinedTail
	}
	b.version++
}

func (b *activeNarrativeBuffer) Text() string {
	if b == nil {
		return ""
	}
	return b.stablePrefixRaw + b.tailRaw
}

func (b *activeNarrativeBuffer) Empty() bool {
	return b == nil || b.Text() == ""
}

func (b *activeNarrativeBuffer) CacheKey() string {
	if b == nil {
		return "active:0"
	}
	return "active:" +
		strconv.FormatUint(b.version, 10) + ":" +
		strconv.Itoa(len(b.stablePrefixRaw)) + ":" +
		strconv.Itoa(len(b.tailRaw))
}

func (b *activeNarrativeBuffer) RenderRows(blockID, rolePrefix string, roleStyle tuikit.LineStyle, ctx BlockRenderContext) []RenderedRow {
	if b == nil || strings.TrimSpace(b.Text()) == "" {
		return nil
	}
	width := ctx.Width
	if width <= 0 {
		width = 1
	}
	themeKey := ctx.renderThemeKey()
	if b.cachedRows != nil &&
		b.cachedVersion == b.version &&
		b.cachedWidth == width &&
		b.cachedThemeKey == themeKey &&
		b.cachedRolePrefix == rolePrefix &&
		b.cachedRoleStyle == roleStyle {
		return b.cachedRows
	}

	ctx.observeGlamourRender()
	rows := b.renderRows(blockID, rolePrefix, roleStyle, width, ctx.Theme)
	b.cachedVersion = b.version
	b.cachedWidth = width
	b.cachedThemeKey = themeKey
	b.cachedRolePrefix = rolePrefix
	b.cachedRoleStyle = roleStyle
	b.cachedRows = rows
	return rows
}

func (b *activeNarrativeBuffer) renderRows(blockID, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme) []RenderedRow {
	raw := b.Text()
	if strings.TrimSpace(b.stablePrefixRaw) == "" {
		return renderActiveNarrativeTailRows(blockID, raw, rolePrefix, roleStyle, width, theme)
	}
	prefixRows := cachedStreamingNarrativePrefixRows(blockID, b.stablePrefixRaw, rolePrefix, roleStyle, width, theme)
	if len(prefixRows) == 0 {
		return glamourStreamingNarrativeRows(blockID, raw, rolePrefix, roleStyle, width, theme)
	}
	prefixWidth := maxInt(graphemeWidth(rolePrefix), 0)
	tailWidth := maxInt(1, width-prefixWidth)
	tailRows := renderStreamingNarrativeTailRows(blockID, b.tailRaw, "", roleStyle, tailWidth, theme)
	if len(tailRows) == 0 {
		return prefixRows
	}
	separatorRows := 0
	if hasStreamingParagraphBoundary(b.stablePrefixRaw) {
		separatorRows = 1
	}
	rows := make([]RenderedRow, 0, len(prefixRows)+separatorRows+len(tailRows))
	rows = append(rows, prefixRows...)
	if separatorRows > 0 {
		separator := strings.Repeat(" ", tailWidth)
		rows = append(rows, RenderedRow{Styled: separator, Plain: separator, BlockID: blockID, PreWrapped: true})
	}
	rows = append(rows, tailRows...)
	return rows
}

func renderActiveNarrativeTailRows(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, width int, theme tuikit.Theme) []RenderedRow {
	if utf8.RuneCountInString(strings.TrimSpace(raw)) >= streamingLightTailMinRunes {
		prefixWidth := maxInt(graphemeWidth(rolePrefix), 0)
		tailWidth := maxInt(1, width-prefixWidth)
		if rows := renderStreamingNarrativeTailRows(blockID, raw, rolePrefix, roleStyle, tailWidth, theme); len(rows) > 0 {
			return rows
		}
	}
	normalized := closeUnclosedCodeFences(raw)
	if rows := glamourNarrativeRows(blockID, normalized, rolePrefix, roleStyle, width, theme); len(rows) > 0 {
		return rows
	}
	prefixWidth := maxInt(graphemeWidth(rolePrefix), 0)
	return renderStreamingNarrativeTailRows(blockID, raw, rolePrefix, roleStyle, maxInt(1, width-prefixWidth), theme)
}

func (b *AssistantBlock) appendActiveDelta(text string) {
	if b == nil || text == "" {
		return
	}
	if b.activeBuffer == nil {
		b.activeBuffer = &activeNarrativeBuffer{}
		if b.Raw != "" {
			b.activeBuffer.Append(b.Raw)
			b.Raw = ""
		}
	}
	b.activeBuffer.Append(text)
}

func (b *AssistantBlock) finalizeActiveText(incoming string) string {
	if b == nil {
		return incoming
	}
	existing := b.Raw
	if b.activeBuffer != nil {
		existing = b.activeBuffer.Text()
	}
	b.activeBuffer = nil
	return mergeStreamChunk(existing, incoming, true)
}

func (b *ReasoningBlock) appendActiveDelta(text string) {
	if b == nil || text == "" {
		return
	}
	if b.activeBuffer == nil {
		b.activeBuffer = &activeNarrativeBuffer{}
		if b.Raw != "" {
			b.activeBuffer.Append(b.Raw)
			b.Raw = ""
		}
	}
	b.activeBuffer.Append(text)
}

func (b *ReasoningBlock) finalizeActiveText(incoming string) string {
	if b == nil {
		return incoming
	}
	existing := b.Raw
	if b.activeBuffer != nil {
		existing = b.activeBuffer.Text()
	}
	b.activeBuffer = nil
	return mergeStreamChunk(existing, incoming, true)
}
