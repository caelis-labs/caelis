package acpagentbridge

import (
	"strconv"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpbridge"
)

// acpChildTerminalProjector is the ACP stdio compatibility renderer for
// delegated child Envelopes. Standard session/update notifications cannot
// carry Caelis Envelope scope or parent_tool fields, so child stream updates
// must not be forwarded as main-Agent narrative. Instead, each child Turn is
// reduced to at most one standard result update on its parent Spawn tool call.
type acpChildTerminalProjector struct {
	mu            sync.Mutex
	turns         map[acpChildTerminalKey]*acpChildTerminalState
	closed        map[acpChildTerminalKey]struct{}
	closedThrough map[acpChildTerminalSeries]int64
	current       map[acpChildParentKey]string
}

type acpChildParentKey struct {
	SessionID  string
	ToolCallID string
}

type acpChildTerminalKey struct {
	acpChildParentKey
	TurnID string
}

type acpChildTerminalSeries struct {
	acpChildParentKey
	Prefix string
}

type acpChildTerminalState struct {
	finalResponse acpbridge.FinalAssistantAccumulator
	closed        bool
}

func newACPChildTerminalProjector() *acpChildTerminalProjector {
	return &acpChildTerminalProjector{
		turns:         map[acpChildTerminalKey]*acpChildTerminalState{},
		closed:        map[acpChildTerminalKey]struct{}{},
		closedThrough: map[acpChildTerminalSeries]int64{},
		current:       map[acpChildParentKey]string{},
	}
}

func isACPChildTerminalEnvelope(env eventstream.Envelope) bool {
	return env.Scope == eventstream.ScopeSubagent && env.ParentTool != nil &&
		strings.TrimSpace(env.ParentTool.ToolCallID) != "" &&
		env.ParentTool.ToolName == spawn.ToolName && env.Update != nil
}

func (p *acpChildTerminalProjector) track(env eventstream.Envelope, fallbackSessionID string) {
	if p == nil || !isACPChildTerminalEnvelope(env) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.childTurnStateLocked(
		childTerminalSessionID(env.SessionID, fallbackSessionID),
		strings.TrimSpace(env.ParentTool.ToolCallID),
		strings.TrimSpace(env.TurnID),
		true,
	)
}

func (p *acpChildTerminalProjector) childTurnStateLocked(
	sessionID string,
	toolCallID string,
	turnID string,
	selectCurrent bool,
) (*acpChildTerminalState, acpChildTerminalKey) {
	parent := acpChildParentKey{
		SessionID:  strings.TrimSpace(sessionID),
		ToolCallID: strings.TrimSpace(toolCallID),
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		turnID = p.current[parent]
	} else if selectCurrent {
		if _, known := p.current[parent]; !known {
			unknownKey := acpChildTerminalKey{acpChildParentKey: parent}
			key := acpChildTerminalKey{acpChildParentKey: parent, TurnID: turnID}
			if unknown, ok := p.turns[unknownKey]; ok {
				p.turns[key] = unknown
				delete(p.turns, unknownKey)
			} else if _, ok := p.closed[unknownKey]; ok {
				delete(p.closed, unknownKey)
				p.markChildTurnClosedLocked(key)
			}
		}
		p.current[parent] = turnID
	}
	key := acpChildTerminalKey{acpChildParentKey: parent, TurnID: turnID}
	if p.childTurnClosedLocked(key) {
		delete(p.turns, key)
		return &acpChildTerminalState{closed: true}, key
	}
	state := p.turns[key]
	if state == nil {
		state = &acpChildTerminalState{}
		p.turns[key] = state
	}
	return state, key
}

func (p *acpChildTerminalProjector) closeChildTurnLocked(key acpChildTerminalKey, state *acpChildTerminalState) {
	if state == nil || state.closed {
		return
	}
	state.closed = true
	state.finalResponse = acpbridge.FinalAssistantAccumulator{}
	delete(p.turns, key)
	p.markChildTurnClosedLocked(key)
}

func (p *acpChildTerminalProjector) markChildTurnClosedLocked(key acpChildTerminalKey) {
	series, sequence, ok := acpChildTerminalTurnSeries(key)
	if !ok {
		p.closed[key] = struct{}{}
		return
	}
	closedThrough := p.closedThrough[series]
	if sequence <= closedThrough {
		return
	}
	if sequence != closedThrough+1 {
		p.closed[key] = struct{}{}
		return
	}
	for {
		p.closedThrough[series] = sequence
		next := acpChildTerminalKey{
			acpChildParentKey: series.acpChildParentKey,
			TurnID:            series.Prefix + ":" + strconv.FormatInt(sequence+1, 10),
		}
		if _, ok := p.closed[next]; !ok {
			return
		}
		delete(p.closed, next)
		sequence++
	}
}

func (p *acpChildTerminalProjector) childTurnClosedLocked(key acpChildTerminalKey) bool {
	if _, ok := p.closed[key]; ok {
		return true
	}
	series, sequence, ok := acpChildTerminalTurnSeries(key)
	return ok && sequence <= p.closedThrough[series]
}

// Physical subagent Turns use the stable "task-id:sequence" cursor. One Task
// runs those Turns serially, so a contiguous high-water mark compacts closed
// identities. Out-of-order closes remain exact tombstones until every earlier
// Turn closes.
func acpChildTerminalTurnSeries(key acpChildTerminalKey) (acpChildTerminalSeries, int64, bool) {
	turnID := strings.TrimSpace(key.TurnID)
	separator := strings.LastIndex(turnID, ":")
	if separator <= 0 || separator == len(turnID)-1 {
		return acpChildTerminalSeries{}, 0, false
	}
	sequence, err := strconv.ParseInt(turnID[separator+1:], 10, 64)
	if err != nil || sequence <= 0 {
		return acpChildTerminalSeries{}, 0, false
	}
	return acpChildTerminalSeries{
		acpChildParentKey: key.acpChildParentKey,
		Prefix:            turnID[:separator],
	}, sequence, true
}

