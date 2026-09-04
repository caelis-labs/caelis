package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

const (
	taskStreamMailboxBatchSize = 64
	taskStreamMailboxBudget    = 16 * time.Millisecond
	taskStreamRetryDelay       = 250 * time.Millisecond
	taskStreamRetryBackoffCap  = 4
	taskStreamCleanExitRetries = 3
)

var errTaskStreamNotDiscoverable = errors.New("task stream is not discoverable yet")
var errTaskStreamUnexpectedClose = errors.New("following task stream ended unexpectedly")

type taskStreamDemand uint8

const (
	taskStreamDemandNone taskStreamDemand = iota
	taskStreamDemandExpandedPanel
	taskStreamDemandVisibleSubagent
)

func (d taskStreamDemand) wanted() bool {
	return d == taskStreamDemandExpandedPanel ||
		d == taskStreamDemandVisibleSubagent
}

type taskStreamOpenedMsg struct {
	sessionID    string
	taskID       string
	token        uint64
	subscription taskstream.Subscription
}

type taskStreamBatchMsg struct {
	sessionID   string
	taskID      string
	token       uint64
	events      []eventstream.Envelope
	cursor      string
	activityID  string
	replacement bool
}

type taskStreamClosedMsg struct {
	sessionID string
	taskID    string
	token     uint64
	cursor    string
	err       error
}

type taskStreamResolvedMsg struct {
	sessionID     string
	callID        string
	handle        string
	taskID        string
	participantID string
	descriptor    taskstream.TaskDescriptor
	token         uint64
	err           error
}

type taskStreamResolveRetryMsg struct {
	sessionID string
	callID    string
	handle    string
	token     uint64
}

type taskStreamSubscribeRetryMsg struct {
	sessionID string
	taskID    string
}

func (m *Model) observeTaskStreamSession(env eventstream.Envelope) {
	if m == nil || (env.Scope != "" && env.Scope != eventstream.ScopeMain) {
		return
	}
	sessionID := strings.TrimSpace(env.SessionID)
	if sessionID == "" || sessionID == m.currentSessionID {
		return
	}
	m.closeTaskStreamSubscriptions()
	m.subagentOutputOverlay = nil
	m.subagentRosterOverlay = nil
	m.subagentRosterPressed = false
	m.subagentOutputViews = map[string]*subagentOutputView{}
	m.resetSubagentDirectoryWatch()
	m.runningHintTracker.resetSession()
	if m.turnRunning() {
		// The first Envelope of a newly created Session discovers its durable
		// identity after beginLiveTurn has already started the response clock.
		// Rebuild the Session-scoped tracker without discarding that Turn clock.
		m.runningHintTracker.beginTurn(m.liveTurn.StartedAt)
	}
	m.refreshRunningActivity()
	m.currentSessionID = sessionID
}

// observeTaskPanelStreamOwner keeps live command output tied to the
// RunCommand panel that owns it. Task control calls are observers or mutations
// of an existing Task and never own a Task-stream subscription.
func (m *Model) observeTaskPanelStreamOwner(env eventstream.Envelope) {
	if m == nil || m.cfg.TaskStreams == nil || m.cfg.ProgramSender == nil ||
		(env.Scope != "" && env.Scope != eventstream.ScopeMain) {
		return
	}
	callID := taskStreamToolCallID(env.Update)
	if callID == "" {
		return
	}
	input, output := taskStreamToolValues(env.Update)
	handle := display.ToolTaskHandle(input, output, nil)
	if handle == "" {
		return
	}
	if m.subagentOutputViews[callID] != nil || !m.taskStreamPanelExpanded(callID, handle) {
		return
	}
	m.reconcileTaskStreamOwner(callID, handle)
}

func taskStreamToolValues(update eventstream.Update) (map[string]any, map[string]any) {
	var rawInput, rawOutput any
	switch typed := update.(type) {
	case eventstream.ToolCall:
		rawInput, rawOutput = typed.RawInput, typed.RawOutput
	case eventstream.ToolCallUpdate:
		rawInput, rawOutput = typed.RawInput, typed.RawOutput
	}
	input, _ := rawInput.(map[string]any)
	output, _ := rawOutput.(map[string]any)
	return input, output
}

func taskStreamToolCallID(update eventstream.Update) string {
	switch typed := update.(type) {
	case eventstream.ToolCall:
		return strings.TrimSpace(typed.ToolCallID)
	case eventstream.ToolCallUpdate:
		return strings.TrimSpace(typed.ToolCallID)
	default:
		return ""
	}
}

