package tuiapp

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

func (m *Model) handleACPEventEnvelope(env eventstream.Envelope) (tea.Model, tea.Cmd) {
	if env.Err != nil {
		if isUserInterruptError(env.Err) {
			model, cmd := m.handleTaskResultMsg(TaskResultMsg{Err: env.Err, Interrupted: true})
			return model, cmd
		}
		model, cmd := m.handleTaskResultMsg(TaskResultMsg{Err: env.Err})
		return model, cmd
	}
	if env.Kind == eventstream.KindError && env.Error != "" {
		model, cmd := m.handleTaskResultMsg(TaskResultMsg{Err: errors.New(env.Error)})
		return model, cmd
	}
	model, cmd := m.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: ProjectACPEventToTranscriptEvents(env)})
	return model, tea.Batch(m.applyACPApprovalReviewHint(env), cmd)
}

func isUserInterruptError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return text == "context canceled" || strings.Contains(text, "context canceled")
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
		tool := firstNonEmpty(strings.TrimSpace(env.ApprovalReview.ToolName), "approval request")
		msg := ApprovalReviewHintMsg{Text: "Reviewing approval request: " + tool, Pending: true}
		m.handleApprovalReviewHintMsg(msg)
		return m.resumeRunningAnimationIfNeeded()
	case "approved", "denied", "timed_out", "failed":
		m.handleApprovalReviewHintMsg(ApprovalReviewHintMsg{})
		return nil
	default:
		return nil
	}
}
