package tuiapp

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type rowCacheTestBlock struct {
	id   string
	kind BlockKind
	rows []RenderedRow
}

func (b *rowCacheTestBlock) BlockID() string                         { return b.id }
func (b *rowCacheTestBlock) Kind() BlockKind                         { return b.kind }
func (b *rowCacheTestBlock) Render(BlockRenderContext) []RenderedRow { return b.rows }

func TestViewportRowCacheMatchesUncachedRows(t *testing.T) {
	for _, kind := range []BlockKind{BlockMainACPTurn, BlockParticipantTurn} {
		t.Run(string(kind), func(t *testing.T) {
			m := NewModel(Config{NoAnimation: true, ColorProfile: colorprofile.TrueColor})
			ctx := m.blockRenderContext(36)
			ctx.Height = 18
			ctx.Theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
			ctx.ThemeKey = ""
			block := &rowCacheTestBlock{id: "rows", kind: kind, rows: []RenderedRow{
				{Styled: "• Ran " + strings.Repeat("command ", 12), ACPHeader: true},
				{Styled: "中文 👩‍💻 " + strings.Repeat("wide row ", 18), ClickToken: "first", ClickStartCol: 3, ClickEndCol: 50, selectionIndent: 2},
				{Styled: "tail"},
			}}
			var cached viewportRowCache
			check := func() {
				t.Helper()
				cached = m.renderViewportRowCache(block, ctx, cached)
				want := m.wrapRenderedRowsForViewport(block, block.Render(ctx), ctx.Width, ctx)
				assertWrappedViewportRowsEqual(t, cached.wrappedViewportRows, want)
				fixed := cached.fixedWidthLines(ctx.Width)
				for index, line := range want.styledLines {
					if fixed[index] != normalizeFullscreenFrameLine(line, ctx.Width) {
						t.Fatalf("stale normalized line %d", index)
					}
				}
			}
			check()
			check() // Same source, including rows borrowed from a block-owned slice.
			block.rows[1].Styled = "short"
			check()
			block.rows[1].ClickToken = "second"
			block.rows[1].ClickStartCol = 1
			block.rows[1].ClickEndCol = 4
			block.rows[1].selectionIndent = 1
			check()
			block.rows = append([]RenderedRow{{Styled: "inserted before"}}, block.rows...)
			check()
			block.rows = append(block.rows[:2], block.rows[3:]...)
			check()
			block.rows = append(block.rows, RenderedRow{Styled: strings.Repeat("new tail 中 ", 30)})
			check()
			ctx.Width = 18
			check()
			ctx.Theme = tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor)
			check()
			ctx.Height = 30
			check()
			ctx.TermWidth = 240
			ctx.Workspace = "/another/workspace"
			check()
			block.rows = nil
			check()
			block.rows = []RenderedRow{{Styled: "new transcript"}}
			check()
		})
	}
}

func assertWrappedViewportRowsEqual(t *testing.T, got, want wrappedViewportRows) {
	t.Helper()
	if !slices.Equal(got.styledLines, want.styledLines) || !slices.Equal(got.plainLines, want.plainLines) ||
		!slices.Equal(got.selectionIndents, want.selectionIndents) || !slices.Equal(got.clickTokens, want.clickTokens) ||
		!slices.Equal(got.clickBounds, want.clickBounds) || !slices.Equal(got.sourceOffsets, want.sourceOffsets) {
		t.Fatalf("incremental rows differ from uncached wrapping: got %d lines, want %d", len(got.styledLines), len(want.styledLines))
	}
}

func TestMainTranscriptIncrementalRowsMatchFreshFrames(t *testing.T) {
	m, block := newMainTranscriptPerformanceModel(t, 90, true)
	check := func() {
		t.Helper()
		cached := m.View().Content
		ctx := m.blockRenderContext(m.viewport.Width())
		for _, entry := range m.viewportRenderEntries {
			if entry.fixedLines != nil {
				t.Fatal("main transcript eagerly allocated overlay-normalized lines")
			}
			want := m.wrapRenderedRowsForViewport(m.doc.Find(entry.blockID), m.doc.Find(entry.blockID).Render(ctx), ctx.Width, ctx)
			assertWrappedViewportRowsEqual(t, entry.wrappedViewportRows, want)
		}
		beforeTokens := slices.Clone(m.viewportClickTokens)
		beforeBounds := slices.Clone(m.viewportClickBounds)
		beforeIndents := slices.Clone(m.viewportSelectionIndents)
		m.viewportRenderEntries = nil
		m.markViewportStructureDirty()
		m.syncViewportContent()
		if fresh := m.View().Content; cached != fresh || !slices.Equal(beforeTokens, m.viewportClickTokens) ||
			!slices.Equal(beforeBounds, m.viewportClickBounds) || !slices.Equal(beforeIndents, m.viewportSelectionIndents) {
			t.Fatal("main viewport frame or interaction metadata differs after full rebuild")
		}
	}
	frames := []string{m.View().Content}
	for range 5 {
		appendMainTranscriptPerformanceDelta(m, "追加中文 👩‍💻 ")
		frames = append(frames, m.View().Content)
		check()
	}
	updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
	assertPhysicalFullscreenFrame(t, m.width, m.height, frames[len(frames)-1], updates)
	// Rewrite historical content in the still-active block, preserving the tail.
	block.Events[0].Text = "replacement **历史内容**"
	m.markViewportBlockDirty(block.BlockID())
	m.syncViewportContent()
	check()
	for _, size := range [][2]int{{60, 18}, {180, 80}, {120, 32}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		check()
	}
	m.applyTheme(tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor))
	check()
}
