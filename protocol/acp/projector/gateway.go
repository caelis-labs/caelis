package projector

import (
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

// ACPEventsFromGatewayHandle returns the ACP-native event stream for one
// gateway turn handle.
func ACPEventsFromGatewayHandle(handle gateway.TurnHandle) <-chan eventstream.Envelope {
	if handle == nil {
		return eventstream.EnsureTerminalLifecycle(nil, "", "", "")
	}
	return eventstream.EnsureTerminalLifecycle(handle.ACPEvents(), handle.HandleID(), handle.RunID(), handle.TurnID())
}

// ProjectGatewayEventEnvelope projects the gateway runtime event envelope into the
// surface-facing ACP event stream. It is the compatibility bridge for current
// runtime events while surfaces migrate away from consuming kernel.Event
// directly.
func ProjectGatewayEventEnvelope(env gateway.EventEnvelope) []eventstream.Envelope {
	if env.Err != nil {
		return []eventstream.Envelope{eventstream.Error(env.Err)}
	}
	base := acpEventBase(env)
	out := make([]eventstream.Envelope, 0, 3)
	out = append(out, projectGatewayStandardACPEvents(base, env.Event)...)
	if len(out) == 0 {
		out = append(out, projectGatewayEventstreamOnlyEvents(base, env.Event)...)
	}
	if env.Event.Usage != nil {
		out = append(out, gatewayUsageEnvelope(base, env.Event.Usage))
	}
	return out
}

func projectGatewayStandardACPEvents(base eventstream.Envelope, ev gateway.Event) []eventstream.Envelope {
	sessionEvent, ok := sessionEventFromGatewayEvent(base, ev)
	if !ok {
		return nil
	}
	return projectSessionEventToACPEnvelopes(base, ev, sessionEvent, EventProjector{})
}

// ProjectSessionEventEnvelope projects one canonical session event into
// ACP-native client envelopes using the supplied envelope metadata as the
// transport context.
func ProjectSessionEventEnvelope(base eventstream.Envelope, event *session.Event) []eventstream.Envelope {
	return ProjectSessionEventEnvelopeWithProjector(base, event, EventProjector{})
}

// ProjectSessionEventEnvelopeWithProjector projects one canonical session event
// with a caller-supplied ACP projector, then appends standard usage_update when
// provider usage is attached to the source event.
func ProjectSessionEventEnvelopeWithProjector(base eventstream.Envelope, event *session.Event, projector Projector) []eventstream.Envelope {
	if projector == nil {
		projector = EventProjector{}
	}
	out := projectSessionEventToACPEnvelopes(base, gateway.Event{}, event, projector)
	if len(out) == 0 {
		out = append(out, projectSessionEventstreamOnlyEvents(base, event)...)
	}
	if usage := gateway.UsageSnapshotFromSessionEvent(event); usage != nil && !containsUsageUpdate(out) {
		out = append(out, gatewayUsageEnvelope(base, usage))
	}
	return out
}

// ProjectSessionEventNotifications projects one canonical session event into
// ACP session/update notifications. Eventstream-only extensions and historical
// request_permission prompts are intentionally not replayed through session/load.
func ProjectSessionEventNotifications(base eventstream.Envelope, event *session.Event, projector Projector) ([]SessionNotification, error) {
	if projector == nil {
		projector = EventProjector{}
	}
	notifications, err := projector.ProjectNotifications(event)
	if err != nil {
		return nil, err
	}
	out := cloneSessionNotifications(notifications, base, event)
	if usage := gateway.UsageSnapshotFromSessionEvent(event); usage != nil && !containsUsageNotification(out) {
		usageEnv := gatewayUsageEnvelope(base, usage)
		out = append(out, SessionNotification{
			SessionID: sessionNotificationID(usageEnv.SessionID, base, event),
			Update:    eventstream.CloneUpdate(usageEnv.Update),
		})
	}
	return out, nil
}

func cloneSessionNotifications(notifications []SessionNotification, base eventstream.Envelope, event *session.Event) []SessionNotification {
	out := make([]SessionNotification, 0, len(notifications))
	for _, notification := range notifications {
		if notification.Update == nil {
			continue
		}
		out = append(out, SessionNotification{
			SessionID: sessionNotificationID(notification.SessionID, base, event),
			Update:    eventstream.CloneUpdate(notification.Update),
		})
	}
	return out
}

func sessionNotificationID(candidate string, base eventstream.Envelope, event *session.Event) string {
	if sessionID := firstNonEmpty(candidate, base.SessionID); sessionID != "" {
		return sessionID
	}
	if event == nil {
		return ""
	}
	return strings.TrimSpace(event.SessionID)
}

func projectSessionEventToACPEnvelopes(base eventstream.Envelope, ev gateway.Event, sessionEvent *session.Event, projector Projector) []eventstream.Envelope {
	out := make([]eventstream.Envelope, 0, 2)
	if permission, ok, err := projector.ProjectPermissionRequest(sessionEvent); err != nil {
		return []eventstream.Envelope{eventstream.Error(err)}
	} else if ok && permission != nil {
		next := base
		next.Kind = eventstream.KindRequestPermission
		permission.Meta = mergeACPEventMeta(permission.Meta, base.Meta)
		if meta := gatewayPermissionToolMeta(base.Meta, ev, sessionEvent); len(meta) > 0 {
			permission.ToolCall.Meta = mergeACPEventMeta(permission.ToolCall.Meta, meta)
		}
		next.Permission = permission
		out = append(out, next)
	}
	updates, err := projector.ProjectEvent(sessionEvent)
	if err != nil {
		return []eventstream.Envelope{eventstream.Error(err)}
	}
	var updateMeta map[string]any
	if update := session.ProtocolUpdateOf(sessionEvent); update != nil {
		updateMeta = update.Meta
	}
	for _, update := range updates {
		if update == nil {
			continue
		}
		next := base
		next.Kind = eventstream.KindSessionUpdate
		next.Update = gatewayCompatibleUpdateContent(update, ev)
		if len(updateMeta) > 0 {
			next.Meta = mergeACPEventMeta(updateMeta, next.Meta)
		}
		out = append(out, next)
	}
	return out
}

func gatewayUsageEnvelope(base eventstream.Envelope, usage *gateway.UsageSnapshot) eventstream.Envelope {
	if usage == nil {
		return eventstream.Envelope{}
	}
	return eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		Cursor:        base.Cursor,
		EventID:       base.EventID,
		ProjectionID:  base.ProjectionID,
		SessionID:     base.SessionID,
		HandleID:      base.HandleID,
		RunID:         base.RunID,
		TurnID:        base.TurnID,
		OccurredAt:    base.OccurredAt,
		Scope:         base.Scope,
		ScopeID:       base.ScopeID,
		Actor:         base.Actor,
		ParticipantID: base.ParticipantID,
		Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{
			PromptTokens:      usage.PromptTokens,
			CachedInputTokens: usage.CachedInputTokens,
			CompletionTokens:  usage.CompletionTokens,
			ReasoningTokens:   usage.ReasoningTokens,
			TotalTokens:       usage.TotalTokens,
		}, base.Meta),
	}
}

