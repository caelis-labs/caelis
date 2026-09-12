package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/charmbracelet/colorprofile"
)

var longHistoryFrameSink string

func longHistoryTranscript(turns int) []TranscriptEvent {
	events := make([]TranscriptEvent, 0, turns*4)
	for turn := range turns {
		id := fmt.Sprint(turn)
		var body strings.Builder
		for section := range 10 {
			fmt.Fprintf(&body, "## 审查结论 %d/%d\n\n- 使用 **类型身份** 与 `go test ./...` 验证\n- [source](https://example.com/%d/%d) ACP 子代理消息保留用户来源，英文 long content and 中文正文。\n\n```go\nfunc verify%d() { fmt.Println(%q) }\n```\n\n", turn, section, turn, section, section, "successful check")
		}
		events = append(events,
			TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionMain, TurnID: id, MessageID: "user-" + id, NarrativeKind: TranscriptNarrativeUser, Text: "请检查第 " + id + " 个任务", Final: true},
			TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionMain, TurnID: id, MessageID: id, NarrativeKind: TranscriptNarrativeAssistant, Text: body.String(), Final: true},
			TranscriptEvent{Kind: TranscriptEventTool, Scope: ACPProjectionMain, TurnID: id, ToolCallID: "tool-" + id, ToolName: surfaceToolRunCommand, ToolArgs: "echo tool-" + id, ToolOutput: "tool-result-" + id, Final: true},
			TranscriptEvent{Kind: TranscriptEventLifecycle, Scope: ACPProjectionMain, TurnID: id, State: "completed"},
		)
	}
	return events
}

func newLongHistoryPerformanceModel(tb testing.TB, turns int) *Model {
	tb.Helper()
	tb.Setenv("CAELIS_THEME", "catppuccin-mocha")
	m := NewModel(Config{NoAnimation: true, ColorProfile: colorprofile.TrueColor})
	m.Update(tea.WindowSizeMsg{Width: 180, Height: 60})
	events := longHistoryTranscript(turns)
	for start := 0; start < len(events); start += resumeReplayTranscriptBatchSize {
		m.Update(TranscriptEventsMsg{Events: events[start:min(len(events), start+resumeReplayTranscriptBatchSize)], ReconnectReplay: true})
	}
	m.flushAllPendingStreamSmoothingWithReason("history-fixture")
	m.syncViewportContent()
	view := m.ensureSubagentOutputView("history-child")
	view.taskHandle, view.actor = "history-child", "history-child"
	for turn := range 12 {
		id := fmt.Sprint(turn)
		view.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: id, MessageID: id, NarrativeKind: TranscriptNarrativeAssistant, Text: strings.Repeat("## 子任务 "+id+"\n\n使用 `go test` 检查 **结果**。\n\n", 10), Final: true})
	}
	m.subagentRosterTasks[view.callID] = taskstream.TaskDescriptor{TaskID: "history-task", Handle: view.taskHandle, Model: "test"}
	m.setSubagentLayout(uipreferences.Right)
	_ = m.View()
	return m
}

func clickLongHistoryPane(tb testing.TB, m *Model, open bool) string {
	tb.Helper()
	var point tea.Mouse
	if open {
		bounds, ok := m.subagentRosterFooterHitBounds()
		if !ok {
			tb.Fatal("no child footer mouse target")
		}
		point = tea.Mouse{X: bounds.x, Y: bounds.y, Button: tea.MouseLeft}
	} else {
		state := m.subagentOutputOverlay
		if state == nil {
			tb.Fatal("child pane is not open")
			return ""
		}
		found := false
		for _, action := range state.headerActions {
			if action.value == "close" {
				point = tea.Mouse{X: state.geometry.contentX + action.x + 1, Y: state.geometry.headerY, Button: tea.MouseLeft}
				found = true
			}
		}
		if !found {
			tb.Fatal("no child header close target")
		}
	}
	m.Update(tea.MouseClickMsg(point))
	point.Button = tea.MouseNone
	m.Update(tea.MouseReleaseMsg(point))
	frame := m.View().Content
	if (m.subagentOutputOverlay != nil) != open {
		tb.Fatalf("mouse action did not set pane open=%v", open)
	}
	return frame
}

func TestLongHistoryMousePaneToggleHasBoundedLayoutAndPhysicalFrames(t *testing.T) {
	m := newLongHistoryPerformanceModel(t, 256)
	defaultGlamourOutputCache.clear()
	frames := []string{m.View().Content}
	for _, open := range []bool{true, false, true, false} {
		before := m.diag.BlockRenderCallsByKind[BlockMainACPTurn]
		frames = append(frames, clickLongHistoryPane(t, m, open))
		if count := m.diag.BlockRenderCallsByKind[BlockMainACPTurn] - before; count > 8 {
			t.Fatalf("pane open=%v rendered %d main turns", open, count)
		}
		assertVisibleViewportMaterialized(t, m)
	}
	updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
	assertPhysicalFullscreenFrame(t, m.width, m.height, frames[len(frames)-1], updates)
}

func TestResumeTranscriptBatchLayoutsOnlyFinalVisibleWindow(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.Update(tea.WindowSizeMsg{Width: 180, Height: 60})
	before := m.diag.ViewportFullSyncs + m.diag.ViewportIncrementalSyncs
	renders := m.diag.BlockRenderCallsByKind[BlockMainACPTurn]
	m.Update(TranscriptEventsMsg{Events: longHistoryTranscript(16), ReconnectReplay: true})
	if syncs := m.diag.ViewportFullSyncs + m.diag.ViewportIncrementalSyncs - before; syncs != 1 {
		t.Fatalf("one replay batch performed %d layouts, want one", syncs)
	}
	if count := m.diag.BlockRenderCallsByKind[BlockMainACPTurn] - renders; count > 8 {
		t.Fatalf("replay batch rendered %d turns outside the final visible window", count)
	}
	assertVisibleViewportMaterialized(t, m)
}

