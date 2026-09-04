package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

const (
	taskStreamHistoryLoadTimeout = 30 * time.Second
	taskStreamHistoryRetryLimit  = 3
)

type taskStreamHistoryBatchMsg struct {
	sessionID  string
	taskID     string
	token      uint64
	activityID string
	events     []eventstream.Envelope
}

type taskStreamHistoryClosedMsg struct {
	sessionID  string
	taskID     string
	token      uint64
	activityID string
	err        error
}

type taskStreamHistoryRetryMsg struct {
	sessionID  string
	taskID     string
	activityID string
}

// taskStreamHistoryRetryState is scoped to one authoritative directory
// activity. A completed retry budget is sticky for that activity so unrelated
// directory revisions cannot reopen the same failing ACP history load.
type taskStreamHistoryRetryState struct {
	activityID string
	attempts   int
	scheduled  bool
	exhausted  bool
}

// subagentOutputHistoryStage is a Surface-local, non-visible projection for
// one finite idle read. History never owns or replaces the live subscription;
// a successful activity-bound read swaps this document into the retained view
// in one Bubble Tea update.
type subagentOutputHistoryStage struct {
	token              uint64
	activityID         string
	expectedActivityID string
	responseActivityID string
	view               *subagentOutputView
}

func (m *Model) taskStreamHistoryInFlight(taskID string) bool {
	if m == nil {
		return false
	}
	taskID = strings.TrimSpace(taskID)
	retry := m.taskStreamHistoryRetries[taskID]
	return m.taskStreamHistoryTokens[taskID] != 0 ||
		m.taskStreamHistoryCancels[taskID] != nil ||
		m.taskStreamHistoryStages[taskID] != nil || retry.scheduled
}

// reconcileTaskStreamHistory hydrates complete idle history independently of
// the visible Runtime follower. Directory state chooses when a finite read is
// useful; it never carries child transcript content.
func (m *Model) reconcileTaskStreamHistory(taskID string) {
	if m == nil || m.cfg.TaskStreams == nil || m.cfg.ProgramSender == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	callID := strings.TrimSpace(m.taskStreamCallIDsByID[taskID])
	view := m.subagentOutputViews[callID]
	if taskID == "" || callID == "" || view == nil || m.subagentOutputOverlay == nil ||
		strings.TrimSpace(m.subagentOutputOverlay.callID) != callID {
		m.cancelTaskStreamHistory(taskID)
		return
	}
	descriptor, ok := m.subagentRosterTasks[callID]
	activityID := subagentRosterDescriptorActivityID(descriptor)
	if !ok || descriptor.Running ||
		!eventstream.IsTerminalLifecycleState(string(descriptor.State)) || activityID == "" {
		m.cancelTaskStreamHistory(taskID)
		return
	}
	if view.directoryActivityID == "" {
		view.directoryActivityID = activityID
	}
	if view.directoryActivityID != activityID {
		m.cancelTaskStreamHistory(taskID)
		return
	}
	if view.liveActivityID != "" && view.liveActivityID != activityID {
		// Content for a newer activity can beat the coalesced Directory snapshot.
		// Wait for Directory convergence instead of reopening the previous
		// activity's finite history and overwriting the live document.
		m.cancelTaskStreamHistory(taskID)
		return
	}
	retry := m.taskStreamHistoryRetries[taskID]
	if retry.activityID != "" && retry.activityID != activityID {
		delete(m.taskStreamHistoryRetries, taskID)
		retry = taskStreamHistoryRetryState{}
	}
	if retry.exhausted || retry.scheduled {
		return
	}
	// When a Runtime follower is already attached, its terminal lifecycle seals
	// the unstable Markdown tail first. Cold terminal workspaces have no live
	// follower and can hydrate immediately from ACP history.
	if m.taskStreamWanted[taskID] && view.block != nil &&
		!eventstream.IsTerminalLifecycleState(view.block.Status) {
		return
	}
	if m.subagentOutputTerminalContentSettled(callID, view) {
		return
	}
	if stage := m.taskStreamHistoryStages[taskID]; stage != nil {
		if stage.activityID == activityID && stage.token == m.taskStreamHistoryTokens[taskID] {
			return
		}
		m.cancelTaskStreamHistory(taskID)
	}
	m.startTaskStreamHistory(
		taskID, callID, activityID, strings.TrimSpace(descriptor.ActivityID), view,
	)
}

