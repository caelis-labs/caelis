package projector

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/displaypolicy"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
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
	UpdateCompact      = schema.UpdateCompact

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
	if event == nil || event.Protocol == nil {
		return nil, false, nil
	}
	protocol := session.CloneEventProtocol(*event.Protocol)
	approval := protocol.Permission
	if approval == nil {
		return nil, false, nil
	}
	toolCall := permissionToolCallUpdateFromProtocol(approval.ToolCall)
	if strings.TrimSpace(toolCall.ToolCallID) == "" &&
		toolCall.Title == nil &&
		toolCall.Kind == nil &&
		len(approval.Options) == 0 &&
		rawInputLen(toolCall.RawInput) == 0 {
		return nil, false, nil
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
		Meta:      cloneAnyMap(event.Meta),
	}, true, nil
}

func permissionToolCallUpdateFromProtocol(call session.ProtocolToolCall) ToolCallUpdate {
	update := ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(call.ID),
	}
	if title := strings.TrimSpace(call.Title); title != "" {
		update.Title = stringPtr(title)
	} else if title := displaypolicy.SummarizeToolCallTitle(call.Name, call.RawInput); title != "" {
		update.Title = stringPtr(title)
	}
	if kind := firstNonEmpty(strings.TrimSpace(call.Kind), displaypolicy.ToolKindForName(call.Name)); kind != "" {
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
	displayTerminalID, _ := displaypolicy.DisplayTerminalID(call.ID, call.Name)
	update.Content = projectToolContent(call.Content, displayTerminalID)
	update.Meta = terminalOutputMetaFromProtocolContent(call.Content, displayTerminalID)
	return update
}

func rawInputLen(raw any) int {
	if raw == nil {
		return 0
	}
	if mapped, ok := raw.(map[string]any); ok {
		return len(mapped)
	}
	return 1
}

func explicitUpdates(event *session.Event) []Update {
	if event == nil || event.Protocol == nil {
		return nil
	}
	switch protocolUpdateType(event) {
	case UpdateUserMessage:
		return contentUpdateForEvent(event, UpdateUserMessage, textForUserEvent(event))
	case UpdateAgentMessage:
		return explicitAssistantMessageUpdates(event)
	case UpdateAgentThought:
		return contentUpdateForEvent(event, UpdateAgentThought, reasoningForAssistantEvent(event))
	case UpdateToolCall:
		return explicitToolCallUpdates(event)
	case UpdateToolCallInfo:
		update, ok, err := toolCallUpdateForEvent(event)
		if err != nil || !ok {
			return nil
		}
		return []Update{update}
	case UpdatePlan:
		if event.Protocol.Update != nil {
			update := session.ProtocolUpdateOf(event)
			if update != nil {
				return []Update{planUpdateFromEntries(update.Entries)}
			}
		}
		update, ok := planUpdateForEvent(event)
		if !ok {
			return nil
		}
		return []Update{update}
	case UpdateCompact:
		return contentUpdateForEvent(event, UpdateCompact, textForUserEvent(event))
	case "":
		return nil
	default:
		return nil
	}
}

func protocolUpdateType(event *session.Event) string {
	if updateType := normalizeUpdateType(session.ProtocolSessionUpdateType(event)); updateType != "" {
		return updateType
	}
	return ""
}

func inferredUpdates(event *session.Event) []Update {
	if event == nil {
		return nil
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		return contentUpdateForEvent(event, UpdateUserMessage, textForUserEvent(event))
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
		update, ok := planUpdateForEvent(event)
		if !ok {
			return nil
		}
		return []Update{update}
	case session.EventTypeCompact:
		return contentUpdateForEvent(event, UpdateCompact, textForUserEvent(event))
	default:
		return nil
	}
}

func inferredAssistantUpdates(event *session.Event) []Update {
	if event == nil {
		return nil
	}
	message := event.Message
	if message == nil {
		if projected, ok := session.ModelMessageOf(event); ok {
			message = &projected
		}
	}
	if message == nil {
		return contentUpdateForEvent(event, UpdateAgentMessage, textForAssistantEvent(event))
	}
	out := make([]Update, 0, 2)
	if reasoning := reasoningForAssistantEvent(event); reasoning != "" {
		out = append(out, contentChunkForEvent(event, UpdateAgentThought, reasoning))
	}
	if text := textForAssistantEvent(event); text != "" {
		out = append(out, contentChunkForEvent(event, UpdateAgentMessage, text))
	}
	return out
}

