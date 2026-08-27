package projector

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// Projector converts canonical session events into ACP-compatible updates and
// request_permission payloads.
type Projector interface {
	ProjectEvent(*session.Event) ([]schema.Update, error)
	ProjectPermissionRequest(*session.Event) (*schema.RequestPermissionRequest, bool, error)
}

// EventProjector is the baseline ACP projection implementation for canonical
// SDK session events.
type EventProjector struct{}

// ProjectEvent converts one canonical event into ACP-compatible update payloads.
func (EventProjector) ProjectEvent(event *session.Event) ([]schema.Update, error) {
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

// ProjectPermissionRequest converts one canonical approval event into one
// ACP-compatible session/request_permission payload.
func (EventProjector) ProjectPermissionRequest(event *session.Event) (*schema.RequestPermissionRequest, bool, error) {
	if event == nil || event.Protocol == nil {
		return nil, false, nil
	}
	protocol := session.CloneEventProtocol(*event.Protocol)
	approval := protocol.Permission
	if approval == nil {
		return nil, false, nil
	}
	req := permissionRequestFromProtocol(strings.TrimSpace(event.SessionID), event.Meta, approval)
	if req == nil {
		return nil, false, nil
	}
	return req, true, nil
}

func permissionToolCallUpdateFromProtocol(call session.ProtocolToolCall) schema.ToolCallUpdate {
	update := schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(call.ID),
	}
	if title := strings.TrimSpace(call.Title); title != "" {
		update.Title = stringPtr(title)
	} else if title := projectedToolTitle(call.Name, call.RawInput); title != "" {
		update.Title = stringPtr(title)
	}
	if kind := firstNonEmpty(strings.TrimSpace(call.Kind), projectedToolKind(call.Name)); kind != "" {
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
	displayTerminalID, _ := projectedDisplayTerminalID(call.ID, call.Name)
	update.Content = projectToolContent(call.Content, displayTerminalID)
	return withDisplayTerminalUpdate(update, call.ID, call.Name)
}

func explicitUpdates(event *session.Event) []schema.Update {
	if event == nil || event.Protocol == nil {
		return nil
	}
	switch protocolUpdateType(event) {
	case schema.UpdateUserMessage:
		return contentUpdateForEvent(event, schema.UpdateUserMessage, textForUserEvent(event))
	case schema.UpdateAgentMessage:
		return explicitAssistantMessageUpdates(event)
	case schema.UpdateAgentThought:
		return contentUpdateForEvent(event, schema.UpdateAgentThought, reasoningForAssistantEvent(event))
	case schema.UpdateToolCall:
		return explicitToolCallUpdates(event)
	case schema.UpdateToolCallInfo:
		update, ok, err := toolCallUpdateForEvent(event)
		if err != nil || !ok {
			return nil
		}
		return []schema.Update{update}
	case schema.UpdatePlan:
		if event.Protocol.Update != nil {
			update := session.ProtocolUpdateOf(event)
			if update != nil {
				return []schema.Update{planUpdateFromEntries(update.Entries)}
			}
		}
		update, ok := planUpdateForEvent(event)
		if !ok {
			return nil
		}
		return []schema.Update{update}
	case schema.UpdateCompact:
		return contentUpdateForEvent(event, schema.UpdateCompact, textForUserEvent(event))
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

func inferredUpdates(event *session.Event) []schema.Update {
	if event == nil {
		return nil
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		return contentUpdateForEvent(event, schema.UpdateUserMessage, textForUserEvent(event))
	case session.EventTypeAssistant:
		return inferredAssistantUpdates(event)
	case session.EventTypeToolCall:
		return inferredToolCallUpdates(event)
	case session.EventTypeToolResult:
		update, ok, err := toolCallUpdateForEvent(event)
		if err != nil || !ok {
			return nil
		}
		return []schema.Update{update}
	case session.EventTypePlan:
		update, ok := planUpdateForEvent(event)
		if !ok {
			return nil
		}
		return []schema.Update{update}
	case session.EventTypeCompact:
		return contentUpdateForEvent(event, schema.UpdateCompact, textForUserEvent(event))
	default:
		return nil
	}
}

func inferredAssistantUpdates(event *session.Event) []schema.Update {
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
		return contentUpdateForEvent(event, schema.UpdateAgentMessage, textForAssistantEvent(event))
	}
	out := make([]schema.Update, 0, 2)
	if reasoning := reasoningForAssistantEvent(event); reasoning != "" {
		out = append(out, contentChunkForEvent(event, schema.UpdateAgentThought, reasoning))
	}
	if text := textForAssistantEvent(event); text != "" {
		out = append(out, contentChunkForEvent(event, schema.UpdateAgentMessage, text))
	}
	return out
}

func explicitAssistantMessageUpdates(event *session.Event) []schema.Update {
	if event == nil {
		return nil
	}
	out := make([]schema.Update, 0, 2)
	if reasoning := reasoningForAssistantEvent(event); reasoning != "" {
		out = append(out, contentChunkForEvent(event, schema.UpdateAgentThought, reasoning))
	}
	if text := textForAssistantEvent(event); text != "" {
		out = append(out, contentChunkForEvent(event, schema.UpdateAgentMessage, text))
	}
	return out
}

func explicitToolCallUpdates(event *session.Event) []schema.Update {
	// Runtime stores the complete assistant response on the first canonical
	// tool-call event, while Event.Tool identifies the one physical call owned
	// by that event. Project the durable narrative siblings first, then exactly
	// that Event.Tool. Iterating Message.ToolUses here would duplicate calls
	// that have their own canonical tool-call events.
	out := inferredAssistantMessageOnly(event)
	call, ok, err := toolCallForEvent(event)
	if err != nil || !ok {
		return out
	}
	out = append(out, call)
	return out
}

func inferredAssistantMessageOnly(event *session.Event) []schema.Update {
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
	out := make([]schema.Update, 0, 2)
	if reasoning := reasoningForAssistantEvent(event); reasoning != "" {
		out = append(out, contentChunkForEvent(event, schema.UpdateAgentThought, reasoning))
	}
	if text := textForAssistantEvent(event); text != "" {
		out = append(out, contentChunkForEvent(event, schema.UpdateAgentMessage, text))
	}
	return out
}

func inferredToolCallUpdates(event *session.Event) []schema.Update {
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
		update := schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    strings.TrimSpace(call.ID),
			Title:         projectedToolTitle(call.Name, args),
			Kind:          projectedToolKind(call.Name),
			Status:        schema.ToolStatusPending,
			RawInput:      cloneAnyMapPayload(args),
		}
		update = withDisplayTerminal(update, call.Name, args)
		out = append(out, update)
	}
	return out
}

