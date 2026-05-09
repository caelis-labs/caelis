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

func TestSideACPSubmissionRestoresFollowTail(t *testing.T) {
	m := newPerfTestModel()
	m.setCommands(append(DefaultCommands(), "codex"))
	seedLongTranscript(m, 100)
	m.viewport.SetYOffset(20)
	m.setViewportFollowState(viewportPinnedHistory)

	updated, _ := m.submitLine("/codex inspect the failing test")
	m = updated.(*Model)

	if m.viewportFollowState != viewportFollowTail {
		t.Fatalf("follow state = %v, want follow tail for side ACP submission", m.viewportFollowState)
	}
	if !m.viewport.AtBottom() {
		t.Fatalf("viewport y offset = %d, want bottom after side ACP submission", m.viewport.YOffset())
	}
}

func TestUnknownSlashSubmissionKeepsPinnedHistory(t *testing.T) {
	m := newPerfTestModel()
	m.setCommands(DefaultCommands())
	seedLongTranscript(m, 100)
	m.viewport.SetYOffset(20)
	m.setViewportFollowState(viewportPinnedHistory)
	before := m.viewport.YOffset()

	updated, _ := m.submitLine("/rbac/inner/workflow/switch Query 参数")
	m = updated.(*Model)

	if m.viewportFollowState != viewportPinnedHistory {
		t.Fatalf("follow state = %v, want pinned history for unknown slash prompt", m.viewportFollowState)
	}
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
