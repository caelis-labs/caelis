package kernel

import (
	"encoding/json"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func canonicalToolKind(event *session.Event) string {
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		return strings.TrimSpace(toolPayload.Kind)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.Kind)
	}
	return ""
}

func canonicalToolTitle(event *session.Event) string {
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		return strings.TrimSpace(toolPayload.Title)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.Title)
	}
	return ""
}

func canonicalToolRawInput(event *session.Event) map[string]any {
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		if len(toolPayload.Input) > 0 {
			return maps.Clone(toolPayload.Input)
		}
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if len(update.RawInput) > 0 {
			return maps.Clone(update.RawInput)
		}
	}
	if raw := toolUseRawInputFromMessage(event); len(raw) > 0 {
		return raw
	}
	return nil
}

func canonicalToolRawOutput(event *session.Event) map[string]any {
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		if len(toolPayload.Output) > 0 {
			return maps.Clone(toolPayload.Output)
		}
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if len(update.RawOutput) > 0 {
			return maps.Clone(update.RawOutput)
		}
	}
	return nil
}

func canonicalToolContent(event *session.Event) []session.ProtocolToolCallContent {
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		return protocolContentFromEventTool(toolPayload.Content)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if content := session.ProtocolToolCallContentOf(update); len(content) > 0 {
			return content
		}
	}
	return nil
}

func protocolContentFromEventTool(content []session.EventToolContent) []session.ProtocolToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]session.ProtocolToolCallContent, 0, len(content))
	for _, item := range content {
		var oldText *string
		if item.OldText != nil {
			value := *item.OldText
			oldText = &value
		}
		var payload any
		if strings.TrimSpace(item.Text) != "" {
			payload = session.ProtocolTextContent(item.Text)
		}
		out = append(out, session.ProtocolToolCallContent{
			Type:       strings.TrimSpace(item.Type),
			Content:    payload,
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			OldText:    oldText,
			NewText:    item.NewText,
		})
	}
	return out
}

func toolUseRawInputFromMessage(event *session.Event) map[string]any {
	if event == nil {
		return nil
	}
	message, ok := session.ModelMessageOf(event)
	if !ok {
		return nil
	}
	callID := ""
	toolName := ""
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		callID = strings.TrimSpace(toolPayload.ID)
		toolName = strings.TrimSpace(toolPayload.Name)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		callID = strings.TrimSpace(update.ToolCallID)
		toolName = session.CanonicalToolName(event, update)
	}
	for _, call := range message.ToolCalls() {
		if callID != "" && strings.TrimSpace(call.ID) != callID {
			continue
		}
		if callID == "" && toolName != "" && !strings.EqualFold(strings.TrimSpace(call.Name), toolName) {
			continue
		}
		raw := rawInputFromJSONString(call.Args)
		if len(raw) > 0 {
			return raw
		}
	}
	return nil
}

func rawInputFromJSONString(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	return payload
}

func canonicalToolCallStatus(status string) ToolStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "pending", "started":
		return ToolStatusStarted
	case "in_progress", "running":
		return ToolStatusRunning
	case "waiting_approval":
		return ToolStatusWaitingApproval
	case "completed":
		return ToolStatusCompleted
	case "error", "failed":
		return ToolStatusFailed
	case "interrupted":
		return ToolStatusInterrupted
	case "cancelled", "canceled":
		return ToolStatusCancelled
	default:
		return ToolStatus(strings.TrimSpace(status))
	}
}

func canonicalToolResultStatus(status string, isErr bool) ToolStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "started":
		return ToolStatusStarted
	case "in_progress", "running":
		return ToolStatusRunning
	case "waiting_approval":
		return ToolStatusWaitingApproval
	case "completed":
		return ToolStatusCompleted
	case "error", "failed":
		return ToolStatusFailed
	case "interrupted":
		return ToolStatusInterrupted
	case "cancelled", "canceled":
		return ToolStatusCancelled
	case "":
		if isErr {
			return ToolStatusFailed
		}
		return ToolStatusCompleted
	default:
		return ToolStatus(strings.TrimSpace(status))
	}
}