func contentUpdateForEvent(event *session.Event, kind string, text string) []schema.Update {
	if text == "" {
		return nil
	}
	return []schema.Update{contentChunkForEvent(event, kind, text)}
}

func contentChunk(kind string, text string) schema.ContentChunk {
	return schema.ContentChunk{
		SessionUpdate: kind,
		Content:       schema.TextContent{Type: "text", Text: text},
	}
}

func contentChunkForEvent(event *session.Event, kind string, text string) schema.ContentChunk {
	chunk := contentChunk(kind, text)
	chunk.MessageID = session.EventMessageID(event)
	// ProtocolUpdate metadata describes that exact ACP update. Do not attach
	// tool/plan metadata to assistant chunks emitted from the durable Message.
	if update := session.ProtocolUpdateOf(event); update != nil && normalizeUpdateType(update.SessionUpdate) == kind {
		chunk.Meta = cloneAnyMap(update.Meta)
	}
	if kind == schema.UpdateAgentMessage && event != nil {
		message := event.Message
		if message == nil {
			if projected, ok := session.ModelMessageOf(event); ok {
				message = &projected
			}
		}
		if message != nil {
			if citations := message.TextContentCitations(); len(citations) > 0 {
				chunk.Meta = metautil.WithSection(chunk.Meta, metautil.Message, map[string]any{
					metautil.MessageCitations: citationMetaPayload(citations),
				})
			}
		}
	}
	return chunk
}

