package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) handleACPEventEnvelope(env eventstream.Envelope) (tea.Model, tea.Cmd) {
	m.observeTaskStreamSession(env)
	if env.Err != nil || env.Kind == eventstream.KindError {
		return m, nil
	}
	if m.suppressPairedCompactNotice(env) {
		return m, nil
	}
	if isMainTurnTerminalLifecycle(env) {
		if !m.turnRunning() && !terminalLifecycleHasTranscriptIdentity(env) {
			return m, nil
		}
		model, cmd := m.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: m.projectACPEventToTranscriptEvents(env)})
		if next, ok := model.(*Model); ok {
			m = next
		}
		finishCmd, _ := m.finishLiveTurnFromEnvelope(env)
		return m, tea.Batch(cmd, finishCmd)
	}
	model, cmd := m.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: m.projectACPEventToTranscriptEvents(env)})
	if next, ok := model.(*Model); ok {
		m = next
	}
	m.observeTaskStreamAnchor(env)
	return m, tea.Batch(m.applyACPRunningActivity(env), cmd)
}

type compactNoticeSource uint8

const (
	compactNoticeSourceCanonical compactNoticeSource = iota + 1
	compactNoticeSourceTransient
)

// suppressPairedCompactNotice folds the durable compact projection and the
// Runtime's transient success notice into one TUI row. Each unmatched signal
// is still rendered, so SDK-only streams and repeated compactions remain
// visible once per actual compaction.
func (m *Model) suppressPairedCompactNotice(env eventstream.Envelope) bool {
	if m == nil || (env.Scope != "" && env.Scope != eventstream.ScopeMain) {
		return false
	}
	source := compactNoticeSource(0)
	switch {
	case env.Kind == eventstream.KindSessionUpdate && eventstream.UpdateType(env.Update) == schema.UpdateCompact:
		source = compactNoticeSourceCanonical
	case env.Kind == eventstream.KindNotice && strings.TrimSpace(env.Notice) == transcript.CompactNoticeLabel:
		source = compactNoticeSourceTransient
	default:
		return false
	}
	turnID := strings.TrimSpace(env.TurnID)
	if turnID == "" {
		return false
	}
	key := strings.TrimSpace(env.SessionID) + "\x00" + turnID
	if m.compactNoticePair.key != key {
		m.compactNoticePair = compactNoticePairState{key: key}
	}
	switch source {
	case compactNoticeSourceCanonical:
		if m.compactNoticePair.transientUnmatched > 0 {
			m.compactNoticePair.transientUnmatched--
			return true
		}
		m.compactNoticePair.canonicalUnmatched++
	case compactNoticeSourceTransient:
		if m.compactNoticePair.canonicalUnmatched > 0 {
			m.compactNoticePair.canonicalUnmatched--
			return true
		}
		m.compactNoticePair.transientUnmatched++
	}
	return false
}

func terminalLifecycleHasTranscriptIdentity(env eventstream.Envelope) bool {
	return strings.TrimSpace(env.SessionID) != "" ||
		strings.TrimSpace(env.ScopeID) != "" ||
		strings.TrimSpace(env.ParticipantID) != "" ||
		strings.TrimSpace(env.Actor) != ""
}

func (m *Model) appendEventStreamTranscriptText(text string) (tea.Model, tea.Cmd) {
	text = formatTranscriptNoticeText(text)
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

func (m *Model) applyACPRunningActivity(env eventstream.Envelope) tea.Cmd {
	if m == nil || env.Kind != eventstream.KindApprovalReview || env.ApprovalReview == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(env.ApprovalReview.Status)) {
	case "in_progress":
		msg := RunningActivityMsg{
			Kind:   runningActivityApprovalReview,
			Detail: approvalReviewPendingHint(env.ApprovalReview.ToolName, env.ApprovalReview.RawInput, 0),
			Active: true,
		}
		m.handleRunningActivityMsg(msg)
		return m.resumeRunningAnimationIfNeeded()
	case "approved", "denied", "timed_out", "failed":
		m.handleRunningActivityMsg(RunningActivityMsg{Kind: runningActivityApprovalReview})
		return nil
	default:
		return nil
	}
}
