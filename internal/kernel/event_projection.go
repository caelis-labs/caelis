package kernel

import (
	"encoding/json"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/approval"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func projectSessionEvents(ref session.SessionRef, events []*session.Event) []EventEnvelope {
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

func replayTranscriptEvents(events []*session.Event, includeTransient bool) []*session.Event {
	if includeTransient {
		return events
	}
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if session.IsCanonicalHistoryEvent(event) || session.IsMirror(event) {
			out = append(out, event)
		}
	}
	return out
}

func replayControlPlaneEvents(events []*session.Event, includeTransient bool) []*session.Event {
	if includeTransient {
		return events
	}
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if session.IsCanonicalHistoryEvent(event) {
			out = append(out, event)
		}
	}
	return out
}

func canonicalProtocolPayload(event *session.Event) *session.EventProtocol {
	if event == nil || event.Protocol == nil {
		return nil
	}
	protocol := session.CloneEventProtocol(*event.Protocol)
	return &protocol
}

// ProjectSessionEvent converts one canonical session event into the stable
// gateway event envelope shape used by adapters.
func ProjectSessionEvent(ref session.SessionRef, event *session.Event) (EventEnvelope, bool) {
	projected := projectSessionEvents(ref, []*session.Event{event})
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

func turnIDFromSessionEvent(event *session.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.TurnID)
}

func sessionEventKind(event *session.Event) EventKind {
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		return EventKindUserMessage
	case session.EventTypeAssistant:
		return EventKindAssistantMessage
	case session.EventTypePlan:
		return EventKindPlanUpdate
	case session.EventTypeToolCall:
		return EventKindToolCall
	case session.EventTypeToolResult:
		return EventKindToolResult
	case session.EventTypeParticipant:
		return EventKindParticipant
	case session.EventTypeHandoff:
		return EventKindHandoff
	case session.EventTypeCompact:
		return EventKindCompact
	case session.EventTypeNotice:
		return EventKindNotice
	case session.EventTypeLifecycle:
		return EventKindLifecycle
	case session.EventTypeSystem:
		return EventKindSystemMessage
	default:
		return EventKindNotice
	}
}