func citationMetaPayload(citations []model.Citation) []any {
	out := make([]any, 0, len(citations))
	for _, citation := range citations {
		sources := make([]any, 0, len(citation.Sources))
		for _, source := range citation.Sources {
			item := map[string]any{}
			putNonEmptyString(item, "ref_id", source.RefID)
			putNonEmptyString(item, "title", source.Title)
			putNonEmptyString(item, "url", source.URL)
			putNonEmptyString(item, "snippet", source.Snippet)
			putNonEmptyString(item, "source", source.Source)
			putNonEmptyString(item, "published_at", source.PublishedAt)
			if len(item) > 0 {
				sources = append(sources, item)
			}
		}
		if len(sources) == 0 {
			continue
		}
		out = append(out, map[string]any{
			"start_index": citation.StartIndex,
			"end_index":   citation.EndIndex,
			"sources":     sources,
		})
	}
	return out
}

func putNonEmptyString(out map[string]any, key string, value string) {
	if value = strings.TrimSpace(value); value != "" {
		out[key] = value
	}
}

func toolCallForEvent(event *session.Event) (schema.ToolCall, bool, error) {
	if event == nil {
		return schema.ToolCall{}, false, nil
	}
	if event.Tool != nil {
		return toolCallFromEventToolPayload(event.Tool), true, nil
	}
	if update := session.ProtocolUpdateOf(event); update != nil && normalizeUpdateType(update.SessionUpdate) == schema.UpdateToolCall {
		return toolCallFromProtocolUpdate(event, update), true, nil
	}
	if event.Message == nil {
		return schema.ToolCall{}, false, nil
	}
	calls := event.Message.ToolCalls()
	if len(calls) == 0 {
		return schema.ToolCall{}, false, nil
	}
	args := parseObject(calls[0].Args)
	call := schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    strings.TrimSpace(calls[0].ID),
		Title:         projectedToolTitle(calls[0].Name, args),
		Kind:          projectedToolKind(calls[0].Name),
		Status:        schema.ToolStatusPending,
		RawInput:      cloneAnyMapPayload(args),
	}
	call = withDisplayTerminal(call, calls[0].Name, args)
	return call, true, nil
}

func toolCallFromEventToolPayload(tool *session.EventTool) schema.ToolCall {
	if tool == nil {
		return schema.ToolCall{SessionUpdate: schema.UpdateToolCall}
	}
	rawInput := cloneAnyMap(tool.Input)
	displayTerminalID, _ := projectedDisplayTerminalID(tool.ID, tool.Name)
	call := schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    strings.TrimSpace(tool.ID),
		Title:         firstNonEmpty(strings.TrimSpace(tool.Title), projectedToolTitle(tool.Name, rawInput), strings.TrimSpace(tool.Name)),
		Kind:          firstNonEmpty(strings.TrimSpace(tool.Kind), projectedToolKind(tool.Name)),
		Status:        firstNonEmpty(acpToolStatus(tool.Status), schema.ToolStatusPending),
		RawInput:      cloneAnyMapPayload(rawInput),
		RawOutput:     cloneAnyMapPayload(tool.Output),
		Content:       projectEventToolContent(tool.Content, displayTerminalID),
		Locations:     projectEventToolLocations(tool.Locations),
	}
	return withDisplayTerminal(call, tool.Name, rawInput)
}