func containsUsageUpdate(events []eventstream.Envelope) bool {
	for _, env := range events {
		if eventstream.UpdateType(env.Update) == schema.UpdateUsage {
			return true
		}
	}
	return false
}

func containsUsageNotification(notifications []SessionNotification) bool {
	for _, notification := range notifications {
		if eventstream.UpdateType(notification.Update) == schema.UpdateUsage {
			return true
		}
	}
	return false
}

func sessionEventFromGatewayEvent(base eventstream.Envelope, ev gateway.Event) (*session.Event, bool) {
	out := &session.Event{
		SessionID:  strings.TrimSpace(base.SessionID),
		Type:       sessionTypeFromEventKind(ev.Kind),
		Time:       ev.OccurredAt,
		Visibility: session.VisibilityCanonical,
		Meta:       cloneAnyMap(ev.Meta),
	}
	if ev.Protocol != nil {
		protocol := session.CloneEventProtocol(*ev.Protocol)
		out.Protocol = &protocol
	}
	if ev.Narrative != nil {
		out.Text = ev.Narrative.Text
		out.Type = sessionTypeFromNarrativeRole(ev.Narrative.Role, out.Type)
		if message, ok := gatewayNarrativeMessage(ev.Narrative); ok {
			out.Message = &message
		}
	}
	if ev.ToolCall != nil {
		out.Type = session.EventTypeToolCall
		if out.Protocol == nil {
			out.Protocol = gatewayToolCallProtocol(ev)
		}
	}
	if ev.ToolResult != nil {
		out.Type = session.EventTypeToolResult
		if out.Protocol == nil {
			out.Protocol = gatewayToolResultProtocol(ev)
		}
	}
	if ev.Plan != nil {
		out.Type = session.EventTypePlan
		if out.Protocol == nil {
			out.Protocol = gatewayPlanProtocol(ev.Plan)
		}
	}
	if ev.ApprovalPayload != nil && ev.Kind == gateway.EventKindApprovalRequested {
		out.Type = session.EventTypeLifecycle
		out.Protocol = gatewayApprovalProtocol(ev.ApprovalPayload)
	}
	if out.Protocol == nil && out.Message == nil {
		return nil, false
	}
	return out, true
}

