package loader

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// spawnReplayProjector reconstructs the stdio Spawn terminal view from durable
// Session history. Child Task streams are transient, so a canonical Task wait
// result is the historical source for a child FinalMessage when no durable
// parent Spawn result exists.
type spawnReplayProjector struct {
	closed        map[string]struct{}
	authoritative map[string]struct{}
}

func newSpawnReplayProjector(events []*session.Event) *spawnReplayProjector {
	p := &spawnReplayProjector{
		closed:        map[string]struct{}{},
		authoritative: map[string]struct{}{},
	}
	for _, event := range events {
		if toolCallID := finalSpawnEventCallID(event); toolCallID != "" {
			p.authoritative[toolCallID] = struct{}{}
		}
	}
	return p
}

func (p *spawnReplayProjector) normalize(
	event *session.Event,
	notification acp.SessionNotification,
) acp.SessionNotification {
	if p == nil {
		return notification
	}
	update, ok := notification.Update.(acp.ToolCallUpdate)
	if !ok || !toolStatusFinal(update.Status) || !sessionEventOwnsSpawnCall(event, update.ToolCallID) {
		return notification
	}
	update = withSpawnReplayTerminal(update, schema.NormalizeRawMap(update.RawOutput))
	p.closed[strings.TrimSpace(update.ToolCallID)] = struct{}{}
	notification.Update = update
	return notification
}

func (p *spawnReplayProjector) observedParentCloses(env eventstream.Envelope, sessionID string) []acp.SessionNotification {
	if p == nil {
		return nil
	}
	results := projector.SpawnTaskResultsFromEnvelope(env)
	if len(results) == 0 {
		return nil
	}
	out := make([]acp.SessionNotification, 0, len(results))
	for _, result := range results {
		parentCallID := strings.TrimSpace(result.ParentCallID)
		if parentCallID == "" {
			continue
		}
		if _, duplicate := p.closed[parentCallID]; duplicate {
			continue
		}
		if _, hasCanonicalParentResult := p.authoritative[parentCallID]; hasCanonicalParentResult {
			continue
		}
		p.closed[parentCallID] = struct{}{}
		status := result.Status
		update := withSpawnReplayTerminal(acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo,
			ToolCallID:    parentCallID,
			Status:        &status,
			RawOutput:     result.RawOutput,
		}, result.RawOutput)
		out = append(out, acp.SessionNotification{
			SessionID: strings.TrimSpace(sessionID),
			Update:    update,
		})
	}
	return out
}

func withSpawnReplayTerminal(update acp.ToolCallUpdate, rawOutput map[string]any) acp.ToolCallUpdate {
	toolCallID := strings.TrimSpace(update.ToolCallID)
	if toolCallID == "" {
		return update
	}
	terminalID := toolCallID
	if info, ok := metautil.TerminalInfo(update.Meta); ok && strings.TrimSpace(info.TerminalID) != "" {
		terminalID = strings.TrimSpace(info.TerminalID)
	}
	update.Meta = metautil.WithTerminalInfo(update.Meta, terminalID)
	if _, exists := metautil.TerminalOutput(update.Meta); !exists {
		text := display.SubagentTaskFinalText(display.MapString(rawOutput, "state"), rawOutput)
		update.Meta = metautil.WithTerminalOutput(update.Meta, terminalID, text)
	}
	if toolStatusFinal(update.Status) {
		update.Meta = metautil.WithTerminalExit(update.Meta, terminalID, nil, nil)
	}
	if !hasTerminalAnchor(update.Content, terminalID) {
		update.Content = append(update.Content, acp.ToolCallContent{
			Type:       "terminal",
			TerminalID: terminalID,
		})
	}
	return update
}

func hasTerminalAnchor(content []acp.ToolCallContent, terminalID string) bool {
	for _, item := range content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "terminal") &&
			strings.TrimSpace(item.TerminalID) == strings.TrimSpace(terminalID) {
			return true
		}
	}
	return false
}

func sessionEventOwnsSpawnCall(event *session.Event, toolCallID string) bool {
	toolCallID = strings.TrimSpace(toolCallID)
	if event == nil || toolCallID == "" {
		return false
	}
	if event.Tool != nil && strings.TrimSpace(event.Tool.ID) == toolCallID &&
		identity.CanonicalOrSelf(event.Tool.Name) == identity.Spawn {
		return true
	}
	message := event.Message
	if message == nil {
		if projected, ok := session.ModelMessageOf(event); ok {
			message = &projected
		}
	}
	if message == nil {
		return false
	}
	for _, call := range message.ToolCalls() {
		if strings.TrimSpace(call.ID) == toolCallID && identity.CanonicalOrSelf(call.Name) == identity.Spawn {
			return true
		}
	}
	response := message.ToolResponse()
	return response != nil && strings.TrimSpace(response.ID) == toolCallID &&
		identity.CanonicalOrSelf(response.Name) == identity.Spawn
}

func finalSpawnEventCallID(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Tool != nil && identity.CanonicalOrSelf(event.Tool.Name) == identity.Spawn &&
		toolStatusFinalString(event.Tool.Status) {
		return strings.TrimSpace(event.Tool.ID)
	}
	update := session.ProtocolUpdateOf(event)
	if update != nil && toolStatusFinalString(update.Status) &&
		sessionEventOwnsSpawnCall(event, update.ToolCallID) {
		return strings.TrimSpace(update.ToolCallID)
	}
	return ""
}

func toolStatusFinal(status *string) bool {
	if status == nil {
		return false
	}
	return toolStatusFinalString(*status)
}

func toolStatusFinalString(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case acp.ToolStatusCompleted, acp.ToolStatusFailed, "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout":
		return true
	default:
		return false
	}
}
