package tuiapp

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestSubmitFromPinnedHistoryRestoresFollowTail(t *testing.T) {
	m := newPerfTestModel()
	seedLongTranscript(m, 100)
	m.viewport.SetYOffset(20)
	m.setViewportFollowState(viewportPinnedHistory)

	updated, _ := m.submitLine("follow up")
	m = updated.(*Model)

	if m.viewportFollowState != viewportFollowTail {
		t.Fatalf("follow state = %v, want follow tail after prompt submission", m.viewportFollowState)
	}
	if !m.viewport.AtBottom() {
		t.Fatalf("viewport y offset = %d, want bottom after prompt submission", m.viewport.YOffset())
	}

	_, _ = m.handleStreamBlock("answer", "assistant", "new output", false)
	_, _ = m.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	if !m.viewport.AtBottom() {
		t.Fatalf("viewport y offset = %d, want stream to keep following bottom", m.viewport.YOffset())
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

func TestUnknownSlashSubmissionRestoresFollowTail(t *testing.T) {
	m := newPerfTestModel()
	m.setCommands(DefaultCommands())
	seedLongTranscript(m, 100)
	m.viewport.SetYOffset(20)
	m.setViewportFollowState(viewportPinnedHistory)

	updated, _ := m.submitLine("/rbac/inner/workflow/switch Query 参数")
	m = updated.(*Model)

	if m.viewportFollowState != viewportFollowTail {
		t.Fatalf("follow state = %v, want follow tail for unknown slash prompt", m.viewportFollowState)
	}
	if !m.viewport.AtBottom() {
		t.Fatalf("viewport y offset = %d, want bottom after unknown slash prompt", m.viewport.YOffset())
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
