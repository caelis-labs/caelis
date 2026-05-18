package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestAssistantStreamViewportSyncCoalescesAndUsesIncrementalTail(t *testing.T) {
	m := newPerfTestModel()
	seedLongTranscript(m, 120)

	_, cmd := m.handleStreamBlock("answer", "assistant", "hello", false)
	if cmd == nil {
		t.Fatal("first stream chunk should schedule viewport sync")
	}
	if !m.viewportSyncPending {
		t.Fatal("first stream chunk should be pending before frame tick")
	}
	_, _ = m.Update(perfTickAt(frameTickViewportSync, time.Now()))
	firstFullSyncs := m.diag.ViewportFullSyncs
	if firstFullSyncs == 0 {
		t.Fatal("first stream chunk should perform a full sync for the new block")
	}

	_, cmd = m.handleStreamBlock("answer", "assistant", " world", false)
	if cmd == nil {
		t.Fatal("tail stream chunk should schedule viewport sync")
	}
	if got := m.diag.ViewportFullSyncs; got != firstFullSyncs {
		t.Fatalf("stream chunk flushed immediately: full syncs = %d, want %d before tick", got, firstFullSyncs)
	}
	_, _ = m.Update(perfTickAt(frameTickViewportSync, time.Now().Add(16*time.Millisecond)))

	if got := m.diag.ViewportFullSyncs; got != firstFullSyncs {
		t.Fatalf("tail stream chunk used full syncs = %d, want %d", got, firstFullSyncs)
	}
	if m.diag.ViewportIncrementalSyncs == 0 {
		t.Fatal("tail stream chunk should use incremental viewport sync")
	}
	if joined := strings.Join(m.viewportPlainLines, "\n"); !strings.Contains(joined, "hello world") {
		t.Fatalf("incremental sync did not update assistant text: %q", joined)
	}
}

func TestViewportSelectionRendersOnlyVisibleLines(t *testing.T) {
	m := newPerfTestModel()
	seedLongTranscript(m, 300)
	m.viewport.SetYOffset(120)
	version := m.lastViewportContentVersion
	fullSyncs := m.diag.ViewportFullSyncs

	m.selectionStart = textSelectionPoint{line: 122, col: 0}
	m.selectionEnd = textSelectionPoint{line: 126, col: 8}
	m.bumpViewportSelectionVersion()
	view := m.renderViewportView()

	if strings.TrimSpace(view) == "" {
		t.Fatal("selection render returned empty viewport")
	}
	if got := m.lastViewportContentVersion; got != version {
		t.Fatalf("selection render changed viewport content version = %d, want %d", got, version)
	}
	if got := m.diag.ViewportFullSyncs; got != fullSyncs {
		t.Fatalf("selection render triggered full syncs = %d, want %d", got, fullSyncs)
	}
	if m.diag.SelectionVisibleRenders == 0 {
		t.Fatal("selection render should be counted as visible-only")
	}
}

func TestDirtyViewportSyncFallsBackWhenRenderContextChanges(t *testing.T) {
	m := newPerfTestModel()
	seedLongTranscript(m, 80)

	block := NewAssistantBlock("assistant")
	block.Raw = "initial assistant text that should wrap at the original width"
	m.doc.Append(block)
	m.markViewportStructureDirty()
	m.syncViewportContent()
	fullSyncs := m.diag.ViewportFullSyncs
	incrementalSyncs := m.diag.ViewportIncrementalSyncs

	block.Raw += " plus more streamed text"
	m.markViewportBlockDirty(block.BlockID())
	m.viewport.SetWidth(36)
	m.syncViewportContent()

	if got := m.diag.ViewportFullSyncs; got <= fullSyncs {
		t.Fatalf("context change used no full sync: got %d, before %d", got, fullSyncs)
	}
	if got := m.diag.ViewportIncrementalSyncs; got != incrementalSyncs {
		t.Fatalf("context change used incremental syncs = %d, want %d", got, incrementalSyncs)
	}
}

func TestSubagentAttachMarksOrderAndAnchorDirty(t *testing.T) {
	m := newPerfTestModel()
	anchor := NewTranscriptBlock("▸ SPAWN helper task", tuikit.LineStyleTool)
	panel := NewSubagentPanelBlock("spawn-1", "", "helper", "")
	m.doc.Append(panel)
	m.doc.Append(anchor)
	m.syncViewportContent()
	m.viewportStructureDirty = false

	m.attachSubagentPanelToCall(panel, "call-1", "SPAWN", true)
	if !m.viewportStructureDirty {
		t.Fatal("attaching an existing panel after an anchor should mark viewport structure dirty")
	}

	m.viewportStructureDirty = false
	panel.Expanded = true
	m.syncInlineSubagentAnchorState(panel)
	if _, ok := m.dirtyViewportBlocks[anchor.BlockID()]; !ok {
		t.Fatal("inline anchor label change should mark the anchor block dirty")
	}
	if !strings.HasPrefix(strings.TrimSpace(anchor.Raw), "▾") {
		t.Fatalf("anchor label was not expanded: %q", anchor.Raw)
	}
}

func BenchmarkViewportSyncLongTranscript(b *testing.B) {
	m := newPerfTestModel()
	seedLongTranscript(m, 2000)
	for i := 0; i < b.N; i++ {
		m.syncViewportContent()
	}
}

