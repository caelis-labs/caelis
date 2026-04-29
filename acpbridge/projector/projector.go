package projector

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/acp/schema"
	bridgeterminal "github.com/OnslaughtSnail/caelis/acpbridge/terminal"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

type Update = schema.Update
type SessionNotification = schema.SessionNotification
type TextContent = schema.TextContent
type ToolCallLocation = schema.ToolCallLocation
type ToolCallContent = schema.ToolCallContent
type ContentChunk = schema.ContentChunk
type ToolCall = schema.ToolCall
type ToolCallUpdate = schema.ToolCallUpdate
type PlanEntry = schema.PlanEntry
type PlanUpdate = schema.PlanUpdate
type PermissionOption = schema.PermissionOption
type RequestPermissionRequest = schema.RequestPermissionRequest

const (
	UpdateUserMessage  = schema.UpdateUserMessage
	UpdateAgentMessage = schema.UpdateAgentMessage
	UpdateAgentThought = schema.UpdateAgentThought
	UpdateToolCall     = schema.UpdateToolCall
	UpdateToolCallInfo = schema.UpdateToolCallInfo
	UpdatePlan         = schema.UpdatePlan

	ToolStatusPending    = schema.ToolStatusPending
	ToolStatusInProgress = schema.ToolStatusInProgress
	ToolStatusCompleted  = schema.ToolStatusCompleted
	ToolStatusFailed     = schema.ToolStatusFailed

	ToolKindRead    = schema.ToolKindRead
	ToolKindEdit    = schema.ToolKindEdit
	ToolKindDelete  = schema.ToolKindDelete
	ToolKindMove    = schema.ToolKindMove
	ToolKindSearch  = schema.ToolKindSearch
	ToolKindExecute = schema.ToolKindExecute
	ToolKindThink   = schema.ToolKindThink
	ToolKindFetch   = schema.ToolKindFetch
	ToolKindSwitch  = schema.ToolKindSwitch
	ToolKindOther   = schema.ToolKindOther
)

// EventProjector is the baseline ACP projection implementation for canonical
// SDK session events.
type EventProjector struct{}

// ProjectEvent converts one canonical event into ACP-compatible update payloads.
func (EventProjector) ProjectEvent(event *sdksession.Event) ([]Update, error) {
	if event == nil {
		return nil, nil
	}
	if _, ok, err := (EventProjector{}).ProjectPermissionRequest(event); err != nil {
		return nil, err
	} else if ok {
		return nil, nil
	}
	updates := explicitUpdates(event)
	if len(updates) > 0 {
		return updates, nil
	}
	return inferredUpdates(event), nil
}

