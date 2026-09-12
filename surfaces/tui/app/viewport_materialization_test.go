package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type demandTestBlock struct {
	id      string
	lines   int
	renders int
}

func (b *demandTestBlock) BlockID() string { return b.id }
func (b *demandTestBlock) Kind() BlockKind { return BlockTranscript }
func (b *demandTestBlock) Render(ctx BlockRenderContext) []RenderedRow {
	b.renders++
	rows := make([]RenderedRow, b.lines)
	for index := range rows {
		text := fmt.Sprintf("%s row %d width %d", b.id, index, ctx.Width)
		rows[index] = RenderedRow{Styled: text, Plain: text, PreWrapped: true, ClickToken: b.id, ClickStartCol: 0, ClickEndCol: 3, selectionIndent: 1}
	}
	return rows
}

func newViewportDemandTestModel(t *testing.T, count int) (*Model, []*demandTestBlock) {
	t.Helper()
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.resetConversationView()
	blocks := make([]*demandTestBlock, count)
	for index := range blocks {
		blocks[index] = &demandTestBlock{id: fmt.Sprintf("block-%03d", index), lines: 24}
		m.doc.Append(blocks[index])
	}
	m.markViewportStructureDirty()
	m.syncViewportContent()
	return m, blocks
}

func demandRenderCount(blocks []*demandTestBlock) int {
	count := 0
	for _, block := range blocks {
		count += block.renders
	}
	return count
}

func assertVisibleViewportMaterialized(t *testing.T, m *Model) {
	t.Helper()
	start := m.viewportVisibleOffset()
	end := min(len(m.viewportPlainLines), start+m.viewport.Height())
	for row := start; row < end; row++ {
		anchor := m.viewportAnchorAt(row)
		index := m.viewportRenderEntryIndex(anchor.blockID)
		if index < 0 || !m.viewportRenderEntries[index].materialized {
			t.Fatalf("visible row %d belongs to unmaterialized block %q", row, anchor.blockID)
		}
		entry := m.viewportRenderEntries[index]
		if row < entry.lineStart { // rhythm gap
			continue
		}
		if m.viewportBlockIDs[row] != entry.blockID {
			t.Fatalf("visible row %d contains a placeholder", row)
		}
	}
}

func TestViewportDemandBoundsColdHistoryAndWidthReflow(t *testing.T) {
	m, blocks := newViewportDemandTestModel(t, 500)
	if count := demandRenderCount(blocks); count > 5 {
		t.Fatalf("cold tail rendered %d blocks for a %d-row viewport", count, m.viewport.Height())
	}
	assertVisibleViewportMaterialized(t, m)
	for _, width := range []int{70, 100, 65, 100} {
		before := demandRenderCount(blocks)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		if count := demandRenderCount(blocks) - before; count > 5 {
			t.Fatalf("width %d rerendered %d blocks", width, count)
		}
		assertVisibleViewportMaterialized(t, m)
		if !m.isViewportFollowTail() || !m.viewport.AtBottom() {
			t.Fatal("reflow lost bottom following")
		}
	}
}

func TestViewportDemandScrollAnchorsAndEviction(t *testing.T) {
	m, blocks := newViewportDemandTestModel(t, 500)
	for _, target := range []int{20, 250, 100, 490} {
		entry := m.viewportRenderEntries[target]
		m.viewport.SetYOffset(entry.lineStart)
		m.setViewportFollowState(viewportPinnedHistory)
		m.materializeVisibleViewport()
		assertVisibleViewportMaterialized(t, m)
		if anchor := m.viewportAnchorAt(m.viewport.YOffset()); anchor.blockID != entry.blockID || anchor.row != 0 {
			t.Fatalf("measuring history moved anchor: got %#v want %s row 0", anchor, entry.blockID)
		}
		if m.isViewportFollowTail() {
			t.Fatal("height reconciliation enabled following")
		}
		retained := 0
		for _, cached := range m.viewportRenderEntries {
			if cached.materialized {
				retained++
			}
		}
		if retained > 8 {
			t.Fatalf("retained %d blocks away from current window", retained)
		}
		before := demandRenderCount(blocks)
		for range 5 {
			m.View()
		}
		if demandRenderCount(blocks) != before {
			t.Fatal("View performed document layout")
		}
	}
}

func TestViewportDemandEndFollowsUnterminatedLogTail(t *testing.T) {
	m, _ := newViewportDemandTestModel(t, 100)
	m.Update(LogChunkMsg{Chunk: strings.Repeat("stream-tail ", 120)})
	m.syncViewportContent()
	m.viewport.SetYOffset(m.viewportRenderEntries[10].lineStart)
	m.refreshViewportFollowStateFromOffset()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !m.isViewportFollowTail() || m.viewportVisibleOffset() != m.viewportMaxOffset() {
		t.Fatalf("End offset=%d max=%d follow=%v", m.viewportVisibleOffset(), m.viewportMaxOffset(), m.isViewportFollowTail())
	}
	if !strings.Contains(m.View().Content, "stream-tail") {
		t.Fatal("End did not reveal the log tail")
	}
}

func TestViewportDemandRemapsSelectionWhenEarlierEstimatesChange(t *testing.T) {
	m, _ := newViewportDemandTestModel(t, 100)
	m.viewport.SetYOffset(m.viewportRenderEntries[50].lineStart)
	m.setViewportFollowState(viewportPinnedHistory)
	m.materializeVisibleViewport()
	entry := m.viewportRenderEntries[50]
	m.selectionStart = textSelectionPoint{line: entry.lineStart + 2, col: 1}
	m.selectionEnd = textSelectionPoint{line: entry.lineStart + 4, col: 5}
	m.selecting = true
	m.setViewportFollowState(viewportSelecting)
	before := selectionTextFromLinesWithIndents(m.viewportPlainLines, m.viewportSelectionIndents, m.selectionStart, m.selectionEnd)
	// A scrollbar/wheel move exposes still-cold history above the selection.
	m.viewport.SetYOffset(m.viewportRenderEntries[40].lineStart)
	m.materializeVisibleViewport()
	after := selectionTextFromLinesWithIndents(m.viewportPlainLines, m.viewportSelectionIndents, m.selectionStart, m.selectionEnd)
	if before == "" || after != before {
		t.Fatalf("measurement changed selected text: before %q after %q", before, after)
	}
	if !strings.Contains(after, "050 row 2") {
		t.Fatalf("selection no longer addresses original block: %q", after)
	}
	assertVisibleViewportMaterialized(t, m)
}
