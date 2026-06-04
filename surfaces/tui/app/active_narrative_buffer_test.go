package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

func TestActiveAssistantBufferDoesNotMutateRawUntilFinal(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(20)

	_, _ = m.handleStreamBlock("answer", "assistant", "hello", false)
	block, ok := m.doc.Blocks()[0].(*AssistantBlock)
	if !ok {
		t.Fatalf("block = %T, want AssistantBlock", m.doc.Blocks()[0])
	}
	if got := block.Raw; got != "" {
		t.Fatalf("streaming assistant Raw = %q, want empty active buffer backing", got)
	}
	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	if joined := strings.Join(m.viewportPlainLines, "\n"); !strings.Contains(joined, "hello") {
		t.Fatalf("active buffer was not rendered: %q", joined)
	}

	_, _ = m.handleStreamBlock("answer", "assistant", " world", false)
	if got := block.Raw; got != "" {
		t.Fatalf("streaming assistant Raw after append = %q, want empty active buffer backing", got)
	}
	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	if joined := strings.Join(m.viewportPlainLines, "\n"); !strings.Contains(joined, "hello world") {
		t.Fatalf("active buffer append was not rendered: %q", joined)
	}

	_, _ = m.handleStreamBlock("answer", "assistant", "", true)
	if block.Streaming {
		t.Fatal("assistant block should be finalized")
	}
	if got := block.Raw; got != "hello world" {
		t.Fatalf("final assistant Raw = %q, want promoted active text", got)
	}
}

func TestActiveReasoningBufferDoesNotMutateRawUntilFinal(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(20)

	_, _ = m.handleStreamBlock("reasoning", "assistant", "think", false)
	block, ok := m.doc.Blocks()[0].(*ReasoningBlock)
	if !ok {
		t.Fatalf("block = %T, want ReasoningBlock", m.doc.Blocks()[0])
	}
	if got := block.Raw; got != "" {
		t.Fatalf("streaming reasoning Raw = %q, want empty active buffer backing", got)
	}

	_, _ = m.handleStreamBlock("reasoning", "assistant", " more", false)
	if got := block.Raw; got != "" {
		t.Fatalf("streaming reasoning Raw after append = %q, want empty active buffer backing", got)
	}

	_, _ = m.handleStreamBlock("reasoning", "assistant", "", true)
	if block.Streaming {
		t.Fatal("reasoning block should be finalized")
	}
	if got := block.Raw; got != "think more" {
		t.Fatalf("final reasoning Raw = %q, want promoted active text", got)
	}
}

func TestActiveReasoningBufferPreservesWhitespaceOnlyDeltas(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(20)

	_, _ = m.handleStreamBlock("reasoning", "assistant", "The", false)
	_, _ = m.handleStreamBlock("reasoning", "assistant", " ", false)
	_, _ = m.handleStreamBlock("reasoning", "assistant", "sandbox", false)
	block, ok := m.doc.Blocks()[0].(*ReasoningBlock)
	if !ok {
		t.Fatalf("block = %T, want ReasoningBlock", m.doc.Blocks()[0])
	}

	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	if joined := strings.Join(m.viewportPlainLines, "\n"); !strings.Contains(joined, "The sandbox") {
		t.Fatalf("active reasoning render = %q, want whitespace-only delta preserved", joined)
	}

	_, _ = m.handleStreamBlock("reasoning", "assistant", "", true)
	if got := block.Raw; got != "The sandbox" {
		t.Fatalf("final reasoning Raw = %q, want whitespace-only delta preserved", got)
	}
}

func TestScheduledReasoningStreamPreservesWhitespaceOnlyDeltas(t *testing.T) {
	m := NewModel(Config{NoColor: true, StreamTickInterval: 16 * time.Millisecond})
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(20)

	now := time.Now()
	for _, text := range []string{"The", " ", "sandbox"} {
		updated, _ := m.Update(perfGatewayReasoningFrame(text))
		m = updated.(*Model)
		updated, _ = m.Update(frameTickMsg{kind: frameTickRenderDrain, at: now})
		m = updated.(*Model)
		now = now.Add(16 * time.Millisecond)
	}

	block, ok := m.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("block = %T, want MainACPTurnBlock", m.doc.Blocks()[0])
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEReasoning {
		t.Fatalf("main ACP events = %#v, want one reasoning event", block.Events)
	}
	if got := block.Events[0].Text; got != "The sandbox" {
		t.Fatalf("scheduled reasoning text = %q, want whitespace-only delta preserved", got)
	}

	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: now})
	if joined := strings.Join(m.viewportPlainLines, "\n"); !strings.Contains(joined, "The sandbox") {
		t.Fatalf("scheduled active reasoning render = %q, want whitespace-only delta preserved", joined)
	}
}

