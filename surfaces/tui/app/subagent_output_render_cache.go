package tuiapp

import "strings"

type subagentOutputRenderEntry struct {
	viewportRowCache
	key  string
	rows []RenderedRow
}

func (m *Model) renderSubagentOutputDocument(view *subagentOutputView, ctx BlockRenderContext, previous []subagentOutputRenderEntry) ([]RenderedRow, []string, []subagentOutputRenderEntry) {
	old := make(map[string]subagentOutputRenderEntry, len(previous))
	for _, entry := range previous {
		old[entry.blockID] = entry
	}
	entries := make([]subagentOutputRenderEntry, 0, view.document.Len())
	var rows []RenderedRow
	var fixed []string
	for _, block := range view.document.Blocks() {
		entry := old[block.BlockID()]
		key := viewportBlockRenderKey(block, ctx)
		if entry.key != key {
			entry = subagentOutputRenderEntry{
				key:              key,
				viewportRowCache: m.renderViewportRowCache(block, ctx, entry.viewportRowCache),
			}
			entry.rows = subagentOutputWrappedRows(entry.blockID, entry.wrappedViewportRows)
			entry.fixedWidthLines(ctx.Width)
		}
		if len(entry.rows) > 0 && len(rows) > 0 && !isACPTranscriptGapRow(rows[len(rows)-1]) {
			rows = append(rows, PlainRow(entry.blockID, ""))
			fixed = append(fixed, normalizeFullscreenFrameLine("", ctx.Width))
		}
		rows = append(rows, entry.rows...)
		fixed = append(fixed, entry.fixedLines...)
		entries = append(entries, entry)
	}
	return rows, fixed, entries
}

func subagentOutputWrappedRows(blockID string, wrapped wrappedViewportRows) []RenderedRow {
	rows := make([]RenderedRow, len(wrapped.styledLines))
	for index, styled := range wrapped.styledLines {
		row := RenderedRow{
			Styled: styled, Plain: wrapped.plainLines[index], BlockID: blockID,
			ClickToken: wrapped.clickTokens[index], PreWrapped: true,
			selectionIndent: wrapped.selectionIndents[index],
			activeTail:      strings.Contains(styled, wideCellRendererSentinel()),
		}
		if bound := wrapped.clickBounds[index]; bound.valid() {
			row.ClickStartCol, row.ClickEndCol = bound.start, bound.end
		}
		rows[index] = row
	}
	return rows
}