func (m *Model) startTaskStreamHistory(
	taskID, callID, activityID, expectedActivityID string,
	view *subagentOutputView,
) {
	if m == nil || view == nil {
		return
	}
	sessionID := strings.TrimSpace(m.currentSessionID)
	if sessionID == "" {
		return
	}
	m.taskStreamNextToken++
	token := m.taskStreamNextToken
	stage := &subagentOutputHistoryStage{
		token: token, activityID: activityID, expectedActivityID: expectedActivityID,
		view: newSubagentOutputHistoryView(view),
	}
	m.taskStreamHistoryTokens[taskID] = token
	m.taskStreamHistoryStages[taskID] = stage
	if retry := m.taskStreamHistoryRetries[taskID]; retry.activityID != activityID {
		m.taskStreamHistoryRetries[taskID] = taskStreamHistoryRetryState{activityID: activityID}
	}
	if !subagentOutputViewHasTranscript(view) {
		view.historyResolved = false
		view.touch(true)
	}

	cfg := m.cfg
	ctx, cancel := context.WithTimeout(
		cfg.ProgramSender.observationContext(cfg.Context),
		taskStreamHistoryLoadTimeout,
	)
	if previous := m.taskStreamHistoryCancels[taskID]; previous != nil {
		previous()
	}
	m.taskStreamHistoryCancels[taskID] = cancel
	started := cfg.ProgramSender.startForwarder(func() {
		defer cancel()
		events, responseActivityID, err := readTaskStreamHistory(ctx, cfg.TaskStreams, taskstream.ReadRequest{
			SessionID: sessionID, TaskID: taskID, ExpectedActivityID: expectedActivityID,
		})
		if err == nil && responseActivityID != expectedActivityID {
			err = errorcode.New(errorcode.Conflict, "task stream history activity changed")
		}
		if err == nil {
			for start := 0; start < len(events); start += taskStreamMailboxBatchSize {
				end := min(start+taskStreamMailboxBatchSize, len(events))
				cfg.ProgramSender.SendMsg(taskStreamHistoryBatchMsg{
					sessionID: sessionID, taskID: taskID, token: token,
					activityID: responseActivityID,
					events:     append([]eventstream.Envelope(nil), events[start:end]...),
				})
			}
		}
		cfg.ProgramSender.SendMsg(taskStreamHistoryClosedMsg{
			sessionID: sessionID, taskID: taskID, token: token,
			activityID: responseActivityID, err: err,
		})
	})
	if !started {
		cancel()
		delete(m.taskStreamHistoryCancels, taskID)
		delete(m.taskStreamHistoryTokens, taskID)
		delete(m.taskStreamHistoryStages, taskID)
		retry := m.taskStreamHistoryRetries[taskID]
		retry.activityID = activityID
		retry.scheduled = false
		retry.exhausted = true
		m.taskStreamHistoryRetries[taskID] = retry
		view.historyResolved = true
		view.touch(true)
	}
}

func (m *Model) handleTaskStreamHistoryBatch(msg taskStreamHistoryBatchMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.sessionID != m.currentSessionID ||
		m.taskStreamHistoryTokens[msg.taskID] != msg.token {
		return m, nil
	}
	stage := m.taskStreamHistoryStages[msg.taskID]
	if stage == nil || stage.token != msg.token || stage.view == nil {
		return m, nil
	}
	if msg.activityID != stage.expectedActivityID {
		return m, nil
	}
	stage.responseActivityID = msg.activityID
	for _, envelope := range msg.events {
		m.observeSubagentOutputHistoryEnvelope(stage, envelope)
	}
	stage.view.historyResolved = true
	return m, nil
}

func readTaskStreamHistory(ctx context.Context, client taskstream.Client, request taskstream.ReadRequest) ([]eventstream.Envelope, string, error) {
	assembler := &taskstream.DeliveryAssembler{}
	events := make([]eventstream.Envelope, 0)
	activityID := ""
	cursor := strings.TrimSpace(request.Cursor)
	for {
		request.Cursor = cursor
		result, err := client.Events(ctx, request)
		if err != nil {
			return nil, activityID, err
		}
		if id := strings.TrimSpace(result.ActivityID); id != "" {
			activityID = id
		}
		nextCursor := cursor
		hadExactEvents := false
		committedReplacement := false
		for _, delivery := range result.Deliveries {
			visible, replacement, acceptErr := assembler.Accept(delivery)
			if acceptErr != nil {
				return nil, activityID, acceptErr
			}
			if delivery.Source == taskstream.SourceExact && len(visible) > 0 {
				hadExactEvents = true
			}
			if replacement {
				events = visible
				committedReplacement = true
			} else {
				events = append(events, visible...)
			}
			if candidate := strings.TrimSpace(delivery.NextCursor); candidate != "" {
				nextCursor = candidate
			}
		}
		if assembler.Pending() {
			return nil, activityID, errorcode.New(errorcode.Unavailable, "Task replacement ended before commit")
		}
		if committedReplacement || !hadExactEvents || nextCursor == cursor {
			return events, activityID, nil
		}
		cursor = nextCursor
	}
}

