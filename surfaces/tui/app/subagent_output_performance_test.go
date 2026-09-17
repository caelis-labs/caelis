package tuiapp

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func newSubagentOutputPerformanceModel(tb testing.TB, width, height, turns int) *Model {
	tb.Helper()
	m := NewModel(Config{NoAnimation: true, ColorProfile: colorprofile.TrueColor})
	m.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	m.themeCacheKey = ""
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.doc.Append(NewUserNarrativeBlock(strings.Repeat("父会话背景 · parent transcript\n", 80)))
	m.markViewportStructureDirty()
	m.syncViewportContent()
	v := m.ensureSubagentOutputView("performance-child")
	for turn := range turns {
		var text strings.Builder
		for section := range 10 {
			fmt.Fprintf(&text, "## 结论 %d/%d 👩‍💻\n\n- 使用 `go test` 验证\n- [链接](https://example.com/%d) 与 **加粗中文**\n\n", turn, section, section)
		}
		v.observeChildEvent(TranscriptEvent{
			Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent,
			TurnID: fmt.Sprintf("turn-%d", turn), NarrativeKind: TranscriptNarrativeAssistant,
			MessageID: fmt.Sprintf("message-%d", turn), Text: text.String(), Final: true,
		})
	}
	m.subagentOutputOverlay = &subagentOutputOverlayState{callID: v.callID, followTail: true}
	_ = m.View()
	m.subagentOutputOverlay.followTail = false
	m.subagentOutputOverlay.offset = m.subagentOutputOverlayMaxOffset() / 2
	_ = m.View()
	return m
}

func subagentPerformanceWheel(m *Model, index int) {
	button := tea.MouseWheelDown
	if index&1 == 0 {
		button = tea.MouseWheelUp
	}
	g := m.subagentOutputOverlay.geometry
	m.Update(tea.MouseWheelMsg(tea.Mouse{X: g.contentX + 2, Y: g.contentY + 2, Button: button}))
}

func appendSubagentPerformanceDelta(m *Model, text string) {
	m.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, ScopeID: "reviewer",
		AnchorToolCallID: "performance-child", AnchorToolName: surfaceToolSpawn,
		TurnID: "live-turn", MessageID: "live-message",
		NarrativeKind: TranscriptNarrativeAssistant, Text: text,
	}}})
	m.Update(subagentOutputRenderTickMsg{callID: "performance-child"})
}

func TestSubagentOutputIncrementalHistoryMatchesFreshRender(t *testing.T) {
	m := newSubagentOutputPerformanceModel(t, 120, 32, 9)
	v := m.subagentOutputViews["performance-child"]
	before := m.diag.BlockRenderCallsByKind[BlockParticipantTurn]
	appendSubagentPerformanceDelta(m, "新增输出")
	_ = m.View()
	if got := m.diag.BlockRenderCallsByKind[BlockParticipantTurn] - before; got != 1 {
		t.Fatalf("new child Turn rendered %d blocks, want only the new block", got)
	}
	assertSubagentOutputCacheMatchesFresh(t, m)

	// A late update must refresh its historical Turn, including both inserted
	// wrapped rows and the unchanged rows following it.
	v.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent,
		TurnID: "turn-3", NarrativeKind: TranscriptNarrativeUser,
		Text: strings.Repeat("中间插入的宽字符和长行 👩‍💻 ", 30),
	})
	v.prepareVisibleRender()
	_ = m.View()
	assertSubagentOutputCacheMatchesFresh(t, m)

	for _, size := range [][2]int{{60, 18}, {180, 60}, {120, 32}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		_ = m.View()
		assertSubagentOutputCacheMatchesFresh(t, m)
	}
	m.applyTheme(tuikit.ResolveThemeWithState(false, false, colorprofile.TrueColor))
	_ = m.View()
	assertSubagentOutputCacheMatchesFresh(t, m)
	v.resetForReplacement()
	_ = m.View()
	assertSubagentOutputCacheMatchesFresh(t, m)
	if strings.Contains(m.renderSubagentOutputOverlay(), "结论") {
		t.Fatal("replacement retained historical cached rows")
	}
}

func TestSubagentOutputIncrementalWrappedMiddleAndSuffix(t *testing.T) {
	m := newSubagentOutputPerformanceModel(t, 60, 18, 0)
	v := m.subagentOutputViews["performance-child"]
	for index := range 12 {
		v.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNotice, Text: fmt.Sprintf("第 %d 行 %s", index, strings.Repeat("中👩‍💻 ", index+1))})
	}
	v.prepareVisibleRender()
	_ = m.View()
	for _, text := range []string{strings.Repeat("changed middle 中文 ", 30), "short", ""} {
		v.block.Events[4].Text = text
		v.touch(true)
		_ = m.View()
		assertSubagentOutputCacheMatchesFresh(t, m)
	}
	// Retain the same character strings while changing a row's interaction
	// metadata: the cache must not preserve an earlier navigation target.
	v.block.AddAgentCommunication(SubagentEvent{Kind: SEAgentCommunication, Text: "same text", SourceName: "reviewer", SourceCallID: "first-child"})
	v.touch(true)
	_ = m.View()
	v.block.Events[len(v.block.Events)-1].SourceCallID = "second-child"
	v.touch(true)
	_ = m.View()
	assertSubagentOutputCacheMatchesFresh(t, m)
}