func sessionTypeFromNarrativeRole(role gateway.NarrativeRole, fallback session.EventType) session.EventType {
	switch role {
	case gateway.NarrativeRoleUser:
		return session.EventTypeUser
	case gateway.NarrativeRoleAssistant:
		return session.EventTypeAssistant
	case gateway.NarrativeRoleSystem:
		return session.EventTypeSystem
	case gateway.NarrativeRoleNotice:
		return session.EventTypeNotice
	default:
		return fallback
	}
}

func gatewayNarrativeMessage(narrative *gateway.NarrativePayload) (model.Message, bool) {
	if narrative == nil {
		return model.Message{}, false
	}
	switch narrative.Role {
	case gateway.NarrativeRoleUser:
		text := strings.TrimSpace(narrative.Text)
		if text == "" {
			return model.Message{}, false
		}
		return model.NewTextMessage(model.RoleUser, text), true
	case gateway.NarrativeRoleAssistant:
		parts := make([]model.Part, 0, 2)
		if narrative.ReasoningText != "" {
			parts = append(parts, model.NewReasoningPart(narrative.ReasoningText, model.ReasoningVisibilityVisible))
		}
		if narrative.Text != "" {
			parts = append(parts, model.NewTextPart(narrative.Text))
		}
		if len(parts) == 0 {
			return model.Message{}, false
		}
		return model.NewMessage(model.RoleAssistant, parts...), true
	default:
		return model.Message{}, false
	}
}

func gatewayToolCallProtocol(ev gateway.Event) *session.EventProtocol {
	payload := ev.ToolCall
	if payload == nil {
		return nil
	}
	toolName := strings.TrimSpace(payload.ToolName)
	rawInput := cloneAnyMap(payload.RawInput)
	update := &session.ProtocolUpdate{
		SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
		ToolCallID:    strings.TrimSpace(payload.CallID),
		Title:         firstNonEmpty(payload.ToolTitle, payload.ToolName, payload.ToolKind),
		Kind:          firstNonEmpty(payload.ToolKind, payload.ToolName),
		Status:        string(payload.Status),
		RawInput:      rawInput,
		Content:       session.CloneProtocolToolCallContent(payload.Content),
		Meta:          acpMetaWithToolName(ev.Meta, toolName),
	}
	return &session.EventProtocol{
		Method: session.ProtocolMethodSessionUpdate,
		Update: update,
	}
}

func gatewayToolResultProtocol(ev gateway.Event) *session.EventProtocol {
	payload := ev.ToolResult
	if payload == nil {
		return nil
	}
	toolName := strings.TrimSpace(payload.ToolName)
	update := &session.ProtocolUpdate{
		SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
		ToolCallID:    strings.TrimSpace(payload.CallID),
		Title:         firstNonEmpty(payload.ToolTitle, payload.ToolName, payload.ToolKind),
		Kind:          firstNonEmpty(payload.ToolKind, payload.ToolName),
		Status:        string(payload.Status),
		RawInput:      cloneAnyMap(payload.RawInput),
		RawOutput:     cloneAnyMap(payload.RawOutput),
		Content:       session.CloneProtocolToolCallContent(payload.Content),
		Meta:          acpMetaWithToolName(ev.Meta, toolName),
	}
	return &session.EventProtocol{
		Method: session.ProtocolMethodSessionUpdate,
		Update: update,
	}
}