func taskStreamPanelHandle(events []SubagentEvent, callID string) string {
	callID = strings.TrimSpace(callID)
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Kind != SEToolCall || strings.TrimSpace(event.CallID) != callID ||
			event.Name != surfaceToolRunCommand {
			continue
		}
		if taskHandle := normalizeTaskStreamHandle(event.TaskHandle); taskHandle != "" {
			return taskHandle
		}
	}
	return ""
}

func (m *Model) taskStreamPanelExpanded(callID, handle string) bool {
	if m == nil || m.doc == nil {
		return false
	}
	handle = normalizeTaskStreamHandle(handle)
	for _, block := range m.doc.Blocks() {
		switch typed := block.(type) {
		case *MainACPTurnBlock:
			if taskStreamPanelHandle(typed.Events, callID) == handle {
				return typed.toolPanelExpanded(callID)
			}
		case *ParticipantTurnBlock:
			if taskStreamPanelHandle(typed.Events, callID) == handle {
				return typed.toolPanelExpanded(callID)
			}
		}
	}
	return false
}

func (m *Model) taskStreamDemandForOwner(callID, handle string) taskStreamDemand {
	if m == nil {
		return taskStreamDemandNone
	}
	callID = strings.TrimSpace(callID)
	handle = normalizeTaskStreamHandle(handle)
	if view := m.subagentOutputViews[callID]; view != nil {
		if m.subagentOutputOverlay != nil &&
			strings.TrimSpace(m.subagentOutputOverlay.callID) == callID {
			return taskStreamDemandVisibleSubagent
		}
		return taskStreamDemandNone
	}
	if m.taskStreamPanelExpanded(callID, handle) {
		return taskStreamDemandExpandedPanel
	}
	return taskStreamDemandNone
}

// subagentOutputTerminalContentSettled prevents a terminal child workspace
// from repeatedly reopening a finite idle read. Normally that read is the
// complete Agent-owned ACP history. If history reload is unavailable, Control
// may instead return the canonical terminal Task result; this Surface cache
// marker makes no completeness claim.
func (m *Model) subagentOutputTerminalContentSettled(callID string, view *subagentOutputView) bool {
	if m == nil || view == nil || !view.idleHistorySettled {
		return false
	}
	// Directory terminality may become visible before the final Task-stream
	// lifecycle frame. Keep an active workspace attached until its retained
	// transcript has consumed that frame; SetStatus then seals the same Markdown
	// buffers used by the main transcript.
	taskID := strings.TrimSpace(m.taskStreamIDsByCallID[strings.TrimSpace(callID)])
	awaitingVisibleLifecycle := taskID != "" && m.taskStreamWanted[taskID]
	if (awaitingVisibleLifecycle || subagentOutputViewHasTranscript(view)) &&
		(view.block == nil || !eventstream.IsTerminalLifecycleState(view.block.Status)) {
		return false
	}
	descriptor, ok := m.subagentRosterTasks[strings.TrimSpace(callID)]
	if !ok || descriptor.Running {
		return false
	}
	activityID := subagentRosterDescriptorActivityID(descriptor)
	if activityID == "" || view.idleHistoryActivityID != activityID {
		return false
	}
	return subagentOutputStatusFromState(string(descriptor.State)) != subagentOutputRunning
}

// reconcileSubagentOutputTaskStreams keeps Task output observation scoped to
// visible presentation demand. A hidden subagent workspace retains its
// Document and absolute cursor but owns no live subscription.
func (m *Model) reconcileSubagentOutputTaskStreams() {
	if m == nil {
		return
	}
	for callID, view := range m.subagentOutputViews {
		if view == nil {
			continue
		}
		m.reconcileTaskStreamOwner(callID, view.taskHandle)
	}
}

