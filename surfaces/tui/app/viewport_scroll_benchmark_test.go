package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

var stableViewportScrollBenchmarkSink string

type stableViewportScrollFixture struct {
	name  string
	build func() Block
}

func stableViewportScrollFixtures() []stableViewportScrollFixture {
	return []stableViewportScrollFixture{
		{
			name: "PlainASCII",
			build: func() Block {
				var text strings.Builder
				for index := range 220 {
					fmt.Fprintf(&text, "plain transcript row %03d with paths and command output that wraps across the viewport width\n", index)
				}
				return NewUserNarrativeBlock(text.String())
			},
		},
		{
			name: "PlainCJK",
			build: func() Block {
				var text strings.Builder
				for index := range 220 {
					fmt.Fprintf(&text, "普通中文文本第 %03d 行，用于对比宽字符滚动成本，并包含路径 surfaces/tui/app/view_render.go\n", index)
				}
				return NewUserNarrativeBlock(text.String())
			},
		},
		{
			name: "MarkdownASCII",
			build: func() Block {
				block := NewMainACPTurnBlock("scroll-markdown-ascii")
				var text strings.Builder
				for index := range 90 {
					fmt.Fprintf(&text, "## Result %03d\n\n- [x] run `go test ./surfaces/tui/app` for case %03d\n- [link](https://example.com/%03d) and **bold text %03d**\n\n", index, index, index, index)
				}
				block.ReplaceFinalStreamEvent(SEAssistant, text.String(), narrativeSourceIdentity{})
				return block
			},
		},
		{
			name: "MarkdownCJK",
			build: func() Block {
				block := NewMainACPTurnBlock("scroll-markdown-cjk")
				var text strings.Builder
				for index := range 90 {
					fmt.Fprintf(&text, "## 排查结论 %03d 👩‍💻\n\n- [x] 使用 `go test ./surfaces/tui/app` 验证场景 %03d\n- [链接](https://example.com/%03d) 与 **加粗中文 %03d**\n\n", index, index, index, index)
				}
				block.ReplaceFinalStreamEvent(SEAssistant, text.String(), narrativeSourceIdentity{})
				return block
			},
		},
	}
}

func newStableViewportScrollModel(tb testing.TB, fixture stableViewportScrollFixture) *Model {
	tb.Helper()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	model = updated.(*Model)
	model.doc.Append(fixture.build())
	model.markViewportStructureDirty()
	model.syncViewportContent()
	model.viewport.SetYOffset(model.viewportMaxOffset() / 2)
	model.refreshViewportFollowStateFromOffset()
	return model
}

func TestStableViewportCachedLinesMatchBubblesView(t *testing.T) {
	fixtures := append([]stableViewportScrollFixture{
		{name: "Short", build: func() Block { return NewUserNarrativeBlock("short row") }},
	}, stableViewportScrollFixtures()...)
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			model := newStableViewportScrollModel(t, fixture)
			offsets := []int{0, model.viewportMaxOffset() / 2, model.viewportMaxOffset()}
			for _, offset := range offsets {
				model.viewport.SetYOffset(offset)
				for _, showScrollbar := range []bool{false, true} {
					if showScrollbar {
						model.viewportScrollbarVisibleUntil = time.Now().Add(time.Hour)
					} else {
						model.viewportScrollbarVisibleUntil = time.Time{}
					}
					want := strings.TrimRight(model.viewport.View(), "\n")
					if showScrollbar {
						want = model.renderViewportScrollbar(want)
					}
					model.lastViewportViewKey = ""
					if got := model.renderViewportView(); got != want {
						t.Fatalf("offset=%d scrollbar=%v cached viewport differs from Bubbles view\n got bytes: %d\nwant bytes: %d", offset, showScrollbar, len(got), len(want))
					}
				}
			}
		})
	}
}

func TestViewportViewCacheKeyIncludesHorizontalOffset(t *testing.T) {
	model := NewModel(Config{NoAnimation: true})
	model.viewport.SetWidth(4)
	model.viewport.SetContent("abcdefgh")
	before := model.viewportViewCacheKey(false)

	model.viewport.SetXOffset(2)
	if got := model.viewport.XOffset(); got != 2 {
		t.Fatalf("horizontal offset = %d, want 2", got)
	}
	if after := model.viewportViewCacheKey(false); after == before {
		t.Fatal("viewport cache key did not change with horizontal offset")
	}
}

func BenchmarkStableViewportScrollFrame(b *testing.B) {
	for _, fixture := range stableViewportScrollFixtures() {
		b.Run(fixture.name, func(b *testing.B) {
			model := newStableViewportScrollModel(b, fixture)
			if len(model.viewportStyledLines) < model.viewport.Height()+4 {
				b.Fatalf("fixture produced only %d viewport rows", len(model.viewportStyledLines))
			}
			model.viewportScrollbarVisibleUntil = time.Now().Add(time.Hour)
			blockKind := model.doc.Last().Kind()
			glamourBefore := model.diag.GlamourRenderCalls
			blockBefore := model.diag.BlockRenderCallsByKind[blockKind]
			b.ReportAllocs()
			b.ReportMetric(float64(len(model.viewportStyledLines)), "rows")
			for index := 0; b.Loop(); index++ {
				button := tea.MouseWheelDown
				if index&1 == 0 {
					button = tea.MouseWheelUp
				}
				updated, _ := model.handleMouse(tea.MouseWheelMsg(tea.Mouse{
					Button: button,
					X:      model.mainColumnX() + tuikit.GutterNarrative + 2,
					Y:      1,
				}))
				model = updated.(*Model)
				stableViewportScrollBenchmarkSink = model.View().Content
			}
			if model.diag.GlamourRenderCalls != glamourBefore {
				b.Fatalf("scroll reran Glamour: before=%d after=%d", glamourBefore, model.diag.GlamourRenderCalls)
			}
			if model.diag.BlockRenderCallsByKind[blockKind] != blockBefore {
				b.Fatal("scroll rerendered document block")
			}
		})
	}
}

func BenchmarkStableViewportView(b *testing.B) {
	for _, fixture := range stableViewportScrollFixtures() {
		b.Run(fixture.name, func(b *testing.B) {
			model := newStableViewportScrollModel(b, fixture)
			model.viewportScrollbarVisibleUntil = time.Now().Add(time.Hour)
			baseOffset := model.viewport.YOffset()
			b.ReportAllocs()
			for index := 0; b.Loop(); index++ {
				model.viewport.SetYOffset(baseOffset - (index & 1))
				stableViewportScrollBenchmarkSink = model.renderViewportView()
			}
		})
	}
}
