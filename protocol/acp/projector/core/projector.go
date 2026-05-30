// Package core projects core/session canonical events into ACP wire updates.
package core

import (
	"encoding/json"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type Update = schema.Update
type SessionNotification = schema.SessionNotification
type RequestPermissionRequest = schema.RequestPermissionRequest

type Projector struct{}

func (Projector) ProjectEvent(event session.Event) ([]Update, error) {
	if event.Approval != nil && event.Approval.Status == session.ApprovalPending {
		return nil, nil
	}
	switch event.Type {
	case session.EventUser:
		return textUpdate(schema.UpdateUserMessage, session.EventText(event)), nil
	case session.EventAssistant:
		return assistantUpdates(event), nil
	case session.EventToolCall:
		if event.Tool == nil {
			return nil, nil
		}
		return []Update{toolCall(*event.Tool)}, nil
	case session.EventToolResult:
		if event.Tool == nil {
			return nil, nil
		}
		update := toolCallUpdate(*event.Tool)
		return []Update{update}, nil
	case session.EventPlan:
		if len(event.Plan) == 0 {
			return nil, nil
		}
		return []Update{planUpdate(event.Plan)}, nil
	default:
		return nil, nil
	}
}

func (p Projector) ProjectNotifications(event session.Event) ([]SessionNotification, error) {
	updates, err := p.ProjectEvent(event)
	if err != nil || len(updates) == 0 {
		return nil, err
	}
	out := make([]SessionNotification, 0, len(updates))
	for _, update := range updates {
		if update == nil {
			continue
		}
		out = append(out, SessionNotification{
			SessionID: strings.TrimSpace(event.SessionID),
			Update:    update,
		})
	}
	return out, nil
}

func (Projector) ProjectPermissionRequest(event session.Event) (*RequestPermissionRequest, bool, error) {
	if event.Approval == nil || event.Approval.Status != session.ApprovalPending {
		return nil, false, nil
	}
	tool := session.ToolEvent{}
	if event.Approval.Tool != nil {
		tool = *event.Approval.Tool
	} else if event.Tool != nil {
		tool = *event.Tool
	}
	options := make([]schema.PermissionOption, 0, len(event.Approval.Options))
	for _, item := range event.Approval.Options {
		options = append(options, schema.PermissionOption{
			OptionID: strings.TrimSpace(item.ID),
			Name:     strings.TrimSpace(item.Name),
			Kind:     strings.TrimSpace(item.Kind),
		})
	}
	update := toolCallUpdate(tool)
	return &schema.RequestPermissionRequest{
		SessionID: strings.TrimSpace(event.SessionID),
		ToolCall:  update,
		Options:   options,
	}, true, nil
}

func assistantUpdates(event session.Event) []Update {
	if event.Message == nil {
		return textUpdate(schema.UpdateAgentMessage, session.EventText(event))
	}
	out := make([]Update, 0, 2)
	if reasoning := reasoningText(*event.Message); reasoning != "" {
		out = append(out, schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentThought,
			Content:       schema.TextContent{Type: "text", Text: reasoning},
		})
	}
	if text := textContent(*event.Message); text != "" {
		out = append(out, schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: text},
		})
	}
	return out
}

func textUpdate(kind string, text string) []Update {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return []Update{schema.ContentChunk{
		SessionUpdate: kind,
		Content:       schema.TextContent{Type: "text", Text: text},
	}}
}

func reasoningText(message model.Message) string {
	var parts []string
	for _, part := range message.Parts {
		if part.Kind == model.PartReasoning && part.Reasoning != nil {
			if text := strings.TrimSpace(part.Reasoning.VisibleText); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func textContent(message model.Message) string {
	return strings.TrimSpace(message.TextContent())
}

func toolCall(tool session.ToolEvent) schema.ToolCall {
	return schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    strings.TrimSpace(tool.ID),
		Title:         toolTitle(tool),
		Kind:          toolKind(tool),
		Status:        toolStatus(tool.Status),
		RawInput:      tool.Input,
		Content:       toolContent(tool.Content),
		Locations:     toolLocations(tool.Locations),
		Meta:          tool.Meta,
	}
}

func toolCallUpdate(tool session.ToolEvent) schema.ToolCallUpdate {
	title := toolTitle(tool)
	kind := toolKind(tool)
	status := toolStatus(tool.Status)
	return schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(tool.ID),
		Title:         stringPtr(title),
		Kind:          stringPtr(kind),
		Status:        stringPtr(status),
		RawInput:      tool.Input,
		RawOutput:     tool.Output,
		Content:       toolContent(tool.Content),
		Locations:     toolLocations(tool.Locations),
		Meta:          tool.Meta,
	}
}

func planUpdate(entries []session.PlanEntry) schema.PlanUpdate {
	out := make([]schema.PlanEntry, 0, len(entries))
	for _, item := range entries {
		out = append(out, schema.PlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: "",
		})
	}
	return schema.PlanUpdate{
		SessionUpdate: schema.UpdatePlan,
		Entries:       out,
	}
}

func toolTitle(tool session.ToolEvent) string {
	if title := strings.TrimSpace(tool.Title); title != "" {
		return title
	}
	if name := strings.TrimSpace(tool.Name); name != "" {
		return name
	}
	return "tool"
}

func toolKind(tool session.ToolEvent) string {
	switch strings.ToLower(strings.TrimSpace(firstNonEmpty(tool.Kind, tool.Name))) {
	case "read", "read_file":
		return schema.ToolKindRead
	case "write", "write_file", "patch", "edit":
		return schema.ToolKindEdit
	case "delete", "remove":
		return schema.ToolKindDelete
	case "move", "rename":
		return schema.ToolKindMove
	case "search", "grep", "glob":
		return schema.ToolKindSearch
	case "shell", "bash", "run_command", "exec":
		return schema.ToolKindExecute
	case "plan", "think":
		return schema.ToolKindThink
	case "fetch", "web":
		return schema.ToolKindFetch
	case "switch", "handoff":
		return schema.ToolKindSwitch
	default:
		return schema.ToolKindOther
	}
}

func toolStatus(status session.ToolStatus) string {
	switch status {
	case session.ToolStarted:
		return schema.ToolStatusPending
	case session.ToolRunning, session.ToolWaitingApproval:
		return schema.ToolStatusInProgress
	case session.ToolCompleted:
		return schema.ToolStatusCompleted
	case session.ToolFailed, session.ToolCancelled:
		return schema.ToolStatusFailed
	default:
		return schema.ToolStatusPending
	}
}

func toolContent(in []session.ToolContent) []schema.ToolCallContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCallContent, 0, len(in))
	for _, item := range in {
		out = append(out, schema.ToolCallContent{
			Type:       strings.TrimSpace(item.Type),
			Content:    strings.TrimSpace(item.Text),
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
		})
	}
	return out
}

func toolLocations(in []session.ToolLocation) []schema.ToolCallLocation {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCallLocation, 0, len(in))
	for _, item := range in {
		out = append(out, schema.ToolCallLocation{
			Path: strings.TrimSpace(item.Path),
			Line: item.Line,
		})
	}
	return out
}

func rawInputMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"raw": strings.TrimSpace(string(raw))}
	}
	return out
}

func stringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