func gatewayPlanProtocol(plan *gateway.PlanPayload) *session.EventProtocol {
	if plan == nil || len(plan.Entries) == 0 {
		return nil
	}
	entries := make([]session.ProtocolPlanEntry, 0, len(plan.Entries))
	for _, item := range plan.Entries {
		entries = append(entries, session.ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: firstNonEmpty(strings.TrimSpace(item.Priority), "medium"),
		})
	}
	return &session.EventProtocol{
		Method: session.ProtocolMethodSessionUpdate,
		Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypePlan),
			Entries:       entries,
		},
	}
}

func gatewayApprovalProtocol(payload *gateway.ApprovalPayload) *session.EventProtocol {
	if payload == nil {
		return nil
	}
	toolName := strings.TrimSpace(payload.ToolName)
	rawInput := acpApprovalRawInput(payload)
	approval := session.ProtocolApproval{
		ToolCall: session.ProtocolToolCall{
			ID:       strings.TrimSpace(payload.ToolCallID),
			Name:     toolName,
			Kind:     toolName,
			Title:    toolName,
			Status:   string(payload.Status),
			RawInput: rawInput,
		},
		Options: gatewayApprovalOptions(payload.Options),
	}
	return &session.EventProtocol{
		Method:     session.ProtocolMethodRequestPermission,
		Permission: &approval,
	}
}

func gatewayApprovalOptions(in []gateway.ApprovalOption) []session.ProtocolApprovalOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.ProtocolApprovalOption, 0, len(in))
	for _, item := range in {
		out = append(out, session.ProtocolApprovalOption{
			ID:   strings.TrimSpace(item.ID),
			Name: strings.TrimSpace(item.Name),
			Kind: strings.TrimSpace(item.Kind),
		})
	}
	return out
}

func gatewayCompatibleUpdateContent(update schema.Update, ev gateway.Event) schema.Update {
	content := gatewayCompatibleToolContent(ev)
	if len(content) == 0 {
		return update
	}
	switch typed := update.(type) {
	case schema.ToolCall:
		typed.Content = content
		return typed
	case schema.ToolCallUpdate:
		typed.Content = content
		return typed
	default:
		return update
	}
}

func gatewayCompatibleToolContent(ev gateway.Event) []schema.ToolCallContent {
	switch {
	case ev.ToolCall != nil && len(ev.ToolCall.Content) > 0:
		return acpToolCallContent(ev.ToolCall.Content)
	case ev.ToolResult != nil && len(ev.ToolResult.Content) > 0:
		return acpToolCallContent(ev.ToolResult.Content)
	default:
		return nil
	}
}

func acpEventBase(env gateway.EventEnvelope) eventstream.Envelope {
	ev := env.Event
	scope := acpEventScope(ev)
	scopeID := acpEventScopeID(ev)
	return eventstream.Envelope{
		Cursor:        strings.TrimSpace(env.Cursor),
		SessionID:     strings.TrimSpace(ev.SessionRef.SessionID),
		HandleID:      strings.TrimSpace(ev.HandleID),
		RunID:         strings.TrimSpace(ev.RunID),
		TurnID:        strings.TrimSpace(ev.TurnID),
		OccurredAt:    ev.OccurredAt,
		Scope:         scope,
		ScopeID:       scopeID,
		Actor:         acpEventActor(ev, gatewayEventActorFallback(ev)),
		ParticipantID: acpEventParticipantID(ev),
		Final:         acpEventFinal(ev),
		Meta:          acpEventMeta(ev),
	}
}

func acpEventMeta(ev gateway.Event) map[string]any {
	meta := gatewayProjectionBridgeMeta(ev.Meta)
	if ev.Invocation == nil {
		return meta
	}
	invocation := session.CloneEventInvocation(*ev.Invocation)
	if invocation.Provider == "" && invocation.Model == "" {
		return meta
	}
	if meta == nil {
		meta = map[string]any{}
	}
	caelis, _ := meta["caelis"].(map[string]any)
	if caelis == nil {
		caelis = map[string]any{}
	} else {
		caelis = cloneAnyMap(caelis)
	}
	caelis["invocation"] = map[string]any{
		"provider": invocation.Provider,
		"model":    invocation.Model,
	}
	meta["caelis"] = caelis
	return meta
}

