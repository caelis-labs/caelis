package tuiapp

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func newWorkspaceScrollPerformanceModel(tb testing.TB, width, height int, mode uipreferences.Layout) *Model {
	m := newSubagentOutputPerformanceModel(tb, width, height, 36)
	// Rich historical Markdown in both panes, with scroll positions pinned.
	var rich strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&rich, "## 审查结论 %d\n\n- 使用 **类型身份** 与 `go test ./...` 验证\n- [source](https://example.com/source/%d) ACP 子代理消息在后续上下文保留用户来源，英文 long content and 中文正文。\n\n", i, i)
	}
	m.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{Kind: TranscriptEventNarrative, Scope: ACPProjectionMain, TurnID: "performance-parent", MessageID: "performance-main", NarrativeKind: TranscriptNarrativeAssistant, Text: rich.String(), Final: true}}})
	m.flushAllPendingStreamSmoothingWithReason("performance")
	m.syncViewportContent()
	m.setSubagentLayout(mode)
	_ = m.View()
	m.viewport.SetYOffset(m.viewportMaxOffset() / 2)
	m.subagentOutputOverlay.offset = m.subagentOutputOverlayMaxOffset() / 2
	m.subagentOutputOverlay.followTail = false
	for i := 0; i < 6; i++ {
		subagentPerformanceWheel(m, i)
		_ = m.View()
	}
	return m
}

// The terminal case includes Draw, Render, and Flush into a buffer. It measures
// CPU-side input-to-diff work, not a terminal application's final paint latency.
func BenchmarkSubagentWorkspaceScrollFrame(b *testing.B) {
	for _, size := range [][2]int{{120, 32}, {180, 60}, {240, 80}} {
		for _, mode := range []uipreferences.Layout{uipreferences.Overlay, uipreferences.Right, uipreferences.Down} {
			for _, target := range []string{"child", "main", "terminal"} {
				b.Run(fmt.Sprintf("%dx%d/%s/%s", size[0], size[1], mode, target), func(b *testing.B) {
					m := newWorkspaceScrollPerformanceModel(b, size[0], size[1], mode)
					if mode == uipreferences.Overlay && target == "main" {
						b.Skip("main is covered by overlay")
					}
					v := m.subagentOutputViews["performance-child"]
					renders, glamour := v.renderCache.renders, m.diag.GlamourRenderCalls
					var buf bytes.Buffer
					renderer := uv.NewTerminalRenderer(&buf, []string{"TERM=xterm-256color", "TTY_FORCE=1"})
					renderer.SetFullscreen(true)
					renderer.SetScrollOptim(true)
					screen := uv.NewScreenBuffer(m.width, m.height)
					screen.Method = ansi.GraphemeWidth
					if target == "terminal" {
						uv.NewStyledString(m.View().Content).Draw(screen, screen.Bounds())
						renderer.Render(screen.RenderBuffer)
						_ = renderer.Flush()
						buf.Reset()
					}
					var bytesOut int64
					var samples []time.Duration
					b.ReportAllocs()
					for i := 0; b.Loop(); i++ {
						start := time.Now()
						switch target {
						case "main":
							button := tea.MouseWheelUp
							if i%2 != 0 {
								button = tea.MouseWheelDown
							}
							r := m.workspaceLayout().main
							m.Update(tea.MouseWheelMsg(tea.Mouse{X: r.x + 5, Y: r.y + 3, Button: button}))
						case "child", "terminal":
							subagentPerformanceWheel(m, i)
						}
						frame := m.View().Content
						if target == "terminal" {
							screen.Clear()
							uv.NewStyledString(frame).Draw(screen, screen.Bounds())
							renderer.Render(screen.RenderBuffer)
							if err := renderer.Flush(); err != nil {
								b.Fatal(err)
							}
							bytesOut += int64(buf.Len())
							buf.Reset()
						}
						samples = append(samples, time.Since(start))
						subagentOutputOverlayBenchmarkSink = frame
					}
					if v.renderCache.renders != renders || m.diag.GlamourRenderCalls != glamour {
						b.Fatal("stable scroll rebuilt transcript")
					}
					slices.Sort(samples)
					reportWorkspaceFrameSamples(b, samples)
					b.ReportMetric(float64(len(v.renderCache.rows)), "childrows")
					b.ReportMetric(float64(len(m.viewportStyledLines)), "mainrows")
					if target == "terminal" {
						b.ReportMetric(float64(bytesOut)/float64(b.N), "wireB/op")
					}
				})
			}
		}
	}
}

func BenchmarkSubagentWorkspaceLongDividerDrag(b *testing.B) {
	for _, mode := range []uipreferences.Layout{uipreferences.Right, uipreferences.Down} {
		b.Run(string(mode), func(b *testing.B) {
			m := newWorkspaceScrollPerformanceModel(b, 240, 80, mode)
			r := m.workspaceLayout().divider
			m.Update(tea.MouseClickMsg{X: r.x, Y: r.y, Button: tea.MouseLeft})
			var samples []time.Duration
			for i := 0; b.Loop(); i++ {
				start := time.Now()
				x, y := r.x, r.y
				if mode == uipreferences.Right {
					x = 90 + i%60
				} else {
					y = 25 + i%28
				}
				m.Update(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
				subagentOutputOverlayBenchmarkSink = m.View().Content
				samples = append(samples, time.Since(start))
			}
			slices.Sort(samples)
			reportWorkspaceFrameSamples(b, samples)
		})
	}
}

func BenchmarkSubagentWorkspaceStreamingScrollFrame(b *testing.B) {
	for _, mode := range []uipreferences.Layout{uipreferences.Overlay, uipreferences.Right, uipreferences.Down} {
		for _, focus := range []string{"child", "main"} {
			b.Run(string(mode)+"/"+focus, func(b *testing.B) {
				if mode == uipreferences.Overlay && focus == "main" {
					b.Skip("main is covered by overlay")
				}
				m := newWorkspaceScrollPerformanceModel(b, 240, 80, mode)
				m.workspace.childFocused = focus == "child"
				var samples []time.Duration
				for index := 0; b.Loop(); index++ {
					start := time.Now()
					if index%8 == 0 {
						appendSubagentPerformanceDelta(m, " 后台子代理输出")
						appendMainTranscriptPerformanceDelta(m, " 后台主代理输出")
					}
					if focus == "child" {
						subagentPerformanceWheel(m, index)
					} else {
						button := tea.MouseWheelUp
						if index%2 != 0 {
							button = tea.MouseWheelDown
						}
						r := m.workspaceLayout().main
						m.Update(tea.MouseWheelMsg{X: r.x + 5, Y: r.y + 3, Button: button})
					}
					subagentOutputOverlayBenchmarkSink = m.View().Content
					samples = append(samples, time.Since(start))
				}
				slices.Sort(samples)
				reportWorkspaceFrameSamples(b, samples)
			})
		}
	}
}

func reportWorkspaceFrameSamples(b *testing.B, samples []time.Duration) {
	for _, metric := range []struct {
		name       string
		percentile int
	}{{"p50-ms", 50}, {"p95-ms", 95}, {"p99-ms", 99}} {
		b.ReportMetric(float64(samples[minInt(len(samples)-1, len(samples)*metric.percentile/100)].Microseconds())/1000, metric.name)
	}
}