func toolCallFromProtocolUpdate(event *session.Event, update *session.ProtocolUpdate) schema.ToolCall {
	update = cloneProtocolUpdateForProjection(update)
	name := protocolToolNameForUpdate(event, update)
	rawInput := cloneAnyMap(update.RawInput)
	call := schema.ToolCall{
		SessionUpdate: update.SessionUpdate,
		ToolCallID:    update.ToolCallID,
		Title:         update.Title,
		Kind:          update.Kind,
		Status:        update.Status,
		RawInput:      cloneAnyMapPayload(update.RawInput),
		RawOutput:     cloneAnyMapPayload(update.RawOutput),
		Locations:     protocolLocationsForProjection(update.Locations),
		Meta:          cloneAnyMap(update.Meta),
	}
	call.Title = firstNonEmpty(strings.TrimSpace(call.Title), projectedToolTitle(name, rawInput), strings.TrimSpace(name))
	call.Kind = firstNonEmpty(strings.TrimSpace(call.Kind), projectedToolKind(name))
	call.Status = firstNonEmpty(acpToolStatus(call.Status), schema.ToolStatusPending)
	displayTerminalID, _ := projectedDisplayTerminalID(call.ToolCallID, name)
	call.Content = projectToolContent(session.ProtocolToolCallContentOf(update), displayTerminalID)
	return withDisplayTerminal(call, name, rawInput)
}

func toolCallUpdateForEvent(event *session.Event) (schema.ToolCallUpdate, bool, error) {
	if event == nil {
		return schema.ToolCallUpdate{}, false, nil
	}
	if event.Tool != nil {
		return toolCallUpdateFromEventToolPayload(event.Tool, event.Meta), true, nil
	}
	if update := session.ProtocolUpdateOf(event); update != nil && normalizeUpdateType(update.SessionUpdate) == schema.UpdateToolCallInfo {
		projected, err := toolCallUpdateFromProtocolUpdate(event, update)
		if err != nil {
			return schema.ToolCallUpdate{}, false, err
		}
		return projected, true, nil
	}
	if event.Message == nil {
		return schema.ToolCallUpdate{}, false, nil
	}
	resp := event.Message.ToolResponse()
	if resp == nil {
		return schema.ToolCallUpdate{}, false, nil
	}
	status := schema.ToolStatusCompleted
	if raw, ok := event.Meta["is_error"].(bool); ok && raw {
		status = schema.ToolStatusFailed
	}
	name := strings.TrimSpace(resp.Name)
	kind := projectedToolKind(name)
	out := schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(resp.ID),
		Kind:          stringPtr(kind),
		Status:        stringPtr(status),
		RawOutput:     cloneAnyMapPayload(resp.Result),
		Meta:          protocolUpdateMeta(event),
	}
	return withDisplayTerminalUpdate(out, resp.ID, name), true, nil
}

func toolCallUpdateFromEventToolPayload(tool *session.EventTool, meta map[string]any) schema.ToolCallUpdate {
	if tool == nil {
		return schema.ToolCallUpdate{SessionUpdate: schema.UpdateToolCallInfo}
	}
	displayTerminalID, _ := projectedDisplayTerminalID(tool.ID, tool.Name)
	out := schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(tool.ID),
		RawInput:      cloneAnyMapPayload(tool.Input),
		RawOutput:     cloneAnyMapPayload(tool.Output),
		Content:       projectEventToolContent(tool.Content, displayTerminalID),
		Locations:     projectEventToolLocations(tool.Locations),
	}
	if len(out.Content) == 0 {
		out.Content = projectedToolResultContent(tool.ID, tool.Name, tool.Input, tool.Output, meta, tool.Status)
	}
	if title := strings.TrimSpace(tool.Title); title != "" {
		out.Title = stringPtr(title)
	} else if title := projectedToolTitle(tool.Name, tool.Input); title != "" {
		out.Title = stringPtr(title)
	}
	if kind := firstNonEmpty(strings.TrimSpace(tool.Kind), projectedToolKind(tool.Name)); kind != "" {
		out.Kind = stringPtr(kind)
	}
	if status := acpToolStatus(tool.Status); status != "" {
		out.Status = stringPtr(status)
	}
	return withDisplayTerminalUpdate(out, tool.ID, tool.Name)
}