// ProjectNotifications wraps projected updates in ACP session/update envelopes.
func (p EventProjector) ProjectNotifications(event *sdksession.Event) ([]SessionNotification, error) {
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

// ProjectPermissionRequest converts one canonical approval event into one
// ACP-compatible session/request_permission payload.
func (EventProjector) ProjectPermissionRequest(event *sdksession.Event) (*RequestPermissionRequest, bool, error) {
	if event == nil || event.Protocol == nil || event.Protocol.Approval == nil {
		return nil, false, nil
	}
	approval := event.Protocol.Approval
	toolCall, err := toolCallUpdateFromProtocol(approval.ToolCall)
	if err != nil {
		return nil, false, err
	}
	options := make([]PermissionOption, 0, len(approval.Options))
	for _, item := range approval.Options {
		options = append(options, PermissionOption{
			OptionID: strings.TrimSpace(item.ID),
			Name:     strings.TrimSpace(item.Name),
			Kind:     strings.TrimSpace(item.Kind),
		})
	}
	return &RequestPermissionRequest{
		SessionID: strings.TrimSpace(event.SessionID),
		ToolCall:  toolCall,
		Options:   options,
	}, true, nil
}

func explicitUpdates(event *sdksession.Event) []Update {
	if event == nil || event.Protocol == nil {
		return nil
	}
	switch normalizeUpdateType(event.Protocol.UpdateType) {
	case UpdateUserMessage:
		return singleContentUpdate(UpdateUserMessage, textForUserEvent(event))
	case UpdateAgentMessage:
		return singleContentUpdate(UpdateAgentMessage, textForAssistantEvent(event))
	case UpdateAgentThought:
		return singleContentUpdate(UpdateAgentThought, reasoningForAssistantEvent(event))
	case UpdateToolCall:
		return explicitToolCallUpdates(event)
	case UpdateToolCallInfo:
		update, ok, err := toolCallUpdateForEvent(event)
		if err != nil || !ok {
			return nil
		}
		return []Update{update}
	case UpdatePlan:
		if event.Protocol.Plan == nil {
			return nil
		}
		return []Update{planUpdateFromProtocol(*event.Protocol.Plan)}
	case "":
		return nil
	default:
		return nil
	}
}

func inferredUpdates(event *sdksession.Event) []Update {
	if event == nil {
		return nil
	}
	switch sdksession.EventTypeOf(event) {
	case sdksession.EventTypeUser:
		return singleContentUpdate(UpdateUserMessage, textForUserEvent(event))
	case sdksession.EventTypeAssistant:
		return inferredAssistantUpdates(event)
	case sdksession.EventTypeToolCall:
		return inferredToolCallUpdates(event)
	case sdksession.EventTypeToolResult:
		update, ok, err := toolCallUpdateForEvent(event)
		if err != nil || !ok {
			return nil
		}
		return []Update{update}
	case sdksession.EventTypePlan:
		if event.Protocol == nil || event.Protocol.Plan == nil {
			return nil
		}
		return []Update{planUpdateFromProtocol(*event.Protocol.Plan)}
	default:
		return nil
	}
}

func inferredAssistantUpdates(event *sdksession.Event) []Update {
	if event == nil || event.Message == nil {
		return singleContentUpdate(UpdateAgentMessage, textForAssistantEvent(event))
	}
	out := make([]Update, 0, 2)
	if reasoning := reasoningForAssistantEvent(event); reasoning != "" {
		out = append(out, ContentChunk{
			SessionUpdate: UpdateAgentThought,
			Content:       TextContent{Type: "text", Text: reasoning},
		})
	}
	if text := textForAssistantEvent(event); text != "" {
		out = append(out, ContentChunk{
			SessionUpdate: UpdateAgentMessage,
			Content:       TextContent{Type: "text", Text: text},
		})
	}
	return out
}

func explicitToolCallUpdates(event *sdksession.Event) []Update {
	out := inferredAssistantMessageOnly(event)
	call, ok, err := toolCallForEvent(event)
	if err != nil || !ok {
		return out
	}
	out = append(out, call)
	return out
}

func inferredAssistantMessageOnly(event *sdksession.Event) []Update {
	if event == nil || event.Message == nil || event.Message.Role != sdkmodel.RoleAssistant {
		return nil
	}
	out := make([]Update, 0, 2)
	if reasoning := reasoningForAssistantEvent(event); reasoning != "" {
		out = append(out, ContentChunk{
			SessionUpdate: UpdateAgentThought,
			Content:       TextContent{Type: "text", Text: reasoning},
		})
	}
	if text := textForAssistantEvent(event); text != "" {
		out = append(out, ContentChunk{
			SessionUpdate: UpdateAgentMessage,
			Content:       TextContent{Type: "text", Text: text},
		})
	}
	return out
}

func inferredToolCallUpdates(event *sdksession.Event) []Update {
	if event == nil {
		return nil
	}
	if event.Protocol != nil && event.Protocol.ToolCall != nil {
		return explicitToolCallUpdates(event)
	}
	out := inferredAssistantMessageOnly(event)
	if event.Message == nil {
		return out
	}
	for _, call := range event.Message.ToolCalls() {
		args := parseObject(call.Args)
		out = append(out, ToolCall{
			SessionUpdate: UpdateToolCall,
			ToolCallID:    strings.TrimSpace(call.ID),
			Title:         summarizeToolCallTitle(call.Name, args),
			Kind:          toolKindForName(call.Name),
			Status:        ToolStatusPending,
			RawInput:      args,
		})
	}
	return out
}

func singleContentUpdate(kind string, text string) []Update {
	if text == "" {
		return nil
	}
	return []Update{ContentChunk{
		SessionUpdate: kind,
		Content:       TextContent{Type: "text", Text: text},
	}}
}

func toolCallForEvent(event *sdksession.Event) (ToolCall, bool, error) {
	if event == nil {
		return ToolCall{}, false, nil
	}
	if event.Protocol != nil && event.Protocol.ToolCall != nil {
		call := event.Protocol.ToolCall
		return ToolCall{
			SessionUpdate: UpdateToolCall,
			ToolCallID:    strings.TrimSpace(call.ID),
			Title:         firstNonEmpty(strings.TrimSpace(call.Title), strings.TrimSpace(call.Name)),
			Kind:          firstNonEmpty(strings.TrimSpace(call.Kind), toolKindForName(call.Name)),
			Status:        firstNonEmpty(strings.TrimSpace(call.Status), ToolStatusPending),
			RawInput:      cloneAnyMap(call.RawInput),
			RawOutput:     cloneAnyMap(call.RawOutput),
		}, true, nil
	}
	if event.Message == nil {
		return ToolCall{}, false, nil
	}
	calls := event.Message.ToolCalls()
	if len(calls) == 0 {
		return ToolCall{}, false, nil
	}
	args := parseObject(calls[0].Args)
	return ToolCall{
		SessionUpdate: UpdateToolCall,
		ToolCallID:    strings.TrimSpace(calls[0].ID),
		Title:         summarizeToolCallTitle(calls[0].Name, args),
		Kind:          toolKindForName(calls[0].Name),
		Status:        ToolStatusPending,
		RawInput:      args,
	}, true, nil
}

func toolCallUpdateForEvent(event *sdksession.Event) (ToolCallUpdate, bool, error) {
	if event == nil {
		return ToolCallUpdate{}, false, nil
	}
	if event.Protocol != nil && event.Protocol.ToolCall != nil {
		update, err := toolCallUpdateFromProtocol(*event.Protocol.ToolCall)
		if err != nil {
			return ToolCallUpdate{}, false, err
		}
		if terminal := bridgeterminal.ContentFromEvent(event); len(terminal) > 0 {
			update.Content = append(update.Content, terminal...)
		}
		if text := strings.TrimSpace(event.Text); text != "" {
			update.Content = append(update.Content, ToolCallContent{Type: "content", Content: TextContent{Type: "text", Text: text}})
		}
		return update, true, nil
	}
	if event.Message == nil {
		return ToolCallUpdate{}, false, nil
	}
	resp := event.Message.ToolResponse()
	if resp == nil {
		return ToolCallUpdate{}, false, nil
	}
	status := ToolStatusCompleted
	if raw, ok := event.Meta["is_error"].(bool); ok && raw {
		status = ToolStatusFailed
	}
	name := strings.TrimSpace(resp.Name)
	kind := toolKindForName(name)
	return ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(resp.ID),
		Kind:          stringPtr(kind),
		Status:        stringPtr(status),
		RawOutput:     cloneAnyMap(resp.Result),
		Content:       bridgeterminal.ContentFromEvent(event),
	}, true, nil
}