func explicitAssistantMessageUpdates(event *session.Event) []Update {
	if event == nil {
		return nil
	}
	out := make([]Update, 0, 2)
	if reasoning := reasoningForAssistantEvent(event); reasoning != "" {
		out = append(out, contentChunkForEvent(event, UpdateAgentThought, reasoning))
	}
	if text := textForAssistantEvent(event); text != "" {
		out = append(out, contentChunkForEvent(event, UpdateAgentMessage, text))
	}
	return out
}

func explicitToolCallUpdates(event *session.Event) []Update {
	out := inferredAssistantMessageOnly(event)
	call, ok, err := toolCallForEvent(event)
	if err != nil || !ok {
		return out
	}
	out = append(out, call)
	return out
}

func inferredAssistantMessageOnly(event *session.Event) []Update {
	if event == nil {
		return nil
	}
	if event.Message != nil {
		if event.Message.Role != model.RoleAssistant {
			return nil
		}
	} else if message, ok := session.ModelMessageOf(event); !ok || message.Role != model.RoleAssistant {
		return nil
	}
	out := make([]Update, 0, 2)
	if reasoning := reasoningForAssistantEvent(event); reasoning != "" {
		out = append(out, contentChunkForEvent(event, UpdateAgentThought, reasoning))
	}
	if text := textForAssistantEvent(event); text != "" {
		out = append(out, contentChunkForEvent(event, UpdateAgentMessage, text))
	}
	return out
}

func inferredToolCallUpdates(event *session.Event) []Update {
	if event == nil {
		return nil
	}
	if event.Tool != nil {
		return explicitToolCallUpdates(event)
	}
	out := inferredAssistantMessageOnly(event)
	message := event.Message
	if message == nil {
		if projected, ok := session.ModelMessageOf(event); ok {
			message = &projected
		}
	}
	if message == nil {
		return out
	}
	for _, call := range message.ToolCalls() {
		args := parseObject(call.Args)
		update := ToolCall{
			SessionUpdate: UpdateToolCall,
			ToolCallID:    strings.TrimSpace(call.ID),
			Title:         displaypolicy.SummarizeToolCallTitle(call.Name, args),
			Kind:          displaypolicy.ToolKindForName(call.Name),
			Status:        ToolStatusPending,
			RawInput:      args,
		}
		update = withDisplayTerminal(update, call.Name, args)
		out = append(out, update)
	}
	return out
}

func contentUpdateForEvent(event *session.Event, kind string, text string) []Update {
	if text == "" {
		return nil
	}
	return []Update{contentChunkForEvent(event, kind, text)}
}

func contentChunk(kind string, text string) ContentChunk {
	return ContentChunk{
		SessionUpdate: kind,
		Content:       TextContent{Type: "text", Text: text},
	}
}

func contentChunkForEvent(event *session.Event, kind string, text string) ContentChunk {
	chunk := contentChunk(kind, text)
	// ProtocolUpdate metadata describes that exact ACP update. Do not attach
	// tool/plan metadata to assistant chunks emitted from the durable Message.
	if update := session.ProtocolUpdateOf(event); update != nil && normalizeUpdateType(update.SessionUpdate) == kind {
		chunk.MessageID = strings.TrimSpace(update.MessageID)
		chunk.Meta = cloneAnyMap(update.Meta)
	}
	return chunk
}

func toolCallForEvent(event *session.Event) (ToolCall, bool, error) {
	if event == nil {
		return ToolCall{}, false, nil
	}
	if event.Tool != nil {
		return toolCallFromEventToolPayload(event.Tool), true, nil
	}
	if update := session.ProtocolUpdateOf(event); update != nil && normalizeUpdateType(update.SessionUpdate) == UpdateToolCall {
		return toolCallFromProtocolUpdate(event, update), true, nil
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
		Title:         displaypolicy.SummarizeToolCallTitle(calls[0].Name, args),
		Kind:          displaypolicy.ToolKindForName(calls[0].Name),
		Status:        ToolStatusPending,
		RawInput:      args,
	}
	call = withDisplayTerminal(call, calls[0].Name, args)
	return call, true, nil
}

