package tuiapp

import (
	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver"
)

// restoreSessionObservationState applies a fresh Control snapshot to the same
// Session without turning preserved drafts or pending prompts into new input.
func (m *Model) restoreSessionObservationState(state appserver.SessionState) tea.Cmd {
	m.sessionObservationRecovering = false
	m.removeHintsByText(sessionObservationRecoveryHint)
	m.closeTaskStreamSubscriptions()
	m.resetSubagentDirectoryWatch()
	m.statusRefreshInFlight = false
	m.runningInterruptRequested = false
	m.sandboxProgress = nil
	m.streamSmoothing = nil
	m.streamSmoothingTickScheduled = false
	m.runningHintTracker.resetSession()
	m.stopLiveTurn()
	m.liveTurn = liveTurnState{}
	m.setBotRunStatus(state.SessionID, state.Run.Status)

	head := ""
	if state.Approval.Active != nil {
		head = string(state.Approval.Active.RequestID)
	}
	var obsolete []string
	if m.activePrompt != nil && m.activePrompt.approvalRequestID != "" && m.activePrompt.approvalRequestID != head {
		obsolete = append(obsolete, m.activePrompt.approvalRequestID)
	}
	for _, prompt := range m.pendingPrompt {
		if prompt.ApprovalRequestID != "" && prompt.ApprovalRequestID != head {
			obsolete = append(obsolete, prompt.ApprovalRequestID)
		}
	}
	for _, id := range obsolete {
		m.dismissApproval(id)
	}
	m.sessionApprovalRefreshPending = head
	if state.Run.Active || state.Approval.Active != nil {
		m.beginLiveTurn(SubmissionModeDefault, true, state.Run.StartedAt)
		m.liveTurn.observed = true
	}
	return m.resumeRunningAnimationIfNeeded()
}

// An unchanged approval keeps its edited selection, but only the new feed may
// receive a future user decision. No buffered response is copied or replayed.
func (m *Model) refreshSessionApproval(req PromptRequestMsg) {
	if req.ApprovalRequestID == "" || req.ApprovalRequestID != m.sessionApprovalRefreshPending {
		if req.dismiss != nil {
			req.dismiss()
		}
		return
	}
	m.sessionApprovalRefreshPending = ""
	if prompt := m.activePrompt; prompt != nil && prompt.approvalRequestID == req.ApprovalRequestID {
		if prompt.dismiss != nil {
			prompt.dismiss()
		}
		prompt.response, prompt.dismiss = req.Response, req.dismiss
		return
	}
	for i, prompt := range m.pendingPrompt {
		if prompt.ApprovalRequestID == req.ApprovalRequestID {
			if prompt.dismiss != nil {
				prompt.dismiss()
			}
			m.pendingPrompt[i] = req
			return
		}
	}
	m.enqueuePrompt(req)
}
