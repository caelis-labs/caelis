package tuiapp

import "strings"

// Offscreen entries retain only their height and identity. Bubbles still owns
// global scroll coordinates; empty, noninteractive rows reserve estimated space
// until the corresponding document blocks enter the viewport or its margin.
// This is a layout cache, never a second transcript or a history truncation.
func estimatedViewportBlockLines(block Block, width int) int {
	textLines := func(text string) int {
		if text == "" {
			return 0
		}
		return 1 + strings.Count(text, "\n") + len(text)/max(1, width)
	}
	switch b := block.(type) {
	case *UserNarrativeBlock:
		return max(1, textLines(b.Raw))
	case *TranscriptBlock:
		return max(1, textLines(b.Raw))
	case *MainACPTurnBlock:
		return estimatedNarrativeLines(b.Events, textLines)
	case *ParticipantTurnBlock:
		return estimatedNarrativeLines(b.Events, textLines)
	default:
		return 1
	}
}

func estimatedNarrativeLines(events []SubagentEvent, textLines func(string) int) int {
	lines := 1
	for _, event := range events {
		switch event.Kind {
		case SEAssistant, SEUserInput:
			lines += textLines(event.Text)
		default:
			lines++
		}
	}
	return lines
}

// viewportRowAnchor addresses a row inside a stable block, rather than a global
// offset that would move when preceding estimated heights are measured.
type viewportRowAnchor struct {
	blockID string
	row     int
	gap     int
	count   int
	width   int
	cold    bool
}

func (m *Model) viewportAnchorAt(line int) viewportRowAnchor {
	if line < 0 {
		return viewportRowAnchor{}
	}
	for _, entry := range m.viewportRenderEntries {
		if entry.lineCount > 0 && line < entry.lineStart+entry.lineCount {
			return viewportRowAnchor{
				blockID: entry.blockID, row: max(0, line-entry.lineStart), gap: min(0, line-entry.lineStart),
				count: entry.lineCount, width: entry.layoutWidth, cold: !entry.materialized,
			}
		}
	}
	return viewportRowAnchor{}
}

func (m *Model) viewportAnchorLine(anchor viewportRowAnchor, fallback int) int {
	for _, entry := range m.viewportRenderEntries {
		if entry.blockID == anchor.blockID {
			row := anchor.row
			if anchor.cold || anchor.width != entry.layoutWidth {
				row = row * entry.lineCount / max(1, anchor.count)
			}
			return max(0, entry.lineStart+min(row, max(0, entry.lineCount-1))+anchor.gap)
		}
	}
	return fallback
}

func (m *Model) indexViewportEntries() int {
	line := 0
	var previous *viewportRenderEntry
	for index := range m.viewportRenderEntries {
		entry := &m.viewportRenderEntries[index]
		if shouldInsertViewportRhythmGap(previous, entry) {
			line++
		}
		entry.lineStart = line
		line += entry.lineCount
		if viewportEntryHasVisibleContent(*entry) {
			previous = entry
		}
	}
	return line
}

func (m *Model) viewportDemandRange(total int, anchor viewportRowAnchor) (int, int, int) {
	height := max(1, m.viewport.Height())
	offset := m.viewportAnchorLine(anchor, m.viewportVisibleOffset())
	if m.isViewportFollowTail() {
		offset = max(0, total-height)
	}
	start, end := max(0, offset-height), offset+2*height
	if m.selecting || m.hasSelectionRange() {
		start = min(start, max(0, min(m.selectionStart.line, m.selectionEnd.line)))
		end = max(end, max(m.selectionStart.line, m.selectionEnd.line)+1)
	}
	return start, end, offset
}

// materializeViewportEntries measures the closest missing block first. Rechecking
// demand after every measurement prevents underestimated heights from causing a
// cold tail to render a whole history's worth of blocks in one update.
func (m *Model) materializeViewportEntries(ctx BlockRenderContext, anchor viewportRowAnchor) (bool, int) {
	changed := false
	selected := m.selecting || m.hasSelectionRange()
	selectionStart := m.viewportAnchorAt(m.selectionStart.line)
	selectionEnd := m.viewportAnchorAt(m.selectionEnd.line)
	total := m.indexViewportEntries()
	for {
		start, end, offset := m.viewportDemandRange(total, anchor)
		candidate, distance := -1, int(^uint(0)>>1)
		for index, entry := range m.viewportRenderEntries {
			if entry.materialized || entry.lineCount == 0 || entry.lineStart >= end || entry.lineStart+entry.lineCount <= start {
				continue
			}
			d := max(0, max(entry.lineStart-offset, offset-entry.lineStart-entry.lineCount+1))
			if m.isViewportFollowTail() {
				d = max(0, total-entry.lineStart-entry.lineCount)
			}
			if d < distance {
				candidate, distance = index, d
			}
		}
		if candidate < 0 {
			return m.evictOffscreenViewportEntries(offset) || changed, offset
		}
		old := m.viewportRenderEntries[candidate]
		block := m.doc.Find(old.blockID)
		if block == nil {
			return changed, offset
		}
		next := m.renderViewportEntry(block, viewportEntryRenderKey(block, ctx, old.compact), ctx, old.viewportRowCache, old.compact)
		m.viewportRenderEntries[candidate] = next
		changed = true
		total = m.indexViewportEntries()
		if selected {
			m.selectionStart.line = m.viewportAnchorLine(selectionStart, m.selectionStart.line)
			m.selectionEnd.line = m.viewportAnchorLine(selectionEnd, m.selectionEnd.line)
		}
	}
}

func (m *Model) evictOffscreenViewportEntries(offset int) bool {
	// Selection freezes the displayed snapshot, including rows scrolled past
	// while dragging. They must stay available to copy without a second render.
	if m.selecting || m.hasSelectionRange() {
		return false
	}
	height := max(1, m.viewport.Height())
	start, end := max(0, offset-2*height), offset+3*height
	changed := false
	for index := range m.viewportRenderEntries {
		entry := &m.viewportRenderEntries[index]
		if !entry.materialized || entry.lineCount == 0 || (entry.lineStart < end && entry.lineStart+entry.lineCount > start) {
			continue
		}
		entry.viewportRowCache = viewportRowCache{blockID: entry.blockID}
		entry.materialized = false
		changed = true
	}
	return changed
}

// materializeVisibleViewport runs after scroll movement, before hit testing or
// painting. View itself only consumes these prepared rows.
func (m *Model) materializeVisibleViewport() {
	if m == nil || m.doc == nil || len(m.viewportRenderEntries) == 0 {
		return
	}
	anchor := m.viewportAnchorAt(m.viewportVisibleOffset())
	selectionStart := m.viewportAnchorAt(m.selectionStart.line)
	selectionEnd := m.viewportAnchorAt(m.selectionEnd.line)
	selected := m.selecting || m.hasSelectionRange()
	ctx := m.blockRenderContext(max(1, m.viewport.Width()))
	changed, offset := m.materializeViewportEntries(ctx, anchor)
	if !changed {
		return
	}
	m.rebuildViewportLineCaches(ctx)
	m.viewportContentVersion++
	m.renderViewportContent("visible_layout", false)
	if !m.isViewportFollowTail() {
		m.viewport.SetYOffset(offset)
	}
	if selected {
		m.selectionStart.line = m.viewportAnchorLine(selectionStart, m.selectionStart.line)
		m.selectionEnd.line = m.viewportAnchorLine(selectionEnd, m.selectionEnd.line)
		m.bumpViewportSelectionVersion()
	}
}
