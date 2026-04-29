package tuiapp

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestPinnedHistoryDoesNotJumpOnSubmitOrStream(t *testing.T) {
	m := newPerfTestModel()
	seedLongTranscript(m, 100)
	m.viewport.SetYOffset(20)
	m.setViewportFollowState(viewportPinnedHistory)

	before := m.viewport.YOffset()
	_, _ = m.submitLine("follow up")
	_, _ = m.handleStreamBlock("answer", "assistant", "new output", false)
	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})

	if got := m.viewport.YOffset(); got != before {
		t.Fatalf("viewport y offset = %d, want pinned offset %d", got, before)
	}
}

func TestEndKeyRestoresFollowTail(t *testing.T) {
	m := newPerfTestModel()
	seedLongTranscript(m, 100)
	m.viewport.SetYOffset(20)
	m.setViewportFollowState(viewportPinnedHistory)

	updated, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	m = updated.(*Model)
	if m.viewportFollowState != viewportFollowTail {
		t.Fatalf("follow state = %v, want follow tail", m.viewportFollowState)
	}
	if !m.viewport.AtBottom() {
		t.Fatalf("viewport y offset = %d, want bottom", m.viewport.YOffset())
	}
}