func toolCallFromEventToolPayload(tool *session.EventTool) ToolCall {
	if tool == nil {
		return ToolCall{SessionUpdate: UpdateToolCall}
	}
	rawInput := cloneAnyMap(tool.Input)
	displayTerminalID, _ := displaypolicy.DisplayTerminalID(tool.ID, tool.Name)
	call := ToolCall{
		SessionUpdate: UpdateToolCall,
		ToolCallID:    strings.TrimSpace(tool.ID),
		Title:         firstNonEmpty(strings.TrimSpace(tool.Title), displaypolicy.SummarizeToolCallTitle(tool.Name, rawInput), strings.TrimSpace(tool.Name)),
		Kind:          firstNonEmpty(strings.TrimSpace(tool.Kind), displaypolicy.ToolKindForName(tool.Name)),
		Status:        firstNonEmpty(acpToolStatus(tool.Status), ToolStatusPending),
		RawInput:      rawInput,
		RawOutput:     cloneAnyMap(tool.Output),
		Content:       projectEventToolContent(tool.Content, displayTerminalID),
		Locations:     projectEventToolLocations(tool.Locations),
		Meta:          terminalOutputMetaFromEventToolContent(tool.Content, displayTerminalID),
	}
	return withDisplayTerminal(call, tool.Name, rawInput)
}

func toolCallFromProtocolUpdate(event *session.Event, update *session.ProtocolUpdate) ToolCall {
	name := protocolToolNameForUpdate(event, update)
	rawInput := cloneAnyMap(update.RawInput)
	displayTerminalID, _ := displaypolicy.DisplayTerminalID(update.ToolCallID, name)
	content := session.ProtocolToolCallContentOf(update)
	call := ToolCall{
		SessionUpdate: UpdateToolCall,
		ToolCallID:    strings.TrimSpace(update.ToolCallID),
		Title:         firstNonEmpty(strings.TrimSpace(update.Title), displaypolicy.SummarizeToolCallTitle(name, rawInput), strings.TrimSpace(name)),
		Kind:          firstNonEmpty(strings.TrimSpace(update.Kind), displaypolicy.ToolKindForName(name)),
		Status:        firstNonEmpty(acpToolStatus(update.Status), ToolStatusPending),
		RawInput:      rawInput,
		RawOutput:     cloneAnyMap(update.RawOutput),
		Content:       projectToolContent(content, displayTerminalID),
		Locations:     projectToolLocations(update.Locations),
		Meta:          mergeMeta(terminalOutputMetaFromProtocolContent(content, displayTerminalID), cloneAnyMap(update.Meta)),
	}
	return withDisplayTerminal(call, name, rawInput)
}

func toolCallUpdateForEvent(event *session.Event) (ToolCallUpdate, bool, error) {
	if event == nil {
		return ToolCallUpdate{}, false, nil
	}
	if event.Tool != nil {
		return toolCallUpdateFromEventToolPayload(event.Tool), true, nil
	}
	if update := session.ProtocolUpdateOf(event); update != nil && normalizeUpdateType(update.SessionUpdate) == UpdateToolCallInfo {
		projected, err := toolCallUpdateFromProtocolUpdate(event, update)
		if err != nil {
			return ToolCallUpdate{}, false, err
		}
		if len(projected.Content) == 0 {
			if text := strings.TrimSpace(event.Text); text != "" {
				projected.Content = append(projected.Content, ToolCallContent{Type: "content", Content: TextContent{Type: "text", Text: text}})
			}
		}
		return projected, true, nil
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
	kind := displaypolicy.ToolKindForName(name)
	return ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(resp.ID),
		Kind:          stringPtr(kind),
		Status:        stringPtr(status),
		RawOutput:     cloneAnyMap(resp.Result),
		Content:       terminalContentFromEvent(event),
		Meta:          protocolUpdateMeta(event),
	}, true, nil
}