// project consumes one typed child update. Agent-message chunks are the only
// standard ACP payload that contributes to the child's FinalResponse; thought,
// plan, nested tool, and notice updates remain process-local. No update is
// emitted until typed child lifecycle proves the Turn terminal.
func (p *acpChildTerminalProjector) project(env eventstream.Envelope, fallbackSessionID string) (eventstream.SessionNotification, bool) {
	if p == nil || !isACPChildTerminalEnvelope(env) {
		return eventstream.SessionNotification{}, false
	}
	parentCallID := strings.TrimSpace(env.ParentTool.ToolCallID)
	if parentCallID == "" {
		return eventstream.SessionNotification{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	state, _ := p.childTurnStateLocked(
		childTerminalSessionID(env.SessionID, fallbackSessionID),
		parentCallID,
		env.TurnID,
		true,
	)
	if state.closed {
		return eventstream.SessionNotification{}, true
	}
	state.finalResponse.ObserveUpdate(env.Update)
	return eventstream.SessionNotification{}, true
}

// projectLifecycle emits the one standard parent Spawn result for a child Turn.
// The terminal status is normalized to ACP v1's completed/failed vocabulary;
// the FinalResponse is ordinary tool result content, never an agent message or
// Caelis terminal-stream extension.
func (p *acpChildTerminalProjector) projectLifecycle(env eventstream.Envelope, fallbackSessionID string) (eventstream.SessionNotification, bool) {
	if p == nil || env.Kind != eventstream.KindLifecycle || env.Scope != eventstream.ScopeSubagent ||
		env.ParentTool == nil || env.Lifecycle == nil || !eventstream.IsTerminalLifecycleState(env.Lifecycle.State) {
		return eventstream.SessionNotification{}, false
	}
	parentCallID := strings.TrimSpace(env.ParentTool.ToolCallID)
	if parentCallID == "" || env.ParentTool.ToolName != spawn.ToolName {
		return eventstream.SessionNotification{}, false
	}
	sessionID := childTerminalSessionID(env.SessionID, fallbackSessionID)

	status := observedSpawnStatus(nil, map[string]any{"state": env.Lifecycle.State})
	p.mu.Lock()
	state, key := p.childTurnStateLocked(sessionID, parentCallID, env.TurnID, true)
	if state.closed {
		p.mu.Unlock()
		return eventstream.SessionNotification{}, true
	}
	text := state.finalResponse.FinalText()
	if status == eventstream.ToolStatusFailed {
		text = strings.TrimSpace(env.Lifecycle.Reason)
	} else if strings.TrimSpace(text) == "" {
		text = strings.TrimSpace(env.Lifecycle.Reason)
	}
	p.closeChildTurnLocked(key, state)
	p.mu.Unlock()

	return childTerminalResultNotification(sessionID, parentCallID, status, text, nil), true
}

// projectNotice consumes child notices without crossing the process boundary.
func (p *acpChildTerminalProjector) projectNotice(env eventstream.Envelope, fallbackSessionID string) (eventstream.SessionNotification, bool) {
	if p == nil || env.Kind != eventstream.KindNotice || env.Scope != eventstream.ScopeSubagent ||
		env.ParentTool == nil {
		return eventstream.SessionNotification{}, false
	}
	parentCallID := strings.TrimSpace(env.ParentTool.ToolCallID)
	if parentCallID == "" || env.ParentTool.ToolName != spawn.ToolName {
		return eventstream.SessionNotification{}, false
	}
	return eventstream.SessionNotification{}, true
}

func observedSpawnStatus(taskStatus *string, rawOutput map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(display.MapString(rawOutput, "state"))) {
	case "completed", "complete", "succeeded", "success", "done":
		return eventstream.ToolStatusCompleted
	case "failed", "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout", "unknown_outcome":
		return eventstream.ToolStatusFailed
	}
	if taskStatus != nil && strings.EqualFold(strings.TrimSpace(*taskStatus), eventstream.ToolStatusFailed) {
		return eventstream.ToolStatusFailed
	}
	return eventstream.ToolStatusCompleted
}

func childTerminalResultText(status string, rawOutput map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(status), eventstream.ToolStatusFailed) {
		return firstChildTerminalText(
			display.MapString(rawOutput, "error"),
			display.MapString(rawOutput, "reason"),
		)
	}
	return display.SubagentTaskFinalText(display.MapString(rawOutput, "state"), rawOutput)
}

func firstChildTerminalText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func childTerminalResultNotification(
	sessionID string,
	parentCallID string,
	status string,
	text string,
	rawOutput map[string]any,
) eventstream.SessionNotification {
	update := eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(parentCallID),
		Status:        &status,
	}
	if rawOutput != nil {
		update.RawOutput = rawOutput
	}
	if strings.TrimSpace(text) != "" {
		update.Content = []eventstream.ToolCallContent{{
			Type:    "content",
			Content: eventstream.TextContent{Type: "text", Text: text},
		}}
	}
	return eventstream.SessionNotification{
		SessionID: strings.TrimSpace(sessionID),
		Update:    update,
	}
}

func childTerminalSessionID(sessionID string, fallbackSessionID string) string {
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(fallbackSessionID)
}