func gatewayProjectionBridgeMeta(meta map[string]any) map[string]any {
	return metautil.Merge(meta, map[string]any{
		"caelis": map[string]any{
			"bridge": map[string]any{"source": "gateway_projection"},
		},
	})
}

func sessionTypeFromEventKind(kind gateway.EventKind) session.EventType {
	switch kind {
	case gateway.EventKindUserMessage:
		return session.EventTypeUser
	case gateway.EventKindAssistantMessage:
		return session.EventTypeAssistant
	case gateway.EventKindPlanUpdate:
		return session.EventTypePlan
	case gateway.EventKindToolCall:
		return session.EventTypeToolCall
	case gateway.EventKindToolResult:
		return session.EventTypeToolResult
	case gateway.EventKindParticipant:
		return session.EventTypeParticipant
	case gateway.EventKindHandoff:
		return session.EventTypeHandoff
	case gateway.EventKindCompact:
		return session.EventTypeCompact
	case gateway.EventKindNotice:
		return session.EventTypeNotice
	case gateway.EventKindLifecycle, gateway.EventKindApprovalRequested, gateway.EventKindApprovalReview:
		return session.EventTypeLifecycle
	case gateway.EventKindSystemMessage:
		return session.EventTypeSystem
	default:
		return session.EventTypeCustom
	}
}

func projectGatewayEventstreamOnlyEvents(base eventstream.Envelope, ev gateway.Event) []eventstream.Envelope {
	switch ev.Kind {
	case gateway.EventKindApprovalReview:
		if ev.ApprovalPayload == nil || !strings.EqualFold(strings.TrimSpace(ev.ApprovalPayload.DecisionSource), string(gateway.ApprovalModeAutoReview)) {
			return nil
		}
		next := base
		next.Kind = eventstream.KindApprovalReview
		next.ApprovalReview = &eventstream.ApprovalReview{
			ToolCallID:    strings.TrimSpace(ev.ApprovalPayload.ToolCallID),
			ToolName:      strings.TrimSpace(ev.ApprovalPayload.ToolName),
			RawInput:      maps.Clone(ev.ApprovalPayload.RawInput),
			Status:        strings.TrimSpace(string(ev.ApprovalPayload.ReviewStatus)),
			Text:          strings.TrimSpace(ev.ApprovalPayload.ReviewText),
			Risk:          strings.TrimSpace(ev.ApprovalPayload.Risk),
			Authorization: strings.TrimSpace(ev.ApprovalPayload.Authorization),
		}
		return []eventstream.Envelope{next}
	case gateway.EventKindParticipant:
		if ev.Participant == nil {
			return nil
		}
		return participantEventstreamEnvelope(base, strings.TrimSpace(string(ev.Participant.Action)), acpEventActor(ev, ev.Participant.Actor))
	case gateway.EventKindLifecycle:
		if ev.Lifecycle == nil {
			return nil
		}
		return lifecycleEventstreamEnvelope(base, string(ev.Lifecycle.Status), ev.Lifecycle.Reason, acpEventActor(ev, ev.Lifecycle.Actor))
	case gateway.EventKindNotice, gateway.EventKindSystemMessage:
		text := ""
		if ev.Narrative != nil {
			text = ev.Narrative.Text
		}
		if strings.TrimSpace(text) == "" {
			return nil
		}
		next := base
		next.Kind = eventstream.KindNotice
		next.Notice = strings.TrimSpace(text)
		return []eventstream.Envelope{next}
	default:
		return nil
	}
}