func toolCallUpdateFromEventToolPayload(tool *session.EventTool) ToolCallUpdate {
	if tool == nil {
		return ToolCallUpdate{SessionUpdate: UpdateToolCallInfo}
	}
	displayTerminalID, _ := displaypolicy.DisplayTerminalID(tool.ID, tool.Name)
	out := ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(tool.ID),
		RawInput:      cloneAnyMap(tool.Input),
		RawOutput:     cloneAnyMap(tool.Output),
		Content:       projectEventToolContent(tool.Content, displayTerminalID),
		Locations:     projectEventToolLocations(tool.Locations),
		Meta:          terminalOutputMetaFromEventToolContent(tool.Content, displayTerminalID),
	}
	if title := strings.TrimSpace(tool.Title); title != "" {
		out.Title = stringPtr(title)
	} else if title := displaypolicy.SummarizeToolCallTitle(tool.Name, tool.Input); title != "" {
		out.Title = stringPtr(title)
	}
	if kind := firstNonEmpty(strings.TrimSpace(tool.Kind), displaypolicy.ToolKindForName(tool.Name)); kind != "" {
		out.Kind = stringPtr(kind)
	}
	if status := acpToolStatus(tool.Status); status != "" {
		out.Status = stringPtr(status)
	}
	return out
}

func toolCallUpdateFromProtocolUpdate(event *session.Event, update *session.ProtocolUpdate) (ToolCallUpdate, error) {
	id := strings.TrimSpace(update.ToolCallID)
	if id == "" {
		return ToolCallUpdate{}, fmt.Errorf("protocol/acp/projector: tool update missing tool call id")
	}
	name := protocolToolNameForUpdate(event, update)
	content := session.ProtocolToolCallContentOf(update)
	displayTerminalID, _ := displaypolicy.DisplayTerminalID(id, name)
	out := ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    id,
		RawInput:      cloneAnyMap(update.RawInput),
		RawOutput:     cloneAnyMap(update.RawOutput),
		Content:       projectToolContent(content, displayTerminalID),
		Locations:     projectToolLocations(update.Locations),
		Meta:          mergeMeta(terminalOutputMetaFromProtocolContent(content, displayTerminalID), cloneAnyMap(update.Meta)),
	}
	if title := strings.TrimSpace(update.Title); title != "" {
		out.Title = stringPtr(title)
	} else if title := displaypolicy.SummarizeToolCallTitle(name, update.RawInput); title != "" {
		out.Title = stringPtr(title)
	}
	kind := strings.TrimSpace(update.Kind)
	if kind == "" && strings.TrimSpace(name) != "" {
		kind = displaypolicy.ToolKindForName(name)
	}
	if kind != "" {
		out.Kind = stringPtr(kind)
	}
	if status := acpToolStatus(update.Status); status != "" {
		out.Status = stringPtr(status)
	}
	return out, nil
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
	} else if title := displaypolicy.SummarizeToolCallTitle(call.Name, call.RawInput); title != "" {
		update.Title = stringPtr(title)
	}
	if kind := firstNonEmpty(strings.TrimSpace(call.Kind), displaypolicy.ToolKindForName(call.Name)); kind != "" {
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
	displayTerminalID, _ := displaypolicy.DisplayTerminalID(call.ID, call.Name)
	update.Content = projectToolContent(call.Content, displayTerminalID)
	update.Meta = terminalOutputMetaFromProtocolContent(call.Content, displayTerminalID)
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
	displayTerminalID, _ := displaypolicy.DisplayTerminalID(toolCallID, name)
	return projectToolContent(content, displayTerminalID)
}

func projectToolLocations(locations []session.ProtocolToolCallLocation) []ToolCallLocation {
	if len(locations) == 0 {
		return nil
	}
	out := make([]ToolCallLocation, 0, len(locations))
	for _, item := range locations {
		var line *int
		if item.Line != nil {
			value := *item.Line
			line = &value
		}
		out = append(out, ToolCallLocation{
			Path: strings.TrimSpace(item.Path),
			Line: line,
		})
	}
	return out
}

func projectEventToolLocations(locations []session.EventToolLocation) []ToolCallLocation {
	if len(locations) == 0 {
		return nil
	}
	out := make([]ToolCallLocation, 0, len(locations))
	for _, item := range locations {
		var line *int
		if item.Line != nil {
			value := *item.Line
			line = &value
		}
		out = append(out, ToolCallLocation{
			Path: strings.TrimSpace(item.Path),
			Line: line,
		})
	}
	return out
}