func (m *Model) reconcileTaskStreamOwner(callID, rawHandle string) {
	if m == nil || m.cfg.TaskStreams == nil || m.cfg.ProgramSender == nil {
		return
	}
	demand := m.taskStreamDemandForOwner(callID, rawHandle)
	wanted := demand.wanted()
	callID = strings.TrimSpace(callID)
	handle := normalizeTaskStreamHandle(rawHandle)
	if callID == "" {
		return
	}
	if taskID := strings.TrimSpace(m.taskStreamIDsByCallID[callID]); taskID != "" {
		m.wantResolvedTaskStream(taskID, wanted)
		return
	}
	if !wanted {
		if m.taskStreamResolveTokens[callID] == 0 && m.taskStreamResolveRetries[callID] == 0 {
			return
		}
		m.taskStreamResolveTokens[callID] = 0
		delete(m.taskStreamResolveRetries, callID)
		return
	}
	if handle == "" {
		return
	}
	if wanted && m.taskStreamResolveTokens[callID] != 0 {
		return
	}
	m.taskStreamNextToken++
	token := m.taskStreamNextToken
	m.taskStreamResolveTokens[callID] = token
	m.startTaskStreamResolver(strings.TrimSpace(m.currentSessionID), callID, handle, token)
}

func (m *Model) startTaskStreamResolver(sessionID, callID, handle string, token uint64) {
	cfg := m.cfg
	if sessionID == "" || cfg.ProgramSender == nil || cfg.TaskStreams == nil {
		return
	}
	ctx := contextOrBackground(cfg.Context)
	cfg.ProgramSender.startForwarder(func() {
		result, err := cfg.TaskStreams.List(ctx, taskstream.ListRequest{SessionID: sessionID})
		if err == nil {
			var matched *taskstream.TaskDescriptor
			for index := range result.Tasks {
				descriptor := &result.Tasks[index]
				if strings.TrimSpace(descriptor.ParentTool.ToolCallID) != callID {
					continue
				}
				if matched != nil && strings.TrimSpace(matched.TaskID) != strings.TrimSpace(descriptor.TaskID) {
					err = fmt.Errorf("task stream directory has multiple Tasks for tool call %q", callID)
					matched = nil
					break
				}
				matched = descriptor
			}
			if err == nil && matched == nil {
				err = fmt.Errorf("%w for handle %q", errTaskStreamNotDiscoverable, handle)
			}
			if matched != nil {
				resolvedHandle := normalizeTaskStreamHandle(matched.Handle)
				if resolvedHandle == "" {
					resolvedHandle = handle
				}
				cfg.ProgramSender.SendMsg(taskStreamResolvedMsg{
					sessionID: sessionID, callID: callID, handle: resolvedHandle,
					taskID: strings.TrimSpace(matched.TaskID), participantID: strings.TrimSpace(matched.ParticipantID),
					descriptor: *matched, token: token,
				})
				return
			}
		}
		cfg.ProgramSender.SendMsg(taskStreamResolvedMsg{sessionID: sessionID, callID: callID, handle: handle, token: token, err: err})
	})
}

func (m *Model) handleTaskStreamResolved(msg taskStreamResolvedMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.sessionID != m.currentSessionID || m.taskStreamResolveTokens[msg.callID] != msg.token {
		return m, nil
	}
	demand := m.taskStreamDemandForOwner(msg.callID, msg.handle)
	if !demand.wanted() {
		m.reconcileTaskStreamOwner(msg.callID, msg.handle)
		return m, nil
	}
	if msg.err != nil || strings.TrimSpace(msg.taskID) == "" {
		if taskStreamRetryable(msg.err) {
			m.taskStreamResolveRetries[msg.callID]++
			return m, taskStreamResolveRetryCmd(msg, m.taskStreamResolveRetries[msg.callID])
		}
		m.taskStreamResolveTokens[msg.callID] = 0
		return m, m.showHint(taskStreamUnavailableHint(msg.handle, msg.err), hintOptions{
			priority: HintPriorityHigh, clearOnMessage: true, clearAfter: systemHintDuration,
		})
	}
	delete(m.taskStreamResolveRetries, msg.callID)
	m.taskStreamHandlesByID[msg.taskID] = msg.handle
	m.taskStreamIDsByCallID[msg.callID] = msg.taskID
	m.taskStreamCallIDsByID[msg.taskID] = msg.callID
	if msg.descriptor.Kind == task.KindSubagent {
		m.subagentRosterTasks[msg.callID] = msg.descriptor
	}
	if view := m.subagentOutputViews[msg.callID]; view != nil {
		view.taskHandle = msg.handle
		view.participantID = strings.TrimSpace(msg.participantID)
		if activityID := subagentRosterDescriptorActivityID(msg.descriptor); activityID != "" {
			view.directoryActivityID = activityID
		}
		if view.actor == "" {
			view.actor = subagentOutputActor("", view.title, msg.handle)
			view.block.Actor = participantActorDisplayName(view.actor)
		}
	}
	m.wantResolvedTaskStream(msg.taskID, true)
	return m, nil
}

