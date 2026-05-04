package core

import (
	"encoding/json"
	"maps"
	"strings"

	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func projectSessionEvents(ref sdksession.SessionRef, events []*sdksession.Event) []EventEnvelope {
	if len(events) == 0 {
		return nil
	}
	out := make([]EventEnvelope, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		out = append(out, EventEnvelope{
			Cursor: event.ID,
			Event: Event{
				Kind:        sessionEventKind(event),
				TurnID:      turnIDFromSessionEvent(event),
				OccurredAt:  event.Time,
				SessionRef:  ref,
				Origin:      canonicalOriginFromSessionEvent(ref, event),
				Meta:        canonicalEventMeta(event),
				Protocol:    canonicalProtocolPayload(event),
				Usage:       usageSnapshotFromSessionEvent(event),
				Narrative:   canonicalNarrativePayload(event),
				ToolCall:    canonicalToolCallPayload(event),
				ToolResult:  canonicalToolResultPayload(event),
				Plan:        canonicalPlanPayload(event),
				Participant: canonicalParticipantPayload(event),
				Lifecycle:   canonicalLifecyclePayload(event),
			},
		})
	}
	return out
}

func canonicalProtocolPayload(event *sdksession.Event) *sdksession.EventProtocol {
	if event == nil || event.Protocol == nil {
		return nil
	}
	protocol := sdksession.CloneEventProtocol(*event.Protocol)
	return &protocol
}

// ProjectSessionEvent converts one canonical session event into the stable
// gateway event envelope shape used by adapters.
func ProjectSessionEvent(ref sdksession.SessionRef, event *sdksession.Event) (EventEnvelope, bool) {
	projected := projectSessionEvents(ref, []*sdksession.Event{event})
	if len(projected) == 0 {
		return EventEnvelope{}, false
	}
	return projected[0], true
}

func replayAfterCursor(events []EventEnvelope, cursor string, limit int) ([]EventEnvelope, error) {
	if len(events) == 0 {
		return nil, nil
	}
	start, err := startIndexAfterCursor(events, cursor)
	if err != nil {
		return nil, err
	}
	out := events[start:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func startIndexAfterCursor(events []EventEnvelope, cursor string) (int, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return 0, nil
	}
	for i, env := range events {
		if env.Cursor == cursor {
			return i + 1, nil
		}
	}
	return 0, &Error{
		Kind:        KindNotFound,
		Code:        CodeCursorNotFound,
		UserVisible: true,
		Message:     "gateway: cursor not found",
		Detail:      cursor,
	}
}

func turnIDFromSessionEvent(event *sdksession.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.TurnID)
}

func sessionEventKind(event *sdksession.Event) EventKind {
	switch sdksession.EventTypeOf(event) {
	case sdksession.EventTypeUser:
		return EventKindUserMessage
	case sdksession.EventTypeAssistant:
		return EventKindAssistantMessage
	case sdksession.EventTypePlan:
		return EventKindPlanUpdate
	case sdksession.EventTypeToolCall:
		return EventKindToolCall
	case sdksession.EventTypeToolResult:
		return EventKindToolResult
	case sdksession.EventTypeParticipant:
		return EventKindParticipant
	case sdksession.EventTypeHandoff:
		return EventKindHandoff
	case sdksession.EventTypeCompact:
		return EventKindCompact
	case sdksession.EventTypeNotice:
		return EventKindNotice
	case sdksession.EventTypeLifecycle:
		return EventKindLifecycle
	case sdksession.EventTypeSystem:
		return EventKindSystemMessage
	default:
		return EventKindNotice
	}
}

func usageSnapshotFromSessionEvent(event *sdksession.Event) *UsageSnapshot {
	if event == nil || event.Meta == nil {
		return nil
	}
	raw, ok := event.Meta["usage"]
	if ok {
		payload, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		usage := usageSnapshotFromPayload(payload)
		if usage == nil {
			return nil
		}
		return usage
	}
	if raw := nestedAny(event.Meta, "caelis", "sdk", "usage"); raw != nil {
		payload, ok := raw.(map[string]any)
		if ok {
			if usage := usageSnapshotFromPayload(payload); usage != nil {
				return usage
			}
		}
	}
	return usageSnapshotFromPayload(event.Meta)
}

// UsageSnapshotFromSessionEvent projects provider token usage from a durable
// session event into the canonical gateway usage contract.
func UsageSnapshotFromSessionEvent(event *sdksession.Event) *UsageSnapshot {
	return usageSnapshotFromSessionEvent(event)
}

func usageSnapshotFromPayload(payload map[string]any) *UsageSnapshot {
	if payload == nil {
		return nil
	}
	promptTokens := firstNonZeroInt(intValue(payload["prompt_tokens"]), intValue(payload["input_tokens"]))
	completionTokens := firstNonZeroInt(intValue(payload["completion_tokens"]), intValue(payload["output_tokens"]))
	totalTokens := intValue(payload["total_tokens"])
	if totalTokens == 0 && (promptTokens != 0 || completionTokens != 0) {
		totalTokens = promptTokens + completionTokens
	}
	usage := &UsageSnapshot{
		PromptTokens:      promptTokens,
		CachedInputTokens: cachedInputTokensFromPayload(payload),
		CompletionTokens:  completionTokens,
		TotalTokens:       totalTokens,
	}
	if usage.PromptTokens == 0 && usage.CachedInputTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	return usage
}

func cachedInputTokensFromPayload(payload map[string]any) int {
	return firstNonZeroInt(
		intValue(payload["cached_input_tokens"]),
		intValue(payload["cached_prompt_tokens"]),
		intValue(payload["cached_tokens"]),
		intValue(payload["prompt_cache_hit_tokens"]),
		intValue(payload["cache_read_input_tokens"]),
		intValue(nestedAny(payload, "input_tokens_details", "cached_tokens")),
		intValue(nestedAny(payload, "prompt_tokens_details", "cached_tokens")),
	)
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func nestedAny(values map[string]any, path ...string) any {
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

func canonicalOriginFromSessionEvent(ref sdksession.SessionRef, event *sdksession.Event) *EventOrigin {
	scope := scopeFromSessionEvent(event)
	source := sourceFromSessionEvent(event)
	participantID := participantIDFromSessionEvent(event)
	participantKind := participantKindFromSessionEvent(event)
	participantSessionID := participantSessionIDFromSessionEvent(event)
	scopeID := canonicalScopeID(ref, scope, participantID, participantSessionID, turnIDFromSessionEvent(event))
	actor := actorDisplayFromSessionEvent(event)
	if scope == EventScopeMain && scopeID == "" && source == "" && actor == "" && participantID == "" && participantKind == "" && participantSessionID == "" {
		return nil
	}
	return &EventOrigin{
		Scope:                scope,
		ScopeID:              scopeID,
		Source:               source,
		Actor:                actor,
		ParticipantID:        participantID,
		ParticipantKind:      participantKind,
		ParticipantSessionID: participantSessionID,
	}
}

func canonicalOriginFromApproval(req *sdkruntime.ApprovalRequest, fallbackRef sdksession.SessionRef, fallbackTurnID string) *EventOrigin {
	if req == nil {
		return nil
	}
	ref := req.SessionRef
	if strings.TrimSpace(ref.SessionID) == "" {
		ref = fallbackRef
	}
	scope := EventScopeMain
	participantID := metadataString(req.Metadata, "participant_id")
	participantKind := metadataString(req.Metadata, "participant_kind")
	participantSessionID := firstNonEmpty(
		metadataString(req.Metadata, "participant_session_id"),
		metadataString(req.Metadata, "session_id"),
	)
	turnID := firstNonEmpty(strings.TrimSpace(req.TurnID), strings.TrimSpace(fallbackTurnID))
	switch {
	case metadataBool(req.Metadata, "subagent"):
		scope = EventScopeSubagent
	case participantID != "" || participantSessionID != "":
		scope = EventScopeParticipant
	case strings.EqualFold(metadataString(req.Metadata, "scope"), string(EventScopeSubagent)):
		scope = EventScopeSubagent
	case strings.EqualFold(metadataString(req.Metadata, "scope"), string(EventScopeParticipant)):
		scope = EventScopeParticipant
	}
	scopeID := firstNonEmpty(
		metadataString(req.Metadata, "scope_id"),
	)
	if scopeID == "" {
		switch scope {
		case EventScopeSubagent:
			scopeID = firstNonEmpty(metadataString(req.Metadata, "task_id"), participantSessionID, participantID)
		case EventScopeParticipant:
			scopeID = firstNonEmpty(participantSessionID, participantID)
		default:
			scopeID = canonicalScopeID(ref, scope, participantID, participantSessionID, turnID)
		}
	}
	if scope == EventScopeMain && scopeID == "" {
		scopeID = canonicalScopeID(ref, scope, participantID, participantSessionID, turnID)
	}
	return &EventOrigin{
		Scope:                scope,
		ScopeID:              scopeID,
		Source:               metadataString(req.Metadata, "source"),
		Actor:                metadataString(req.Metadata, "agent"),
		ParticipantID:        participantID,
		ParticipantKind:      participantKind,
		ParticipantSessionID: participantSessionID,
	}
}

func canonicalScopeID(ref sdksession.SessionRef, scope EventScope, participantID string, participantSessionID string, turnID string) string {
	switch scope {
	case EventScopeParticipant:
		return firstNonEmpty(strings.TrimSpace(turnID), participantSessionID, participantID)
	case EventScopeSubagent:
		return firstNonEmpty(participantSessionID, participantID, strings.TrimSpace(turnID))
	default:
		return firstNonEmpty(strings.TrimSpace(ref.SessionID), strings.TrimSpace(turnID))
	}
}

func participantKindFromSessionEvent(event *sdksession.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(string(event.Scope.Participant.Kind))
}

func sourceFromSessionEvent(event *sdksession.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.Source)
}

func participantSessionIDFromSessionEvent(event *sdksession.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.ACP.SessionID)
}

func metadataString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	value, ok := meta[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func metadataBool(meta map[string]any, key string) bool {
	if len(meta) == 0 {
		return false
	}
	value, ok := meta[key]
	if !ok {
		return false
	}
	flag, ok := value.(bool)
	return ok && flag
}

func AssistantText(event Event) string {
	if event.Narrative != nil && event.Narrative.Role == NarrativeRoleAssistant {
		return strings.TrimSpace(event.Narrative.Text)
	}
	return ""
}

func PromptTokens(event Event) int {
	if event.Usage == nil {
		return 0
	}
	return event.Usage.PromptTokens
}

func CachedInputTokens(event Event) int {
	if event.Usage == nil {
		return 0
	}
	return event.Usage.CachedInputTokens
}

func canonicalNarrativePayload(event *sdksession.Event) *NarrativePayload {
	if event == nil {
		return nil
	}
	payload := &NarrativePayload{
		Actor:         actorIDFromSessionEvent(event),
		ReasoningText: reasoningTextFromSessionEvent(event),
		Visibility:    string(event.Visibility),
		UpdateType:    updateTypeFromSessionEvent(event),
		Scope:         scopeFromSessionEvent(event),
		ParticipantID: participantIDFromSessionEvent(event),
	}
	switch sdksession.EventTypeOf(event) {
	case sdksession.EventTypeUser:
		payload.Role = NarrativeRoleUser
		payload.Text = strings.TrimSpace(sdksession.EventText(event))
		payload.Final = true
	case sdksession.EventTypeAssistant:
		payload.Role = NarrativeRoleAssistant
		payload.Text = assistantTextFromSessionEvent(event)
		payload.Final = event.Visibility != sdksession.VisibilityUIOnly && !isLiveStreamingNarrativeUpdate(event)
	case sdksession.EventTypeSystem:
		payload.Role = NarrativeRoleSystem
		payload.Text = strings.TrimSpace(sdksession.EventText(event))
		payload.Final = true
	case sdksession.EventTypeNotice:
		payload.Role = NarrativeRoleNotice
		if notice, ok := sdksession.NoticeOf(event); ok {
			payload.Text = strings.TrimSpace(notice.Text)
		}
		if payload.Text == "" {
			payload.Text = strings.TrimSpace(sdksession.EventText(event))
		}
		payload.Final = true
	default:
		return nil
	}
	if !hasNarrativePayloadContent(event, payload) {
		return nil
	}
	return payload
}

func isLiveStreamingNarrativeUpdate(event *sdksession.Event) bool {
	if event == nil || strings.TrimSpace(event.ID) != "" {
		return false
	}
	if !sessionEventFromACP(event) {
		return false
	}
	updateType := strings.TrimSpace(updateTypeFromSessionEvent(event))
	switch updateType {
	case string(sdksession.ProtocolUpdateTypeAgentMessage), string(sdksession.ProtocolUpdateTypeAgentThought):
		return true
	default:
		return false
	}
}

func sessionEventFromACP(event *sdksession.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp") {
		return true
	}
	return event.Scope.Controller.Kind == sdksession.ControllerKindACP
}

func hasNarrativePayloadContent(event *sdksession.Event, payload *NarrativePayload) bool {
	if payload == nil {
		return false
	}
	if strings.TrimSpace(payload.Text) != "" || strings.TrimSpace(payload.ReasoningText) != "" {
		return true
	}
	switch updateTypeFromSessionEvent(event) {
	case string(sdksession.ProtocolUpdateTypeAgentMessage), string(sdksession.ProtocolUpdateTypeAgentThought):
		return payload.Text != "" || payload.ReasoningText != ""
	default:
		return false
	}
}

func canonicalToolCallPayload(event *sdksession.Event) *ToolCallPayload {
	if event == nil || sdksession.EventTypeOf(event) != sdksession.EventTypeToolCall {
		return nil
	}
	update := sdksession.ProtocolUpdateOf(event)
	callID := ""
	toolName := ""
	rawStatus := ""
	if update != nil {
		callID = strings.TrimSpace(update.ToolCallID)
		toolName = canonicalToolName(event, update)
		rawStatus = strings.TrimSpace(update.Status)
	}
	if callID == "" && event.Protocol != nil && event.Protocol.ToolCall != nil {
		callID = strings.TrimSpace(event.Protocol.ToolCall.ID)
	}
	if toolName == "" {
		toolName = canonicalToolName(event, update)
	}
	if callID == "" && toolName == "" && len(canonicalToolRawInput(event)) == 0 {
		return nil
	}
	return &ToolCallPayload{
		CallID:        callID,
		ToolName:      toolName,
		ToolKind:      canonicalToolKind(event),
		ToolTitle:     canonicalToolTitle(event),
		RawInput:      canonicalToolRawInput(event),
		Status:        canonicalToolCallStatus(rawStatus),
		Actor:         actorIDFromSessionEvent(event),
		Scope:         scopeFromSessionEvent(event),
		ParticipantID: participantIDFromSessionEvent(event),
	}
}

func canonicalToolResultPayload(event *sdksession.Event) *ToolResultPayload {
	if event == nil || sdksession.EventTypeOf(event) != sdksession.EventTypeToolResult {
		return nil
	}
	update := sdksession.ProtocolUpdateOf(event)
	callID := ""
	toolName := ""
	rawStatus := ""
	if update != nil {
		callID = strings.TrimSpace(update.ToolCallID)
		toolName = canonicalToolName(event, update)
		rawStatus = strings.TrimSpace(update.Status)
	}
	if callID == "" && event.Protocol != nil && event.Protocol.ToolCall != nil {
		callID = strings.TrimSpace(event.Protocol.ToolCall.ID)
	}
	if toolName == "" {
		toolName = canonicalToolName(event, update)
	}
	isErr := strings.EqualFold(rawStatus, "error") || strings.EqualFold(rawStatus, "failed")
	if callID == "" && toolName == "" && len(canonicalToolRawOutput(event)) == 0 {
		return nil
	}
	return &ToolResultPayload{
		CallID:        callID,
		ToolName:      toolName,
		ToolKind:      canonicalToolKind(event),
		ToolTitle:     canonicalToolTitle(event),
		RawInput:      canonicalToolRawInput(event),
		RawOutput:     canonicalToolRawOutput(event),
		Status:        canonicalToolResultStatus(rawStatus, isErr),
		Error:         isErr,
		Actor:         actorIDFromSessionEvent(event),
		Scope:         scopeFromSessionEvent(event),
		ParticipantID: participantIDFromSessionEvent(event),
	}
}

func canonicalApprovalPayload(req *sdkruntime.ApprovalRequest) *ApprovalPayload {
	if req == nil {
		return nil
	}
	payload := &ApprovalPayload{
		ToolName: strings.TrimSpace(req.Tool.Name),
		Status:   ApprovalStatusPending,
	}
	if payload.ToolName == "" {
		payload.ToolName = strings.TrimSpace(req.Call.Name)
	}
	if req.Approval != nil {
		if toolName := strings.TrimSpace(req.Approval.ToolCall.Name); toolName != "" {
			payload.ToolName = toolName
		}
		payload.RawInput = maps.Clone(req.Approval.ToolCall.RawInput)
		if len(req.Approval.Options) > 0 {
			payload.Options = make([]ApprovalOption, 0, len(req.Approval.Options))
			for _, option := range req.Approval.Options {
				payload.Options = append(payload.Options, ApprovalOption{
					ID:   strings.TrimSpace(option.ID),
					Name: strings.TrimSpace(option.Name),
					Kind: strings.TrimSpace(option.Kind),
				})
			}
		}
	}
	if len(payload.RawInput) == 0 {
		payload.RawInput = rawInputFromJSONString(string(req.Call.Input))
	}
	if payload.ToolName == "" && len(payload.RawInput) == 0 && len(payload.Options) == 0 {
		return nil
	}
	return payload
}

func canonicalPlanPayload(event *sdksession.Event) *PlanPayload {
	if event == nil || event.Protocol == nil {
		return nil
	}
	entries := []sdksession.ProtocolPlanEntry(nil)
	if update := sdksession.ProtocolUpdateOf(event); update != nil {
		entries = update.Entries
	}
	if len(entries) == 0 && event.Protocol.Plan != nil {
		entries = event.Protocol.Plan.Entries
	}
	if len(entries) == 0 {
		return nil
	}
	payload := &PlanPayload{Entries: make([]PlanEntryPayload, 0, len(entries))}
	for _, entry := range entries {
		content := strings.TrimSpace(entry.Content)
		status := strings.TrimSpace(entry.Status)
		priority := strings.TrimSpace(entry.Priority)
		if content == "" && status == "" && priority == "" {
			continue
		}
		payload.Entries = append(payload.Entries, PlanEntryPayload{
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

func canonicalParticipantPayload(event *sdksession.Event) *ParticipantPayload {
	if event == nil || event.Protocol == nil || event.Protocol.Participant == nil {
		return nil
	}
	action := strings.TrimSpace(event.Protocol.Participant.Action)
	if action == "" && (event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == "") {
		return nil
	}
	payload := &ParticipantPayload{
		ParticipantID: participantIDFromSessionEvent(event),
		Action:        ParticipantAction(strings.ToLower(action)),
		Actor:         actorIDFromSessionEvent(event),
		Scope:         scopeFromSessionEvent(event),
	}
	if event.Scope != nil {
		payload.ParticipantKind = strings.TrimSpace(string(event.Scope.Participant.Kind))
		payload.Role = strings.TrimSpace(string(event.Scope.Participant.Role))
		payload.DelegationID = strings.TrimSpace(event.Scope.Participant.DelegationID)
		payload.ParentTurnID = strings.TrimSpace(event.Scope.TurnID)
		payload.SessionID = strings.TrimSpace(event.Scope.ACP.SessionID)
	}
	return payload
}

func canonicalLifecyclePayload(event *sdksession.Event) *LifecyclePayload {
	if event == nil || event.Lifecycle == nil {
		return nil
	}
	status := strings.TrimSpace(event.Lifecycle.Status)
	reason := strings.TrimSpace(event.Lifecycle.Reason)
	if status == "" && reason == "" {
		return nil
	}
	return &LifecyclePayload{
		Status:        canonicalLifecycleStatus(status),
		Reason:        reason,
		Actor:         actorIDFromSessionEvent(event),
		Scope:         scopeFromSessionEvent(event),
		ParticipantID: participantIDFromSessionEvent(event),
	}
}

func assistantTextFromSessionEvent(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	if event.Message != nil {
		if text := event.Message.TextContent(); text != "" {
			return text
		}
		if event.Message.ReasoningText() != "" {
			return ""
		}
	}
	switch updateTypeFromSessionEvent(event) {
	case string(sdksession.ProtocolUpdateTypeAgentThought):
		return ""
	case string(sdksession.ProtocolUpdateTypeAgentMessage):
		return sdksession.EventText(event)
	}
	return strings.TrimSpace(sdksession.EventText(event))
}

func reasoningTextFromSessionEvent(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	if event.Message != nil {
		if reasoning := event.Message.ReasoningText(); reasoning != "" {
			return reasoning
		}
	}
	if updateTypeFromSessionEvent(event) == string(sdksession.ProtocolUpdateTypeAgentThought) {
		return sdksession.EventText(event)
	}
	return ""
}

func updateTypeFromSessionEvent(event *sdksession.Event) string {
	if event == nil || event.Protocol == nil {
		return ""
	}
	if update := sdksession.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.SessionUpdate)
	}
	return strings.TrimSpace(event.Protocol.UpdateType)
}

func actorIDFromSessionEvent(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(event.Actor.ID)
}

func actorDisplayFromSessionEvent(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	return firstNonEmpty(strings.TrimSpace(event.Actor.Name), strings.TrimSpace(event.Actor.ID))
}

func participantIDFromSessionEvent(event *sdksession.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.Participant.ID)
}

func scopeFromSessionEvent(event *sdksession.Event) EventScope {
	if event == nil || event.Scope == nil {
		return EventScopeMain
	}
	if strings.TrimSpace(event.Scope.Participant.ID) != "" {
		if event.Scope.Participant.Kind == sdksession.ParticipantKindSubagent {
			return EventScopeSubagent
		}
		return EventScopeParticipant
	}
	return EventScopeMain
}

func canonicalToolKind(event *sdksession.Event) string {
	if update := sdksession.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.Kind)
	}
	if event == nil || event.Protocol == nil || event.Protocol.ToolCall == nil {
		return ""
	}
	return strings.TrimSpace(event.Protocol.ToolCall.Kind)
}

