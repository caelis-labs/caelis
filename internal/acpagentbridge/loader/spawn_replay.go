package loader

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
)

// spawnReplayProjector normalizes canonical parent Spawn results from durable
// Session history into standard ACP tool-result content. It never derives a
// parent result from a Task read or wait observation.
type spawnReplayProjector struct{}

func newSpawnReplayProjector(_ []*session.Event) *spawnReplayProjector {
	return &spawnReplayProjector{}
}

func (p *spawnReplayProjector) normalize(
	event *session.Event,
	notification eventstream.SessionNotification,
) eventstream.SessionNotification {
	if p == nil {
		return notification
	}
	update, ok := notification.Update.(eventstream.ToolCallUpdate)
	if !ok || !toolStatusFinal(update.Status) || !sessionEventOwnsSpawnCall(event, update.ToolCallID) {
		return notification
	}
	update = withSpawnReplayResult(update, session.NormalizeProtocolRawMap(update.RawOutput))
	notification.Update = update
	return notification
}

func withSpawnReplayResult(update eventstream.ToolCallUpdate, rawOutput map[string]any) eventstream.ToolCallUpdate {
	if strings.TrimSpace(update.ToolCallID) == "" {
		return update
	}
	status := ""
	if update.Status != nil {
		status = *update.Status
	}
	if toolStatusFinal(update.Status) {
		status = spawnReplayStatus(status, rawOutput)
		update.Status = &status
	}
	text := spawnReplayResultText(status, rawOutput)
	if strings.TrimSpace(text) == "" && !strings.EqualFold(strings.TrimSpace(status), eventstream.ToolStatusFailed) {
		if output, ok := acpmeta.ReadTerminalOutput(update.Meta); ok {
			text = output.Data
		}
	}

	meta := acpmeta.WithoutTerminalOutput(update.Meta)
	delete(meta, acpmeta.TerminalInfoKey)
	delete(meta, acpmeta.TerminalExitKey)
	if len(meta) == 0 {
		meta = nil
	}
	update.Meta = meta

	content := make([]eventstream.ToolCallContent, 0, len(update.Content)+1)
	hasResult := false
	for _, item := range update.Content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Type), "content") {
			if strings.EqualFold(strings.TrimSpace(status), eventstream.ToolStatusFailed) {
				continue
			}
			hasResult = true
		}
		content = append(content, item)
	}
	if !hasResult && strings.TrimSpace(text) != "" {
		content = append(content, eventstream.ToolCallContent{
			Type:    "content",
			Content: eventstream.TextContent{Type: "text", Text: text},
		})
	}
	update.Content = content
	return update
}

func spawnReplayResultText(status string, rawOutput map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(status), eventstream.ToolStatusFailed) {
		for _, key := range []string{"error", "reason"} {
			if text := display.MapString(rawOutput, key); strings.TrimSpace(text) != "" {
				return text
			}
		}
		return ""
	}
	return display.SubagentTaskFinalText(display.MapString(rawOutput, "state"), rawOutput)
}

func sessionEventOwnsSpawnCall(event *session.Event, toolCallID string) bool {
	toolCallID = strings.TrimSpace(toolCallID)
	if event == nil || toolCallID == "" {
		return false
	}
	if event.Tool != nil && strings.TrimSpace(event.Tool.ID) == toolCallID &&
		event.Tool.Name == spawn.ToolName {
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
		if strings.TrimSpace(call.ID) == toolCallID && call.Name == spawn.ToolName {
			return true
		}
	}
	response := message.ToolResponse()
	return response != nil && strings.TrimSpace(response.ID) == toolCallID &&
		response.Name == spawn.ToolName
}

func spawnReplayStatus(status string, rawOutput map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(display.MapString(rawOutput, "state"))) {
	case "completed", "complete", "succeeded", "success", "done":
		return eventstream.ToolStatusCompleted
	case "failed", "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout", "unknown_outcome":
		return eventstream.ToolStatusFailed
	}
	if strings.EqualFold(strings.TrimSpace(status), eventstream.ToolStatusCompleted) {
		return eventstream.ToolStatusCompleted
	}
	return eventstream.ToolStatusFailed
}

func toolStatusFinal(status *string) bool {
	if status == nil {
		return false
	}
	return toolStatusFinalString(*status)
}

func toolStatusFinalString(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case eventstream.ToolStatusCompleted, eventstream.ToolStatusFailed, "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout", "unknown_outcome":
		return true
	default:
		return false
	}
}