func (m *Model) wantResolvedTaskStream(taskID string, wanted bool) {
	if m == nil || m.cfg.TaskStreams == nil || m.cfg.ProgramSender == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	if !wanted {
		delete(m.taskStreamRetries, taskID)
		if !m.taskStreamWanted[taskID] && m.taskStreamSubscriptions[taskID] == nil &&
			m.taskStreamCancels[taskID] == nil && !m.taskStreamHistoryInFlight(taskID) {
			return
		}
		m.taskStreamWanted[taskID] = false
		m.taskStreamNextToken++
		m.taskStreamTokens[taskID] = m.taskStreamNextToken
		m.stopResolvedTaskStream(taskID)
		m.cancelTaskStreamHistory(taskID)
		return
	}
	demand := m.taskStreamDemandForTaskID(taskID)
	if demand == taskStreamDemandVisibleSubagent {
		m.reconcileTaskStreamHistory(taskID)
	}
	if !m.taskStreamLiveWanted(taskID, demand) {
		m.taskStreamWanted[taskID] = false
		return
	}
	if m.taskStreamWanted[taskID] && (m.taskStreamSubscriptions[taskID] != nil || m.taskStreamTokens[taskID] != 0) {
		return
	}
	sessionID := strings.TrimSpace(m.currentSessionID)
	if sessionID == "" {
		return
	}
	m.taskStreamWanted[taskID] = true
	m.taskStreamNextToken++
	token := m.taskStreamNextToken
	m.taskStreamTokens[taskID] = token
	cursor := m.taskStreamCursors[taskID]
	m.startTaskStreamForwarder(sessionID, taskID, token, cursor, demand == taskStreamDemandVisibleSubagent)
}

func (m *Model) taskStreamLiveWanted(taskID string, demand taskStreamDemand) bool {
	if m == nil || !demand.wanted() {
		return false
	}
	if demand == taskStreamDemandExpandedPanel {
		return true
	}
	// Once attached, a visible subagent observer remains parked across idle
	// activity boundaries. Closing the overlay is the only Surface action that
	// detaches it.
	if m.taskStreamWanted[taskID] || m.taskStreamSubscriptions[taskID] != nil ||
		m.taskStreamCancels[taskID] != nil {
		return true
	}
	callID := strings.TrimSpace(m.taskStreamCallIDsByID[strings.TrimSpace(taskID)])
	descriptor, ok := m.subagentRosterTasks[callID]
	if !ok {
		return true
	}
	// A cold terminal Task is hydrated through the finite ACP history read.
	// Runtime following begins when the directory next observes it running.
	return descriptor.Running || !eventstream.IsTerminalLifecycleState(string(descriptor.State))
}

func (m *Model) stopResolvedTaskStream(taskID string) {
	if m == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if cancel := m.taskStreamCancels[taskID]; cancel != nil {
		cancel()
		delete(m.taskStreamCancels, taskID)
	}
	if sub := m.taskStreamSubscriptions[taskID]; sub != nil {
		_ = sub.Close()
		delete(m.taskStreamSubscriptions, taskID)
	}
}

