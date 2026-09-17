package tuiapp

import "slices"

// viewportRowCache owns incremental wrapping for both the main transcript and
// detached overlays. Whole source rows, including interaction metadata, define
// reuse; offsets preserve boundaries when one source row spans several lines.
type viewportRowCache struct {
	wrappedViewportRows
	blockID    string
	contextKey string
	height     int
	sourceRows []RenderedRow
	// fixedLines is populated only by overlays that need width-normalized rows.
	fixedLines []string
}

func (m *Model) renderViewportRowCache(block Block, ctx BlockRenderContext, previous viewportRowCache) viewportRowCache {
	m.observeBlockRender(block.Kind())
	return m.renderViewportRowCacheFromRows(block, ctx, previous, block.Render(ctx))
}

func (m *Model) renderViewportRowCacheFromRows(block Block, ctx BlockRenderContext, previous viewportRowCache, source []RenderedRow) viewportRowCache {
	contextKey := viewportRenderContextKey(ctx)
	if previous.blockID != block.BlockID() || previous.contextKey != contextKey || previous.height != ctx.Height {
		previous = viewportRowCache{}
	}
	prefix := 0
	for prefix < len(source) && prefix < len(previous.sourceRows) && source[prefix] == previous.sourceRows[prefix] {
		prefix++
	}
	if prefix == len(source) && prefix == len(previous.sourceRows) && previous.contextKey != "" {
		return previous
	}
	suffix := 0
	for suffix < len(source)-prefix && suffix < len(previous.sourceRows)-prefix &&
		source[len(source)-1-suffix] == previous.sourceRows[len(previous.sourceRows)-1-suffix] {
		suffix++
	}
	start, end := 0, len(previous.styledLines)
	if prefix > 0 {
		start = previous.sourceOffsets[prefix]
	}
	if suffix > 0 {
		end = previous.sourceOffsets[len(previous.sourceRows)-suffix]
	}
	wrapped := m.wrapRenderedRowsForViewport(block, source[prefix:len(source)-suffix], ctx.Width, ctx)
	changedLines := len(wrapped.styledLines)
	offsets := make([]int, 0, len(source)+1)
	offsets = append(offsets, previous.sourceOffsets[:prefix]...)
	for _, offset := range wrapped.sourceOffsets {
		offsets = append(offsets, start+offset)
	}
	delta := start + changedLines - end
	for index := len(previous.sourceRows) - suffix + 1; index < len(previous.sourceOffsets); index++ {
		offsets = append(offsets, previous.sourceOffsets[index]+delta)
	}
	wrapped.sourceOffsets = offsets
	if len(previous.styledLines) > 0 {
		wrapped.styledLines = spliceStrings(previous.styledLines, start, end-start, wrapped.styledLines)
		wrapped.plainLines = spliceStrings(previous.plainLines, start, end-start, wrapped.plainLines)
		wrapped.selectionIndents = spliceInts(previous.selectionIndents, start, end-start, wrapped.selectionIndents)
		wrapped.clickTokens = spliceStrings(previous.clickTokens, start, end-start, wrapped.clickTokens)
		wrapped.altClickTokens = spliceStrings(previous.altClickTokens, start, end-start, wrapped.altClickTokens)
		wrapped.clickBounds = spliceClickColumnRanges(previous.clickBounds, start, end-start, wrapped.clickBounds)
	}
	var fixed []string
	if previous.fixedLines != nil {
		fixed = make([]string, len(wrapped.styledLines))
		copy(fixed, previous.fixedLines[:start])
		copy(fixed[start+changedLines:], previous.fixedLines[end:])
	}
	return viewportRowCache{
		wrappedViewportRows: wrapped, blockID: block.BlockID(), contextKey: contextKey, height: ctx.Height,
		sourceRows: slices.Clone(source), fixedLines: fixed,
	}
}

func (c *viewportRowCache) fixedWidthLines(width int) []string {
	if c.fixedLines == nil {
		c.fixedLines = make([]string, len(c.styledLines))
	}
	for index, line := range c.styledLines {
		if c.fixedLines[index] == "" {
			c.fixedLines[index] = normalizeFullscreenFrameLine(line, width)
		}
	}
	return c.fixedLines
}