func TestViewportHistoricalTurnsKeepRecentDetailsAndSourceEvents(t *testing.T) {
	m := newLongHistoryPerformanceModel(t, 4)
	var frames []string
	for turn := range 4 {
		id := fmt.Sprint(turn)
		retained := false
		var rendered []string
		for _, candidate := range m.doc.Blocks() {
			block, ok := candidate.(*MainACPTurnBlock)
			if !ok || block.TurnKey != id {
				continue
			}
			for _, event := range block.Events {
				retained = retained || event.CallID == "tool-"+id
			}
			index := m.viewportRenderEntryIndex(block.BlockID())
			m.viewport.SetYOffset(m.viewportRenderEntries[index].lineStart)
			m.setViewportFollowState(viewportPinnedHistory)
			m.materializeVisibleViewport()
			rendered = append(rendered, m.viewportRenderEntries[index].plainLines...)
		}
		if !retained {
			t.Fatalf("display policy removed tool source from turn %s", id)
		}
		plain := strings.Join(rendered, "\n")
		if !strings.Contains(plain, "审查结论 "+id+"/0") {
			t.Fatalf("turn %s lost assistant narrative", id)
		}
		if got := strings.Contains(plain, "tool-result-"+id); got != (turn >= 2) {
			t.Fatalf("turn %s tool visibility=%v, want newest two only", id, got)
		}
		frames = append(frames, m.View().Content)
	}
	updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
	assertPhysicalFullscreenFrame(t, m.width, m.height, frames[len(frames)-1], updates)
}

func BenchmarkLongHistoryPaneToggle(b *testing.B) {
	for _, turns := range []int{32, 256} {
		for _, cold := range []bool{true, false} {
			b.Run(fmt.Sprintf("turns=%d/cold=%v", turns, cold), func(b *testing.B) {
				m := newLongHistoryPerformanceModel(b, turns)
				clickLongHistoryPane(b, m, true)
				clickLongHistoryPane(b, m, false)
				var opened, closed time.Duration
				b.ReportAllocs()
				for b.Loop() {
					if cold {
						defaultGlamourOutputCache.clear()
					}
					start := time.Now()
					clickLongHistoryPane(b, m, true)
					opened += time.Since(start)
					start = time.Now()
					clickLongHistoryPane(b, m, false)
					closed += time.Since(start)
				}
				b.ReportMetric(float64(opened.Microseconds())/float64(b.N)/1000, "open-ms")
				b.ReportMetric(float64(closed.Microseconds())/float64(b.N)/1000, "close-ms")
			})
		}
	}
}

// A single visible block still uses the block renderer as its layout unit.
// Keep its cold-width cost visible rather than hiding it in many-turn results.
func BenchmarkGiantNarrativeWidthReflow(b *testing.B) {
	for _, streaming := range []bool{false, true} {
		b.Run(fmt.Sprintf("streaming=%v", streaming), func(b *testing.B) {
			b.Setenv("CAELIS_THEME", "catppuccin-mocha")
			m := NewModel(Config{NoAnimation: true, ColorProfile: colorprofile.TrueColor})
			m.Update(tea.WindowSizeMsg{Width: 180, Height: 60})
			block := NewMainACPTurnBlock("giant")
			if streaming {
				block.AppendStreamEvent(SEAssistant, "```go\n"+strings.Repeat("fmt.Println(\"streaming 中文\")\n", 10000), narrativeSourceIdentity{})
			} else {
				block.ReplaceFinalStreamEvent(SEAssistant, strings.Repeat("## 结果\n\n- **检查** `go test`\n- [source](https://example.com)\n\n", 1000), narrativeSourceIdentity{})
			}
			m.doc.Append(block)
			m.syncViewportContent()
			b.ReportAllocs()
			for index := 0; b.Loop(); index++ {
				defaultGlamourOutputCache.clear()
				m.Update(tea.WindowSizeMsg{Width: 100 + index%2, Height: 60})
				longHistoryFrameSink = m.View().Content
			}
		})
	}
}

// This benchmark starts after Control's replacement commit. It measures TUI
// replay application separately from transport and Runtime startup.
func BenchmarkLongHistoryResumeLayout(b *testing.B) {
	for _, turns := range []int{32, 256} {
		b.Run(fmt.Sprintf("turns=%d", turns), func(b *testing.B) {
			events := longHistoryTranscript(turns)
			b.Setenv("CAELIS_THEME", "catppuccin-mocha")
			b.ReportAllocs()
			for b.Loop() {
				defaultGlamourOutputCache.clear()
				m := NewModel(Config{NoAnimation: true, ColorProfile: colorprofile.TrueColor})
				m.Update(tea.WindowSizeMsg{Width: 180, Height: 60})
				for start := 0; start < len(events); start += resumeReplayTranscriptBatchSize {
					m.Update(TranscriptEventsMsg{Events: events[start:min(len(events), start+resumeReplayTranscriptBatchSize)], ReconnectReplay: true})
				}
				m.syncViewportContent()
				longHistoryFrameSink = m.View().Content
			}
		})
	}
}
