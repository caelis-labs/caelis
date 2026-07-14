package acpagentbridge

import (
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

// acpChildTerminalProjector is the ACP stdio compatibility renderer for
// delegated child Envelopes. Standard session/update notifications cannot
// carry Caelis Envelope scope or parent_tool fields, so forwarding the child
// update unchanged would flatten it into the main Agent transcript in clients
// such as Zed. The typed Envelope relation selects the already-mounted parent
// Spawn terminal; metadata never supplies that relation.
type acpChildTerminalProjector struct {
	mu      sync.Mutex
	parents map[acpChildTerminalKey]*acpChildTerminalState
}

type acpChildTerminalKey struct {
	SessionID  string
	ToolCallID string
}

type acpChildTerminalState struct {
	started  bool
	endsLine bool
	lastKind string
	tools    map[string]acpChildToolState
}

type acpChildToolState struct {
	title     string
	announced bool
}

func newACPChildTerminalProjector() *acpChildTerminalProjector {
	return &acpChildTerminalProjector{parents: map[acpChildTerminalKey]*acpChildTerminalState{}}
}

func isACPChildTerminalEnvelope(env eventstream.Envelope) bool {
	return env.Scope == eventstream.ScopeSubagent && env.ParentTool != nil &&
		strings.TrimSpace(env.ParentTool.ToolCallID) != "" && env.Update != nil
}

func (p *acpChildTerminalProjector) track(env eventstream.Envelope, fallbackSessionID string) {
	if p == nil || !isACPChildTerminalEnvelope(env) {
		return
	}
	sessionID := strings.TrimSpace(env.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(fallbackSessionID)
	}
	key := acpChildTerminalKey{
		SessionID:  sessionID,
		ToolCallID: strings.TrimSpace(env.ParentTool.ToolCallID),
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.parents[key] == nil {
		p.parents[key] = &acpChildTerminalState{tools: map[string]acpChildToolState{}}
	}
}

// project converts one typed subagent Envelope into a parent terminal update.
// handled is true even when the child update has no useful text, because such
// an update must not leak into the main ACP transcript.
func (p *acpChildTerminalProjector) project(env eventstream.Envelope, fallbackSessionID string) (acp.SessionNotification, bool) {
	if p == nil || !isACPChildTerminalEnvelope(env) {
		return acp.SessionNotification{}, false
	}
	parentCallID := strings.TrimSpace(env.ParentTool.ToolCallID)
	if parentCallID == "" {
		return acp.SessionNotification{}, false
	}
	sessionID := strings.TrimSpace(env.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(fallbackSessionID)
	}
	key := acpChildTerminalKey{SessionID: sessionID, ToolCallID: parentCallID}

	p.mu.Lock()
	state := p.parents[key]
	if state == nil {
		state = &acpChildTerminalState{tools: map[string]acpChildToolState{}}
		p.parents[key] = state
	}
	text, kind, lineOriented := childTerminalSegment(state, env.Update)
	text = state.prepare(text, kind, lineOriented)
	p.mu.Unlock()

	if text == "" {
		return acp.SessionNotification{}, true
	}
	status := acp.ToolStatusInProgress
	meta := metautil.WithCompactRuntimeSection(nil, metautil.RuntimeStream, map[string]any{
		metautil.RuntimeStreamMode: "append",
	})
	meta = metautil.WithTerminalInfo(meta, parentCallID)
	meta = metautil.WithTerminalOutput(meta, parentCallID, text)
	return acp.SessionNotification{
		SessionID: sessionID,
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo,
			ToolCallID:    parentCallID,
			Status:        &status,
			Content: []acp.ToolCallContent{{
				Type:       "terminal",
				TerminalID: parentCallID,
			}},
			Meta: meta,
		},
	}, true
}

// normalizeParentClose restores terminal lifecycle metadata on the canonical
// parent Spawn result. Child narrative boundaries deliberately do not close
// this terminal.
func (p *acpChildTerminalProjector) normalizeParentClose(notification acp.SessionNotification) acp.SessionNotification {
	if p == nil {
		return notification
	}
	update, ok := notification.Update.(acp.ToolCallUpdate)
	if !ok || update.Status == nil || !acpToolStatusFinalString(*update.Status) {
		return notification
	}
	key := acpChildTerminalKey{
		SessionID:  strings.TrimSpace(notification.SessionID),
		ToolCallID: strings.TrimSpace(update.ToolCallID),
	}
	if key.ToolCallID == "" {
		return notification
	}
	p.mu.Lock()
	_, tracked := p.parents[key]
	p.mu.Unlock()
	if !tracked {
		return notification
	}
	update.Meta = metautil.WithTerminalInfo(update.Meta, key.ToolCallID)
	notification.Update = update
	return notification
}

func childTerminalSegment(state *acpChildTerminalState, update acp.Update) (string, string, bool) {
	switch typed := update.(type) {
	case acp.ContentChunk:
		return childNarrativeTerminalSegment(typed)
	case *acp.ContentChunk:
		if typed == nil {
			return "", "", false
		}
		return childNarrativeTerminalSegment(*typed)
	case acp.ToolCall:
		return childToolCallTerminalSegment(state, typed), "tool", true
	case *acp.ToolCall:
		if typed == nil {
			return "", "", false
		}
		return childToolCallTerminalSegment(state, *typed), "tool", true
	case acp.ToolCallUpdate:
		return childToolUpdateTerminalSegment(state, typed)
	case *acp.ToolCallUpdate:
		if typed == nil {
			return "", "", false
		}
		return childToolUpdateTerminalSegment(state, *typed)
	case acp.PlanUpdate:
		return childPlanTerminalSegment(typed), "plan", true
	case *acp.PlanUpdate:
		if typed == nil {
			return "", "", false
		}
		return childPlanTerminalSegment(*typed), "plan", true
	default:
		return "", "", false
	}
}

func childNarrativeTerminalSegment(update acp.ContentChunk) (string, string, bool) {
	updateType, _, text, ok := acpContentChunkText(update)
	if !ok || text == "" {
		return "", "", false
	}
	return text, "narrative:" + updateType, false
}

func childToolCallTerminalSegment(state *acpChildTerminalState, update acp.ToolCall) string {
	toolCallID := strings.TrimSpace(update.ToolCallID)
	tool := state.tool(toolCallID)
	if title := childTerminalToolTitle(update.Title, update.Kind); title != "" {
		tool.title = title
	}
	if tool.title == "" {
		tool.title = "Tool"
	}
	if tool.announced || tool.title == "" {
		state.setTool(toolCallID, tool)
		return ""
	}
	tool.announced = true
	state.setTool(toolCallID, tool)
	return tool.title
}

func childToolUpdateTerminalSegment(state *acpChildTerminalState, update acp.ToolCallUpdate) (string, string, bool) {
	toolCallID := strings.TrimSpace(update.ToolCallID)
	tool := state.tool(toolCallID)
	if title := childTerminalToolTitle(stringPtrText(update.Title), stringPtrText(update.Kind)); title != "" {
		tool.title = title
	}
	state.setTool(toolCallID, tool)

	meta, _ := terminalExtensionMetaFromACPContent(update.Meta, toolCallID, update.Content)
	if output, ok := metautil.TerminalOutput(meta); ok && output.Data != "" {
		return output.Data, "terminal", false
	}

	status := strings.ToLower(strings.TrimSpace(stringPtrText(update.Status)))
	switch status {
	case acp.ToolStatusCompleted, "complete", "succeeded", "success", "done":
		return "", "", false
	case acp.ToolStatusFailed, "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout":
		line := strings.TrimSpace(tool.title)
		if line == "" {
			line = "Tool"
		}
		return strings.TrimSpace(line + " " + status), "tool", true
	}
	if tool.announced || tool.title == "" {
		return "", "", false
	}
	tool.announced = true
	state.setTool(toolCallID, tool)
	return tool.title, "tool", true
}

func childPlanTerminalSegment(update acp.PlanUpdate) string {
	var text strings.Builder
	for _, entry := range update.Entries {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		text.WriteString("Plan")
		if status := strings.TrimSpace(entry.Status); status != "" {
			text.WriteString(" [")
			text.WriteString(status)
			text.WriteString("]")
		}
		text.WriteString(": ")
		text.WriteString(content)
		text.WriteByte('\n')
	}
	return text.String()
}

func childTerminalToolTitle(title string, kind string) string {
	if title = strings.TrimSpace(title); title != "" {
		return title
	}
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return ""
}

func stringPtrText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *acpChildTerminalState) tool(toolCallID string) acpChildToolState {
	if s == nil || s.tools == nil {
		return acpChildToolState{}
	}
	return s.tools[strings.TrimSpace(toolCallID)]
}

func (s *acpChildTerminalState) setTool(toolCallID string, tool acpChildToolState) {
	if s == nil {
		return
	}
	if s.tools == nil {
		s.tools = map[string]acpChildToolState{}
	}
	s.tools[strings.TrimSpace(toolCallID)] = tool
}

func (s *acpChildTerminalState) prepare(text string, kind string, lineOriented bool) string {
	if s == nil || text == "" {
		return ""
	}
	kind = strings.TrimSpace(kind)
	if lineOriented {
		text = strings.TrimSpace(text)
		if text == "" {
			return ""
		}
		text += "\n"
	}
	if s.started && !s.endsLine && kind != "" && kind != s.lastKind && !strings.HasPrefix(text, "\n") {
		text = "\n" + text
	}
	s.started = true
	s.endsLine = strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\r")
	s.lastKind = kind
	return text
}
