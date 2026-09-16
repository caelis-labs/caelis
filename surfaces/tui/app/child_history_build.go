package tuiapp

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

// Child snapshots mutate a detached narrative only. The mounted pane, its
// directory state and its cursor remain unchanged until the final page commits.
func (m *Model) handleChildHistoryPage(msg taskStreamBatchMsg) (bool, tea.Cmd) {
	if msg.phase != taskstream.DeliveryReplaceBegin && msg.phase != taskstream.DeliveryReplacePage && msg.phase != taskstream.DeliveryReplaceEnd {
		return false, nil
	}
	callID := m.taskStreamCallIDsByID[msg.taskID]
	view := m.subagentOutputViews[callID]
	if view == nil {
		return true, nil
	}
	switch msg.phase {
	case taskstream.DeliveryReplaceBegin:
		now := time.Now()
		if since := m.taskStreamFollowing[msg.taskID]; !since.IsZero() && now.Sub(since) >= 5*time.Second {
			m.noteTaskStreamFollowing(msg.taskID, now)
		}
		m.taskStreamRecoveryDeadline(callID, now)
		delete(m.taskStreamFollowing, msg.taskID)
		m.cancelEarlierHistory(callID)
		build := *view
		build.history, build.pane = nil, nil
		build.resetForReplacement()
		view.history = &build
	case taskstream.DeliveryReplacePage:
		if view.history == nil {
			return true, nil
		}
		for _, envelope := range msg.events {
			for _, event := range m.projectACPEventToTranscriptEvents(envelope) {
				if eventTargetsSubagentOutputView(event) && event.AnchorToolCallID == callID {
					view.history.observeChildEvent(event)
				}
			}
		}
	case taskstream.DeliveryReplaceEnd:
		build := view.history
		if build == nil {
			return true, nil
		}
		// Parent-feed decisions can arrive after replacement began, even after
		// the matching tool's page. Join current decisions before committing.
		build.approvalReviews = view.approvalReviews.clone()
		build.restoreChildReviews()
		view.history = nil
		view.historyBefore = msg.before
		view.document, view.turnBlocks, view.turnID = build.document, build.turnBlocks, build.turnID
		view.block, view.activity = build.block, build.activity
		view.seenProjections, view.liveNarratives = build.seenProjections, build.liveNarratives
		view.renderCache = subagentOutputRenderCache{}
		view.historyResolved = true
		view.liveActivityID = taskStreamActivityKey(msg.activityID)
		m.taskStreamCursors[msg.taskID] = msg.cursor
		m.noteTaskStreamFollowing(msg.taskID, time.Now())
		view.touch(true)
		m.reconcileTaskStreamOwner(callID, view.taskHandle)
		return true, m.requestSubagentOutputRender()
	}
	return true, nil
}
