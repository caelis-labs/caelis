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
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
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
	sessionID string
	taskID    string
	token     uint64
	events    []eventstream.Envelope
}

type taskStreamClosedMsg struct {
	sessionID string
	taskID    string
	token     uint64
	cursor    string
	err       error
}

type taskStreamResolvedMsg struct {
	sessionID string
	callID    string
	handle    string
	taskID    string
	token     uint64
	err       error
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
	m.resetSubagentRosterRefresh()
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

func taskStreamToolValues(update schema.Update) (map[string]any, map[string]any) {
	var rawInput, rawOutput any
	switch typed := update.(type) {
	case schema.ToolCall:
		rawInput, rawOutput = typed.RawInput, typed.RawOutput
	case schema.ToolCallUpdate:
		rawInput, rawOutput = typed.RawInput, typed.RawOutput
	}
	input, _ := rawInput.(map[string]any)
	output, _ := rawOutput.(map[string]any)
	return input, output
}

func taskStreamToolCallID(update schema.Update) string {
	switch typed := update.(type) {
	case schema.ToolCall:
		return strings.TrimSpace(typed.ToolCallID)
	case schema.ToolCallUpdate:
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
			names.CanonicalOrSelf(toolSemanticName(event.Name, event.ToolKind)) != names.RunCommand {
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
			if m.subagentOutputTerminalHistoryCached(callID, view) {
				return taskStreamDemandNone
			}
			return taskStreamDemandVisibleSubagent
		}
		return taskStreamDemandNone
	}
	if m.taskStreamPanelExpanded(callID, handle) {
		return taskStreamDemandExpandedPanel
	}
	return taskStreamDemandNone
}

// subagentOutputTerminalHistoryCached prevents a terminal child workspace from
// reopening durable Session history once this process already resolved it. The
// Task directory owns terminality; historyResolved is only a presentation-cache
// marker and remains false for a cold replay shell until the selected overlay
// has actually loaded (including a successfully resolved empty history).
func (m *Model) subagentOutputTerminalHistoryCached(callID string, view *subagentOutputView) bool {
	if m == nil || view == nil || !view.historyResolved {
		return false
	}
	descriptor, ok := m.subagentRosterTasks[strings.TrimSpace(callID)]
	if !ok || descriptor.Running {
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
				cfg.ProgramSender.SendMsg(taskStreamResolvedMsg{sessionID: sessionID, callID: callID, handle: resolvedHandle, taskID: strings.TrimSpace(matched.TaskID), token: token})
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
	if view := m.subagentOutputViews[msg.callID]; view != nil {
		view.taskHandle = msg.handle
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
		if !m.taskStreamWanted[taskID] && m.taskStreamSubscriptions[taskID] == nil {
			return
		}
		m.taskStreamWanted[taskID] = false
		m.taskStreamNextToken++
		m.taskStreamTokens[taskID] = m.taskStreamNextToken
		if sub := m.taskStreamSubscriptions[taskID]; sub != nil {
			_ = sub.Close()
			delete(m.taskStreamSubscriptions, taskID)
		}
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
	m.startTaskStreamForwarder(sessionID, taskID, token, m.taskStreamCursors[taskID])
}

func (m *Model) startTaskStreamForwarder(sessionID, taskID string, token uint64, cursor string) {
	cfg := m.cfg
	if cfg.ProgramSender == nil || cfg.TaskStreams == nil {
		return
	}
	follow := m.taskStreamDemandForTaskID(taskID) == taskStreamDemandVisibleSubagent
	ctx := contextOrBackground(cfg.Context)
	cfg.ProgramSender.startForwarder(func() {
		result, err := cfg.TaskStreams.Subscribe(ctx, taskstream.SubscribeRequest{
			SessionID: sessionID,
			TaskID:    taskID,
			Cursor:    cursor,
			Follow:    follow,
		})
		if err != nil {
			cfg.ProgramSender.SendMsg(taskStreamClosedMsg{sessionID: sessionID, taskID: taskID, token: token, cursor: cursor, err: err})
			return
		}
		sub := result.Subscription
		cfg.ProgramSender.SendMsg(taskStreamOpenedMsg{sessionID: sessionID, taskID: taskID, token: token, subscription: sub})
		if sub == nil {
			cfg.ProgramSender.SendMsg(taskStreamClosedMsg{sessionID: sessionID, taskID: taskID, token: token, err: errors.New("task stream subscription is unavailable")})
			return
		}
		defer sub.Close()
		for {
			batch, open := readTaskStreamMailbox(ctx, sub.Events())
			if len(batch) > 0 {
				cfg.ProgramSender.SendMsg(taskStreamBatchMsg{sessionID: sessionID, taskID: taskID, token: token, events: batch})
			}
			if !open {
				cfg.ProgramSender.SendMsg(taskStreamClosedMsg{
					sessionID: sessionID, taskID: taskID, token: token,
					cursor: sub.LastCursor(), err: sub.Err(),
				})
				return
			}
		}
	})
}

func readTaskStreamMailbox(ctx context.Context, events <-chan eventstream.Envelope) ([]eventstream.Envelope, bool) {
	select {
	case <-ctx.Done():
		return nil, false
	case event, ok := <-events:
		if !ok {
			return nil, false
		}
		batch := []eventstream.Envelope{event}
		timer := time.NewTimer(taskStreamMailboxBudget)
		defer timer.Stop()
		for len(batch) < taskStreamMailboxBatchSize {
			select {
			case <-ctx.Done():
				return batch, false
			case event, ok = <-events:
				if !ok {
					return batch, false
				}
				batch = append(batch, event)
			case <-timer.C:
				return batch, true
			}
		}
		return batch, true
	}
}

func (m *Model) handleTaskStreamOpened(msg taskStreamOpenedMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.subscription == nil {
		return m, nil
	}
	if msg.sessionID != m.currentSessionID || !m.taskStreamWanted[msg.taskID] || m.taskStreamTokens[msg.taskID] != msg.token {
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
	if m == nil || msg.sessionID != m.currentSessionID || !m.taskStreamWanted[msg.taskID] || m.taskStreamTokens[msg.taskID] != msg.token {
		return m, nil
	}
	delete(m.taskStreamRetries, msg.taskID)
	cmds := make([]tea.Cmd, 0, len(msg.events))
	for _, envelope := range msg.events {
		if cursor := strings.TrimSpace(envelope.Cursor); cursor != "" {
			m.taskStreamCursors[msg.taskID] = cursor
		}
		if taskstream.IsTransientGapEnvelope(envelope) {
			if callID := strings.TrimSpace(m.taskStreamCallIDsByID[msg.taskID]); callID != "" {
				if view := m.subagentOutputViews[callID]; view != nil {
					view.resetForCurrentState()
				}
			}
			continue
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
			view.touch(true)
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
	if cursor := strings.TrimSpace(msg.cursor); cursor != "" {
		m.taskStreamCursors[msg.taskID] = cursor
	}
	demand := m.taskStreamDemandForTaskID(msg.taskID)
	if !demand.wanted() {
		m.wantResolvedTaskStream(msg.taskID, false)
		return m, nil
	}
	if msg.err == nil && demand == taskStreamDemandVisibleSubagent {
		callID := strings.TrimSpace(m.taskStreamCallIDsByID[msg.taskID])
		view := m.subagentOutputViews[callID]
		if view != nil && view.historyResolved && m.subagentOutputCurrentStatus(view) != subagentOutputRunning {
			// A cold historical replay is finite. Detach cleanly once its terminal
			// assistant history is delivered; a later accepted SendMessage will
			// reopen observation for the next activity period.
			m.wantResolvedTaskStream(msg.taskID, false)
			return m, nil
		}
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
	// Delivery failures are local to this panel. Recoverable failures resume
	// from the last accepted cursor; an evicted prefix is returned as a gap.
	if taskStreamRetryable(msg.err) && m.taskStreamWanted[msg.taskID] && demand.wanted() {
		m.taskStreamRetries[msg.taskID]++
		m.taskStreamTokens[msg.taskID] = 0
		return m, taskStreamSubscribeRetryCmd(msg.sessionID, msg.taskID, m.taskStreamRetries[msg.taskID])
	}
	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
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

func taskStreamRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, errTaskStreamNotDiscoverable) || errors.Is(err, taskstream.ErrSlowConsumer) {
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
	for taskID, sub := range m.taskStreamSubscriptions {
		if sub != nil {
			_ = sub.Close()
		}
		delete(m.taskStreamSubscriptions, taskID)
	}
	m.taskStreamWanted = map[string]bool{}
	m.taskStreamTokens = map[string]uint64{}
	m.taskStreamCursors = map[string]string{}
	m.taskStreamHandlesByID = map[string]string{}
	m.taskStreamIDsByCallID = map[string]string{}
	m.taskStreamCallIDsByID = map[string]string{}
	m.taskStreamResolveTokens = map[string]uint64{}
	m.taskStreamResolveRetries = map[string]int{}
	m.taskStreamRetries = map[string]int{}
}