func TestActiveNarrativeBufferDoesNotRerenderCompletedHistory(t *testing.T) {
	m := newPerfTestModel()
	seedLongTranscript(m, 120)

	_, _ = m.handleStreamBlock("answer", "assistant", "hello", false)
	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	beforeTranscriptRenders := m.diag.BlockRenderCallsByKind[BlockTranscript]
	beforeFullSyncs := m.diag.ViewportFullSyncs

	for range 20 {
		_, _ = m.handleStreamBlock("answer", "assistant", " token", false)
		_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	}

	if got := m.diag.BlockRenderCallsByKind[BlockTranscript]; got != beforeTranscriptRenders {
		t.Fatalf("completed transcript block renders = %d, want %d", got, beforeTranscriptRenders)
	}
	if got := m.diag.ViewportFullSyncs; got != beforeFullSyncs {
		t.Fatalf("active stream full syncs = %d, want %d", got, beforeFullSyncs)
	}
}

func TestActiveMarkdownStreamUsesIncrementalSyncWithoutPerChunkGlamour(t *testing.T) {
	m := newPerfTestModel()
	seedLongTranscript(m, 120)

	_, _ = m.handleStreamBlock("answer", "assistant", "```go\nfmt.Println(\"start\")\n", false)
	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	beforeFullSyncs := m.diag.ViewportFullSyncs
	beforeGlamourRenders := m.diag.GlamourRenderCalls
	beforeTranscriptRenders := m.diag.BlockRenderCallsByKind[BlockTranscript]

	for range 30 {
		_, _ = m.handleStreamBlock("answer", "assistant", "// token\n", false)
		_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	}

	if got := m.diag.BlockRenderCallsByKind[BlockTranscript]; got != beforeTranscriptRenders {
		t.Fatalf("completed transcript block renders = %d, want %d", got, beforeTranscriptRenders)
	}
	if got := m.diag.ViewportFullSyncs; got != beforeFullSyncs {
		t.Fatalf("active markdown stream full syncs = %d, want %d", got, beforeFullSyncs)
	}
	if got := m.diag.GlamourRenderCalls; got != beforeGlamourRenders {
		t.Fatalf("active markdown stream Glamour renders = %d, want unchanged %d", got, beforeGlamourRenders)
	}
	if m.diag.ViewportIncrementalSyncs == 0 {
		t.Fatal("active markdown stream should keep using incremental viewport sync")
	}
}