func toolCallUpdateFromProtocolUpdate(event *session.Event, update *session.ProtocolUpdate) (schema.ToolCallUpdate, error) {
	update = cloneProtocolUpdateForProjection(update)
	id := strings.TrimSpace(update.ToolCallID)
	if id == "" {
		return schema.ToolCallUpdate{}, fmt.Errorf("protocol/acp/projector: tool update missing tool call id")
	}
	name := protocolToolNameForUpdate(event, update)
	out := schema.ToolCallUpdate{
		SessionUpdate: update.SessionUpdate,
		ToolCallID:    update.ToolCallID,
		Title:         stringPtr(update.Title),
		Kind:          stringPtr(update.Kind),
		Status:        stringPtr(update.Status),
		RawInput:      cloneAnyMapPayload(update.RawInput),
		RawOutput:     cloneAnyMapPayload(update.RawOutput),
		Locations:     protocolLocationsForProjection(update.Locations),
		Meta:          cloneAnyMap(update.Meta),
	}
	displayTerminalID, _ := projectedDisplayTerminalID(id, name)
	out.Content = projectToolContent(session.ProtocolToolCallContentOf(update), displayTerminalID)
	if title := strings.TrimSpace(stringFromPtr(out.Title)); title != "" {
		out.Title = stringPtr(title)
	} else if title := projectedToolTitle(name, update.RawInput); title != "" {
		out.Title = stringPtr(title)
	}
	kind := strings.TrimSpace(stringFromPtr(out.Kind))
	if kind == "" && strings.TrimSpace(name) != "" {
		kind = projectedToolKind(name)
	}
	if kind != "" {
		out.Kind = stringPtr(kind)
	}
	if status := acpToolStatus(stringFromPtr(out.Status)); status != "" {
		out.Status = stringPtr(status)
	}
	return withDisplayTerminalUpdate(out, id, name), nil
}

func toolCallUpdateFromProtocol(call session.ProtocolToolCall) (schema.ToolCallUpdate, error) {
	id := strings.TrimSpace(call.ID)
	if id == "" {
		return schema.ToolCallUpdate{}, fmt.Errorf("protocol/acp/projector: approval or tool update missing tool call id")
	}
	update := schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    id,
	}
	if title := strings.TrimSpace(call.Title); title != "" {
		update.Title = stringPtr(title)
	} else if title := projectedToolTitle(call.Name, call.RawInput); title != "" {
		update.Title = stringPtr(title)
	}
	if kind := firstNonEmpty(strings.TrimSpace(call.Kind), projectedToolKind(call.Name)); kind != "" {
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
	displayTerminalID, _ := projectedDisplayTerminalID(call.ID, call.Name)
	update.Content = projectToolContent(call.Content, displayTerminalID)
	return withDisplayTerminalUpdate(update, call.ID, call.Name), nil
}

func projectToolContentForTool(content []session.ProtocolToolCallContent, toolCallID string, name string) []schema.ToolCallContent {
	displayTerminalID, _ := projectedDisplayTerminalID(toolCallID, name)
	return projectToolContent(content, displayTerminalID)
}

func projectToolLocations(locations []session.ProtocolToolCallLocation) []schema.ToolCallLocation {
	if len(locations) == 0 {
		return nil
	}
	out := make([]schema.ToolCallLocation, 0, len(locations))
	for _, item := range locations {
		var line *int
		if item.Line != nil {
			value := *item.Line
			line = &value
		}
		out = append(out, schema.ToolCallLocation{
			Path: strings.TrimSpace(item.Path),
			Line: line,
		})
	}
	return out
}

func projectEventToolLocations(locations []session.EventToolLocation) []schema.ToolCallLocation {
	if len(locations) == 0 {
		return nil
	}
	out := make([]schema.ToolCallLocation, 0, len(locations))
	for _, item := range locations {
		var line *int
		if item.Line != nil {
			value := *item.Line
			line = &value
		}
		out = append(out, schema.ToolCallLocation{
			Path: strings.TrimSpace(item.Path),
			Line: line,
		})
	}
	return out
}

