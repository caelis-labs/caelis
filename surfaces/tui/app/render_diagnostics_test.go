package tuiapp

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestRenderDiagnosticsCountsMessageLaneAndViewportSetContent(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(20)

	updated, cmd := m.Update(LogChunkMsg{Chunk: "hello\n"})
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("LogChunkMsg should schedule a viewport sync")
	}
	updated, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	m = updated.(*Model)

	if m.diag.UpdateMessagesByLane[renderLaneLog] == 0 {
		t.Fatal("log lane update counter was not incremented")
	}
	if m.diag.ViewportSetContentLines == 0 {
		t.Fatal("SetContentLines counter was not incremented")
	}
	if m.diag.ViewportSetContentReason["full_sync"] == 0 && m.diag.ViewportSetContentReason["incremental_sync"] == 0 {
		t.Fatalf("missing SetContentLines reason counts: %#v", m.diag.ViewportSetContentReason)
	}
	if m.diag.ViewportSetContentLineCount == 0 {
		t.Fatal("SetContentLines line counter was not incremented")
	}
	if m.diag.ViewportSetContentBytes == 0 {
		t.Fatal("SetContentLines byte counter was not incremented")
	}
	if m.diag.BlockRenderCallsByKind[BlockTranscript] == 0 {
		t.Fatal("transcript block render counter was not incremented")
	}
}

func TestRenderDiagnosticsCountsSmoothingFlushReason(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	_, _ = m.enqueueMainDelta("answer", "assistant", "hello", false)

	m.flushAllPendingStreamSmoothingWithReason("semantic_barrier")

	if got := m.diag.StreamSmoothingFlushReason["semantic_barrier"]; got != 1 {
		t.Fatalf("semantic_barrier flush count = %d, want 1", got)
	}
}

func TestRenderDiagnosticsCountsOneRenderPerViewportEntry(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(20)
	m.diag.BlockRenderCallsByKind = make(map[BlockKind]uint64)

	m.doc.Append(NewTranscriptBlock("one line", tuikit.LineStyleDefault))
	m.markViewportStructureDirty()
	m.syncViewportContent()

	if got := m.diag.BlockRenderCallsByKind[BlockTranscript]; got != 1 {
		t.Fatalf("transcript block render calls = %d, want 1", got)
	}
}

func TestRenderDiagnosticsCountsMarkdownGlamourAndStatusCallbacks(t *testing.T) {
	m := NewModel(Config{
		NoColor: true,
		RefreshStatus: func() (string, string) {
			return "test-model", "test-context"
		},
	})
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(20)

	statusBefore := m.diag.ControlStatusCalls
	m.handleStatusTickMsg()
	if got := m.diag.ControlStatusCalls; got <= statusBefore {
		t.Fatalf("driver status callback calls = %d, want > %d", got, statusBefore)
	}

	_ = m.renderInlineMarkdown("plain **bold** text", m.theme.TextStyle())
	if got := m.diag.InlineMarkdownCalls; got == 0 {
		t.Fatal("inline markdown render counter was not incremented")
	}

	block := NewAssistantBlock("assistant")
	block.Raw = "**bold** answer"
	block.Streaming = false
	m.doc.Append(block)
	m.markViewportStructureDirty()
	m.syncViewportContent()
	if got := m.diag.GlamourRenderCalls; got == 0 {
		t.Fatal("glamour render counter was not incremented")
	}
}

func TestStableMarkdownWheelScrollDoesNotRerenderDocument(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 96, Height: 24})
	m = updated.(*Model)
	block := NewAssistantBlock("assistant")
	block.Raw = strings.Repeat("## 中文标题\n\n这是一段稳定的 Markdown 内容，用于验证滚动路径。\n\n", 24)
	block.Streaming = false
	m.doc.Append(block)
	m.markViewportStructureDirty()
	m.syncViewportContent()
	m.viewport.GotoBottom()
	m.refreshViewportFollowStateFromOffset()
	_ = m.View()

	offsetBefore := m.viewport.YOffset()
	glamourBefore := m.diag.GlamourRenderCalls
	blockRendersBefore := m.diag.BlockRenderCallsByKind[BlockAssistant]
	updated, _ = m.handleMouse(tea.MouseWheelMsg(tea.Mouse{
		Button: tea.MouseWheelUp,
		X:      m.mainColumnX() + tuikit.GutterNarrative + 2,
		Y:      1,
	}))
	m = updated.(*Model)
	_ = m.View()

	if got := m.viewport.YOffset(); got >= offsetBefore {
		t.Fatalf("wheel did not move stable Markdown viewport up: offset %d, before %d", got, offsetBefore)
	}
	if got := m.diag.GlamourRenderCalls; got != glamourBefore {
		t.Fatalf("stable Markdown wheel reran Glamour: calls %d, before %d", got, glamourBefore)
	}
	if got := m.diag.BlockRenderCallsByKind[BlockAssistant]; got != blockRendersBefore {
		t.Fatalf("stable Markdown wheel rerendered assistant block: calls %d, before %d", got, blockRendersBefore)
	}
}

func TestStableViewNormalizesFullscreenFrameOnce(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 96, Height: 24})
	m = updated.(*Model)

	before := m.diag.FullscreenNormalizeCalls
	_ = m.View()

	if got := m.diag.FullscreenNormalizeCalls - before; got != 1 {
		t.Fatalf("stable View fullscreen normalization calls = %d, want 1", got)
	}
}

func TestOverlayViewNormalizesBaseAndFinalFrame(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 96, Height: 24})
	m = updated.(*Model)
	m.activePrompt = newPromptState(PromptRequestMsg{
		Title:    "approval",
		Choices:  []PromptChoice{{Label: "allow", Value: "allow"}},
		Response: make(chan PromptResponse, 1),
	})

	before := m.diag.FullscreenNormalizeCalls
	_ = m.View()

	if got := m.diag.FullscreenNormalizeCalls - before; got != 2 {
		t.Fatalf("overlay View fullscreen normalization calls = %d, want 2", got)
	}
}

func TestRenderDiagnosticsWritesDebugFile(t *testing.T) {
	path := t.TempDir() + "/render-diagnostics.json"
	m := NewModel(Config{
		NoColor:              true,
		DiagnosticsDebugFile: path,
	})

	m.observeRender(time.Millisecond, 42, "incremental")

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read diagnostics debug file: %v", err)
	}
	text := string(payload)
	if !strings.Contains(text, `"Frames": 1`) {
		t.Fatalf("diagnostics debug file missing frame count: %s", text)
	}
	if !strings.Contains(text, `"RenderBytes": 42`) {
		t.Fatalf("diagnostics debug file missing render bytes: %s", text)
	}
}

func TestRenderDiagnosticsDebugFileIsRateLimited(t *testing.T) {
	path := t.TempDir() + "/render-diagnostics.json"
	m := NewModel(Config{
		NoColor:              true,
		DiagnosticsDebugFile: path,
	})

	m.observeRender(time.Millisecond, 42, "incremental")
	m.observeRender(time.Millisecond, 99, "incremental")

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read diagnostics debug file: %v", err)
	}
	text := string(payload)
	if strings.Contains(text, `"Frames": 2`) {
		t.Fatalf("diagnostics debug file was rewritten inside rate limit window: %s", text)
	}
	if !strings.Contains(text, `"Frames": 1`) {
		t.Fatalf("diagnostics debug file missing initial frame count: %s", text)
	}
}
