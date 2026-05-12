package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
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