func projectEventToolContent(content []session.EventToolContent, displayTerminalID string) []schema.ToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]schema.ToolCallContent, 0, len(content))
	for _, item := range content {
		contentType := strings.TrimSpace(item.Type)
		terminalID := strings.TrimSpace(item.TerminalID)
		var payload any
		if strings.TrimSpace(item.Text) != "" {
			payload = schema.TextContent{Type: "text", Text: item.Text}
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
		out = append(out, schema.ToolCallContent{
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

func projectToolContent(content []session.ProtocolToolCallContent, displayTerminalID string) []schema.ToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]schema.ToolCallContent, 0, len(content))
	for _, item := range content {
		contentType := strings.TrimSpace(item.Type)
		terminalID := strings.TrimSpace(item.TerminalID)
		contentPayload := item.Content
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
		out = append(out, schema.ToolCallContent{
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

func eventMeta(event *session.Event) map[string]any {
	if event == nil {
		return nil
	}
	return event.Meta
}

func acpToolStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", schema.ToolStatusPending, schema.ToolStatusInProgress, schema.ToolStatusCompleted, schema.ToolStatusFailed:
		return strings.TrimSpace(status)
	case "started", "running", "waiting_approval":
		return schema.ToolStatusInProgress
	case "cancelled", "canceled", "interrupted", "terminated", "timed_out", "timeout":
		return schema.ToolStatusFailed
	default:
		return strings.TrimSpace(status)
	}
}

func planUpdateFromProtocol(plan session.ProtocolPlan) schema.PlanUpdate {
	return planUpdateFromEntries(plan.Entries)
}

func planUpdateForEvent(event *session.Event) (schema.PlanUpdate, bool) {
	if event == nil {
		return schema.PlanUpdate{}, false
	}
	if event.Protocol != nil {
		if update := session.ProtocolUpdateOf(event); update != nil && (len(update.Entries) > 0 || normalizeUpdateType(update.SessionUpdate) == schema.UpdatePlan) {
			return planUpdateFromEntries(update.Entries), true
		}
	}
	payload := session.PlanPayloadOf(event)
	if payload == nil {
		return schema.PlanUpdate{}, false
	}
	return planUpdateFromPayload(*payload), true
}

func planUpdateFromEntries(protocolEntries []session.ProtocolPlanEntry) schema.PlanUpdate {
	entries := make([]schema.PlanEntry, 0, len(protocolEntries))
	for _, item := range protocolEntries {
		entries = append(entries, schema.PlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: firstNonEmpty(strings.TrimSpace(item.Priority), "medium"),
		})
	}
	return schema.PlanUpdate{SessionUpdate: schema.UpdatePlan, Entries: entries}
}

func planUpdateFromPayload(payload session.EventPlanPayload) schema.PlanUpdate {
	entries := make([]schema.PlanEntry, 0, len(payload.Entries))
	for _, item := range payload.Entries {
		entries = append(entries, schema.PlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: firstNonEmpty(strings.TrimSpace(item.Priority), "medium"),
		})
	}
	return schema.PlanUpdate{
		SessionUpdate: schema.UpdatePlan,
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
		if normalizeUpdateType(update.SessionUpdate) == schema.UpdateAgentThought {
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

func cloneProtocolUpdateForProjection(update *session.ProtocolUpdate) *session.ProtocolUpdate {
	if update == nil {
		return nil
	}
	protocol := session.CloneEventProtocol(session.EventProtocol{Update: update})
	return protocol.Update
}

func protocolLocationsForProjection(in []session.ProtocolToolCallLocation) []schema.ToolCallLocation {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCallLocation, 0, len(in))
	for _, item := range in {
		var line *int
		if item.Line != nil {
			value := *item.Line
			line = &value
		}
		out = append(out, schema.ToolCallLocation{Path: item.Path, Line: line})
	}
	return out
}

func cloneAnyMapPayload(values map[string]any) any {
	cloned := cloneAnyMap(values)
	if len(cloned) == 0 {
		// ACP tool payload fields are optional. Keep empty maps omitted instead
		// of serializing rawInput/rawOutput as {}, and avoid typed-nil maps
		// becoming explicit null values.
		return nil
	}
	return cloned
}
