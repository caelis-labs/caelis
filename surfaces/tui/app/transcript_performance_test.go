package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func newMainTranscriptPerformanceModel(tb testing.TB, sections int, followTail bool) (*Model, *MainACPTurnBlock) {
	tb.Helper()
	m := NewModel(Config{NoAnimation: true, ColorProfile: colorprofile.TrueColor})
	m.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	m.themeCacheKey = ""
	m.Update(tea.WindowSizeMsg{Width: 180, Height: 60})
	m.liveTurn.Active = true
	var history strings.Builder
	for section := range sections {
		fmt.Fprintf(&history, "## 结论 %d 👩‍💻\n\n- 使用 `go test` 验证\n- [链接](https://example.com/%d) 与 **加粗中文**\n\n", section, section)
	}
	m.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionMain,
		TurnID: "long-turn", MessageID: "history", NarrativeKind: TranscriptNarrativeAssistant,
		Text: history.String(), Final: true,
	}}})
	appendMainTranscriptPerformanceDelta(m, "live output")
	_ = m.View()
	if !followTail {
		m.viewport.SetYOffset(m.viewportMaxOffset() / 2)
		m.refreshViewportFollowStateFromOffset()
	}
	_ = m.View()
	block, ok := m.doc.Find(m.mainTimelineTailID).(*MainACPTurnBlock)
	if !ok || len(m.viewportStyledLines) < sections {
		tb.Fatalf("long Turn fixture missing: rows=%d block=%T", len(m.viewportStyledLines), block)
	}
	if !mainACPBlockContainsText(block, fmt.Sprintf("## 结论 %d", sections-1)) {
		tb.Fatal("historical Markdown is outside the active block")
	}
	return m, block
}

func appendMainTranscriptPerformanceDelta(m *Model, text string) {
	m.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionMain,
		TurnID: "long-turn", MessageID: "live-message", NarrativeKind: TranscriptNarrativeAssistant,
		Text: text,
	}}})
	m.Update(frameTickMsg{kind: frameTickRenderDrain, at: time.Now()})
	// Deliver pending text and the viewport tick explicitly, excluding timer
	// waiting while retaining the production reducer and rendering work.
	m.flushAllPendingStreamSmoothingWithReason("performance")
	m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
}

func mainTranscriptPerformanceWheel(m *Model, index int) {
	button := tea.MouseWheelDown
	if index&1 == 0 {
		button = tea.MouseWheelUp
	}
	m.Update(tea.MouseWheelMsg(tea.Mouse{X: m.mainColumnX() + tuikit.GutterNarrative + 2, Y: 1, Button: button}))
}

func TestMainTranscriptLongTurnStreamReachesActiveBlock(t *testing.T) {
	m, block := newMainTranscriptPerformanceModel(t, 90, true)
	before := m.diag.BlockRenderCallsByKind[BlockMainACPTurn]
	for range 5 {
		appendMainTranscriptPerformanceDelta(m, "追加 ")
		_ = m.View()
	}
	if !mainACPBlockContainsText(block, strings.Repeat("追加 ", 5)) {
		t.Fatal("streaming deltas did not reach the existing long Turn")
	}
	if got := m.diag.BlockRenderCallsByKind[BlockMainACPTurn] - before; got != 5 {
		t.Fatalf("5 deltas rendered %d blocks, want 5", got)
	}
}

func BenchmarkMainTranscriptStreamFrame(b *testing.B) {
	for _, sections := range []int{20, 90, 360} {
		for _, followTail := range []bool{false, true} {
			b.Run(fmt.Sprintf("sections=%d/follow=%t", sections, followTail), func(b *testing.B) {
				m, block := newMainTranscriptPerformanceModel(b, sections, followTail)
				glamour := m.diag.GlamourRenderCalls
				b.ReportAllocs()
				for index := 0; b.Loop(); index++ {
					appendMainTranscriptPerformanceDelta(m, "追加 ")
					if !followTail {
						mainTranscriptPerformanceWheel(m, index)
					}
					stableViewportScrollBenchmarkSink = m.View().Content
				}
				b.ReportMetric(float64(len(m.viewportStyledLines)), "rows")
				if m.diag.GlamourRenderCalls != glamour || !mainACPBlockContainsText(block, "追加") {
					b.Fatal("fixture reparsed stable Markdown or missed the streaming update")
				}
			})
		}
	}
}