func (m *Model) startTaskStreamForwarder(
	sessionID, taskID string,
	token uint64,
	cursor string,
	follow bool,
) {
	cfg := m.cfg
	if cfg.ProgramSender == nil || cfg.TaskStreams == nil {
		return
	}
	ctx, cancel := context.WithCancel(cfg.ProgramSender.observationContext(cfg.Context))
	if previous := m.taskStreamCancels[taskID]; previous != nil {
		previous()
	}
	m.taskStreamCancels[taskID] = cancel
	started := cfg.ProgramSender.startForwarder(func() {
		defer cancel()
		result, err := cfg.TaskStreams.Subscribe(ctx, taskstream.SubscribeRequest{
			SessionID: sessionID,
			TaskID:    taskID,
			Cursor:    cursor,
			Follow:    follow,
		})
		if err != nil {
			cfg.ProgramSender.SendMsg(taskStreamClosedMsg{
				sessionID: sessionID, taskID: taskID, token: token, cursor: cursor, err: err,
			})
			return
		}
		sub := result.Subscription
		cfg.ProgramSender.SendMsg(taskStreamOpenedMsg{
			sessionID: sessionID, taskID: taskID, token: token, subscription: sub,
		})
		if sub == nil {
			cfg.ProgramSender.SendMsg(taskStreamClosedMsg{
				sessionID: sessionID, taskID: taskID, token: token,
				err: errors.New("task stream subscription is unavailable"),
			})
			return
		}
		defer sub.Close()
		assembler := &taskstream.DeliveryAssembler{}
		committedCursor := cursor
		for {
			events, cursor, activityID, replacement, open, readErr := readTaskStreamMailbox(ctx, sub.Deliveries(), assembler)
			if len(events) > 0 || replacement || cursor != "" || activityID != "" {
				cfg.ProgramSender.SendMsg(taskStreamBatchMsg{
					sessionID: sessionID, taskID: taskID, token: token, events: events,
					cursor: cursor, activityID: activityID, replacement: replacement,
				})
				if cursor != "" {
					committedCursor = cursor
				}
			}
			if !open {
				if readErr == nil {
					readErr = sub.Err()
				}
				cfg.ProgramSender.SendMsg(taskStreamClosedMsg{
					sessionID: sessionID, taskID: taskID, token: token,
					cursor: committedCursor, err: readErr,
				})
				return
			}
		}
	})
	if !started {
		cancel()
		if m.taskStreamTokens[taskID] == token {
			delete(m.taskStreamCancels, taskID)
			m.taskStreamTokens[taskID] = 0
			m.taskStreamWanted[taskID] = false
		}
	}
}

func readTaskStreamMailbox(
	ctx context.Context,
	deliveries <-chan taskstream.Delivery,
	assembler *taskstream.DeliveryAssembler,
) ([]eventstream.Envelope, string, string, bool, bool, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, "", "", false, false, ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				if assembler.Pending() {
					return nil, "", "", false, false, errorcode.New(errorcode.Unavailable, "Task replacement ended before commit")
				}
				return nil, "", "", false, false, nil
			}
			events, replacement, err := assembler.Accept(delivery)
			if err != nil {
				return nil, "", "", false, false, err
			}
			if len(events) > 0 || replacement || delivery.NextCursor != "" || delivery.ActivityID != "" && delivery.Kind == taskstream.DeliveryStatus {
				return events, delivery.NextCursor, delivery.ActivityID, replacement, true, nil
			}
		}
	}
}

func (m *Model) handleTaskStreamOpened(msg taskStreamOpenedMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.subscription == nil {
		return m, nil
	}
	if msg.sessionID != m.currentSessionID || !m.taskStreamWanted[msg.taskID] ||
		m.taskStreamTokens[msg.taskID] != msg.token {
		_ = msg.subscription.Close()
		return m, nil
	}
	if previous := m.taskStreamSubscriptions[msg.taskID]; previous != nil && previous != msg.subscription {
		_ = previous.Close()
	}
	m.taskStreamSubscriptions[msg.taskID] = msg.subscription
	return m, nil
}