func canonicalToolTitle(event *sdksession.Event) string {
	if update := sdksession.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.Title)
	}
	if event == nil || event.Protocol == nil || event.Protocol.ToolCall == nil {
		return ""
	}
	return strings.TrimSpace(event.Protocol.ToolCall.Title)
}

func canonicalToolRawInput(event *sdksession.Event) map[string]any {
	if update := sdksession.ProtocolUpdateOf(event); update != nil {
		if len(update.RawInput) > 0 {
			return maps.Clone(update.RawInput)
		}
	}
	if event != nil && event.Protocol != nil && event.Protocol.ToolCall != nil {
		if len(event.Protocol.ToolCall.RawInput) > 0 {
			return maps.Clone(event.Protocol.ToolCall.RawInput)
		}
	}
	if raw := toolUseRawInputFromMessage(event); len(raw) > 0 {
		return raw
	}
	return nil
}

func canonicalToolRawOutput(event *sdksession.Event) map[string]any {
	if update := sdksession.ProtocolUpdateOf(event); update != nil {
		if len(update.RawOutput) > 0 {
			return maps.Clone(update.RawOutput)
		}
	}
	if event != nil && event.Protocol != nil && event.Protocol.ToolCall != nil {
		if len(event.Protocol.ToolCall.RawOutput) > 0 {
			return maps.Clone(event.Protocol.ToolCall.RawOutput)
		}
	}
	return nil
}