func toolCallUpdateFromProtocol(call sdksession.ProtocolToolCall) (ToolCallUpdate, error) {
	id := strings.TrimSpace(call.ID)
	if id == "" {
		return ToolCallUpdate{}, fmt.Errorf("acpbridge/projector: approval or tool update missing tool call id")
	}
	update := ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    id,
	}
	if title := strings.TrimSpace(call.Title); title != "" {
		update.Title = stringPtr(title)
	}
	if kind := firstNonEmpty(strings.TrimSpace(call.Kind), toolKindForName(call.Name)); kind != "" {
		update.Kind = stringPtr(kind)
	}
	if status := acpToolStatus(call.Status); status != "" {
		update.Status = stringPtr(status)
	}
	if input := cloneAnyMap(call.RawInput); len(input) > 0 {
		update.RawInput = input
	}
	if output := cloneAnyMap(call.RawOutput); len(output) > 0 {
		update.RawOutput = output
	}
	return update, nil
}

func acpToolStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", ToolStatusPending, ToolStatusInProgress, ToolStatusCompleted, ToolStatusFailed:
		return strings.TrimSpace(status)
	case "running", "waiting_approval":
		return ToolStatusInProgress
	case "cancelled", "canceled", "interrupted", "terminated", "timed_out", "timeout":
		return ToolStatusFailed
	default:
		return strings.TrimSpace(status)
	}
}

func planUpdateFromProtocol(plan sdksession.ProtocolPlan) PlanUpdate {
	entries := make([]PlanEntry, 0, len(plan.Entries))
	for _, item := range plan.Entries {
		entries = append(entries, PlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: firstNonEmpty(strings.TrimSpace(item.Priority), "medium"),
		})
	}
	return PlanUpdate{
		SessionUpdate: UpdatePlan,
		Entries:       entries,
	}
}

func normalizeUpdateType(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}

func textForUserEvent(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	if text := strings.TrimSpace(event.Text); text != "" {
		return text
	}
	if event.Message != nil {
		return strings.TrimSpace(event.Message.TextContent())
	}
	return ""
}

func textForAssistantEvent(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	if text := event.Text; text != "" {
		return text
	}
	if event.Message != nil {
		return event.Message.TextContent()
	}
	return ""
}

func reasoningForAssistantEvent(event *sdksession.Event) string {
	if event == nil || event.Message == nil {
		return ""
	}
	return event.Message.ReasoningText()
}

func parseObject(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func summarizeToolCallTitle(name string, args map[string]any) string {
	name = strings.TrimSpace(strings.ToUpper(name))
	switch name {
	case "READ", "WRITE", "PATCH", "SEARCH", "LIST", "GLOB":
		if path, _ := args["path"].(string); strings.TrimSpace(path) != "" {
			return strings.TrimSpace(name + " " + path)
		}
	case "BASH", "TASK":
		if command, _ := args["command"].(string); strings.TrimSpace(command) != "" {
			return strings.TrimSpace(name + " " + command)
		}
		if action, _ := args["action"].(string); strings.TrimSpace(action) != "" {
			if taskID, _ := args["task_id"].(string); strings.TrimSpace(taskID) != "" {
				return strings.TrimSpace(name + " " + action + " " + taskID)
			}
			return strings.TrimSpace(name + " " + action)
		}
	}
	return name
}

func toolKindForName(name string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ":
		return ToolKindRead
	case "WRITE", "PATCH":
		return ToolKindEdit
	case "SEARCH", "GLOB", "LIST":
		return ToolKindSearch
	case "BASH", "TASK":
		return ToolKindExecute
	default:
		return ToolKindOther
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func stringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func cloneAnyMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	return maps.Clone(values)
}