func projectSessionEventstreamOnlyEvents(base eventstream.Envelope, event *session.Event) []eventstream.Envelope {
	if event == nil {
		return nil
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeParticipant:
		participant := session.ProtocolParticipantOf(event)
		if participant == nil {
			return nil
		}
		return participantEventstreamEnvelope(base, participant.Action, firstNonEmpty(strings.TrimSpace(base.Actor), strings.TrimSpace(event.Actor.Name), strings.TrimSpace(event.Actor.ID)))
	case session.EventTypeHandoff:
		handoff := session.ProtocolHandoffOf(event)
		if handoff == nil {
			return nil
		}
		return lifecycleEventstreamEnvelope(base, handoff.Phase, "", firstNonEmpty(strings.TrimSpace(base.Actor), strings.TrimSpace(event.Actor.Name), strings.TrimSpace(event.Actor.ID)))
	case session.EventTypeLifecycle:
		if event.Lifecycle == nil {
			return nil
		}
		return lifecycleEventstreamEnvelope(base, event.Lifecycle.Status, event.Lifecycle.Reason, firstNonEmpty(strings.TrimSpace(base.Actor), strings.TrimSpace(event.Actor.Name), strings.TrimSpace(event.Actor.ID)))
	default:
		return nil
	}
}

func participantEventstreamEnvelope(base eventstream.Envelope, state string, actor string) []eventstream.Envelope {
	state = strings.TrimSpace(state)
	if state == "" {
		return nil
	}
	next := base
	next.Kind = eventstream.KindParticipant
	next.Actor = strings.TrimSpace(actor)
	next.Participant = &eventstream.Participant{State: state}
	return []eventstream.Envelope{next}
}

func lifecycleEventstreamEnvelope(base eventstream.Envelope, state string, reason string, actor string) []eventstream.Envelope {
	state = strings.TrimSpace(state)
	reason = strings.TrimSpace(reason)
	if state == "" && reason == "" {
		return nil
	}
	next := base
	next.Kind = eventstream.KindLifecycle
	next.Actor = strings.TrimSpace(actor)
	next.Lifecycle = &eventstream.Lifecycle{
		State:  strings.ToLower(state),
		Reason: reason,
	}
	return []eventstream.Envelope{next}
}

func acpApprovalRawInput(payload *gateway.ApprovalPayload) map[string]any {
	if payload == nil {
		return nil
	}
	raw := maps.Clone(payload.RawInput)
	raw = putRawStringIfMissing(raw, "approval_reason", payload.Reason)
	raw = putRawStringIfMissing(raw, "justification", payload.Justification)
	raw = putRawStringIfMissing(raw, "sandbox_permissions", payload.SandboxPermissions)
	return raw
}

func putRawStringIfMissing(raw map[string]any, key string, value string) map[string]any {
	value = strings.TrimSpace(value)
	if value == "" {
		return raw
	}
	if raw == nil {
		raw = map[string]any{}
	}
	if _, exists := raw[key]; !exists {
		raw[key] = value
	}
	return raw
}

func acpToolCallContent(in []session.ProtocolToolCallContent) []schema.ToolCallContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCallContent, 0, len(in))
	for _, item := range in {
		out = append(out, schema.ToolCallContent{
			Type:       strings.TrimSpace(item.Type),
			Content:    item.Content,
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			OldText:    item.OldText,
			NewText:    item.NewText,
		})
	}
	return out
}

func acpEventScope(ev gateway.Event) eventstream.Scope {
	if ev.Origin != nil && ev.Origin.Scope != "" {
		return eventstream.Scope(ev.Origin.Scope)
	}
	if ev.Narrative != nil && ev.Narrative.Scope != "" {
		return eventstream.Scope(ev.Narrative.Scope)
	}
	if ev.Participant != nil && ev.Participant.Scope != "" {
		return eventstream.Scope(ev.Participant.Scope)
	}
	if ev.Lifecycle != nil && ev.Lifecycle.Scope != "" {
		return eventstream.Scope(ev.Lifecycle.Scope)
	}
	return eventstream.ScopeMain
}

