package tuiapp

import (
	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"maps"
)

const sessionHistoryLoadingHint = "Loading session history…"

// A history build owns a private document, with no subscriptions or rendering.
// The Update loop feeds it bounded batches while the previous view stays usable.
type sessionHistoryPositionMsg struct{ before string }
type sessionHistoryResetMsg struct{}
type sessionHistoryReplacementMsg struct{ state appserver.SessionState }

type sessionHistoryBuild struct {
	start             sessionViewStartMsg
	model             *Model
	pendingNavigation bool
	before            string
}

func (m *Model) beginSessionHistory(start sessionViewStartMsg) tea.Cmd {
	m.cancelEarlierHistory("")
	cfg := m.cfg
	cfg.ProgramSender, cfg.TaskStreams, cfg.ControlService = nil, nil, nil
	cfg.NoAnimation, cfg.ShowWelcomeCard = true, false
	builder := newModelWithTheme(cfg, m.theme)
	builder.historyBuilding = true
	builder.beginDeferredViewportSync()
	builder.applySessionReconnectState(start.state)
	if start.state.SessionID == m.currentSessionID {
		builder.subagentRosterTasks = maps.Clone(m.subagentRosterTasks)
	}
	pending := m.sessionSwitchPending
	if m.sessionHistory != nil {
		pending = m.sessionHistory.pendingNavigation
	}
	m.sessionHistory = &sessionHistoryBuild{start: start, model: builder, pendingNavigation: pending}
	if !start.recovery {
		m.sessionSwitchPending = true
	}
	return m.showSessionHistoryLoading()
}

func (m *Model) showSessionHistoryLoading() tea.Cmd {
	if m.dismissWelcomeCard() {
		m.syncViewportContent()
	}
	m.removeHintsByText(sessionHistoryLoadingHint)
	return tea.Batch(
		m.showHint(sessionHistoryLoadingHint, hintOptions{priority: HintPriorityHigh}),
		m.scheduleSpinnerTick(),
	)
}

func (m *Model) abortSessionHistory() {
	m.removeHintsByText(sessionHistoryLoadingHint)
	if m.sessionHistory != nil {
		m.sessionHistoryFailed = true
	}
	m.sessionHistory = nil
	m.sessionSwitchPending = false
}

func (m *Model) commitSessionHistory() tea.Cmd {
	build := m.sessionHistory
	if build == nil {
		return nil
	}
	m.removeHintsByText(sessionHistoryLoadingHint)
	m.sessionHistory = nil
	m.sessionHistoryFailed = false
	m.sessionObservationRecovering = false
	m.removeHintsByText(sessionObservationRecoveryHint)
	m.sessionSwitchPending = (build.start.automatic || build.start.recovery) && build.pendingNavigation
	claimDraft := build.start.automatic && m.currentSessionID == ""
	if claimDraft {
		m.saveSessionDraft()
		m.sessionDrafts[build.start.state.SessionID] = m.sessionDrafts[""]
	}
	m.beginDeferredViewportSync()
	defer m.endDeferredViewportSync()
	var cmd tea.Cmd
	previousChildren := m.subagentOutputViews
	sameSession := m.currentSessionID == build.start.state.SessionID
	if build.start.recovery && m.currentSessionID == build.start.state.SessionID {
		cmd = m.restoreSessionObservationState(build.start.state)
	} else if !build.start.replacement {
		cmd = m.applySessionReconnectState(build.start.state)
		if build.start.recovery && build.start.state.Approval.Active != nil {
			m.sessionApprovalRefreshPending = string(build.start.state.Approval.Active.RequestID)
		}
	}
	if claimDraft {
		delete(m.sessionDrafts, "")
	}
	if build.start.state.SessionID == "" {
		return cmd
	}
	b := build.model
	m.sessionHistoryBefore = build.before
	m.doc = b.doc
	m.mainTimelineTailID = b.mainTimelineTailID
	m.mainAnchorBlockIDs = b.mainAnchorBlockIDs
	m.participantTurnIDs = b.participantTurnIDs
	m.activeParticipantTurnSessionID = b.activeParticipantTurnSessionID
	m.subagentOutputViews = b.subagentOutputViews
	if sameSession && (build.start.replacement || build.start.recovery) {
		// Directory-discovered children need not have a StartThread display
		// event in the parent history (for example, an external ACP controller).
		for callID, previous := range previousChildren {
			if m.subagentOutputViews[callID] == nil {
				m.subagentOutputViews[callID] = previous
			}
		}
		for callID, view := range m.subagentOutputViews {
			if previous := previousChildren[callID]; previous != nil && previous.taskHandle == view.taskHandle {
				m.subagentOutputViews[callID] = previous
			}
		}
	}
	m.planEntries = b.planEntries
	m.streamLine = b.streamLine
	m.pendingLogBuffer = b.pendingLogBuffer
	m.logStreamBuffer = b.logStreamBuffer
	m.transientBlockID = b.transientBlockID
	m.transientIsRetry = b.transientIsRetry
	m.transientRemove = b.transientRemove
	if !build.start.replacement {
		m.statusUsageTotal = b.statusUsageTotal
		m.statusUsageWindow = b.statusUsageWindow
		m.statusUsageControllerEpoch = b.statusUsageControllerEpoch
		m.statusUsageIdentityKnown = b.statusUsageIdentityKnown
		m.statusContext = b.statusContext
		m.statusView.Tokens = b.statusView.Tokens
	}
	m.refreshHistoryTailState()
	m.seedReconnectReplayExploration()
	m.markViewportStructureDirty()
	m.syncViewportContent()
	m.reconcileSubagentDirectoryViews()
	m.reconcileSubagentOutputTaskStreams()
	return tea.Batch(cmd, m.ensureSubagentDirectoryWatch())
}
