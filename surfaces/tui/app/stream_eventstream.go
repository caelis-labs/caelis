package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

func (m *Model) handleACPEventEnvelope(env eventstream.Envelope) (tea.Model, tea.Cmd) {
	if env.Err != nil || env.Kind == eventstream.KindError {
		return m, nil
	}
	if eventstream.IsTerminalLifecycle(env) {
		if !m.turnRunning() && !terminalLifecycleHasTranscriptIdentity(env) {
			return m, nil
		}
		model, cmd := m.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: ProjectACPEventToTranscriptEvents(env)})
		if next, ok := model.(*Model); ok {
			m = next
		}
		finishCmd, _ := m.finishLiveTurnFromEnvelope(env)
		return m, tea.Batch(cmd, finishCmd)
	}
	model, cmd := m.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: ProjectACPEventToTranscriptEvents(env)})
	return model, tea.Batch(m.applyACPApprovalReviewHint(env), cmd)
}

func terminalLifecycleHasTranscriptIdentity(env eventstream.Envelope) bool {
	return strings.TrimSpace(env.SessionID) != "" ||
		strings.TrimSpace(env.ScopeID) != "" ||
		strings.TrimSpace(env.ParticipantID) != "" ||
		strings.TrimSpace(env.Actor) != ""
}

func (m *Model) appendEventStreamTranscriptText(text string) (tea.Model, tea.Cmd) {
	text = strings.TrimSpace(text)
	if text == "" {
		return m, nil
	}
	m.finalizeAssistantBlock()
	m.finalizeReasoningBlock()
	block := NewTranscriptBlock(text, tuikit.DetectLineStyle(text))
	m.doc.Append(block)
	m.hasCommittedLine = true
	m.lastCommittedStyle = block.Style
	m.syncViewportContent()
	return m, nil
}

func (m *Model) applyACPApprovalReviewHint(env eventstream.Envelope) tea.Cmd {
	if m == nil || env.Kind != eventstream.KindApprovalReview || env.ApprovalReview == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(env.ApprovalReview.Status)) {
	case "in_progress":
		msg := ApprovalReviewHintMsg{
			Text:    approvalReviewPendingHint(env.ApprovalReview.ToolName, env.ApprovalReview.RawInput, 0),
			Pending: true,
		}
		m.handleApprovalReviewHintMsg(msg)
		return m.resumeRunningAnimationIfNeeded()
	case "approved", "denied", "timed_out", "failed":
		m.handleApprovalReviewHintMsg(ApprovalReviewHintMsg{})
		return nil
	default:
		return nil
	}
}