func BenchmarkAssistantTailIncrementalSync(b *testing.B) {
	m := newPerfTestModel()
	seedLongTranscript(m, 2000)
	_, _ = m.handleStreamBlock("answer", "assistant", "hello", false)
	_, _ = m.Update(perfTickAt(frameTickViewportSync, time.Now()))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = m.handleStreamBlock("answer", "assistant", " x", false)
		_, _ = m.Update(perfTickAt(frameTickViewportSync, time.Now()))
	}
}

func BenchmarkAssistantActiveBufferLongStream(b *testing.B) {
	m := newPerfTestModel()
	seedLongTranscript(m, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = m.handleStreamBlock("answer", "assistant", "token ", false)
		_, _ = m.Update(perfTickAt(frameTickViewportSync, time.Now()))
	}
}

func BenchmarkToolOutputStream10kChunks(b *testing.B) {
	m := newPerfTestModel()
	block := m.ensureMainACPTurnBlock("session-1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		block.UpdateToolWithMeta("call-1", "RUN_COMMAND", "go test", "line\n", false, false, ToolUpdateMeta{TaskID: "task-1"})
		m.markViewportBlockDirty(block.BlockID())
		m.syncViewportContent()
	}
}

func BenchmarkVisibleSelectionRenderLongTranscript(b *testing.B) {
	m := newPerfTestModel()
	seedLongTranscript(m, 2000)
	m.viewport.SetYOffset(1000)
	m.selectionStart = textSelectionPoint{line: 1000, col: 0}
	m.selectionEnd = textSelectionPoint{line: 1018, col: 24}
	m.bumpViewportSelectionVersion()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.bumpViewportSelectionVersion()
		_ = m.renderViewportView()
	}
}

func BenchmarkRenderSchedulerMixedStreams(b *testing.B) {
	m := newPerfTestModel()
	m.cfg.StreamTickInterval = 16 * time.Millisecond
	seedLongTranscript(m, 2000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, msg := range []tea.Msg{
			perfGatewayNarrativeFrame("answer "),
			perfGatewayReasoningFrame("reason "),
			LogChunkMsg{Chunk: "log line\n"},
			perfGatewayNarrativeFrame("gateway "),
			perfTerminalFrame("terminal\n", int64(i+1)),
		} {
			updated, _, handled := m.dispatchRenderEvent(msg)
			if !handled {
				b.Fatalf("dispatchRenderEvent(%T) was not handled", msg)
			}
			m = updated.(*Model)
		}
		updated, _ := m.Update(perfTickAt(frameTickRenderDrain, time.Now()))
		m = updated.(*Model)
		updated, _ = m.Update(perfTickAt(frameTickViewportSync, time.Now()))
		m = updated.(*Model)
	}
}

func BenchmarkRenderInlineMarkdownPlainText(b *testing.B) {
	m := newPerfTestModel()
	text := strings.Repeat("plain text with a link https://example.test/path ", 16)
	base := m.theme.TextStyle()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderInlineMarkdown(text, base)
	}
}

func BenchmarkWrapNarrativeRowStyled(b *testing.B) {
	m := newPerfTestModel()
	row := RenderedRow{
		Styled:  strings.Repeat("assistant response with enough text to wrap cleanly ", 12),
		Plain:   strings.Repeat("assistant response with enough text to wrap cleanly ", 12),
		BlockID: "bench-block",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.wrapNarrativeRowStyled(row, 64)
	}
}

func BenchmarkRunningTickerText(b *testing.B) {
	m := newPerfTestModel()
	m.running = true
	text := "Review the latest tool output before sending follow-up guidance."
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.runningTick = uint64(i)
		_ = m.renderRunningTickerText(text)
	}
}

func newPerfTestModel() *Model {
	m := NewModel(Config{NoColor: true})
	m.theme = tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	m.width = 100
	m.height = 30
	m.ready = true
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(20)
	return m
}

func seedLongTranscript(m *Model, lines int) {
	for i := 0; i < lines; i++ {
		m.doc.Append(NewTranscriptBlock(fmt.Sprintf("* history-%04d with enough text to wrap occasionally", i), tuikit.LineStyleAssistant))
	}
	m.syncViewportContent()
}

func perfTickAt(kind frameTickKind, at time.Time) tea.Msg {
	return frameTickMsg{kind: kind, at: at}
}

func perfGatewayNarrativeFrame(text string) kernel.EventEnvelope {
	return kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
			SessionRef: session.SessionRef{SessionID: "session-1"},
			Narrative: &kernel.NarrativePayload{
				Role:       kernel.NarrativeRoleAssistant,
				Text:       text,
				Visibility: string(session.VisibilityUIOnly),
				UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				Scope:      kernel.EventScopeMain,
			},
		},
	}
}

func perfGatewayReasoningFrame(text string) kernel.EventEnvelope {
	return kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
			SessionRef: session.SessionRef{SessionID: "session-1"},
			Narrative: &kernel.NarrativePayload{
				Role:          kernel.NarrativeRoleAssistant,
				ReasoningText: text,
				Visibility:    string(session.VisibilityUIOnly),
				UpdateType:    string(session.ProtocolUpdateTypeAgentThought),
				Scope:         kernel.EventScopeMain,
			},
		},
	}
}

func perfTerminalFrame(text string, cursor int64) kernel.EventEnvelope {
	_ = cursor
	return kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
			SessionRef: session.SessionRef{SessionID: "session-1"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				Status:   kernel.ToolStatusRunning,
				Content:  testTerminalContentWithID(text, "terminal-1"),
			},
		},
	}
}