func (m *Model) handleTaskStreamHistoryClosed(msg taskStreamHistoryClosedMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.sessionID != m.currentSessionID ||
		m.taskStreamHistoryTokens[msg.taskID] != msg.token {
		return m, nil
	}
	if cancel := m.taskStreamHistoryCancels[msg.taskID]; cancel != nil {
		cancel()
		delete(m.taskStreamHistoryCancels, msg.taskID)
	}
	delete(m.taskStreamHistoryTokens, msg.taskID)
	stage := m.taskStreamHistoryStages[msg.taskID]
	activityID := ""
	if stage != nil && stage.token == msg.token {
		activityID = stage.activityID
		stage.responseActivityID = msg.activityID
	}
	callID := strings.TrimSpace(m.taskStreamCallIDsByID[msg.taskID])
	view := m.subagentOutputViews[callID]
	if msg.err == nil {
		delete(m.taskStreamHistoryRetries, msg.taskID)
		if m.installTaskStreamHistoryStage(msg.taskID, msg.token, callID, view) {
			return m, m.requestSubagentOutputRender()
		}
		delete(m.taskStreamHistoryStages, msg.taskID)
		m.reconcileTaskStreamHistory(msg.taskID)
		return m, nil
	}
	delete(m.taskStreamHistoryStages, msg.taskID)
	if errors.Is(msg.err, context.Canceled) {
		return m, nil
	}
	retry := m.taskStreamHistoryRetries[msg.taskID]
	if retry.activityID != activityID {
		retry = taskStreamHistoryRetryState{activityID: activityID}
	}
	retry.attempts++
	if errorcode.Is(msg.err, errorcode.Conflict) {
		// A finite read that lost its Activity fence is stale, not unavailable.
		// Keep this activity's retry state closed until the Directory publishes
		// the successor, which resets the activity-scoped state.
		retry.scheduled = false
		retry.exhausted = true
		m.taskStreamHistoryRetries[msg.taskID] = retry
		if view != nil {
			view.historyResolved = true
			view.touch(true)
		}
		return m, nil
	}
	if taskStreamRetryable(msg.err) && activityID != "" &&
		m.taskStreamDemandForTaskID(msg.taskID) == taskStreamDemandVisibleSubagent &&
		subagentRosterDescriptorActivityID(m.subagentRosterTasks[callID]) == activityID &&
		retry.attempts <= taskStreamHistoryRetryLimit {
		retry.scheduled = true
		m.taskStreamHistoryRetries[msg.taskID] = retry
		return m, taskStreamHistoryRetryCmd(msg.sessionID, msg.taskID, activityID, retry.attempts)
	}
	retry.scheduled = false
	retry.exhausted = true
	m.taskStreamHistoryRetries[msg.taskID] = retry
	if view != nil {
		view.historyResolved = true
		view.touch(true)
	}
	handle := m.taskStreamHandlesByID[msg.taskID]
	return m, m.showHint(taskStreamHistoryUnavailableHint(handle, msg.err), hintOptions{
		priority: HintPriorityHigh, clearOnMessage: true, clearAfter: systemHintDuration,
	})
}

func (m *Model) handleTaskStreamHistoryRetry(msg taskStreamHistoryRetryMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.sessionID != m.currentSessionID {
		return m, nil
	}
	retry, ok := m.taskStreamHistoryRetries[msg.taskID]
	if !ok || !retry.scheduled || retry.exhausted || retry.activityID != msg.activityID {
		return m, nil
	}
	retry.scheduled = false
	m.taskStreamHistoryRetries[msg.taskID] = retry
	callID := strings.TrimSpace(m.taskStreamCallIDsByID[msg.taskID])
	if m.taskStreamDemandForTaskID(msg.taskID) != taskStreamDemandVisibleSubagent ||
		subagentRosterDescriptorActivityID(m.subagentRosterTasks[callID]) != msg.activityID ||
		m.taskStreamHistoryTokens[msg.taskID] != 0 ||
		m.taskStreamHistoryCancels[msg.taskID] != nil ||
		m.taskStreamHistoryStages[msg.taskID] != nil {
		delete(m.taskStreamHistoryRetries, msg.taskID)
		return m, nil
	}
	m.reconcileTaskStreamHistory(msg.taskID)
	return m, nil
}