func projectEventToolContent(content []session.EventToolContent, displayTerminalID string) []ToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]ToolCallContent, 0, len(content))
	for _, item := range content {
		contentType := strings.TrimSpace(item.Type)
		terminalID := strings.TrimSpace(item.TerminalID)
		var payload any
		if !strings.EqualFold(contentType, "terminal") && strings.TrimSpace(item.Text) != "" {
			payload = TextContent{Type: "text", Text: item.Text}
		}
		if strings.EqualFold(contentType, "terminal") {
			if strings.TrimSpace(displayTerminalID) != "" {
				terminalID = strings.TrimSpace(displayTerminalID)
			}
		}
		var oldText *string
		if item.OldText != nil {
			value := *item.OldText
			oldText = &value
		}
		out = append(out, ToolCallContent{
			Type:       contentType,
			Content:    payload,
			TerminalID: terminalID,
			Path:       strings.TrimSpace(item.Path),
			OldText:    oldText,
			NewText:    item.NewText,
		})
	}
	return out
}

func projectToolContent(content []session.ProtocolToolCallContent, displayTerminalID string) []ToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]ToolCallContent, 0, len(content))
	for _, item := range content {
		contentType := strings.TrimSpace(item.Type)
		terminalID := strings.TrimSpace(item.TerminalID)
		contentPayload := item.Content
		if strings.EqualFold(contentType, "terminal") {
			contentPayload = nil
			if strings.TrimSpace(displayTerminalID) != "" {
				terminalID = strings.TrimSpace(displayTerminalID)
			}
		}
		var oldText *string
		if item.OldText != nil {
			value := *item.OldText
			oldText = &value
		}
		out = append(out, ToolCallContent{
			Type:       contentType,
			Content:    contentPayload,
			TerminalID: terminalID,
			Path:       strings.TrimSpace(item.Path),
			OldText:    oldText,
			NewText:    item.NewText,
		})
	}
	return out
}

func terminalIDFromEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Tool != nil {
		for _, item := range event.Tool.Content {
			if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
				continue
			}
			if terminalID := strings.TrimSpace(item.TerminalID); terminalID != "" {
				return terminalID
			}
		}
		if terminalID, ok := displaypolicy.DisplayTerminalID(event.Tool.ID, event.Tool.Name); ok {
			return terminalID
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
	case "started", "running", "waiting_approval":
		return ToolStatusInProgress
	case "cancelled", "canceled", "interrupted", "terminated", "timed_out", "timeout":
		return ToolStatusFailed
	default:
		return strings.TrimSpace(status)
	}
}

func planUpdateFromProtocol(plan session.ProtocolPlan) PlanUpdate {
	return planUpdateFromEntries(plan.Entries)
}

func planUpdateForEvent(event *session.Event) (PlanUpdate, bool) {
	if event == nil {
		return PlanUpdate{}, false
	}
	if event.Protocol != nil {
		if update := session.ProtocolUpdateOf(event); update != nil && (len(update.Entries) > 0 || normalizeUpdateType(update.SessionUpdate) == UpdatePlan) {
			return planUpdateFromEntries(update.Entries), true
		}
	}
	payload := session.PlanPayloadOf(event)
	if payload == nil {
		return PlanUpdate{}, false
	}
	return planUpdateFromPayload(*payload), true
}

func planUpdateFromEntries(protocolEntries []session.ProtocolPlanEntry) PlanUpdate {
	entries := make([]PlanEntry, 0, len(protocolEntries))
	for _, item := range protocolEntries {
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

func planUpdateFromPayload(payload session.EventPlanPayload) PlanUpdate {
	entries := make([]PlanEntry, 0, len(payload.Entries))
	for _, item := range payload.Entries {
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
	if message, ok := session.ModelMessageOf(event); ok {
		return message.TextContent()
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
	} else if message, ok := session.ModelMessageOf(event); ok {
		if reasoning := message.ReasoningText(); reasoning != "" {
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

func protocolUpdateMeta(event *session.Event) map[string]any {
	if update := session.ProtocolUpdateOf(event); update != nil {
		return cloneAnyMap(update.Meta)
	}
	return nil
}

func mergeMeta(base map[string]any, extra map[string]any) map[string]any {
	return metautil.Merge(base, extra)
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
	return metautil.CloneMap(values)
}