func usageSnapshotFromSessionEvent(event *session.Event) *UsageSnapshot {
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
func UsageSnapshotFromSessionEvent(event *session.Event) *UsageSnapshot {
	return usageSnapshotFromSessionEvent(event)
}

// UsageSnapshotFromMap projects one provider-style usage payload into the
// canonical gateway usage contract.
func UsageSnapshotFromMap(payload map[string]any) *UsageSnapshot {
	return usageSnapshotFromPayload(payload)
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
		ReasoningTokens:   reasoningTokensFromPayload(payload),
		TotalTokens:       totalTokens,
	}
	if usage.PromptTokens == 0 && usage.CachedInputTokens == 0 && usage.CompletionTokens == 0 && usage.ReasoningTokens == 0 && usage.TotalTokens == 0 {
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

func reasoningTokensFromPayload(payload map[string]any) int {
	return firstNonZeroInt(
		intValue(payload["reasoning_tokens"]),
		intValue(payload["reasoning_output_tokens"]),
		intValue(payload["thinking_tokens"]),
		intValue(payload["thinking_output_tokens"]),
		intValue(payload["thoughts_token_count"]),
		intValue(payload["thoughtsTokenCount"]),
		intValue(nestedAny(payload, "completion_tokens_details", "reasoning_tokens")),
		intValue(nestedAny(payload, "output_tokens_details", "reasoning_tokens")),
		intValue(nestedAny(payload, "usage_metadata", "thoughts_token_count")),
		intValue(nestedAny(payload, "usageMetadata", "thoughtsTokenCount")),
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

func canonicalOriginFromSessionEvent(ref session.SessionRef, event *session.Event) *EventOrigin {
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

func canonicalOriginFromApproval(req *agent.ApprovalRequest, fallbackRef session.SessionRef, fallbackTurnID string) *EventOrigin {
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

func canonicalScopeID(ref session.SessionRef, scope EventScope, participantID string, participantSessionID string, turnID string) string {
	switch scope {
	case EventScopeParticipant:
		return firstNonEmpty(strings.TrimSpace(turnID), participantSessionID, participantID)
	case EventScopeSubagent:
		return firstNonEmpty(participantSessionID, participantID, strings.TrimSpace(turnID))
	default:
		return firstNonEmpty(strings.TrimSpace(ref.SessionID), strings.TrimSpace(turnID))
	}
}

func participantKindFromSessionEvent(event *session.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(string(event.Scope.Participant.Kind))
}

func sourceFromSessionEvent(event *session.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.Source)
}

func participantSessionIDFromSessionEvent(event *session.Event) string {
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

func canonicalNarrativePayload(event *session.Event) *NarrativePayload {
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
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		payload.Role = NarrativeRoleUser
		payload.Text = strings.TrimSpace(session.EventText(event))
		payload.Final = true
	case session.EventTypeAssistant:
		payload.Role = NarrativeRoleAssistant
		payload.Text = assistantTextFromSessionEvent(event)
		payload.Final = event.Visibility != session.VisibilityUIOnly && !isLiveStreamingNarrativeUpdate(event)
	case session.EventTypeSystem:
		payload.Role = NarrativeRoleSystem
		payload.Text = strings.TrimSpace(session.EventText(event))
		payload.Final = true
	case session.EventTypeNotice:
		payload.Role = NarrativeRoleNotice
		if notice, ok := session.NoticeOf(event); ok {
			payload.Text = strings.TrimSpace(notice.Text)
		}
		if payload.Text == "" {
			payload.Text = strings.TrimSpace(session.EventText(event))
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

func isLiveStreamingNarrativeUpdate(event *session.Event) bool {
	if event == nil || strings.TrimSpace(event.ID) != "" {
		return false
	}
	if !sessionEventFromACP(event) {
		return false
	}
	updateType := strings.TrimSpace(updateTypeFromSessionEvent(event))
	switch updateType {
	case string(session.ProtocolUpdateTypeAgentMessage), string(session.ProtocolUpdateTypeAgentThought):
		return true
	default:
		return false
	}
}

func sessionEventFromACP(event *session.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp") {
		return true
	}
	return event.Scope.Controller.Kind == session.ControllerKindACP
}

func hasNarrativePayloadContent(event *session.Event, payload *NarrativePayload) bool {
	if payload == nil {
		return false
	}
	if strings.TrimSpace(payload.Text) != "" || strings.TrimSpace(payload.ReasoningText) != "" {
		return true
	}
	switch updateTypeFromSessionEvent(event) {
	case string(session.ProtocolUpdateTypeAgentMessage), string(session.ProtocolUpdateTypeAgentThought):
		return payload.Text != "" || payload.ReasoningText != ""
	default:
		return false
	}
}

func canonicalToolCallPayload(event *session.Event) *ToolCallPayload {
	if event == nil || session.EventTypeOf(event) != session.EventTypeToolCall {
		return nil
	}
	update := session.ProtocolUpdateOf(event)
	callID := ""
	toolName := ""
	rawStatus := ""
	if event.Tool != nil {
		callID = strings.TrimSpace(event.Tool.ID)
		toolName = canonicalToolName(event, update)
		rawStatus = strings.TrimSpace(event.Tool.Status)
	} else if update != nil {
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
	if callID == "" && toolName == "" && len(canonicalToolRawInput(event)) == 0 && len(canonicalToolContent(event)) == 0 {
		return nil
	}
	return &ToolCallPayload{
		CallID:        callID,
		ToolName:      toolName,
		ToolKind:      canonicalToolKind(event),
		ToolTitle:     canonicalToolTitle(event),
		RawInput:      canonicalToolRawInput(event),
		Content:       canonicalToolContent(event),
		Status:        canonicalToolCallStatus(rawStatus),
		Actor:         actorIDFromSessionEvent(event),
		Scope:         scopeFromSessionEvent(event),
		ParticipantID: participantIDFromSessionEvent(event),
	}
}

func canonicalToolResultPayload(event *session.Event) *ToolResultPayload {
	if event == nil || session.EventTypeOf(event) != session.EventTypeToolResult {
		return nil
	}
	update := session.ProtocolUpdateOf(event)
	callID := ""
	toolName := ""
	rawStatus := ""
	if event.Tool != nil {
		callID = strings.TrimSpace(event.Tool.ID)
		toolName = canonicalToolName(event, update)
		rawStatus = strings.TrimSpace(event.Tool.Status)
	} else if update != nil {
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
	if callID == "" && toolName == "" && len(canonicalToolRawOutput(event)) == 0 && len(canonicalToolContent(event)) == 0 {
		return nil
	}
	return &ToolResultPayload{
		CallID:        callID,
		ToolName:      toolName,
		ToolKind:      canonicalToolKind(event),
		ToolTitle:     canonicalToolTitle(event),
		RawInput:      canonicalToolRawInput(event),
		RawOutput:     canonicalToolRawOutput(event),
		Content:       canonicalToolContent(event),
		Status:        canonicalToolResultStatus(rawStatus, isErr),
		Error:         isErr,
		Actor:         actorIDFromSessionEvent(event),
		Scope:         scopeFromSessionEvent(event),
		ParticipantID: participantIDFromSessionEvent(event),
	}
}

func canonicalApprovalPayload(req *agent.ApprovalRequest) *ApprovalPayload {
	if req == nil {
		return nil
	}
	return approval.PayloadFromRuntimeRequest(*req)
}

func cloneApprovalPayload(in *ApprovalPayload) *ApprovalPayload {
	return approval.ClonePayload(in)
}

func approvalRawString(raw map[string]any, key string) string {
	if len(raw) == 0 {
		return ""
	}
	value, ok := raw[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func metadataAnyMap(meta map[string]any, key string) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	return anyMapValue(meta[key])
}

func approvalRawMap(raw map[string]any, key string) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	return anyMapValue(raw[key])
}

func anyMapValue(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return maps.Clone(typed)
	}
	return nil
}

func firstNonEmptyMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return maps.Clone(value)
		}
	}
	return nil
}

func canonicalPlanPayload(event *session.Event) *PlanPayload {
	if event == nil || event.Protocol == nil {
		return nil
	}
	entries := []session.ProtocolPlanEntry(nil)
	if update := session.ProtocolUpdateOf(event); update != nil {
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

func canonicalParticipantPayload(event *session.Event) *ParticipantPayload {
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

func canonicalLifecyclePayload(event *session.Event) *LifecyclePayload {
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

func assistantTextFromSessionEvent(event *session.Event) string {
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
	case string(session.ProtocolUpdateTypeAgentThought):
		return ""
	case string(session.ProtocolUpdateTypeAgentMessage):
		return session.EventText(event)
	}
	return strings.TrimSpace(session.EventText(event))
}

func reasoningTextFromSessionEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Message != nil {
		if reasoning := event.Message.ReasoningText(); reasoning != "" {
			return reasoning
		}
	}
	if reasoning := stringFromNestedMap(event.Meta, "caelis", "runtime", "replay", "reasoning_text"); reasoning != "" {
		return reasoning
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if reasoning := reasoningTextFromProtocolContent(update.Content); reasoning != "" {
			return reasoning
		}
	}
	if updateTypeFromSessionEvent(event) == string(session.ProtocolUpdateTypeAgentThought) {
		return session.EventText(event)
	}
	return ""
}

func reasoningTextFromProtocolContent(content any) string {
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
		return reasoningTextFromProtocolContent(decoded)
	case map[string]any:
		for _, key := range []string{"reasoningText", "reasoning_text", "reasoning", "thought"} {
			if text, _ := typed[key].(string); strings.TrimSpace(text) != "" {
				return text
			}
		}
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := reasoningTextFromProtocolContent(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func updateTypeFromSessionEvent(event *session.Event) string {
	if event == nil || event.Protocol == nil {
		return ""
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.SessionUpdate)
	}
	return strings.TrimSpace(event.Protocol.UpdateType)
}

func actorIDFromSessionEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(event.Actor.ID)
}

func actorDisplayFromSessionEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	return firstNonEmpty(strings.TrimSpace(event.Actor.Name), strings.TrimSpace(event.Actor.ID))
}

func participantIDFromSessionEvent(event *session.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.Participant.ID)
}

func scopeFromSessionEvent(event *session.Event) EventScope {
	if event == nil || event.Scope == nil {
		return EventScopeMain
	}
	if strings.TrimSpace(event.Scope.Participant.ID) != "" {
		if event.Scope.Participant.Kind == session.ParticipantKindSubagent {
			return EventScopeSubagent
		}
		return EventScopeParticipant
	}
	return EventScopeMain
}

func canonicalToolKind(event *session.Event) string {
	if event != nil && event.Tool != nil {
		return strings.TrimSpace(event.Tool.Kind)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.Kind)
	}
	if event == nil || event.Protocol == nil || event.Protocol.ToolCall == nil {
		return ""
	}
	return strings.TrimSpace(event.Protocol.ToolCall.Kind)
}

func canonicalToolTitle(event *session.Event) string {
	if event != nil && event.Tool != nil {
		return strings.TrimSpace(event.Tool.Title)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.Title)
	}
	if event == nil || event.Protocol == nil || event.Protocol.ToolCall == nil {
		return ""
	}
	return strings.TrimSpace(event.Protocol.ToolCall.Title)
}

func canonicalToolRawInput(event *session.Event) map[string]any {
	if event != nil && event.Tool != nil {
		if len(event.Tool.Input) > 0 {
			return maps.Clone(event.Tool.Input)
		}
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
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

func canonicalToolRawOutput(event *session.Event) map[string]any {
	if event != nil && event.Tool != nil {
		if len(event.Tool.Output) > 0 {
			return maps.Clone(event.Tool.Output)
		}
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
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

func canonicalToolContent(event *session.Event) []session.ProtocolToolCallContent {
	if event != nil && event.Tool != nil {
		return protocolContentFromEventTool(event.Tool.Content)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if content := session.ProtocolToolCallContentOf(update); len(content) > 0 {
			return content
		}
	}
	if event != nil && event.Protocol != nil && event.Protocol.ToolCall != nil {
		return session.CloneProtocolToolCallContent(event.Protocol.ToolCall.Content)
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
	if event == nil || event.Message == nil {
		return nil
	}
	callID := ""
	toolName := ""
	if event.Tool != nil {
		callID = strings.TrimSpace(event.Tool.ID)
		toolName = strings.TrimSpace(event.Tool.Name)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
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

func canonicalToolName(event *session.Event, update *session.ProtocolUpdate) string {
	if name := stringFromNestedMap(eventMeta(event), "caelis", "runtime", "tool", "name"); name != "" {
		return name
	}
	if event != nil && event.Tool != nil {
		if name := strings.TrimSpace(event.Tool.Name); name != "" {
			return name
		}
	}
	if event != nil && event.Protocol != nil && event.Protocol.ToolCall != nil {
		if name := strings.TrimSpace(event.Protocol.ToolCall.Name); name != "" {
			return name
		}
	}
	if update != nil {
		if kind := strings.TrimSpace(update.Kind); kind != "" {
			return kind
		}
		if title := strings.Fields(strings.TrimSpace(update.Title)); len(title) > 0 {
			return title[0]
		}
	}
	return ""
}

func eventMeta(event *session.Event) map[string]any {
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

func canonicalEventMeta(event *session.Event) map[string]any {
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