func taskStreamHistoryRetryCmd(sessionID, taskID, activityID string, attempt int) tea.Cmd {
	return tea.Tick(taskStreamRetryBackoff(attempt), func(time.Time) tea.Msg {
		return taskStreamHistoryRetryMsg{sessionID: sessionID, taskID: taskID, activityID: activityID}
	})
}

func newSubagentOutputHistoryView(source *subagentOutputView) *subagentOutputView {
	if source == nil {
		return nil
	}
	block := NewParticipantTurnBlock(source.callID, source.actor)
	block.ParticipantID = source.taskHandle
	document := NewDocument()
	document.Append(block)
	return &subagentOutputView{
		callID: source.callID, taskHandle: source.taskHandle,
		participantID: source.participantID, actor: source.actor, title: source.title,
		document: document, turnBlocks: map[string]*ParticipantTurnBlock{}, block: block,
		directoryActivityID: source.directoryActivityID,
	}
}

func (m *Model) observeSubagentOutputHistoryEnvelope(stage *subagentOutputHistoryStage, envelope eventstream.Envelope) {
	if m == nil || stage == nil || stage.view == nil {
		return
	}
	for _, event := range m.projectACPEventToTranscriptEvents(envelope) {
		if !eventTargetsSubagentOutputView(event) ||
			strings.TrimSpace(event.AnchorToolCallID) != stage.view.callID {
			continue
		}
		stage.view.observeChildEvent(event)
	}
}

func (m *Model) installTaskStreamHistoryStage(taskID string, token uint64, callID string, live *subagentOutputView) bool {
	if m == nil || live == nil {
		return false
	}
	stage := m.taskStreamHistoryStages[strings.TrimSpace(taskID)]
	if stage == nil || stage.token != token || stage.view == nil {
		return false
	}
	descriptor, ok := m.subagentRosterTasks[strings.TrimSpace(callID)]
	activityID := subagentRosterDescriptorActivityID(descriptor)
	if !ok || descriptor.Running ||
		!eventstream.IsTerminalLifecycleState(string(descriptor.State)) ||
		activityID == "" || activityID != stage.activityID ||
		live.directoryActivityID != activityID ||
		(live.liveActivityID != "" && live.liveActivityID != activityID) ||
		stage.responseActivityID != stage.expectedActivityID ||
		strings.TrimSpace(descriptor.ActivityID) != stage.responseActivityID {
		return false
	}

	staged := stage.view
	live.document = staged.document
	live.turnBlocks = staged.turnBlocks
	live.turnID = staged.turnID
	live.block = staged.block
	live.seenProjections = staged.seenProjections
	live.historyResolved = true
	live.idleHistorySettled = true
	live.idleHistoryActivityID = activityID
	live.renderCache = subagentOutputRenderCache{}
	live.touch(true)
	delete(m.taskStreamHistoryStages, strings.TrimSpace(taskID))
	return true
}

func (m *Model) cancelTaskStreamHistory(taskID string) {
	if m == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	if cancel := m.taskStreamHistoryCancels[taskID]; cancel != nil {
		cancel()
		delete(m.taskStreamHistoryCancels, taskID)
	}
	if m.taskStreamHistoryTokens[taskID] != 0 || m.taskStreamHistoryStages[taskID] != nil {
		m.taskStreamNextToken++
	}
	delete(m.taskStreamHistoryTokens, taskID)
	delete(m.taskStreamHistoryStages, taskID)
	delete(m.taskStreamHistoryRetries, taskID)
}

func (m *Model) cancelTaskStreamHistoryForCallID(callID string) {
	if m == nil {
		return
	}
	if taskID := strings.TrimSpace(m.taskStreamIDsByCallID[strings.TrimSpace(callID)]); taskID != "" {
		m.cancelTaskStreamHistory(taskID)
	}
}

func taskStreamHistoryUnavailableHint(handle string, err error) string {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		handle = "task"
	}
	if err == nil {
		return fmt.Sprintf("Task %s history is unavailable", handle)
	}
	return fmt.Sprintf("Task %s history is unavailable: %v", handle, err)
}