func acpEventScopeID(ev gateway.Event) string {
	if ev.Origin != nil && strings.TrimSpace(ev.Origin.ScopeID) != "" {
		return strings.TrimSpace(ev.Origin.ScopeID)
	}
	if sessionID := strings.TrimSpace(ev.SessionRef.SessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(ev.TurnID)
}

func acpEventActor(ev gateway.Event, fallback string) string {
	if ev.Origin != nil {
		if actor := strings.TrimSpace(ev.Origin.Actor); actor != "" {
			return actor
		}
	}
	return strings.TrimSpace(fallback)
}

func gatewayEventActorFallback(ev gateway.Event) string {
	switch {
	case ev.Narrative != nil:
		return strings.TrimSpace(ev.Narrative.Actor)
	case ev.ToolCall != nil:
		return strings.TrimSpace(ev.ToolCall.Actor)
	case ev.ToolResult != nil:
		return strings.TrimSpace(ev.ToolResult.Actor)
	case ev.Participant != nil:
		return strings.TrimSpace(ev.Participant.Actor)
	case ev.Lifecycle != nil:
		return strings.TrimSpace(ev.Lifecycle.Actor)
	default:
		return ""
	}
}

func acpEventParticipantID(ev gateway.Event) string {
	if ev.Origin != nil && strings.TrimSpace(ev.Origin.ParticipantID) != "" {
		return strings.TrimSpace(ev.Origin.ParticipantID)
	}
	switch {
	case ev.Narrative != nil:
		return strings.TrimSpace(ev.Narrative.ParticipantID)
	case ev.ToolCall != nil:
		return strings.TrimSpace(ev.ToolCall.ParticipantID)
	case ev.ToolResult != nil:
		return strings.TrimSpace(ev.ToolResult.ParticipantID)
	case ev.Participant != nil:
		return strings.TrimSpace(ev.Participant.ParticipantID)
	case ev.Lifecycle != nil:
		return strings.TrimSpace(ev.Lifecycle.ParticipantID)
	default:
		return ""
	}
}

func acpEventFinal(ev gateway.Event) bool {
	if ev.Narrative != nil {
		return ev.Narrative.Final
	}
	return false
}

func mergeACPEventMeta(base map[string]any, overlay map[string]any) map[string]any {
	return metautil.Merge(base, overlay)
}

func gatewayPermissionToolMeta(baseMeta map[string]any, ev gateway.Event, sessionEvent *session.Event) map[string]any {
	toolName := ""
	if ev.ApprovalPayload != nil {
		toolName = strings.TrimSpace(ev.ApprovalPayload.ToolName)
	}
	if toolName == "" && sessionEvent != nil && sessionEvent.Protocol != nil {
		if permission := session.ProtocolPermissionOf(sessionEvent); permission != nil {
			toolName = strings.TrimSpace(permission.ToolCall.Name)
		}
	}
	return acpMetaWithToolName(baseMeta, toolName)
}

func acpMetaWithToolName(meta map[string]any, toolName string) map[string]any {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return cloneAnyMap(meta)
	}
	return metautil.WithRuntimeSection(meta, gateway.EventMetaRuntimeTool, map[string]any{
		gateway.EventMetaRuntimeToolName: toolName,
	})
}

func ApprovalPayloadFromPermission(req *schema.RequestPermissionRequest) *gateway.ApprovalPayload {
	if req == nil {
		return nil
	}
	rawInput := rawInputMap(req.ToolCall.RawInput)
	payload := &gateway.ApprovalPayload{
		ToolCallID:         strings.TrimSpace(req.ToolCall.ToolCallID),
		ToolName:           firstNonEmpty(stringFromPtr(req.ToolCall.Title), stringFromPtr(req.ToolCall.Kind)),
		RawInput:           rawInput,
		Reason:             firstNonEmpty(rawString(rawInput, "approval_reason"), rawString(rawInput, "reason")),
		Justification:      rawString(rawInput, "justification"),
		SandboxPermissions: rawString(rawInput, "sandbox_permissions"),
		Status:             gateway.ApprovalStatusPending,
	}
	if len(req.Options) > 0 {
		payload.Options = make([]gateway.ApprovalOption, 0, len(req.Options))
		for _, option := range req.Options {
			payload.Options = append(payload.Options, gateway.ApprovalOption{
				ID:   strings.TrimSpace(option.OptionID),
				Name: strings.TrimSpace(option.Name),
				Kind: strings.TrimSpace(option.Kind),
			})
		}
	}
	return payload
}

func rawString(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	text, _ := values[key].(string)
	return strings.TrimSpace(text)
}

func rawInputMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	default:
		return nil
	}
}

func stringFromPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
