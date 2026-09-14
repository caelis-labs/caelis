package tuiapp

// Prepending preserves the live blocks and their identities. Existing viewport
// anchoring then relocates both the top row and selection after layout changes.
func (m *Model) prependSessionHistory(older *Model) {
	m.doc = prependHistoryDocument(older.doc, m.doc)
	m.mainAnchorBlockIDs = mergeHistoryMap(m.mainAnchorBlockIDs, older.mainAnchorBlockIDs)
	m.participantTurnIDs = mergeHistoryMap(m.participantTurnIDs, older.participantTurnIDs)
	m.subagentOutputViews = mergeHistoryMap(m.subagentOutputViews, older.subagentOutputViews)
	m.markViewportStructureDirty()
	m.syncViewportContent()
	m.reconcileSubagentOutputTaskStreams()
}

func prependHistoryDocument(older, current *Document) *Document {
	result := NewDocument()
	if older != nil {
		for _, block := range older.Blocks() {
			result.Append(block)
		}
	}
	if current != nil {
		for _, block := range current.Blocks() {
			result.Append(block)
		}
	}
	return result
}

func mergeHistoryMap[K comparable, V any](current, older map[K]V) map[K]V {
	if current == nil {
		current = make(map[K]V)
	}
	for key, value := range older {
		if _, exists := current[key]; !exists {
			current[key] = value
		}
	}
	return current
}

func (m *Model) prependChildHistory(view, older *subagentOutputView) {
	// Keep the mounted pane's existing top and selected rows. Width and style
	// remain unchanged during this single Update. Live output may have arrived
	// since the last paint, so total row-count differences are not an anchor.
	cache := view.renderCache
	pane := view.pane
	oldRows := cache.rows
	view.document = prependHistoryDocument(older.document, view.document)
	view.turnBlocks = mergeHistoryMap(view.turnBlocks, older.turnBlocks)
	view.seenProjections = mergeHistoryMap(view.seenProjections, older.seenProjections)
	view.liveNarratives = mergeHistoryMap(view.liveNarratives, older.liveNarratives)
	view.touch(true)
	if pane != nil && len(oldRows) > 0 {
		rows := m.subagentOutputRows(view, max(1, cache.width), max(1, cache.height))
		if !pane.followTail {
			pane.offset = relocateHistoryRow(oldRows, rows, pane.offset)
		}
		if pane.selecting || pane.selectStart != pane.selectEnd {
			pane.selectStart.line = relocateHistoryRow(oldRows, rows, pane.selectStart.line)
			pane.selectEnd.line = relocateHistoryRow(oldRows, rows, pane.selectEnd.line)
		}
	}
}

func relocateHistoryRow(previous, current []RenderedRow, line int) int {
	if line < 0 || line >= len(previous) {
		return line
	}
	id := previous[line].BlockID
	row := 0
	for n := line - 1; n >= 0 && previous[n].BlockID == id; n-- {
		row++
	}
	for n, r := range current {
		if r.BlockID == id {
			return min(n+row, len(current)-1)
		}
	}
	return line
}