func (m *Model) handleTaskStreamBatch(msg taskStreamBatchMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.sessionID != m.currentSessionID || !m.taskStreamWanted[msg.taskID] ||
		m.taskStreamTokens[msg.taskID] != msg.token {
		return m, nil
	}
	// A live frame always wins over an idle snapshot already in flight. The
	// directory may publish the next ActivityID a few milliseconds later; do
	// not let the older finite read overwrite output that has already arrived.
	m.cancelTaskStreamHistory(msg.taskID)
	delete(m.taskStreamRetries, msg.taskID)
	if cursor := strings.TrimSpace(msg.cursor); cursor != "" {
		m.taskStreamCursors[msg.taskID] = cursor
	}
	if activityID := taskStreamActivityKey(msg.activityID); activityID != "" {
		if callID := strings.TrimSpace(m.taskStreamCallIDsByID[msg.taskID]); callID != "" {
			if view := m.subagentOutputViews[callID]; view != nil {
				view.liveActivityID = activityID
			}
		}
	}
	if msg.replacement {
		if callID := strings.TrimSpace(m.taskStreamCallIDsByID[msg.taskID]); callID != "" {
			if view := m.subagentOutputViews[callID]; view != nil {
				view.resetForReplacement()
			}
		}
	}
	cmds := make([]tea.Cmd, 0, len(msg.events))
	for _, envelope := range msg.events {
		if activityID := taskStreamActivityKey(envelope.ActivityID); activityID != "" {
			if callID := strings.TrimSpace(m.taskStreamCallIDsByID[msg.taskID]); callID != "" {
				if view := m.subagentOutputViews[callID]; view != nil {
					view.liveActivityID = activityID
				}
			}
		}
		if cursor := strings.TrimSpace(envelope.Cursor); cursor != "" {
			m.taskStreamCursors[msg.taskID] = cursor
		}
		model, cmd := m.handleACPEventEnvelope(envelope)
		if next, ok := model.(*Model); ok {
			m = next
		}
		cmds = append(cmds, cmd)
	}
	if callID := strings.TrimSpace(m.taskStreamCallIDsByID[msg.taskID]); callID != "" {
		if view := m.subagentOutputViews[callID]; view != nil {
			view.historyResolved = true
			view.idleHistorySettled = false
			// Content batches may arrive every mailbox window. Keep their document
			// mutations exact, but let the overlay's existing render scheduler fold
			// several tiny batches into one physical frame. Lifecycle and Directory
			// state remain authoritative regardless of this presentation cadence.
			view.touch(false)
			// A terminal lifecycle frame, not Task-directory metadata, is the
			// transcript sealing boundary. Reconcile only after applying this batch
			// so a completed descriptor cannot close the subscription one frame early.
			m.reconcileTaskStreamOwner(callID, view.taskHandle)
		}
	}
	cmds = append(cmds, m.requestSubagentOutputRender())
	return m, tea.Batch(cmds...)
}

func (m *Model) handleTaskStreamClosed(msg taskStreamClosedMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.sessionID != m.currentSessionID || m.taskStreamTokens[msg.taskID] != msg.token {
		return m, nil
	}
	delete(m.taskStreamSubscriptions, msg.taskID)
	if cancel := m.taskStreamCancels[msg.taskID]; cancel != nil {
		cancel()
		delete(m.taskStreamCancels, msg.taskID)
	}
	if cursor := strings.TrimSpace(msg.cursor); cursor != "" {
		m.taskStreamCursors[msg.taskID] = cursor
	}
	demand := m.taskStreamDemandForTaskID(msg.taskID)
	if !demand.wanted() {
		m.wantResolvedTaskStream(msg.taskID, false)
		return m, nil
	}
	if msg.err == nil && demand == taskStreamDemandVisibleSubagent && m.taskStreamWanted[msg.taskID] {
		m.taskStreamRetries[msg.taskID]++
		if m.taskStreamRetries[msg.taskID] > taskStreamCleanExitRetries {
			m.taskStreamTokens[msg.taskID] = 0
			handle := m.taskStreamHandlesByID[msg.taskID]
			return m, m.showHint(taskStreamUnavailableHint(handle, errTaskStreamUnexpectedClose), hintOptions{
				priority: HintPriorityHigh, clearOnMessage: true, clearAfter: systemHintDuration,
			})
		}
		m.taskStreamTokens[msg.taskID] = 0
		return m, taskStreamSubscribeRetryCmd(msg.sessionID, msg.taskID, m.taskStreamRetries[msg.taskID])
	}
	// Delivery failures are local to this panel. A valid cursor prefers the
	// retained trace; Control may answer a cache miss with an atomic replacement,
	// which this panel applies through the delivery assembler before retrying.
	if taskStreamRetryable(msg.err) && m.taskStreamWanted[msg.taskID] && demand.wanted() {
		m.taskStreamRetries[msg.taskID]++
		m.taskStreamTokens[msg.taskID] = 0
		return m, taskStreamSubscribeRetryCmd(msg.sessionID, msg.taskID, m.taskStreamRetries[msg.taskID])
	}
	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
		m.taskStreamTokens[msg.taskID] = 0
		m.taskStreamWanted[msg.taskID] = false
		handle := m.taskStreamHandlesByID[msg.taskID]
		return m, m.showHint(taskStreamUnavailableHint(handle, msg.err), hintOptions{
			priority: HintPriorityHigh, clearOnMessage: true, clearAfter: systemHintDuration,
		})
	}
	return m, nil
}

func (m *Model) handleTaskStreamResolveRetry(msg taskStreamResolveRetryMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.sessionID != m.currentSessionID || m.taskStreamResolveTokens[msg.callID] != msg.token ||
		!m.taskStreamDemandForOwner(msg.callID, msg.handle).wanted() {
		return m, nil
	}
	m.startTaskStreamResolver(msg.sessionID, msg.callID, msg.handle, msg.token)
	return m, nil
}