func toolUseRawInputFromMessage(event *sdksession.Event) map[string]any {
	if event == nil || event.Message == nil {
		return nil
	}
	callID := ""
	toolName := ""
	if update := sdksession.ProtocolUpdateOf(event); update != nil {
		callID = strings.TrimSpace(update.ToolCallID)
		toolName = canonicalToolName(event, update)
	}
	if event.Protocol != nil && event.Protocol.ToolCall != nil {
		callID = firstNonEmpty(callID, strings.TrimSpace(event.Protocol.ToolCall.ID))
		toolName = firstNonEmpty(toolName, strings.TrimSpace(event.Protocol.ToolCall.Name))
	}
	for _, call := range event.Message.ToolCalls() {
		if callID != "" && strings.TrimSpace(call.ID) != callID {
			continue
		}
		if toolName != "" && !strings.EqualFold(strings.TrimSpace(call.Name), toolName) {
			continue
		}
		raw := rawInputFromJSONString(call.Args)
		if len(raw) > 0 {
			return raw
		}
	}
	return nil
}

func canonicalToolName(event *sdksession.Event, update *sdksession.ProtocolUpdate) string {
	if name := stringFromNestedMap(eventMeta(event), "caelis", "runtime", "tool", "name"); name != "" {
		return name
	}
	if event != nil && event.Protocol != nil && event.Protocol.ToolCall != nil {
		if name := strings.TrimSpace(event.Protocol.ToolCall.Name); name != "" {
			return name
		}
	}
	if update != nil {
		if title := strings.Fields(strings.TrimSpace(update.Title)); len(title) > 0 {
			return title[0]
		}
		return strings.TrimSpace(update.Kind)
	}
	return ""
}

