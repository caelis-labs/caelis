package tuiapp

import (
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
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

func (b *activeNarrativeBuffer) SetText(text string) {
	if b == nil {
		return
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	stableRaw, tailRaw := splitStableStreamingMarkdown(text)
	if b.stablePrefixRaw == stableRaw && b.tailRaw == tailRaw {
		return
	}
	b.stablePrefixRaw = stableRaw
	b.tailRaw = tailRaw
	b.version++
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
		streamingNarrativeRendererVersion + ":" +
		strconv.FormatUint(b.version, 10) + ":" +
		strconv.Itoa(len(b.stablePrefixRaw)) + ":" +
		strconv.Itoa(len(b.tailRaw))
}

func (b *activeNarrativeBuffer) RenderRows(blockID, rolePrefix string, roleStyle tuikit.LineStyle, ctx BlockRenderContext) []RenderedRow {
	return b.RenderRowsAtWidth(blockID, rolePrefix, roleStyle, ctx.Width, ctx)
}

func (b *activeNarrativeBuffer) RenderRowsAtWidth(blockID, rolePrefix string, roleStyle tuikit.LineStyle, width int, ctx BlockRenderContext) []RenderedRow {
	if b == nil || strings.TrimSpace(b.Text()) == "" {
		return nil
	}
	if width <= 0 {
		width = ctx.Width
		if width <= 0 {
			width = 1
		}
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

	rows := b.renderRows(blockID, rolePrefix, roleStyle, width, ctx)
	b.cachedVersion = b.version
	b.cachedWidth = width
	b.cachedThemeKey = themeKey
	b.cachedRolePrefix = rolePrefix
	b.cachedRoleStyle = roleStyle
	b.cachedRows = rows
	return rows
}

func (b *activeNarrativeBuffer) renderRows(blockID, rolePrefix string, roleStyle tuikit.LineStyle, width int, ctx BlockRenderContext) []RenderedRow {
	raw := b.Text()
	kind := TextAssistant
	policy := MarkdownStableTail
	if roleStyle == tuikit.LineStyleReasoning {
		kind = TextReasoning
		policy = MarkdownNone
	}
	return RenderTextWithContext(ctx, TextRenderRequest{
		Kind:            kind,
		Mode:            RenderStream,
		MarkdownPolicy:  policy,
		Raw:             raw,
		Prefix:          rolePrefix,
		Width:           width,
		BlockID:         blockID,
		LineStyle:       roleStyle,
		StablePrefixRaw: b.stablePrefixRaw,
		TailRaw:         b.tailRaw,
	}).Rows
}

func renderActiveNarrativeTextRows(blockID, raw, rolePrefix string, roleStyle tuikit.LineStyle, ctx BlockRenderContext) []RenderedRow {
	buffer := &activeNarrativeBuffer{}
	buffer.SetText(raw)
	return buffer.RenderRows(blockID, rolePrefix, roleStyle, ctx)
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