func (m *Model) handleTaskStreamSubscribeRetry(msg taskStreamSubscribeRetryMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.sessionID != m.currentSessionID || !m.taskStreamWanted[msg.taskID] ||
		m.taskStreamTokens[msg.taskID] != 0 || !m.taskStreamDemandForTaskID(msg.taskID).wanted() {
		return m, nil
	}
	m.wantResolvedTaskStream(msg.taskID, true)
	return m, nil
}

func (m *Model) taskStreamDemandForTaskID(taskID string) taskStreamDemand {
	if m == nil {
		return taskStreamDemandNone
	}
	taskID = strings.TrimSpace(taskID)
	handle := m.taskStreamHandlesByID[taskID]
	if callID := strings.TrimSpace(m.taskStreamCallIDsByID[taskID]); callID != "" {
		return m.taskStreamDemandForOwner(callID, handle)
	}
	return taskStreamDemandNone
}

func taskStreamResolveRetryCmd(msg taskStreamResolvedMsg, attempt int) tea.Cmd {
	return tea.Tick(taskStreamRetryBackoff(attempt), func(time.Time) tea.Msg {
		return taskStreamResolveRetryMsg{
			sessionID: msg.sessionID, callID: msg.callID, handle: msg.handle, token: msg.token,
		}
	})
}

func taskStreamSubscribeRetryCmd(sessionID, taskID string, attempt int) tea.Cmd {
	return tea.Tick(taskStreamRetryBackoff(attempt), func(time.Time) tea.Msg {
		return taskStreamSubscribeRetryMsg{sessionID: sessionID, taskID: taskID}
	})
}

func taskStreamRetryBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return taskStreamRetryDelay
	}
	delay := taskStreamRetryDelay << min(attempt-1, taskStreamRetryBackoffCap-1)
	return delay
}

func normalizeTaskStreamHandle(handle string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(handle), "@"))
}

func taskStreamActivityKey(activityID string) string {
	activityID = strings.TrimSpace(activityID)
	if activityID == "" {
		return ""
	}
	return "activity:" + activityID
}

func taskStreamRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, errTaskStreamNotDiscoverable) {
		return true
	}
	switch errorcode.CodeOf(err) {
	case errorcode.Unknown, errorcode.ResourceExhausted, errorcode.RateLimited, errorcode.Overloaded,
		errorcode.Timeout, errorcode.Unavailable:
		return true
	default:
		return false
	}
}

func taskStreamUnavailableHint(handle string, err error) string {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		handle = "task"
	}
	if err == nil {
		return fmt.Sprintf("Task %s live output is unavailable", handle)
	}
	return fmt.Sprintf("Task %s live output is unavailable: %v", handle, err)
}

func (m *Model) closeTaskStreamSubscriptions() {
	if m == nil {
		return
	}
	for taskID, cancel := range m.taskStreamCancels {
		if cancel != nil {
			cancel()
		}
		delete(m.taskStreamCancels, taskID)
	}
	for taskID, sub := range m.taskStreamSubscriptions {
		if sub != nil {
			_ = sub.Close()
		}
		delete(m.taskStreamSubscriptions, taskID)
	}
	for taskID, cancel := range m.taskStreamHistoryCancels {
		if cancel != nil {
			cancel()
		}
		delete(m.taskStreamHistoryCancels, taskID)
	}
	m.taskStreamWanted = map[string]bool{}
	m.taskStreamTokens = map[string]uint64{}
	m.taskStreamCancels = map[string]context.CancelFunc{}
	m.taskStreamCursors = map[string]string{}
	m.taskStreamHandlesByID = map[string]string{}
	m.taskStreamIDsByCallID = map[string]string{}
	m.taskStreamCallIDsByID = map[string]string{}
	m.taskStreamResolveTokens = map[string]uint64{}
	m.taskStreamResolveRetries = map[string]int{}
	m.taskStreamRetries = map[string]int{}
	m.taskStreamHistoryStages = map[string]*subagentOutputHistoryStage{}
	m.taskStreamHistoryTokens = map[string]uint64{}
	m.taskStreamHistoryCancels = map[string]context.CancelFunc{}
	m.taskStreamHistoryRetries = map[string]taskStreamHistoryRetryState{}
}