func TestACPNarrativeStreamsUseActiveBufferFastPath(t *testing.T) {
	cases := []struct {
		name               string
		apply              func(*Model, string) *Model
		active             func(*testing.T, *Model) *activeNarrativeBuffer
		blockKind          BlockKind
		wantInitialGlamour uint64
	}{
		{
			name: "main",
			apply: func(m *Model, text string) *Model {
				return applyTranscriptNarrativeTestChunk(m, ACPProjectionMain, "session-1", "", text)
			},
			active: func(t *testing.T, m *Model) *activeNarrativeBuffer {
				t.Helper()
				block, ok := m.doc.Blocks()[len(m.doc.Blocks())-1].(*MainACPTurnBlock)
				if !ok {
					t.Fatalf("last block = %T, want MainACPTurnBlock", m.doc.Blocks()[len(m.doc.Blocks())-1])
				}
				return activeBufferForEventKind(t, block.Events, SEAssistant)
			},
			blockKind:          BlockMainACPTurn,
			wantInitialGlamour: 1,
		},
		{
			name: "participant",
			apply: func(m *Model, text string) *Model {
				return applyTranscriptNarrativeTestChunk(m, ACPProjectionParticipant, "worker-1", "@codex", text)
			},
			active: func(t *testing.T, m *Model) *activeNarrativeBuffer {
				t.Helper()
				block, ok := m.doc.Blocks()[len(m.doc.Blocks())-1].(*ParticipantTurnBlock)
				if !ok {
					t.Fatalf("last block = %T, want ParticipantTurnBlock", m.doc.Blocks()[len(m.doc.Blocks())-1])
				}
				return activeBufferForEventKind(t, block.Events, SEAssistant)
			},
			blockKind:          BlockParticipantTurn,
			wantInitialGlamour: 1,
		},
		{
			name: "subagent",
			apply: func(m *Model, text string) *Model {
				return applyTranscriptNarrativeTestChunk(m, ACPProjectionSubagent, "spawn-1", "", text)
			},
			active: func(t *testing.T, m *Model) *activeNarrativeBuffer {
				t.Helper()
				block, ok := m.doc.Blocks()[len(m.doc.Blocks())-1].(*SubagentPanelBlock)
				if !ok {
					t.Fatalf("last block = %T, want SubagentPanelBlock", m.doc.Blocks()[len(m.doc.Blocks())-1])
				}
				return activeBufferForEventKind(t, block.Events, SEAssistant)
			},
			blockKind:          BlockSubagent,
			wantInitialGlamour: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newPerfTestModel()
			seedLongTranscript(m, 120)
			m = tc.apply(m, stablePrefixTailBenchmarkInitialText())

			if buffer := tc.active(t, m); buffer == nil || buffer.Empty() {
				t.Fatal("ACP narrative stream did not keep an active buffer")
			}
			if got := m.diag.GlamourRenderCalls; got != tc.wantInitialGlamour {
				t.Fatalf("initial stable prefix Glamour renders = %d, want %d", got, tc.wantInitialGlamour)
			}
			beforeGlamour := m.diag.GlamourRenderCalls
			beforeTranscriptRenders := m.diag.BlockRenderCallsByKind[BlockTranscript]
			beforeFullSyncs := m.diag.ViewportFullSyncs
			beforeIncrementalSyncs := m.diag.ViewportIncrementalSyncs
			beforeBlockRenders := m.diag.BlockRenderCallsByKind[tc.blockKind]

			for range 20 {
				m = tc.apply(m, " tail-token")
			}

			if got := m.diag.GlamourRenderCalls; got != beforeGlamour {
				t.Fatalf("tail chunks re-rendered stable prefix with Glamour: got %d, want %d", got, beforeGlamour)
			}
			if got := m.diag.BlockRenderCallsByKind[BlockTranscript]; got != beforeTranscriptRenders {
				t.Fatalf("completed transcript block renders = %d, want %d", got, beforeTranscriptRenders)
			}
			if got := m.diag.ViewportFullSyncs; got != beforeFullSyncs {
				t.Fatalf("tail chunks used full viewport syncs = %d, want %d", got, beforeFullSyncs)
			}
			if got := m.diag.ViewportIncrementalSyncs; got == beforeIncrementalSyncs {
				t.Fatal("tail chunks did not use incremental viewport sync")
			}
			if got := m.diag.BlockRenderCallsByKind[tc.blockKind]; got <= beforeBlockRenders {
				t.Fatalf("active ACP block renders = %d, want > %d", got, beforeBlockRenders)
			}
		})
	}
}

func TestActiveSubagentNarrativeHonorsPanelContentWidth(t *testing.T) {
	m := newPerfTestModel()
	ctx := m.blockRenderContext(80)
	contentWidth := 24
	panel := NewSubagentPanelBlock("spawn-1", "", "helper", "call-1")
	text := strings.Repeat("narrow panel wraps active output ", 4)

	panel.AppendStreamChunk(SEAssistant, text)
	activeLines := renderSubagentInnerLines(panel, ctx, contentWidth)
	assertRenderedLinesWithinWidth(t, activeLines, contentWidth)

	panel.ReplaceFinalStreamChunk(SEAssistant, text)
	finalLines := renderSubagentInnerLines(panel, ctx, contentWidth)
	assertRenderedLinesWithinWidth(t, finalLines, contentWidth)
}

