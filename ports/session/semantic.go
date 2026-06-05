package session

import (
	"encoding/json"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

const EventSchemaSemanticV2 = 2

// EventMessagePayload is the Caelis-owned durable message semantic shape. It is
// projected to provider-specific model.Message values at invocation time.
type EventMessagePayload struct {
	Role     string         `json:"role,omitempty"`
	Parts    []EventPart    `json:"parts,omitempty"`
	Origin   *EventOrigin   `json:"origin,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// EventOrigin stores provider metadata without making provider wire messages
// the durable session schema.
type EventOrigin struct {
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	RawFinishReason string `json:"raw_finish_reason,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
}

// EventPart is the durable semantic part representation shared by user,
// assistant, and tool-result payloads.
type EventPart struct {
	Kind       string                `json:"kind,omitempty"`
	Text       string                `json:"text,omitempty"`
	Reasoning  *EventReasoningPart   `json:"reasoning,omitempty"`
	ToolCall   *EventToolCallPayload `json:"tool_call,omitempty"`
	ToolResult *EventToolResultPart  `json:"tool_result,omitempty"`
	Media      *EventMediaPart       `json:"media,omitempty"`
	JSON       json.RawMessage       `json:"json,omitempty"`
	FileRef    *EventFileRefPart     `json:"file_ref,omitempty"`
}

type EventReasoningPart struct {
	Text            string                     `json:"text,omitempty"`
	Visibility      string                     `json:"visibility,omitempty"`
	Replay          *EventReplayMeta           `json:"replay,omitempty"`
	ProviderDetails map[string]json.RawMessage `json:"provider_details,omitempty"`
}

type EventReplayMeta struct {
	Provider string `json:"provider,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Token    string `json:"token,omitempty"`
}

type EventToolCallPayload struct {
	ID              string                     `json:"id,omitempty"`
	Name            string                     `json:"name,omitempty"`
	Kind            string                     `json:"kind,omitempty"`
	Title           string                     `json:"title,omitempty"`
	Status          string                     `json:"status,omitempty"`
	Args            json.RawMessage            `json:"args,omitempty"`
	Input           map[string]any             `json:"input,omitempty"`
	Metadata        map[string]any             `json:"metadata,omitempty"`
	Content         []EventToolContent         `json:"content,omitempty"`
	Locations       []EventToolLocation        `json:"locations,omitempty"`
	Replay          *EventReplayMeta           `json:"replay,omitempty"`
	ProviderDetails map[string]json.RawMessage `json:"provider_details,omitempty"`
}

type EventToolResultPayload struct {
	ToolCallID string              `json:"tool_call_id,omitempty"`
	Name       string              `json:"name,omitempty"`
	Kind       string              `json:"kind,omitempty"`
	Title      string              `json:"title,omitempty"`
	Status     string              `json:"status,omitempty"`
	IsError    bool                `json:"is_error,omitempty"`
	Input      map[string]any      `json:"input,omitempty"`
	Output     map[string]any      `json:"output,omitempty"`
	Metadata   map[string]any      `json:"metadata,omitempty"`
	Content    []EventPart         `json:"content,omitempty"`
	Display    []EventToolContent  `json:"display,omitempty"`
	Locations  []EventToolLocation `json:"locations,omitempty"`
	Truncation map[string]any      `json:"truncation,omitempty"`
}

type EventPlanPayload struct {
	Entries []EventPlanEntry `json:"entries,omitempty"`
}

type EventPlanEntry struct {
	Content  string `json:"content,omitempty"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

type EventToolResultPart struct {
	ToolCallID string      `json:"tool_call_id,omitempty"`
	Name       string      `json:"name,omitempty"`
	Content    []EventPart `json:"content,omitempty"`
	IsError    bool        `json:"is_error,omitempty"`
}

type EventMediaPart struct {
	Modality string           `json:"modality,omitempty"`
	Source   EventMediaSource `json:"source,omitempty"`
	MimeType string           `json:"mime_type,omitempty"`
	Name     string           `json:"name,omitempty"`
}

type EventMediaSource struct {
	Kind     string `json:"kind,omitempty"`
	Data     string `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	LocalRef string `json:"local_ref,omitempty"`
}

type EventFileRefPart struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	URI      string `json:"uri,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	LocalRef string `json:"local_ref,omitempty"`
}

func eventHasSemanticPayload(event *Event) bool {
	return event != nil &&
		(event.UserMessage != nil ||
			event.AssistantMessage != nil ||
			event.SystemContext != nil ||
			event.ToolCallPayload != nil ||
			event.ToolResultPayload != nil ||
			event.PlanPayload != nil)
}

func shouldStripLegacyCoreProjection(event *Event) bool {
	if !eventHasSemanticPayload(event) {
		return false
	}
	switch event.Visibility {
	case "", VisibilityCanonical, VisibilityMirror:
		return true
	default:
		return false
	}
}

func semanticizeCoreEvent(event *Event) {
	if event == nil {
		return
	}
	if eventHasSemanticPayload(event) {
		event.Schema = EventSchemaSemanticV2
		return
	}
	switch EventTypeOf(event) {
	case EventTypeUser:
		if event.Message != nil {
			payload := eventMessagePayloadFromModel(*event.Message)
			payload.Role = string(model.RoleUser)
			applyEventMessageMetadata(&payload, event.Meta)
			event.UserMessage = &payload
		} else if text := EventText(event); text != "" {
			payload := eventMessagePayloadFromModel(model.NewTextMessage(model.RoleUser, text))
			applyEventMessageMetadata(&payload, event.Meta)
			event.UserMessage = &payload
		}
	case EventTypeAssistant:
		if event.Message != nil {
			payload := eventMessagePayloadFromModel(*event.Message)
			payload.Role = string(model.RoleAssistant)
			applyEventMessageMetadata(&payload, event.Meta)
			event.AssistantMessage = &payload
		} else if text := EventText(event); text != "" {
			payload := eventMessagePayloadFromModel(model.NewTextMessage(model.RoleAssistant, text))
			applyEventMessageMetadata(&payload, event.Meta)
			event.AssistantMessage = &payload
		}
	case EventTypeSystem:
		if event.Message != nil {
			payload := eventMessagePayloadFromModel(*event.Message)
			payload.Role = string(model.RoleSystem)
			applyEventMessageMetadata(&payload, event.Meta)
			event.SystemContext = &payload
		} else if text := EventText(event); text != "" {
			payload := eventMessagePayloadFromModel(model.NewTextMessage(model.RoleSystem, text))
			applyEventMessageMetadata(&payload, event.Meta)
			event.SystemContext = &payload
		}
	case EventTypeToolCall:
		if event.Message != nil {
			payload := eventMessagePayloadFromModel(*event.Message)
			payload.Role = string(model.RoleAssistant)
			applyEventMessageMetadata(&payload, event.Meta)
			event.AssistantMessage = &payload
		}
		if event.Tool != nil {
			payload := eventToolCallPayloadFromTool(*event.Tool)
			applyEventToolCallMetadata(&payload, event.Meta)
			event.ToolCallPayload = &payload
		} else if event.Message != nil {
			if calls := event.Message.ToolCalls(); len(calls) > 0 {
				payload := eventToolCallPayloadFromModel(calls[0])
				applyEventToolCallMetadata(&payload, event.Meta)
				event.ToolCallPayload = &payload
			}
		} else if payload := eventToolCallPayloadFromProtocol(event); payload != nil {
			applyEventToolCallMetadata(payload, event.Meta)
			event.ToolCallPayload = payload
		}
	case EventTypeToolResult:
		payload := eventToolResultPayloadFromLegacy(event)
		if payload != nil {
			event.ToolResultPayload = payload
		}
	case EventTypePlan:
		if payload := eventPlanPayloadFromProtocol(event); payload != nil {
			event.PlanPayload = payload
		}
	}
	if eventHasSemanticPayload(event) {
		event.Schema = EventSchemaSemanticV2
	}
}

func stripLegacyCoreProjection(event *Event) {
	if event == nil || !eventHasSemanticPayload(event) {
		return
	}
	event.Message = nil
	event.Tool = nil
	event.Protocol = nil
	event.Meta = nil
}

func eventTextFromSemanticPayload(event *Event) string {
	if event == nil {
		return ""
	}
	switch {
	case event.UserMessage != nil:
		return textFromEventMessagePayload(*event.UserMessage)
	case event.AssistantMessage != nil:
		return textFromEventMessagePayload(*event.AssistantMessage)
	case event.SystemContext != nil:
		return textFromEventMessagePayload(*event.SystemContext)
	case event.ToolResultPayload != nil:
		return textFromEventParts(event.ToolResultPayload.Content)
	}
	return ""
}

func textFromEventMessagePayload(payload EventMessagePayload) string {
	if text := textFromEventTextParts(payload.Parts); text != "" {
		return text
	}
	return reasoningTextFromEventParts(payload.Parts)
}

func textFromEventParts(parts []EventPart) string {
	if len(parts) == 0 {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			texts = append(texts, part.Text)
			continue
		}
		if part.Reasoning != nil && part.Reasoning.Text != "" {
			texts = append(texts, part.Reasoning.Text)
			continue
		}
		if part.ToolResult != nil {
			if text := textFromEventParts(part.ToolResult.Content); text != "" {
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n")
}

func textFromEventTextParts(parts []EventPart) string {
	if len(parts) == 0 {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func reasoningTextFromEventParts(parts []EventPart) string {
	if len(parts) == 0 {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Reasoning != nil && part.Reasoning.Text != "" {
			texts = append(texts, part.Reasoning.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func eventMessagePayloadFromModel(message model.Message) EventMessagePayload {
	payload := EventMessagePayload{
		Role:  strings.TrimSpace(string(message.Role)),
		Parts: eventPartsFromModelParts(message.Parts),
	}
	if message.Origin != nil {
		payload.Origin = &EventOrigin{
			Provider:        strings.TrimSpace(message.Origin.Provider),
			Model:           strings.TrimSpace(message.Origin.Model),
			RawFinishReason: strings.TrimSpace(message.Origin.RawFinishReason),
			CreatedAt:       strings.TrimSpace(message.Origin.CreatedAt),
		}
	}
	return payload
}

func applyEventMessageMetadata(payload *EventMessagePayload, meta map[string]any) {
	if payload == nil || len(meta) == 0 {
		return
	}
	sdk, _ := nestedAnyFromMap(meta, "caelis", "sdk").(map[string]any)
	if len(sdk) > 0 {
		payload.Metadata = map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"sdk":     maps.Clone(sdk),
			},
		}
		return
	}
	if hasDurableUsageMetadata(meta) {
		payload.Metadata = maps.Clone(meta)
	}
}

func applyEventToolCallMetadata(payload *EventToolCallPayload, meta map[string]any) {
	if payload == nil || len(meta) == 0 || !hasDurableUsageMetadata(meta) {
		return
	}
	payload.Metadata = maps.Clone(meta)
}

func eventPartsFromModelParts(parts []model.Part) []EventPart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]EventPart, 0, len(parts))
	for _, part := range parts {
		out = append(out, eventPartFromModelPart(part))
	}
	return out
}

func eventPartFromModelPart(part model.Part) EventPart {
	out := EventPart{Kind: strings.TrimSpace(string(part.Kind))}
	switch {
	case part.Text != nil:
		out.Text = part.Text.Text
	case part.Reasoning != nil:
		out.Reasoning = &EventReasoningPart{
			Visibility:      strings.TrimSpace(string(part.Reasoning.Visibility)),
			ProviderDetails: cloneRawMessageMap(part.Reasoning.ProviderDetails),
		}
		if part.Reasoning.VisibleText != nil {
			out.Reasoning.Text = *part.Reasoning.VisibleText
		}
		out.Reasoning.Replay = eventReplayMetaFromModel(part.Reasoning.Replay)
	case part.ToolUse != nil:
		call := EventToolCallPayload{
			ID:              strings.TrimSpace(part.ToolUse.ID),
			Name:            strings.TrimSpace(part.ToolUse.Name),
			Args:            append(json.RawMessage(nil), part.ToolUse.Input...),
			ProviderDetails: cloneRawMessageMap(part.ToolUse.ProviderDetails),
			Replay:          eventReplayMetaFromModel(part.ToolUse.Replay),
		}
		if len(call.Args) > 0 {
			var input map[string]any
			if err := json.Unmarshal(call.Args, &input); err == nil {
				call.Input = maps.Clone(input)
			}
		}
		out.ToolCall = &call
	case part.ToolResult != nil:
		out.ToolResult = &EventToolResultPart{
			ToolCallID: strings.TrimSpace(part.ToolResult.ToolUseID),
			Name:       strings.TrimSpace(part.ToolResult.Name),
			Content:    eventPartsFromModelParts(part.ToolResult.Content),
			IsError:    part.ToolResult.IsError,
		}
	case part.Media != nil:
		out.Media = &EventMediaPart{
			Modality: strings.TrimSpace(string(part.Media.Modality)),
			Source: EventMediaSource{
				Kind:     strings.TrimSpace(string(part.Media.Source.Kind)),
				Data:     part.Media.Source.Data,
				URI:      strings.TrimSpace(part.Media.Source.URI),
				FileID:   strings.TrimSpace(part.Media.Source.FileID),
				LocalRef: strings.TrimSpace(part.Media.Source.LocalRef),
			},
			MimeType: strings.TrimSpace(part.Media.MimeType),
			Name:     strings.TrimSpace(part.Media.Name),
		}
	case part.JSON != nil:
		out.JSON = append(json.RawMessage(nil), part.JSON.Value...)
	case part.FileRef != nil:
		out.FileRef = &EventFileRefPart{
			Name:     strings.TrimSpace(part.FileRef.Name),
			MimeType: strings.TrimSpace(part.FileRef.MimeType),
			URI:      strings.TrimSpace(part.FileRef.URI),
			FileID:   strings.TrimSpace(part.FileRef.FileID),
			LocalRef: strings.TrimSpace(part.FileRef.LocalRef),
		}
	}
	return out
}

func eventReplayMetaFromModel(in *model.ReplayMeta) *EventReplayMeta {
	if in == nil {
		return nil
	}
	return &EventReplayMeta{
		Provider: strings.TrimSpace(in.Provider),
		Kind:     strings.TrimSpace(in.Kind),
		Token:    strings.TrimSpace(in.Token),
	}
}

func modelReplayMetaFromEvent(in *EventReplayMeta) *model.ReplayMeta {
	if in == nil {
		return nil
	}
	return &model.ReplayMeta{
		Provider: strings.TrimSpace(in.Provider),
		Kind:     strings.TrimSpace(in.Kind),
		Token:    strings.TrimSpace(in.Token),
	}
}

func eventToolCallPayloadFromTool(in EventTool) EventToolCallPayload {
	return EventToolCallPayload{
		ID:        strings.TrimSpace(in.ID),
		Name:      strings.TrimSpace(in.Name),
		Kind:      strings.TrimSpace(in.Kind),
		Title:     strings.TrimSpace(in.Title),
		Status:    strings.TrimSpace(in.Status),
		Input:     maps.Clone(in.Input),
		Args:      mustRawJSON(in.Input),
		Content:   cloneEventToolContent(in.Content),
		Locations: cloneEventToolLocations(in.Locations),
	}
}

func eventToolCallPayloadFromModel(call model.ToolCall) EventToolCallPayload {
	args := json.RawMessage(strings.TrimSpace(call.Args))
	payload := EventToolCallPayload{
		ID:   strings.TrimSpace(call.ID),
		Name: strings.TrimSpace(call.Name),
		Args: append(json.RawMessage(nil), args...),
	}
	if strings.TrimSpace(call.ThoughtSignature) != "" {
		payload.Replay = &EventReplayMeta{Token: strings.TrimSpace(call.ThoughtSignature)}
	}
	var input map[string]any
	if err := json.Unmarshal(args, &input); err == nil {
		payload.Input = maps.Clone(input)
	}
	return payload
}

func eventToolCallPayloadFromProtocol(event *Event) *EventToolCallPayload {
	if event == nil || event.Protocol == nil {
		return nil
	}
	protocol := CloneEventProtocol(*event.Protocol)
	if protocol.ToolCall == nil {
		return nil
	}
	call := protocol.ToolCall
	payload := EventToolCallPayload{
		ID:        strings.TrimSpace(call.ID),
		Name:      firstNonEmpty(strings.TrimSpace(call.Name), strings.TrimSpace(call.Kind)),
		Kind:      strings.TrimSpace(call.Kind),
		Title:     strings.TrimSpace(call.Title),
		Status:    strings.TrimSpace(call.Status),
		Input:     maps.Clone(call.RawInput),
		Args:      mustRawJSON(call.RawInput),
		Content:   eventToolContentFromProtocol(call.Content),
		Locations: eventToolLocationsFromProtocolUpdate(protocol.Update),
	}
	if payload.ID == "" && payload.Name == "" && len(payload.Input) == 0 && len(payload.Content) == 0 {
		return nil
	}
	return &payload
}

func eventPlanPayloadFromProtocol(event *Event) *EventPlanPayload {
	if event == nil || event.Protocol == nil {
		return nil
	}
	protocol := CloneEventProtocol(*event.Protocol)
	entries := []ProtocolPlanEntry(nil)
	if protocol.Plan != nil {
		entries = protocol.Plan.Entries
	}
	if len(entries) == 0 && protocol.Update != nil {
		entries = protocol.Update.Entries
	}
	if len(entries) == 0 {
		return nil
	}
	payload := &EventPlanPayload{Entries: make([]EventPlanEntry, 0, len(entries))}
	for _, entry := range entries {
		content := strings.TrimSpace(entry.Content)
		status := strings.TrimSpace(entry.Status)
		priority := strings.TrimSpace(entry.Priority)
		if content == "" && status == "" && priority == "" {
			continue
		}
		payload.Entries = append(payload.Entries, EventPlanEntry{
			Content:  content,
			Status:   status,
			Priority: priority,
		})
	}
	if len(payload.Entries) == 0 {
		return nil
	}
	return payload
}

func eventToolResultPayloadFromLegacy(event *Event) *EventToolResultPayload {
	if event == nil {
		return nil
	}
	var payload EventToolResultPayload
	payload.Metadata = eventToolRuntimeMetadataFromMeta(event.Meta)
	payload.Truncation = eventToolTruncationFromMeta(event.Meta)
	applyProtocolToolResultPayload(event, &payload)
	if event.Tool != nil {
		payload.ToolCallID = strings.TrimSpace(event.Tool.ID)
		payload.Name = strings.TrimSpace(event.Tool.Name)
		payload.Kind = strings.TrimSpace(event.Tool.Kind)
		payload.Title = strings.TrimSpace(event.Tool.Title)
		payload.Status = strings.TrimSpace(event.Tool.Status)
		payload.Input = maps.Clone(event.Tool.Input)
		payload.Output = maps.Clone(event.Tool.Output)
		payload.Display = cloneEventToolContent(event.Tool.Content)
		payload.Locations = cloneEventToolLocations(event.Tool.Locations)
	}
	if event.Message != nil {
		results := event.Message.ToolResults()
		if len(results) > 0 {
			result := results[0]
			if payload.ToolCallID == "" {
				payload.ToolCallID = strings.TrimSpace(result.ToolUseID)
			}
			if payload.Name == "" {
				payload.Name = strings.TrimSpace(result.Name)
			}
			payload.IsError = result.IsError
			payload.Content = eventPartsFromModelParts(result.Content)
			if len(payload.Output) == 0 {
				if out, err := toolResultOutputFromEventParts(payload.Content); err == nil {
					payload.Output = out
				}
			}
		}
	}
	if payload.ToolCallID == "" && payload.Name == "" && len(payload.Output) == 0 && len(payload.Content) == 0 {
		return nil
	}
	return &payload
}

func eventToolTruncationFromMeta(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	value := nestedAnyFromMap(meta, "caelis", "runtime", "tool", "truncation")
	truncation, _ := value.(map[string]any)
	return maps.Clone(truncation)
}

func eventToolRuntimeMetadataFromMeta(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	runtimeMeta, _ := nestedAnyFromMap(meta, "caelis", "runtime").(map[string]any)
	if len(runtimeMeta) == 0 {
		return nil
	}
	runtimeTool, _ := nestedAnyFromMap(meta, "caelis", "runtime", "tool").(map[string]any)
	runtimeTask, _ := nestedAnyFromMap(meta, "caelis", "runtime", "task").(map[string]any)
	runtimeStream, _ := nestedAnyFromMap(meta, "caelis", "runtime", "stream").(map[string]any)
	if len(runtimeTool) == 0 && len(runtimeTask) == 0 && len(runtimeStream) == 0 {
		return nil
	}
	runtimeOut := map[string]any{}
	if len(runtimeTool) > 0 {
		runtimeOut["tool"] = maps.Clone(runtimeTool)
	}
	if len(runtimeTask) > 0 {
		runtimeOut["task"] = maps.Clone(runtimeTask)
	}
	if len(runtimeStream) > 0 {
		runtimeOut["stream"] = maps.Clone(runtimeStream)
	}
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": runtimeOut,
		},
	}
}

func nestedAnyFromMap(values map[string]any, path ...string) any {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	return current
}

func applyProtocolToolResultPayload(event *Event, payload *EventToolResultPayload) {
	if event == nil || event.Protocol == nil || payload == nil {
		return
	}
	protocol := CloneEventProtocol(*event.Protocol)
	if protocol.ToolCall != nil {
		call := protocol.ToolCall
		payload.ToolCallID = firstNonEmpty(payload.ToolCallID, strings.TrimSpace(call.ID))
		payload.Name = firstNonEmpty(payload.Name, strings.TrimSpace(call.Name), strings.TrimSpace(call.Kind))
		payload.Kind = firstNonEmpty(payload.Kind, strings.TrimSpace(call.Kind))
		payload.Title = firstNonEmpty(payload.Title, strings.TrimSpace(call.Title))
		payload.Status = firstNonEmpty(payload.Status, strings.TrimSpace(call.Status))
		if len(payload.Input) == 0 {
			payload.Input = maps.Clone(call.RawInput)
		}
		if len(payload.Output) == 0 {
			payload.Output = maps.Clone(call.RawOutput)
		}
		if len(payload.Display) == 0 {
			payload.Display = eventToolContentFromProtocol(call.Content)
		}
	}
	if protocol.Update != nil {
		update := protocol.Update
		payload.ToolCallID = firstNonEmpty(payload.ToolCallID, strings.TrimSpace(update.ToolCallID))
		payload.Name = firstNonEmpty(payload.Name, strings.TrimSpace(update.Kind))
		payload.Kind = firstNonEmpty(payload.Kind, strings.TrimSpace(update.Kind))
		payload.Title = firstNonEmpty(payload.Title, strings.TrimSpace(update.Title))
		payload.Status = firstNonEmpty(payload.Status, strings.TrimSpace(update.Status))
		if len(payload.Input) == 0 {
			payload.Input = maps.Clone(update.RawInput)
		}
		if len(payload.Output) == 0 {
			payload.Output = maps.Clone(update.RawOutput)
		}
		if len(payload.Display) == 0 {
			payload.Display = eventToolContentFromProtocol(ProtocolToolCallContentOf(update))
		}
		if len(payload.Locations) == 0 {
			payload.Locations = eventToolLocationsFromProtocolUpdate(update)
		}
	}
	if len(payload.Content) == 0 && len(payload.Output) > 0 {
		payload.Content = []EventPart{{Kind: string(model.PartKindJSON), JSON: mustRawJSON(payload.Output)}}
	}
}

func eventToolContentFromProtocol(in []ProtocolToolCallContent) []EventToolContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]EventToolContent, 0, len(in))
	for _, item := range in {
		content := EventToolContent{
			Type:       strings.TrimSpace(item.Type),
			Text:       textFromProtocolContent(item.Content),
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			NewText:    item.NewText,
		}
		if item.OldText != nil {
			value := *item.OldText
			content.OldText = &value
		}
		if content.Type == "" && content.Text == "" && content.TerminalID == "" && content.Path == "" && content.OldText == nil && content.NewText == "" {
			continue
		}
		out = append(out, content)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func eventToolLocationsFromProtocolUpdate(update *ProtocolUpdate) []EventToolLocation {
	if update == nil || len(update.Locations) == 0 {
		return nil
	}
	out := make([]EventToolLocation, 0, len(update.Locations))
	for _, item := range update.Locations {
		location := EventToolLocation{Path: strings.TrimSpace(item.Path)}
		if item.Line != nil {
			value := *item.Line
			location.Line = &value
		}
		if location.Path == "" && location.Line == nil {
			continue
		}
		out = append(out, location)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toolResultOutputFromEventParts(parts []EventPart) (map[string]any, error) {
	for _, part := range parts {
		if len(part.JSON) == 0 {
			continue
		}
		var decoded any
		if err := json.Unmarshal(part.JSON, &decoded); err != nil {
			return nil, err
		}
		if payload, ok := decoded.(map[string]any); ok {
			return maps.Clone(payload), nil
		}
		return map[string]any{"result": decoded}, nil
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part.Kind) == string(model.PartKindText) && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	if len(texts) > 0 {
		return map[string]any{"result": strings.Join(texts, "\n")}, nil
	}
	return nil, nil
}

// ModelMessageOf projects one semantic session event to provider-neutral model
// context. Legacy v1 fields are accepted only as a migration source.
func ModelMessageOf(event *Event) (model.Message, bool) {
	if event == nil {
		return model.Message{}, false
	}
	switch EventTypeOf(event) {
	case EventTypeUser:
		if event.UserMessage != nil {
			return modelMessageFromEventPayload(*event.UserMessage, model.RoleUser), true
		}
	case EventTypeAssistant, EventTypeToolCall:
		if event.AssistantMessage != nil {
			return modelMessageFromEventPayload(*event.AssistantMessage, model.RoleAssistant), true
		}
		if event.ToolCallPayload != nil {
			return model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{modelToolCallFromEventPayload(*event.ToolCallPayload)}, ""), true
		}
	case EventTypeSystem:
		if event.SystemContext != nil {
			return modelMessageFromEventPayload(*event.SystemContext, model.RoleSystem), true
		}
	case EventTypeToolResult:
		if event.ToolResultPayload != nil {
			return modelMessageFromToolResultPayload(*event.ToolResultPayload), true
		}
	}
	if event.Message != nil {
		return model.CloneMessage(*event.Message), true
	}
	return model.Message{}, false
}

func modelMessageFromEventPayload(payload EventMessagePayload, fallback model.Role) model.Message {
	role := model.Role(strings.TrimSpace(payload.Role))
	if role == "" {
		role = fallback
	}
	message := model.Message{
		Role:  role,
		Parts: modelPartsFromEventParts(payload.Parts),
	}
	if payload.Origin != nil {
		message.Origin = &model.MessageOrigin{
			Provider:        strings.TrimSpace(payload.Origin.Provider),
			Model:           strings.TrimSpace(payload.Origin.Model),
			RawFinishReason: strings.TrimSpace(payload.Origin.RawFinishReason),
			CreatedAt:       strings.TrimSpace(payload.Origin.CreatedAt),
		}
	}
	return message
}

func modelMessageFromToolResultPayload(payload EventToolResultPayload) model.Message {
	content := modelPartsFromEventParts(payload.Content)
	if len(content) == 0 {
		content = []model.Part{model.NewJSONPart(mustRawJSON(payload.Output))}
	}
	return model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{{
			Kind: model.PartKindToolResult,
			ToolResult: &model.ToolResultPart{
				ToolUseID: strings.TrimSpace(payload.ToolCallID),
				Name:      strings.TrimSpace(payload.Name),
				Content:   content,
				IsError:   payload.IsError,
			},
		}},
	}
}

func modelToolCallFromEventPayload(payload EventToolCallPayload) model.ToolCall {
	call := model.ToolCall{
		ID:   strings.TrimSpace(payload.ID),
		Name: strings.TrimSpace(payload.Name),
		Args: string(payload.Args),
	}
	if strings.TrimSpace(call.Args) == "" {
		call.Args = string(mustRawJSON(payload.Input))
	}
	if payload.Replay != nil {
		call.ThoughtSignature = strings.TrimSpace(payload.Replay.Token)
	}
	return call
}

func modelPartsFromEventParts(parts []EventPart) []model.Part {
	if len(parts) == 0 {
		return nil
	}
	out := make([]model.Part, 0, len(parts))
	for _, part := range parts {
		out = append(out, modelPartFromEventPart(part))
	}
	return out
}

func modelPartFromEventPart(part EventPart) model.Part {
	kind := model.PartKind(strings.TrimSpace(part.Kind))
	switch {
	case part.Text != "":
		return model.NewTextPart(part.Text)
	case part.Reasoning != nil:
		out := model.NewReasoningPart(part.Reasoning.Text, model.ReasoningVisibility(part.Reasoning.Visibility))
		if out.Reasoning != nil {
			out.Reasoning.Replay = modelReplayMetaFromEvent(part.Reasoning.Replay)
			out.Reasoning.ProviderDetails = cloneRawMessageMap(part.Reasoning.ProviderDetails)
		}
		return out
	case part.ToolCall != nil:
		out := model.NewToolUsePart(part.ToolCall.ID, part.ToolCall.Name, part.ToolCall.Args)
		if out.ToolUse != nil {
			out.ToolUse.Replay = modelReplayMetaFromEvent(part.ToolCall.Replay)
			out.ToolUse.ProviderDetails = cloneRawMessageMap(part.ToolCall.ProviderDetails)
		}
		return out
	case part.ToolResult != nil:
		return model.Part{
			Kind: model.PartKindToolResult,
			ToolResult: &model.ToolResultPart{
				ToolUseID: strings.TrimSpace(part.ToolResult.ToolCallID),
				Name:      strings.TrimSpace(part.ToolResult.Name),
				Content:   modelPartsFromEventParts(part.ToolResult.Content),
				IsError:   part.ToolResult.IsError,
			},
		}
	case part.Media != nil:
		return model.NewMediaPart(
			model.MediaModality(part.Media.Modality),
			model.MediaSource{
				Kind:     model.MediaSourceKind(part.Media.Source.Kind),
				Data:     part.Media.Source.Data,
				URI:      part.Media.Source.URI,
				FileID:   part.Media.Source.FileID,
				LocalRef: part.Media.Source.LocalRef,
			},
			part.Media.MimeType,
			part.Media.Name,
		)
	case len(part.JSON) > 0:
		return model.NewJSONPart(part.JSON)
	case part.FileRef != nil:
		return model.NewFileRefPart(part.FileRef.Name, part.FileRef.MimeType, part.FileRef.URI, part.FileRef.FileID, part.FileRef.LocalRef)
	default:
		return model.Part{Kind: kind}
	}
}

func ToolCallPayloadOf(event *Event) *EventToolCallPayload {
	if event == nil {
		return nil
	}
	if event.ToolCallPayload != nil {
		out := cloneEventToolCallPayload(*event.ToolCallPayload)
		return &out
	}
	if event.Tool != nil {
		out := eventToolCallPayloadFromTool(*event.Tool)
		return &out
	}
	if event.Message != nil {
		if calls := event.Message.ToolCalls(); len(calls) > 0 {
			out := eventToolCallPayloadFromModel(calls[0])
			return &out
		}
	}
	return nil
}

func ToolResultPayloadOf(event *Event) *EventToolResultPayload {
	if event == nil {
		return nil
	}
	if event.ToolResultPayload != nil {
		out := cloneEventToolResultPayload(*event.ToolResultPayload)
		return &out
	}
	return eventToolResultPayloadFromLegacy(event)
}

func PlanPayloadOf(event *Event) *EventPlanPayload {
	if event == nil {
		return nil
	}
	if event.PlanPayload != nil {
		out := cloneEventPlanPayload(*event.PlanPayload)
		return &out
	}
	return eventPlanPayloadFromProtocol(event)
}

func EventToolProjection(event *Event) *EventTool {
	if event == nil {
		return nil
	}
	switch EventTypeOf(event) {
	case EventTypeToolCall:
		if payload := ToolCallPayloadOf(event); payload != nil {
			return &EventTool{
				ID:        strings.TrimSpace(payload.ID),
				Name:      strings.TrimSpace(payload.Name),
				Kind:      strings.TrimSpace(payload.Kind),
				Title:     strings.TrimSpace(payload.Title),
				Status:    strings.TrimSpace(payload.Status),
				Input:     maps.Clone(payload.Input),
				Content:   cloneEventToolContent(payload.Content),
				Locations: cloneEventToolLocations(payload.Locations),
			}
		}
	case EventTypeToolResult:
		if payload := ToolResultPayloadOf(event); payload != nil {
			return &EventTool{
				ID:        strings.TrimSpace(payload.ToolCallID),
				Name:      strings.TrimSpace(payload.Name),
				Kind:      strings.TrimSpace(payload.Kind),
				Title:     strings.TrimSpace(payload.Title),
				Status:    strings.TrimSpace(payload.Status),
				Input:     maps.Clone(payload.Input),
				Output:    maps.Clone(payload.Output),
				Content:   cloneEventToolContent(payload.Display),
				Locations: cloneEventToolLocations(payload.Locations),
			}
		}
	}
	return nil
}

func cloneEventToolCallPayload(in EventToolCallPayload) EventToolCallPayload {
	out := in
	out.Args = append(json.RawMessage(nil), in.Args...)
	out.Input = maps.Clone(in.Input)
	out.Metadata = maps.Clone(in.Metadata)
	out.Content = cloneEventToolContent(in.Content)
	out.Locations = cloneEventToolLocations(in.Locations)
	out.ProviderDetails = cloneRawMessageMap(in.ProviderDetails)
	if in.Replay != nil {
		replay := *in.Replay
		out.Replay = &replay
	}
	return out
}

func cloneEventToolResultPayload(in EventToolResultPayload) EventToolResultPayload {
	out := in
	out.Input = maps.Clone(in.Input)
	out.Output = maps.Clone(in.Output)
	out.Metadata = maps.Clone(in.Metadata)
	out.Content = cloneEventParts(in.Content)
	out.Display = cloneEventToolContent(in.Display)
	out.Locations = cloneEventToolLocations(in.Locations)
	out.Truncation = maps.Clone(in.Truncation)
	return out
}

func cloneEventPlanPayload(in EventPlanPayload) EventPlanPayload {
	out := in
	if len(in.Entries) > 0 {
		out.Entries = make([]EventPlanEntry, 0, len(in.Entries))
		for _, item := range in.Entries {
			out.Entries = append(out.Entries, EventPlanEntry{
				Content:  strings.TrimSpace(item.Content),
				Status:   strings.TrimSpace(item.Status),
				Priority: strings.TrimSpace(item.Priority),
			})
		}
	}
	return out
}

func cloneEventMessagePayload(in EventMessagePayload) EventMessagePayload {
	out := in
	out.Parts = cloneEventParts(in.Parts)
	out.Metadata = maps.Clone(in.Metadata)
	if in.Origin != nil {
		origin := *in.Origin
		out.Origin = &origin
	}
	return out
}

func cloneEventParts(in []EventPart) []EventPart {
	if len(in) == 0 {
		return nil
	}
	out := make([]EventPart, 0, len(in))
	for _, item := range in {
		part := item
		part.JSON = append(json.RawMessage(nil), item.JSON...)
		if item.Reasoning != nil {
			reasoning := *item.Reasoning
			reasoning.ProviderDetails = cloneRawMessageMap(item.Reasoning.ProviderDetails)
			if item.Reasoning.Replay != nil {
				replay := *item.Reasoning.Replay
				reasoning.Replay = &replay
			}
			part.Reasoning = &reasoning
		}
		if item.ToolCall != nil {
			toolCall := cloneEventToolCallPayload(*item.ToolCall)
			part.ToolCall = &toolCall
		}
		if item.ToolResult != nil {
			result := *item.ToolResult
			result.Content = cloneEventParts(item.ToolResult.Content)
			part.ToolResult = &result
		}
		if item.Media != nil {
			media := *item.Media
			part.Media = &media
		}
		if item.FileRef != nil {
			fileRef := *item.FileRef
			part.FileRef = &fileRef
		}
		out = append(out, part)
	}
	return out
}

func cloneEventToolContent(in []EventToolContent) []EventToolContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]EventToolContent, 0, len(in))
	for _, item := range in {
		cp := item
		if item.OldText != nil {
			value := *item.OldText
			cp.OldText = &value
		}
		out = append(out, cp)
	}
	return out
}

func cloneEventToolLocations(in []EventToolLocation) []EventToolLocation {
	if len(in) == 0 {
		return nil
	}
	out := make([]EventToolLocation, 0, len(in))
	for _, item := range in {
		cp := item
		if item.Line != nil {
			value := *item.Line
			cp.Line = &value
		}
		out = append(out, cp)
	}
	return out
}

func cloneRawMessageMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

func mustRawJSON(value any) json.RawMessage {
	if value == nil {
		value = map[string]any{}
	}
	raw, _ := json.Marshal(value)
	return raw
}
