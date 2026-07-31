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
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

const (
	taskStreamMailboxBatchSize = 64
	taskStreamMailboxBudget    = 16 * time.Millisecond
	taskStreamRetryDelay       = 250 * time.Millisecond
	taskStreamRetryBackoffCap  = 4
)

var errTaskStreamNotDiscoverable = errors.New("task stream is not discoverable yet")

type taskStreamDemand uint8

const (
	taskStreamDemandNone taskStreamDemand = iota
	taskStreamDemandFinishedSubagent
	taskStreamDemandExpandedPanel
	taskStreamDemandBackgroundSubagent
)

func (d taskStreamDemand) wanted() bool {
	return d == taskStreamDemandExpandedPanel || d == taskStreamDemandBackgroundSubagent
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
	m.subagentOutputViews = map[string]*subagentOutputView{}
	m.runningActivityTracker.resetSession()
	m.refreshRunningActivity()
	m.currentSessionID = sessionID
}

func (m *Model) observeTaskStreamAnchor(env eventstream.Envelope) {
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
	if view := m.subagentOutputViews[callID]; view != nil {
		view.taskHandle = normalizeTaskStreamHandle(handle)
	}
	m.applyTaskStreamDemand(callID, handle, m.taskStreamDemandForAnchor(callID, handle))
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

func taskHandleForToolPanel(events []SubagentEvent, callID string) string {
	callID = strings.TrimSpace(callID)
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == SEToolCall && strings.TrimSpace(events[i].CallID) == callID {
			if taskHandle := strings.TrimSpace(events[i].TaskHandle); taskHandle != "" {
				return taskHandle
			}
		}
	}
	return ""
}

func (m *Model) taskPanelExpanded(callID, handle string) bool {
	if m == nil || m.doc == nil {
		return false
	}
	for _, block := range m.doc.Blocks() {
		switch typed := block.(type) {
		case *MainACPTurnBlock:
			if taskHandleForToolPanel(typed.Events, callID) == handle {
				return typed.toolPanelExpanded(callID)
			}
		case *ParticipantTurnBlock:
			if taskHandleForToolPanel(typed.Events, callID) == handle {
				return typed.toolPanelExpanded(callID)
			}
		}
	}
	return false
}

func (m *Model) taskStreamDemandForAnchor(callID, handle string) taskStreamDemand {
	if m == nil {
		return taskStreamDemandNone
	}
	callID = strings.TrimSpace(callID)
	handle = normalizeTaskStreamHandle(handle)
	if view := m.subagentOutputViews[callID]; view != nil {
		if view.block == nil {
			return taskStreamDemandNone
		}
		if subagentOutputStatusFromState(view.block.Status) != subagentOutputRunning {
			return taskStreamDemandFinishedSubagent
		}
		return taskStreamDemandBackgroundSubagent
	}
	if m.taskPanelExpanded(callID, handle) {
		return taskStreamDemandExpandedPanel
	}
	return taskStreamDemandNone
}

func (m *Model) taskStreamDemandForHandle(handle string) taskStreamDemand {
	if m == nil {
		return taskStreamDemandNone
	}
	handle = normalizeTaskStreamHandle(handle)
	if handle == "" {
		return taskStreamDemandNone
	}
	demand := taskStreamDemandNone
	for _, view := range m.subagentOutputViews {
		if view != nil && normalizeTaskStreamHandle(view.taskHandle) == handle {
			viewDemand := m.taskStreamDemandForAnchor(view.callID, handle)
			if viewDemand == taskStreamDemandBackgroundSubagent {
				return viewDemand
			}
			if viewDemand > demand {
				demand = viewDemand
			}
		}
	}
	if m.doc == nil {
		return demand
	}
	for _, block := range m.doc.Blocks() {
		var events []SubagentEvent
		var expanded func(string) bool
		switch typed := block.(type) {
		case *MainACPTurnBlock:
			events, expanded = typed.Events, typed.toolPanelExpanded
		case *ParticipantTurnBlock:
			events, expanded = typed.Events, typed.toolPanelExpanded
		default:
			continue
		}
		for _, event := range events {
			if event.Kind != SEToolCall || normalizeTaskStreamHandle(event.TaskHandle) != handle {
				continue
			}
			if m.subagentOutputViews[strings.TrimSpace(event.CallID)] != nil {
				continue
			}
			if expanded(event.CallID) {
				return taskStreamDemandExpandedPanel
			}
		}
	}
	return demand
}

func (m *Model) applyTaskStreamDemand(callID, rawHandle string, demand taskStreamDemand) {
	if m == nil || m.cfg.TaskStreams == nil || m.cfg.ProgramSender == nil {
		return
	}
	wanted := demand.wanted()
	callID = strings.TrimSpace(callID)
	handle := normalizeTaskStreamHandle(rawHandle)
	if callID == "" || handle == "" {
		return
	}
	if taskID := strings.TrimSpace(m.taskStreamIDsByCallID[callID]); taskID != "" {
		m.wantResolvedTaskStream(taskID, wanted)
		return
	}
	if taskID := strings.TrimSpace(m.taskStreamIDsByHandle[handle]); taskID != "" {
		m.wantResolvedTaskStream(taskID, wanted)
		return
	}
	if !wanted {
		m.taskStreamNextToken++
		m.taskStreamResolveTokens[callID] = 0
		delete(m.taskStreamResolveRetries, callID)
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
	demand := m.taskStreamDemandForAnchor(msg.callID, msg.handle)
	if !demand.wanted() {
		m.applyTaskStreamDemand(msg.callID, msg.handle, demand)
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
	m.taskStreamIDsByHandle[msg.handle] = msg.taskID
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
		m.taskStreamWanted[taskID] = false
		delete(m.taskStreamRetries, taskID)
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
	ctx := contextOrBackground(cfg.Context)
	cfg.ProgramSender.startForwarder(func() {
		result, err := cfg.TaskStreams.Subscribe(ctx, taskstream.SubscribeRequest{
			SessionID: sessionID,
			TaskID:    taskID,
			Cursor:    cursor,
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
			continue
		}
		model, cmd := m.handleACPEventEnvelope(envelope)
		if next, ok := model.(*Model); ok {
			m = next
		}
		cmds = append(cmds, cmd)
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
		if demand == taskStreamDemandFinishedSubagent {
			return m, nil
		}
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
		!m.taskStreamDemandForAnchor(msg.callID, msg.handle).wanted() {
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
		if demand := m.taskStreamDemandForAnchor(callID, handle); demand != taskStreamDemandNone {
			return demand
		}
	}
	return m.taskStreamDemandForHandle(handle)
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
	m.taskStreamIDsByHandle = map[string]string{}
	m.taskStreamHandlesByID = map[string]string{}
	m.taskStreamIDsByCallID = map[string]string{}
	m.taskStreamCallIDsByID = map[string]string{}
	m.taskStreamResolveTokens = map[string]uint64{}
	m.taskStreamResolveRetries = map[string]int{}
	m.taskStreamRetries = map[string]int{}
}
