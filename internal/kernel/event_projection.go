package kernel

import (
	"encoding/json"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/approval"
	gatewayapi "github.com/OnslaughtSnail/caelis/ports/gateway"
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
	return session.FilterReplayTranscriptEvents(events, includeTransient)
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

func sessionEventsAfterCursor(events []*session.Event, cursor string) ([]*session.Event, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return events, nil
	}
	for i, event := range events {
		if event == nil {
			continue
		}
		if event.ID == cursor {
			return events[i+1:], nil
		}
	}
	return nil, cursorNotFoundError(cursor)
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
	return 0, cursorNotFoundError(cursor)
}

func cursorNotFoundError(cursor string) error {
	return &Error{
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

// UsageSnapshotFromMap projects one provider-style usage payload into the
// canonical gateway usage contract.
func UsageSnapshotFromMap(payload map[string]any) *UsageSnapshot {
	return gatewayapi.UsageSnapshotFromMap(payload)
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
	toolPayload := session.EventToolProjection(event)
	callID := ""
	toolName := ""
	rawStatus := ""
	if toolPayload != nil {
		callID = strings.TrimSpace(toolPayload.ID)
		toolName = canonicalToolName(event, update)
		rawStatus = strings.TrimSpace(toolPayload.Status)
	} else if update != nil {
		callID = strings.TrimSpace(update.ToolCallID)
		toolName = canonicalToolName(event, update)
		rawStatus = strings.TrimSpace(update.Status)
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
	toolPayload := session.EventToolProjection(event)
	callID := ""
	toolName := ""
	rawStatus := ""
	if toolPayload != nil {
		callID = strings.TrimSpace(toolPayload.ID)
		toolName = canonicalToolName(event, update)
		rawStatus = strings.TrimSpace(toolPayload.Status)
	} else if update != nil {
		callID = strings.TrimSpace(update.ToolCallID)
		toolName = canonicalToolName(event, update)
		rawStatus = strings.TrimSpace(update.Status)
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
	if event == nil {
		return nil
	}
	entries := []session.ProtocolPlanEntry(nil)
	if payload := session.PlanPayloadOf(event); payload != nil {
		for _, entry := range payload.Entries {
			entries = append(entries, session.ProtocolPlanEntry(entry))
		}
	}
	if len(entries) == 0 && event.Protocol != nil {
		if update := session.ProtocolUpdateOf(event); update != nil {
			entries = update.Entries
		}
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
	if event == nil {
		return nil
	}
	participant := session.ProtocolParticipantOf(event)
	if participant == nil {
		return nil
	}
	action := strings.TrimSpace(participant.Action)
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
	if message, ok := session.ModelMessageOf(event); ok {
		if text := message.TextContent(); text != "" {
			return text
		}
		if message.ReasoningText() != "" {
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
	if message, ok := session.ModelMessageOf(event); ok {
		if reasoning := message.ReasoningText(); reasoning != "" {
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
	return session.ProtocolSessionUpdateType(event)
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
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		return strings.TrimSpace(toolPayload.Kind)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.Kind)
	}
	return ""
}

func canonicalToolTitle(event *session.Event) string {
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		return strings.TrimSpace(toolPayload.Title)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.Title)
	}
	return ""
}

func canonicalToolRawInput(event *session.Event) map[string]any {
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		if len(toolPayload.Input) > 0 {
			return maps.Clone(toolPayload.Input)
		}
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if len(update.RawInput) > 0 {
			return maps.Clone(update.RawInput)
		}
	}
	if raw := toolUseRawInputFromMessage(event); len(raw) > 0 {
		return raw
	}
	return nil
}

func canonicalToolRawOutput(event *session.Event) map[string]any {
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		if len(toolPayload.Output) > 0 {
			return maps.Clone(toolPayload.Output)
		}
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if len(update.RawOutput) > 0 {
			return maps.Clone(update.RawOutput)
		}
	}
	return nil
}

func canonicalToolContent(event *session.Event) []session.ProtocolToolCallContent {
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		return protocolContentFromEventTool(toolPayload.Content)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if content := session.ProtocolToolCallContentOf(update); len(content) > 0 {
			return content
		}
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
	if event == nil {
		return nil
	}
	message, ok := session.ModelMessageOf(event)
	if !ok {
		return nil
	}
	callID := ""
	toolName := ""
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		callID = strings.TrimSpace(toolPayload.ID)
		toolName = strings.TrimSpace(toolPayload.Name)
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		callID = strings.TrimSpace(update.ToolCallID)
		toolName = canonicalToolName(event, update)
	}
	for _, call := range message.ToolCalls() {
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
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		if name := strings.TrimSpace(toolPayload.Name); name != "" {
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
	return eventMeta(event)
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