func TestACPActiveAndFinalNarrativeKeepStablePrefixBodyWidth(t *testing.T) {
	m := newPerfTestModel()
	width := 36
	ctx := m.blockRenderContext(width)
	text := cjkStablePrefixTailText()

	active := NewMainACPTurnBlock("session-1")
	active.AppendStreamChunk(SEAssistant, text)
	activePlain := renderedPlainRows(active.Render(ctx))
	assertNarrativeRowsKeepFixedPrefixBodyWidth(t, activePlain, width)

	final := NewMainACPTurnBlock("session-1")
	final.ReplaceFinalStreamChunk(SEAssistant, text)
	final.SetStatus("completed", "", "", time.Now())
	finalPlain := renderedPlainRows(final.Render(ctx))
	assertNarrativeRowsKeepFixedPrefixBodyWidth(t, finalPlain, width)

	activeBodies := narrativeBodies(activePlain)
	finalBodies := narrativeBodies(finalPlain)
	if len(activeBodies) != len(finalBodies) {
		t.Fatalf("active/final narrative line count mismatch: active=%d final=%d\nactive=%q\nfinal=%q", len(activeBodies), len(finalBodies), strings.Join(activePlain, "\n"), strings.Join(finalPlain, "\n"))
	}
	for i := range activeBodies {
		if activeBodies[i] != finalBodies[i] {
			t.Fatalf("active/final narrative body line %d mismatch\nactive=%q\nfinal=%q\nactive all=%q\nfinal all=%q", i, activeBodies[i], finalBodies[i], strings.Join(activePlain, "\n"), strings.Join(finalPlain, "\n"))
		}
	}
}

func TestACPStreamFullFrameLinesStayWithinTerminalWidth(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	model, _ := m.Update(tea.WindowSizeMsg{Width: 54, Height: 18})
	m = model.(*Model)
	seedLongTranscript(m, 40)
	m.setViewportFollowState(viewportFollowTail)

	m = applyTranscriptNarrativeTestChunk(m, ACPProjectionMain, "session-1", "", cjkStablePrefixTailText())
	assertFullFrameLinesWithinTerminalWidth(t, m.View().Content, m.width)

	for range 8 {
		m = applyTranscriptNarrativeTestChunk(m, ACPProjectionMain, "session-1", "", "追加流式正文用于触发滚动与尾部重排。")
		assertFullFrameLinesWithinTerminalWidth(t, m.View().Content, m.width)
	}
}

func TestActiveTailViewportSyncDoesNotReplaceFullViewportContent(t *testing.T) {
	m := newPerfTestModel()
	seedLongTranscript(m, 120)
	m.viewport.SetHeight(5)
	m.syncViewportContent()

	_, _ = m.handleStreamBlock("answer", "assistant", "hello", false)
	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	setContentLines := m.diag.ViewportSetContentLines

	_, _ = m.handleStreamBlock("answer", "assistant", " world"+strings.Repeat("\nnext line", 8), false)
	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})

	if got := m.diag.ViewportSetContentLines; got != setContentLines {
		t.Fatalf("active tail SetContentLines calls = %d, want %d", got, setContentLines)
	}
	if view := m.renderViewportView(); !strings.Contains(view, "next line") {
		t.Fatalf("active tail was not rendered in visible viewport: %q", view)
	}
}

func TestActiveTailHitTestingUsesVisibleTailOffset(t *testing.T) {
	m := newPerfTestModel()
	seedLongTranscript(m, 120)
	m.viewport.SetHeight(5)
	m.syncViewportContent()

	_, _ = m.handleStreamBlock("answer", "assistant", "hello", false)
	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	_, _ = m.handleStreamBlock("answer", "assistant", " world"+strings.Repeat("\nnext line", 8), false)
	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})

	if !m.viewportContentStale {
		t.Fatal("test setup did not enter stale-tail rendering")
	}
	y := m.viewport.Height() - 1
	wantLine := m.viewportVisibleOffset() + y

	contentLine, ok := m.contentLineAtViewportY(y)
	if !ok {
		t.Fatal("content line hit test failed")
	}
	if contentLine != wantLine {
		t.Fatalf("content line = %d, want visible tail line %d", contentLine, wantLine)
	}

	point, ok := m.mousePointToContentPoint(m.mainColumnX()+tuikit.GutterNarrative, y, false)
	if !ok {
		t.Fatal("mouse point hit test failed")
	}
	if point.line != wantLine {
		t.Fatalf("mouse point line = %d, want visible tail line %d", point.line, wantLine)
	}
}

