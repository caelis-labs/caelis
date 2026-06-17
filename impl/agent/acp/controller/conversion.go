package acp

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
)

func acpToolDisplayName(kind string, title string) string {
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return strings.TrimSpace(title)
}

func acpToolRawInput(kind string, title string, raw any) map[string]any {
	out := acpRawMap(raw)
	if len(out) == 0 {
		return nil
	}
	return out
}

func acpToolRawOutput(raw any) map[string]any {
	out := acpRawMap(raw)
	if len(out) == 0 {
		return nil
	}
	return out
}

func acpToolProtocolUpdate(updateType string, tool *session.ProtocolToolCall, meta map[string]any) *session.ProtocolUpdate {
	if tool == nil {
		return &session.ProtocolUpdate{SessionUpdate: strings.TrimSpace(updateType)}
	}
	update := &session.ProtocolUpdate{
		SessionUpdate: strings.TrimSpace(updateType),
		ToolCallID:    strings.TrimSpace(tool.ID),
		Kind:          strings.TrimSpace(tool.Kind),
		Title:         strings.TrimSpace(tool.Title),
		Status:        strings.TrimSpace(tool.Status),
		RawInput:      maps.Clone(tool.RawInput),
		RawOutput:     maps.Clone(tool.RawOutput),
		Meta:          maps.Clone(meta),
	}
	if len(tool.Content) > 0 {
		update.Content = session.CloneProtocolToolCallContent(tool.Content)
	}
	return update
}

func acpToolContent(content []client.ToolCallContent) []session.ProtocolToolCallContent {
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
		out = append(out, session.ProtocolToolCallContent{
			Type:       strings.TrimSpace(item.Type),
			Content:    item.Content,
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			OldText:    oldText,
			NewText:    item.NewText,
		})
	}
	return session.CloneProtocolToolCallContent(out)
}

func acpRawMap(raw any) map[string]any {
	switch typed := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		return maps.Clone(typed)
	default:
		if text := strings.TrimSpace(textFromContentValue(typed)); text != "" {
			return map[string]any{"text": text}
		}
		if text := strings.TrimSpace(fmt.Sprint(typed)); text != "" && text != "<nil>" {
			return map[string]any{"text": text}
		}
		return nil
	}
}

func messageForContentChunk(chunk client.ContentChunk, text string) model.Message {
	role := model.RoleAssistant
	if strings.TrimSpace(chunk.SessionUpdate) == client.UpdateUserMessage {
		role = model.RoleUser
	}
	if strings.TrimSpace(chunk.SessionUpdate) == client.UpdateAgentThought {
		return model.NewReasoningMessage(role, text, model.ReasoningVisibilityVisible)
	}
	return model.NewTextMessage(role, text)
}

func planEntries(in []client.PlanEntry) []session.ProtocolPlanEntry {
	out := make([]session.ProtocolPlanEntry, 0, len(in))
	for _, item := range in {
		out = append(out, session.ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: "",
		})
	}
	return out
}

func toolEventTypeFromStatus(status string) session.EventType {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled":
		return session.EventTypeToolResult
	default:
		return session.EventTypeToolCall
	}
}

func buildPromptParts(input string, parts []model.ContentPart) []json.RawMessage {
	if len(parts) == 0 {
		input = strings.TrimSpace(input)
		if input == "" {
			return nil
		}
		raw, _ := json.Marshal(client.TextContent{
			Type: "text",
			Text: input,
		})
		return []json.RawMessage{raw}
	}
	out := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case model.ContentPartImage:
			raw, _ := json.Marshal(client.ImageContent{
				Type:     "image",
				MimeType: strings.TrimSpace(part.MimeType),
				Data:     strings.TrimSpace(part.Data),
				Name:     strings.TrimSpace(part.FileName),
			})
			out = append(out, raw)
		default:
			text := part.Text
			if text == "" {
				continue
			}
			raw, _ := json.Marshal(client.TextContent{
				Type: "text",
				Text: text,
			})
			out = append(out, raw)
		}
	}
	if len(out) == 0 && strings.TrimSpace(input) != "" {
		raw, _ := json.Marshal(client.TextContent{
			Type: "text",
			Text: strings.TrimSpace(input),
		})
		out = append(out, raw)
	}
	return out
}

func ptrMessage(msg model.Message) *model.Message {
	return &msg
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func pickWorkDir(preferred string, fallback string) string {
	if strings.TrimSpace(preferred) != "" {
		return strings.TrimSpace(preferred)
	}
	return strings.TrimSpace(fallback)
}

func derefString(in *string) string {
	if in == nil {
		return ""
	}
	return strings.TrimSpace(*in)
}
