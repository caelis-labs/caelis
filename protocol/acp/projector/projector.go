package projector

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/internal/displaypolicy"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
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

// Projector converts canonical session events into ACP-compatible session/update
// notifications and request_permission payloads.
type Projector interface {
	ProjectEvent(*session.Event) ([]Update, error)
	ProjectNotifications(*session.Event) ([]SessionNotification, error)
	ProjectPermissionRequest(*session.Event) (*RequestPermissionRequest, bool, error)
}

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
func (EventProjector) ProjectEvent(event *session.Event) ([]Update, error) {
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
func (p EventProjector) ProjectNotifications(event *session.Event) ([]SessionNotification, error) {
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
func (EventProjector) ProjectPermissionRequest(event *session.Event) (*RequestPermissionRequest, bool, error) {
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

func explicitUpdates(event *session.Event) []Update {
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

func inferredUpdates(event *session.Event) []Update {
	if event == nil {
		return nil
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		return singleContentUpdate(UpdateUserMessage, textForUserEvent(event))
	case session.EventTypeAssistant:
		return inferredAssistantUpdates(event)
	case session.EventTypeToolCall:
		return inferredToolCallUpdates(event)
	case session.EventTypeToolResult:
		update, ok, err := toolCallUpdateForEvent(event)
		if err != nil || !ok {
			return nil
		}
		return []Update{update}
	case session.EventTypePlan:
		if event.Protocol == nil || event.Protocol.Plan == nil {
			return nil
		}
		return []Update{planUpdateFromProtocol(*event.Protocol.Plan)}
	default:
		return nil
	}
}

func inferredAssistantUpdates(event *session.Event) []Update {
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

func explicitToolCallUpdates(event *session.Event) []Update {
	out := inferredAssistantMessageOnly(event)
	call, ok, err := toolCallForEvent(event)
	if err != nil || !ok {
		return out
	}
	toolName := ""
	if event.Protocol != nil && event.Protocol.ToolCall != nil {
		toolName = event.Protocol.ToolCall.Name
	}
	rawInput := anyStringMap(call.RawInput)
	call = withDisplayTerminal(call, toolName, rawInput)
	out = append(out, call)
	if update, ok := displayTerminalInitialOutputUpdate(call.ToolCallID, toolName, rawInput); ok {
		out = append(out, update)
	}
	return out
}

func inferredAssistantMessageOnly(event *session.Event) []Update {
	if event == nil || event.Message == nil || event.Message.Role != model.RoleAssistant {
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

func inferredToolCallUpdates(event *session.Event) []Update {
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
		update := ToolCall{
			SessionUpdate: UpdateToolCall,
			ToolCallID:    strings.TrimSpace(call.ID),
			Title:         summarizeToolCallTitle(call.Name, args),
			Kind:          toolKindForName(call.Name),
			Status:        ToolStatusPending,
			RawInput:      args,
		}
		update = withDisplayTerminal(update, call.Name, args)
		out = append(out, update)
		if terminalUpdate, ok := displayTerminalInitialOutputUpdate(update.ToolCallID, call.Name, args); ok {
			out = append(out, terminalUpdate)
		}
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

func toolCallForEvent(event *session.Event) (ToolCall, bool, error) {
	if event == nil {
		return ToolCall{}, false, nil
	}
	if event.Protocol != nil && event.Protocol.ToolCall != nil {
		call := event.Protocol.ToolCall
		rawInput := cloneAnyMap(call.RawInput)
		return ToolCall{
			SessionUpdate: UpdateToolCall,
			ToolCallID:    strings.TrimSpace(call.ID),
			Title:         firstNonEmpty(strings.TrimSpace(call.Title), summarizeToolCallTitle(call.Name, rawInput), strings.TrimSpace(call.Name)),
			Kind:          firstNonEmpty(strings.TrimSpace(call.Kind), toolKindForName(call.Name)),
			Status:        firstNonEmpty(strings.TrimSpace(call.Status), ToolStatusPending),
			RawInput:      rawInput,
			RawOutput:     cloneAnyMap(call.RawOutput),
			Content:       projectToolContentForTool(call.Content, call.ID, call.Name),
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
	call := ToolCall{
		SessionUpdate: UpdateToolCall,
		ToolCallID:    strings.TrimSpace(calls[0].ID),
		Title:         summarizeToolCallTitle(calls[0].Name, args),
		Kind:          toolKindForName(calls[0].Name),
		Status:        ToolStatusPending,
		RawInput:      args,
	}
	call = withDisplayTerminal(call, calls[0].Name, args)
	return call, true, nil
}

func toolCallUpdateForEvent(event *session.Event) (ToolCallUpdate, bool, error) {
	if event == nil {
		return ToolCallUpdate{}, false, nil
	}
	if event.Protocol != nil && event.Protocol.ToolCall != nil {
		update, err := toolCallUpdateFromProtocol(*event.Protocol.ToolCall)
		if err != nil {
			return ToolCallUpdate{}, false, err
		}
		if len(update.Content) == 0 {
			if terminal := terminalContentFromEvent(event); len(terminal) > 0 {
				update.Content = append(update.Content, terminal...)
			}
		}
		if len(update.Content) == 0 {
			if updateFromProtocol := session.ProtocolUpdateOf(event); updateFromProtocol != nil {
				update.Content = projectToolContentForTool(session.ProtocolToolCallContentOf(updateFromProtocol), event.Protocol.ToolCall.ID, event.Protocol.ToolCall.Name)
			}
		}
		if len(update.Content) == 0 {
			if text := strings.TrimSpace(event.Text); text != "" {
				update.Content = append(update.Content, ToolCallContent{Type: "content", Content: TextContent{Type: "text", Text: text}})
			}
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
		Content:       terminalContentFromEvent(event),
	}, true, nil
}

func toolCallUpdateFromProtocol(call session.ProtocolToolCall) (ToolCallUpdate, error) {
	id := strings.TrimSpace(call.ID)
	if id == "" {
		return ToolCallUpdate{}, fmt.Errorf("protocol/acp/projector: approval or tool update missing tool call id")
	}
	update := ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    id,
	}
	if title := strings.TrimSpace(call.Title); title != "" {
		update.Title = stringPtr(title)
	} else if title := summarizeToolCallTitle(call.Name, call.RawInput); title != "" {
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
	update.Content = projectToolContentForTool(call.Content, call.ID, call.Name)
	return update, nil
}

func terminalContentFromEvent(event *session.Event) []ToolCallContent {
	terminalID := terminalIDFromEvent(event)
	if terminalID == "" {
		return nil
	}
	return []ToolCallContent{{
		Type:       "terminal",
		TerminalID: terminalID,
	}}
}

func projectToolContentForTool(content []session.ProtocolToolCallContent, toolCallID string, name string) []ToolCallContent {
	displayTerminalID, _ := displayTerminalID(toolCallID, name)
	return projectToolContent(content, displayTerminalID)
}

func projectToolContent(content []session.ProtocolToolCallContent, displayTerminalID string) []ToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]ToolCallContent, 0, len(content))
	for _, item := range content {
		contentType := strings.TrimSpace(item.Type)
		terminalID := strings.TrimSpace(item.TerminalID)
		if strings.EqualFold(contentType, "terminal") && strings.TrimSpace(displayTerminalID) != "" {
			terminalID = strings.TrimSpace(displayTerminalID)
		}
		out = append(out, ToolCallContent{
			Type:       contentType,
			Content:    item.Content,
			TerminalID: terminalID,
		})
	}
	return out
}

func terminalIDFromEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Meta != nil {
		if terminalID, _ := event.Meta["terminal_id"].(string); strings.TrimSpace(terminalID) != "" {
			return strings.TrimSpace(terminalID)
		}
	}
	if event.Protocol != nil && event.Protocol.ToolCall != nil {
		for _, item := range event.Protocol.ToolCall.Content {
			if terminalID := strings.TrimSpace(item.TerminalID); terminalID != "" {
				return terminalID
			}
		}
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		for _, item := range session.ProtocolToolCallContentOf(update) {
			if terminalID := strings.TrimSpace(item.TerminalID); terminalID != "" {
				return terminalID
			}
		}
	}
	return ""
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

func planUpdateFromProtocol(plan session.ProtocolPlan) PlanUpdate {
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

func textForUserEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if text := strings.TrimSpace(event.Text); text != "" {
		return text
	}
	if event.Message != nil {
		return strings.TrimSpace(event.Message.TextContent())
	}
	return strings.TrimSpace(session.EventText(event))
}

func textForAssistantEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if text := event.Text; text != "" {
		return text
	}
	if event.Message != nil {
		return event.Message.TextContent()
	}
	return session.EventText(event)
}

func reasoningForAssistantEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Message != nil {
		if reasoning := event.Message.ReasoningText(); reasoning != "" {
			return reasoning
		}
	}
	if reasoning := nestedString(event.Meta, "caelis", "runtime", "replay", "reasoning_text"); reasoning != "" {
		return reasoning
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if reasoning := reasoningFromProtocolContent(update.Content); reasoning != "" {
			return reasoning
		}
		if normalizeUpdateType(update.SessionUpdate) == UpdateAgentThought {
			return session.EventText(event)
		}
	}
	if event.Protocol != nil && normalizeUpdateType(event.Protocol.UpdateType) == UpdateAgentThought {
		return session.EventText(event)
	}
	return ""
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

func reasoningFromProtocolContent(content any) string {
	switch typed := content.(type) {
	case nil:
		return ""
	case json.RawMessage:
		if len(typed) == 0 {
			return ""
		}
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err != nil {
			return ""
		}
		return reasoningFromProtocolContent(decoded)
	case map[string]any:
		for _, key := range []string{"reasoningText", "reasoning_text", "reasoning", "thought"} {
			if text, _ := typed[key].(string); text != "" {
				return text
			}
		}
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := reasoningFromProtocolContent(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func withDisplayTerminal(call ToolCall, name string, args map[string]any) ToolCall {
	terminalID, ok := displayTerminalID(call.ToolCallID, name)
	if !ok {
		return call
	}
	hasDisplayTerminal := false
	for i := range call.Content {
		if strings.EqualFold(strings.TrimSpace(call.Content[i].Type), "terminal") {
			call.Content[i].TerminalID = terminalID
			hasDisplayTerminal = true
		}
	}
	if !hasDisplayTerminal {
		call.Content = append(call.Content, ToolCallContent{
			Type:       "terminal",
			TerminalID: terminalID,
		})
	}
	call.Meta = mergeMeta(call.Meta, displayTerminalInfoMeta(terminalID, args))
	return call
}

func displayTerminalInitialOutputUpdate(toolCallID string, name string, args map[string]any) (ToolCallUpdate, bool) {
	terminalID, ok := displayTerminalID(toolCallID, name)
	if !ok {
		return ToolCallUpdate{}, false
	}
	text := displayTerminalInitialOutput(name, args)
	if text == "" {
		return ToolCallUpdate{}, false
	}
	return ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(toolCallID),
		Content: []ToolCallContent{{
			Type:       "terminal",
			Content:    TextContent{Type: "text", Text: text},
			TerminalID: terminalID,
		}},
	}, true
}

func displayTerminalID(toolCallID string, name string) (string, bool) {
	return displaypolicy.DisplayTerminalID(toolCallID, name)
}

func displayTerminalInfoMeta(terminalID string, args map[string]any) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return nil
	}
	info := map[string]any{"terminal_id": terminalID}
	if cwd := firstNonEmpty(mapString(args, "workdir"), mapString(args, "cwd")); cwd != "" {
		info["cwd"] = cwd
	}
	return map[string]any{"terminal_info": info}
}

func displayTerminalInitialOutput(name string, args map[string]any) string {
	return displaypolicy.DisplayTerminalInitialOutput(name, args)
}

func mergeMeta(base map[string]any, extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func mapString(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func anyStringMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

func nestedString(values map[string]any, path ...string) string {
	var current any = values
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = m[key]
	}
	text, _ := current.(string)
	return strings.TrimSpace(text)
}

func summarizeToolCallTitle(name string, args map[string]any) string {
	return displaypolicy.SummarizeToolCallTitle(name, args)
}

func toolKindForName(name string) string {
	return displaypolicy.ToolKindForName(name)
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