func eventMeta(event *sdksession.Event) map[string]any {
	if event == nil {
		return nil
	}
	return event.Meta
}

func stringFromNestedMap(values map[string]any, path ...string) string {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapped[key]
	}
	text, _ := current.(string)
	return strings.TrimSpace(text)
}

func canonicalEventMeta(event *sdksession.Event) map[string]any {
	if event == nil {
		return nil
	}
	if len(event.Meta) == 0 {
		return nil
	}
	return maps.Clone(event.Meta)
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

func canonicalLifecycleStatus(status string) LifecycleStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running":
		return LifecycleStatusRunning
	case "waiting_approval":
		return LifecycleStatusWaitingApproval
	case "interrupted":
		return LifecycleStatusInterrupted
	case "failed":
		return LifecycleStatusFailed
	case "completed":
		return LifecycleStatusCompleted
	default:
		return LifecycleStatus(strings.TrimSpace(status))
	}
}

func intValue(v any) int {
	switch value := v.(type) {
	case int:
		return value
	case int8:
		return int(value)
	case int16:
		return int(value)
	case int32:
		return int(value)
	case int64:
		return int(value)
	case uint:
		return int(value)
	case uint8:
		return int(value)
	case uint16:
		return int(value)
	case uint32:
		return int(value)
	case uint64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	default:
		return 0
	}
}