func TestSubagentOutputCacheRefreshesApprovalAndFullToolOutput(t *testing.T) {
	m := newSubagentOutputPerformanceModel(t, 120, 32, 1)
	v := m.subagentOutputViews["performance-child"]
	v.block.AddApprovalReviewEvent("approval", "RunCommand", "pwd", "pending", "waiting")
	v.touch(true)
	_ = m.View()
	v.block.AddApprovalReviewEvent("approval", "RunCommand", "pwd", "denied", "scope changed")
	v.touch(true)
	_ = m.View()
	assertSubagentOutputCacheMatchesFresh(t, m)

	tail := strings.Repeat("unchanged tail\n", 20)
	v.block.UpdateTool("command", "RunCommand", "pwd", "first head\n"+tail, true, false)
	v.block.ExpandedTools = map[string]bool{"command": true}
	v.block.ExpandedToolOutput = map[string]bool{"command": true}
	v.touch(true)
	_ = m.View()
	for index := range v.block.Events {
		if v.block.Events[index].CallID == "command" {
			v.block.Events[index].Output = "changed head\n" + tail
		}
	}
	v.touch(true)
	_ = m.View()
	assertSubagentOutputCacheMatchesFresh(t, m)
	if !strings.Contains(strings.Join(v.renderCache.fixedRows, "\n"), "changed head") {
		t.Fatal("expanded output retained a stale head with an unchanged terminal tail")
	}
}

func assertSubagentOutputCacheMatchesFresh(t *testing.T, m *Model) {
	t.Helper()
	v := m.subagentOutputViews[m.subagentOutputOverlay.callID]
	cached := v.renderCache
	v.renderCache = subagentOutputRenderCache{}
	_ = m.renderSubagentOutputOverlay()
	if !slices.Equal(cached.rows, v.renderCache.rows) || !slices.Equal(cached.fixedRows, v.renderCache.fixedRows) {
		t.Fatalf("incremental output differs from fresh rendering: cached rows=%d fresh rows=%d", len(cached.rows), len(v.renderCache.rows))
	}
	v.renderCache = cached
}

func TestSubagentOutputCachedCompositionTracksBackgroundAndWheel(t *testing.T) {
	m := newSubagentOutputPerformanceModel(t, 120, 32, 2)
	for index := range 6 {
		if index == 2 {
			m.doc.Append(NewUserNarrativeBlock("changed parent background"))
			m.markViewportStructureDirty()
			m.syncViewportContent()
		}
		subagentPerformanceWheel(m, index)
		cached := m.View().Content
		m.subagentOutputOverlay.composition = centeredOverlayCache{}
		fresh := m.View().Content
		if cached != fresh {
			t.Fatal("overlay composition retained an obsolete frame")
		}
	}
	if m.diag.P95InputLatency <= 0 || m.diag.LastInputAt.IsZero() {
		t.Fatal("wheel input did not enter input latency diagnostics")
	}
	frames := []string{m.View().Content}
	for index := range 4 {
		subagentPerformanceWheel(m, index)
		frames = append(frames, m.View().Content)
	}
	updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
	assertPhysicalFullscreenFrame(t, m.width, m.height, frames[len(frames)-1], updates)
}

func BenchmarkSubagentOutputScrollFrame(b *testing.B) {
	for _, size := range [][2]int{{120, 32}, {180, 60}, {240, 80}} {
		for _, scrolling := range []bool{true, false} {
			b.Run(fmt.Sprintf("%dx%d/scroll=%t", size[0], size[1], scrolling), func(b *testing.B) {
				m := newSubagentOutputPerformanceModel(b, size[0], size[1], 9)
				v := m.subagentOutputViews["performance-child"]
				renders, glamour := v.renderCache.renders, m.diag.GlamourRenderCalls
				b.ReportAllocs()
				for index := 0; b.Loop(); index++ {
					if scrolling {
						subagentPerformanceWheel(m, index)
					}
					subagentOutputOverlayBenchmarkSink = m.View().Content
				}
				if v.renderCache.renders != renders || m.diag.GlamourRenderCalls != glamour {
					b.Fatal("stable scroll rebuilt the transcript")
				}
			})
		}
	}
}

func BenchmarkSubagentOutputStreamFrame(b *testing.B) {
	for _, turns := range []int{1, 9, 36} {
		b.Run(fmt.Sprintf("turns=%d", turns), func(b *testing.B) {
			m := newSubagentOutputPerformanceModel(b, 180, 60, turns)
			appendSubagentPerformanceDelta(m, "live output")
			_ = m.View()
			b.ReportAllocs()
			for index := 0; b.Loop(); index++ {
				appendSubagentPerformanceDelta(m, "追加 ")
				subagentPerformanceWheel(m, index)
				subagentOutputOverlayBenchmarkSink = m.View().Content
			}
		})
	}
}

func TestSubagentOutputStreamRendersOnlyActiveBlock(t *testing.T) {
	m := newSubagentOutputPerformanceModel(t, 180, 60, 36)
	before := m.diag.BlockRenderCallsByKind[BlockParticipantTurn]
	for range 5 {
		appendSubagentPerformanceDelta(m, "追加 ")
		_ = m.View()
	}
	if got := m.diag.BlockRenderCallsByKind[BlockParticipantTurn] - before; got != 5 {
		t.Fatalf("5 child deltas rendered %d blocks, want only their active block", got)
	}
}
