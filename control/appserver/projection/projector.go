package projection

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/internal/eventmeta"
)

// ProjectEvent converts one canonical event into ACP-compatible update payloads.
func ProjectEvent(event *session.Event) ([]eventstream.Update, error) {
	if event == nil {
		return nil, nil
	}
	if _, ok, err := projectPermissionRequest(event); err != nil {
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

func projectPermissionRequest(event *session.Event) (*eventstream.RequestPermissionRequest, bool, error) {
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

func permissionToolCallUpdateFromProtocol(call session.ProtocolToolCall) eventstream.ToolCallUpdate {
	update := eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(call.ID),
	}
	if title := projectedToolLifecycleTitle(call.Name, call.RawInput, call.Status, call.Title); title != "" {
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
	update = withDisplayTerminalUpdate(update, call.ID, call.Name)
	// RequestPermission carries a complete tool snapshot rather than one
	// incremental lifecycle patch. Preserve the declared terminal reference so
	// clients can render the approval context without prior feed state.
	if update.Content == nil && displayTerminalID != "" {
		update.Meta, update.Content = terminalExtensionMetaFromContent(update.Meta, displayTerminalID, nil)
	}
	return update
}

func explicitUpdates(event *session.Event) []eventstream.Update {
	if event == nil || event.Protocol == nil {
		return nil
	}
	switch protocolUpdateType(event) {
	case eventstream.UpdateUserMessage:
		return contentUpdateForEvent(event, eventstream.UpdateUserMessage, textForUserEvent(event))
	case eventstream.UpdateAgentMessage:
		return explicitAssistantMessageUpdates(event)
	case eventstream.UpdateAgentThought:
		return contentUpdateForEvent(event, eventstream.UpdateAgentThought, reasoningForAssistantEvent(event))
	case eventstream.UpdateToolCall:
		return explicitToolCallUpdates(event)
	case eventstream.UpdateToolCallInfo:
		update, ok, err := toolCallUpdateForEvent(event)
		if err != nil || !ok {
			return nil
		}
		return []eventstream.Update{update}
	case eventstream.UpdatePlan:
		if event.Protocol.Update != nil {
			update := session.ProtocolUpdateOf(event)
			if update != nil {
				return []eventstream.Update{planUpdateFromEntries(update.Entries)}
			}
		}
		update, ok := planUpdateForEvent(event)
		if !ok {
			return nil
		}
		return []eventstream.Update{update}
	case eventstream.UpdateCompact:
		return contentUpdateForEvent(event, eventstream.UpdateCompact, textForUserEvent(event))
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

func inferredUpdates(event *session.Event) []eventstream.Update {
	if event == nil {
		return nil
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		return contentUpdateForEvent(event, eventstream.UpdateUserMessage, textForUserEvent(event))
	case session.EventTypeAssistant:
		return inferredAssistantUpdates(event)
	case session.EventTypeToolCall:
		return inferredToolCallUpdates(event)
	case session.EventTypeToolResult:
		update, ok, err := toolCallUpdateForEvent(event)
		if err != nil || !ok {
			return nil
		}
		return []eventstream.Update{update}
	case session.EventTypePlan:
		update, ok := planUpdateForEvent(event)
		if !ok {
			return nil
		}
		return []eventstream.Update{update}
	case session.EventTypeCompact:
		return contentUpdateForEvent(event, eventstream.UpdateCompact, textForUserEvent(event))
	default:
		return nil
	}
}

func inferredAssistantUpdates(event *session.Event) []eventstream.Update {
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
		return contentUpdateForEvent(event, eventstream.UpdateAgentMessage, textForAssistantEvent(event))
	}
	out := make([]eventstream.Update, 0, 2)
	if reasoning := reasoningForAssistantEvent(event); reasoning != "" {
		out = append(out, contentChunkForEvent(event, eventstream.UpdateAgentThought, reasoning))
	}
	if text := textForAssistantEvent(event); text != "" {
		out = append(out, contentChunkForEvent(event, eventstream.UpdateAgentMessage, text))
	}
	return out
}

func explicitAssistantMessageUpdates(event *session.Event) []eventstream.Update {
	if event == nil {
		return nil
	}
	out := make([]eventstream.Update, 0, 2)
	if reasoning := reasoningForAssistantEvent(event); reasoning != "" {
		out = append(out, contentChunkForEvent(event, eventstream.UpdateAgentThought, reasoning))
	}
	if text := textForAssistantEvent(event); text != "" {
		out = append(out, contentChunkForEvent(event, eventstream.UpdateAgentMessage, text))
	}
	return out
}

func explicitToolCallUpdates(event *session.Event) []eventstream.Update {
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

func inferredAssistantMessageOnly(event *session.Event) []eventstream.Update {
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
	out := make([]eventstream.Update, 0, 2)
	if reasoning := reasoningForAssistantEvent(event); reasoning != "" {
		out = append(out, contentChunkForEvent(event, eventstream.UpdateAgentThought, reasoning))
	}
	if text := textForAssistantEvent(event); text != "" {
		out = append(out, contentChunkForEvent(event, eventstream.UpdateAgentMessage, text))
	}
	return out
}

func inferredToolCallUpdates(event *session.Event) []eventstream.Update {
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
		update := eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    strings.TrimSpace(call.ID),
			Title:         projectedToolTitle(call.Name, args, eventstream.ToolStatusPending),
			Kind:          projectedToolKind(call.Name),
			Status:        eventstream.ToolStatusPending,
			RawInput:      cloneAnyMapPayload(args),
		}
		update = withDisplayTerminal(update, call.Name, args)
		out = append(out, update)
	}
	return out
}

func contentUpdateForEvent(event *session.Event, kind string, text string) []eventstream.Update {
	if text == "" {
		return nil
	}
	return []eventstream.Update{contentChunkForEvent(event, kind, text)}
}

func contentChunk(kind string, text string) eventstream.ContentChunk {
	return eventstream.ContentChunk{
		SessionUpdate: kind,
		Content:       eventstream.TextContent{Type: "text", Text: text},
	}
}

func contentChunkForEvent(event *session.Event, kind string, text string) eventstream.ContentChunk {
	chunk := contentChunk(kind, text)
	chunk.MessageID = session.EventMessageID(event)
	// ProtocolUpdate metadata describes that exact ACP update. Do not attach
	// tool/plan metadata to assistant chunks emitted from the durable Message.
	if update := session.ProtocolUpdateOf(event); update != nil && normalizeUpdateType(update.SessionUpdate) == kind {
		chunk.Meta = cloneAnyMap(update.Meta)
	}
	if kind == eventstream.UpdateAgentMessage && event != nil {
		message := event.Message
		if message == nil {
			if projected, ok := session.ModelMessageOf(event); ok {
				message = &projected
			}
		}
		if message != nil {
			if citations := message.TextContentCitations(); len(citations) > 0 {
				chunk.Meta = eventmeta.Merge(chunk.Meta, map[string]any{
					eventmeta.Root: map[string]any{
						"version": 1,
						"message": map[string]any{
							"citations": citationMetaPayload(citations),
						},
					},
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

func toolCallForEvent(event *session.Event) (eventstream.ToolCall, bool, error) {
	if event == nil {
		return eventstream.ToolCall{}, false, nil
	}
	if event.Tool != nil {
		return toolCallFromEventToolPayload(event.Tool), true, nil
	}
	if update := session.ProtocolUpdateOf(event); update != nil && normalizeUpdateType(update.SessionUpdate) == eventstream.UpdateToolCall {
		return toolCallFromProtocolUpdate(event, update), true, nil
	}
	if event.Message == nil {
		return eventstream.ToolCall{}, false, nil
	}
	calls := event.Message.ToolCalls()
	if len(calls) == 0 {
		return eventstream.ToolCall{}, false, nil
	}
	args := parseObject(calls[0].Args)
	call := eventstream.ToolCall{
		SessionUpdate: eventstream.UpdateToolCall,
		ToolCallID:    strings.TrimSpace(calls[0].ID),
		Title:         projectedToolTitle(calls[0].Name, args, eventstream.ToolStatusPending),
		Kind:          projectedToolKind(calls[0].Name),
		Status:        eventstream.ToolStatusPending,
		RawInput:      cloneAnyMapPayload(args),
	}
	call = withDisplayTerminal(call, calls[0].Name, args)
	return call, true, nil
}

func toolCallFromEventToolPayload(tool *session.EventTool) eventstream.ToolCall {
	if tool == nil {
		return eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall}
	}
	rawInput := cloneAnyMap(tool.Input)
	displayTerminalID, _ := projectedDisplayTerminalID(tool.ID, tool.Name)
	call := eventstream.ToolCall{
		SessionUpdate: eventstream.UpdateToolCall,
		ToolCallID:    strings.TrimSpace(tool.ID),
		Title:         projectedToolLifecycleTitle(tool.Name, rawInput, tool.Status, tool.Title),
		Kind:          firstNonEmpty(strings.TrimSpace(tool.Kind), projectedToolKind(tool.Name)),
		Status:        firstNonEmpty(acpToolStatus(tool.Status), eventstream.ToolStatusPending),
		RawInput:      cloneAnyMapPayload(rawInput),
		RawOutput:     cloneAnyMapPayload(tool.Output),
		Content:       projectEventToolContent(tool.Content, displayTerminalID),
		Locations:     projectEventToolLocations(tool.Locations),
	}
	return withDisplayTerminal(call, tool.Name, rawInput)
}

func toolCallFromProtocolUpdate(event *session.Event, update *session.ProtocolUpdate) eventstream.ToolCall {
	update = cloneProtocolUpdateForProjection(update)
	name := protocolToolNameForUpdate(event, update)
	rawInput := cloneAnyMap(update.RawInput)
	call := eventstream.ToolCall{
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
	call.Title = projectedToolLifecycleTitle(name, rawInput, call.Status, call.Title)
	call.Kind = firstNonEmpty(strings.TrimSpace(call.Kind), projectedToolKind(name))
	call.Status = firstNonEmpty(acpToolStatus(call.Status), eventstream.ToolStatusPending)
	displayTerminalID, _ := projectedDisplayTerminalID(call.ToolCallID, name)
	call.Content = projectToolContent(session.ProtocolToolCallContentOf(update), displayTerminalID)
	return withDisplayTerminal(call, name, rawInput)
}

func toolCallUpdateForEvent(event *session.Event) (eventstream.ToolCallUpdate, bool, error) {
	if event == nil {
		return eventstream.ToolCallUpdate{}, false, nil
	}
	if event.Tool != nil {
		return toolCallUpdateFromEventToolPayload(event.Tool, event.Meta), true, nil
	}
	if update := session.ProtocolUpdateOf(event); update != nil && normalizeUpdateType(update.SessionUpdate) == eventstream.UpdateToolCallInfo {
		projected, err := toolCallUpdateFromProtocolUpdate(event, update)
		if err != nil {
			return eventstream.ToolCallUpdate{}, false, err
		}
		return projected, true, nil
	}
	if event.Message == nil {
		return eventstream.ToolCallUpdate{}, false, nil
	}
	resp := event.Message.ToolResponse()
	if resp == nil {
		return eventstream.ToolCallUpdate{}, false, nil
	}
	status := eventstream.ToolStatusCompleted
	if raw, ok := event.Meta["is_error"].(bool); ok && raw {
		status = eventstream.ToolStatusFailed
	}
	name := strings.TrimSpace(resp.Name)
	kind := projectedToolKind(name)
	out := eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(resp.ID),
		Kind:          stringPtr(kind),
		Status:        stringPtr(status),
		RawOutput:     cloneAnyMapPayload(resp.Result),
		Meta:          protocolUpdateMeta(event),
	}
	if title := projectedToolLifecycleTitle(name, nil, status, ""); projectedToolTitleTracksLifecycle(name) && title != "" {
		out.Title = stringPtr(title)
	}
	out = withDisplayTerminalUpdate(out, resp.ID, name)
	return withRuntimeCommandObservation(out, event.Meta, name), true, nil
}

func toolCallUpdateFromEventToolPayload(tool *session.EventTool, meta map[string]any) eventstream.ToolCallUpdate {
	if tool == nil {
		return eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo}
	}
	displayTerminalID, _ := projectedDisplayTerminalID(tool.ID, tool.Name)
	out := eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(tool.ID),
		RawInput:      cloneAnyMapPayload(tool.Input),
		RawOutput:     cloneAnyMapPayload(tool.Output),
		Content:       projectEventToolContent(tool.Content, displayTerminalID),
		Locations:     projectEventToolLocations(tool.Locations),
	}
	if len(out.Content) == 0 && !taskBackedRunningCommandUpdate(tool.Name, tool.Status, meta) {
		out.Content = projectedToolResultContent(tool.ID, tool.Name, tool.Input, tool.Output, meta, tool.Status)
	}
	if title := projectedToolLifecycleTitle(tool.Name, tool.Input, tool.Status, tool.Title); title != "" {
		out.Title = stringPtr(title)
	}
	if kind := firstNonEmpty(strings.TrimSpace(tool.Kind), projectedToolKind(tool.Name)); kind != "" {
		out.Kind = stringPtr(kind)
	}
	if status := acpToolStatus(tool.Status); status != "" {
		out.Status = stringPtr(status)
	}
	out = withDisplayTerminalUpdate(out, tool.ID, tool.Name)
	return withRuntimeCommandObservation(out, meta, tool.Name)
}

func toolCallUpdateFromProtocolUpdate(event *session.Event, update *session.ProtocolUpdate) (eventstream.ToolCallUpdate, error) {
	update = cloneProtocolUpdateForProjection(update)
	id := strings.TrimSpace(update.ToolCallID)
	if id == "" {
		return eventstream.ToolCallUpdate{}, fmt.Errorf("control/appserver/projection: tool update missing tool call id")
	}
	name := protocolToolNameForUpdate(event, update)
	out := eventstream.ToolCallUpdate{
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
	if title := projectedToolLifecycleTitle(name, update.RawInput, update.Status, stringFromPtr(out.Title)); title != "" {
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
	out = withDisplayTerminalUpdate(out, id, name)
	return withRuntimeCommandObservation(out, eventmeta.Merge(event.Meta, update.Meta), name), nil
}

func projectEventToolLocations(locations []session.EventToolLocation) []eventstream.ToolCallLocation {
	if len(locations) == 0 {
		return nil
	}
	out := make([]eventstream.ToolCallLocation, 0, len(locations))
	for _, item := range locations {
		var line *int
		if item.Line != nil {
			value := *item.Line
			line = &value
		}
		out = append(out, eventstream.ToolCallLocation{
			Path: strings.TrimSpace(item.Path),
			Line: line,
		})
	}
	return out
}

func projectEventToolContent(content []session.EventToolContent, displayTerminalID string) []eventstream.ToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]eventstream.ToolCallContent, 0, len(content))
	for _, item := range content {
		contentType := strings.TrimSpace(item.Type)
		terminalID := strings.TrimSpace(item.TerminalID)
		var payload any
		if strings.TrimSpace(item.Text) != "" {
			payload = eventstream.TextContent{Type: "text", Text: item.Text}
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
		out = append(out, eventstream.ToolCallContent{
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

func projectToolContent(content []session.ProtocolToolCallContent, displayTerminalID string) []eventstream.ToolCallContent {
	if content == nil {
		return nil
	}
	out := make([]eventstream.ToolCallContent, 0, len(content))
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
		out = append(out, eventstream.ToolCallContent{
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

func acpToolStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", eventstream.ToolStatusPending, eventstream.ToolStatusInProgress, eventstream.ToolStatusCompleted, eventstream.ToolStatusFailed:
		return strings.TrimSpace(status)
	case "started", "running", "waiting_approval":
		return eventstream.ToolStatusInProgress
	case "cancelled", "canceled", "interrupted", "terminated", "timed_out", "timeout":
		return eventstream.ToolStatusFailed
	default:
		return strings.TrimSpace(status)
	}
}

func planUpdateForEvent(event *session.Event) (eventstream.PlanUpdate, bool) {
	if event == nil {
		return eventstream.PlanUpdate{}, false
	}
	if event.Protocol != nil {
		if update := session.ProtocolUpdateOf(event); update != nil && (len(update.Entries) > 0 || normalizeUpdateType(update.SessionUpdate) == eventstream.UpdatePlan) {
			return planUpdateFromEntries(update.Entries), true
		}
	}
	payload := session.PlanPayloadOf(event)
	if payload == nil {
		return eventstream.PlanUpdate{}, false
	}
	return planUpdateFromPayload(*payload), true
}

func planUpdateFromEntries(protocolEntries []session.ProtocolPlanEntry) eventstream.PlanUpdate {
	entries := make([]eventstream.PlanEntry, 0, len(protocolEntries))
	for _, item := range protocolEntries {
		entries = append(entries, eventstream.PlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: firstNonEmpty(strings.TrimSpace(item.Priority), "medium"),
		})
	}
	return eventstream.PlanUpdate{SessionUpdate: eventstream.UpdatePlan, Entries: entries}
}

func planUpdateFromPayload(payload session.EventPlanPayload) eventstream.PlanUpdate {
	entries := make([]eventstream.PlanEntry, 0, len(payload.Entries))
	for _, item := range payload.Entries {
		entries = append(entries, eventstream.PlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: firstNonEmpty(strings.TrimSpace(item.Priority), "medium"),
		})
	}
	return eventstream.PlanUpdate{
		SessionUpdate: eventstream.UpdatePlan,
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
	if reasoning := eventmeta.String(event.Meta, "caelis", "runtime", "replay", "reasoning_text"); reasoning != "" {
		return reasoning
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if reasoning := reasoningFromProtocolContent(update.Content); reasoning != "" {
			return reasoning
		}
		if normalizeUpdateType(update.SessionUpdate) == eventstream.UpdateAgentThought {
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
	return eventmeta.CloneMap(values)
}

func cloneProtocolUpdateForProjection(update *session.ProtocolUpdate) *session.ProtocolUpdate {
	if update == nil {
		return nil
	}
	protocol := session.CloneEventProtocol(session.EventProtocol{Update: update})
	return protocol.Update
}

func protocolLocationsForProjection(in []session.ProtocolToolCallLocation) []eventstream.ToolCallLocation {
	if len(in) == 0 {
		return nil
	}
	out := make([]eventstream.ToolCallLocation, 0, len(in))
	for _, item := range in {
		var line *int
		if item.Line != nil {
			value := *item.Line
			line = &value
		}
		out = append(out, eventstream.ToolCallLocation{Path: item.Path, Line: line})
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
