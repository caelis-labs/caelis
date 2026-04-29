package tuiapp

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
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

	statusBefore := m.diag.DriverStatusCalls
	m.handleStatusTickMsg()
	if got := m.diag.DriverStatusCalls; got <= statusBefore {
		t.Fatalf("driver status callback calls = %d, want > %d", got, statusBefore)
	}

	_ = m.renderInlineMarkdown("plain **bold** text", m.theme.TextStyle())
	if got := m.diag.InlineMarkdownCalls; got == 0 {
		t.Fatal("inline markdown render counter was not incremented")
	}

	block := NewAssistantBlock("assistant")
	block.Raw = "**bold** answer"
	m.doc.Append(block)
	m.markViewportStructureDirty()
	m.syncViewportContent()
	if got := m.diag.GlamourRenderCalls; got == 0 {
		t.Fatal("glamour render counter was not incremented")
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