func cjkStablePrefixTailText() string {
	stableIntro := strings.Repeat("检查日志记录状态同步和渲染缓存确保每一步都有完整证据。", 6)
	stableList := strings.Join([]string{
		"- 检查活动缓冲区是否保持固定正文列",
		"- 检查流式尾部是否按照同一个宽度换行",
	}, "\n")
	tail := strings.Repeat("尾部继续追加中文正文用于验证行宽稳定和滚动重绘。", 5)
	return strings.Join([]string{stableIntro, "", stableList, "", tail}, "\n")
}

func applyTranscriptNarrativeTestChunk(m *Model, scope ACPProjectionScope, scopeID, actor, text string) *Model {
	model, _ := m.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind:          TranscriptEventNarrative,
		Scope:         scope,
		ScopeID:       scopeID,
		Actor:         actor,
		NarrativeKind: TranscriptNarrativeAssistant,
		Text:          text,
		OccurredAt:    time.Now(),
	}}})
	m = model.(*Model)
	model, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	return model.(*Model)
}

func activeBufferForEventKind(t *testing.T, events []SubagentEvent, kind SubagentEventKind) *activeNarrativeBuffer {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == kind {
			return events[i].ActiveBuffer
		}
	}
	t.Fatalf("events = %#v, want kind %d", events, kind)
	return nil
}

func assertNarrativeRowsKeepFixedPrefixBodyWidth(t *testing.T, rows []string, width int) {
	t.Helper()
	bodyWidth := width - displayColumns("· ")
	if bodyWidth <= 0 {
		t.Fatalf("width %d leaves no body column", width)
	}
	for _, line := range rows {
		plain := strings.TrimRight(ansi.Strip(line), " ")
		if strings.TrimSpace(plain) == "" {
			continue
		}
		body, ok := stripNarrativePrefixColumn(plain)
		if !ok {
			t.Fatalf("narrative row missing fixed prefix column: %q\nall=%q", plain, strings.Join(rows, "\n"))
		}
		if got := displayColumns(body); got > bodyWidth {
			t.Fatalf("narrative body width = %d, want <= %d\nline=%q\nall=%q", got, bodyWidth, plain, strings.Join(rows, "\n"))
		}
	}
}

func narrativeBodies(rows []string) []string {
	var bodies []string
	for _, line := range rows {
		plain := strings.TrimRight(ansi.Strip(line), " ")
		if strings.TrimSpace(plain) == "" {
			continue
		}
		body, ok := stripNarrativePrefixColumn(plain)
		if !ok {
			continue
		}
		bodies = append(bodies, body)
	}
	return bodies
}

func stripNarrativePrefixColumn(line string) (string, bool) {
	switch {
	case strings.HasPrefix(line, "· "):
		return strings.TrimRight(strings.TrimPrefix(line, "· "), " "), true
	case strings.HasPrefix(line, "› "):
		return strings.TrimRight(strings.TrimPrefix(line, "› "), " "), true
	case strings.HasPrefix(line, "  "):
		return strings.TrimRight(strings.TrimPrefix(line, "  "), " "), true
	default:
		return "", false
	}
}

func assertRenderedLinesWithinWidth(t *testing.T, lines []string, width int) {
	t.Helper()
	for _, line := range lines {
		if strings.TrimSpace(ansi.Strip(line)) == "" {
			continue
		}
		if got := displayColumns(ansi.Strip(line)); got > width {
			t.Fatalf("rendered line width = %d, want <= %d\nline=%q\nall=%q", got, width, ansi.Strip(line), strings.Join(lines, "\n"))
		}
	}
}

func assertFullFrameLinesWithinTerminalWidth(t *testing.T, frame string, width int) {
	t.Helper()
	for lineNo, line := range strings.Split(frame, "\n") {
		plain := ansi.Strip(line)
		if got := displayColumns(plain); got > width {
			t.Fatalf("frame line %d display width = %d, want <= %d\nline=%q\nframe=%q", lineNo, got, width, plain, frame)
		}
		if got := ansi.StringWidthWc(plain); got > width {
			t.Fatalf("frame line %d wc width = %d, want <= %d\nline=%q\nframe=%q", lineNo, got, width, plain, frame)
		}
	}
}
