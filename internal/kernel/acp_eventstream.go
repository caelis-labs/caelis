package kernel

import (
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	acpprojector "github.com/OnslaughtSnail/caelis/protocol/acp/projector"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

// ProjectACPEventEnvelope projects the gateway runtime event envelope into the
// surface-facing ACP event stream. It is the compatibility bridge for current
// runtime events while surfaces migrate away from consuming kernel.Event
// directly.
func ProjectACPEventEnvelope(env EventEnvelope) []eventstream.Envelope {
	if env.Err != nil {
		return []eventstream.Envelope{eventstream.Error(env.Err)}
	}
	base := acpEventBase(env)
	out := make([]eventstream.Envelope, 0, 3)
	if env.Event.Protocol != nil {
		out = append(out, projectACPProtocolEvents(base, env.Event)...)
	}
	if len(out) == 0 {
		out = append(out, inferACPEventsFromGatewayEvent(base, env.Event)...)
	}
	if env.Event.Usage != nil {
		usage := *env.Event.Usage
		out = append(out, eventstream.Envelope{
			Kind:       eventstream.KindUsage,
			Cursor:     base.Cursor,
			SessionID:  base.SessionID,
			HandleID:   base.HandleID,
			RunID:      base.RunID,
			TurnID:     base.TurnID,
			OccurredAt: base.OccurredAt,
			Scope:      base.Scope,
			ScopeID:    base.ScopeID,
			Actor:      base.Actor,
			Usage: &eventstream.UsageSnapshot{
				PromptTokens:      usage.PromptTokens,
				CachedInputTokens: usage.CachedInputTokens,
				CompletionTokens:  usage.CompletionTokens,
				ReasoningTokens:   usage.ReasoningTokens,
				TotalTokens:       usage.TotalTokens,
			},
			Meta: maps.Clone(base.Meta),
		})
	}
	return out
}

func projectACPProtocolEvents(base eventstream.Envelope, ev Event) []eventstream.Envelope {
	if ev.Protocol == nil {
		return nil
	}
	protocol := session.CloneEventProtocol(*ev.Protocol)
	sessionEvent := session.Event{
		SessionID: base.SessionID,
		Type:      sessionTypeFromEventKind(ev.Kind),
		Protocol:  &protocol,
	}
	if ev.Narrative != nil {
		sessionEvent.Text = ev.Narrative.Text
	}
	projector := acpprojector.EventProjector{}
	out := make([]eventstream.Envelope, 0, 2)
	if permission, ok, err := projector.ProjectPermissionRequest(&sessionEvent); err != nil {
		return []eventstream.Envelope{eventstream.Error(err)}
	} else if ok && permission != nil {
		next := base
		next.Kind = eventstream.KindRequestPermission
		next.Permission = permission
		out = append(out, next)
	}
	updates, err := projector.ProjectEvent(&sessionEvent)
	if err != nil {
		return []eventstream.Envelope{eventstream.Error(err)}
	}
	for _, update := range updates {
		if update == nil {
			continue
		}
		next := base
		next.Kind = eventstream.KindSessionUpdate
		next.Update = update
		if protocol.Update != nil {
			next.Meta = mergeACPEventMeta(protocol.Update.Meta, next.Meta)
		}
		out = append(out, next)
	}
	return out
}

func acpEventBase(env EventEnvelope) eventstream.Envelope {
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
		Actor:         acpEventActor(ev, ""),
		ParticipantID: acpEventParticipantID(ev),
		Final:         acpEventFinal(ev),
		Meta:          maps.Clone(ev.Meta),
	}
}

func sessionTypeFromEventKind(kind EventKind) session.EventType {
	switch kind {
	case EventKindUserMessage:
		return session.EventTypeUser
	case EventKindAssistantMessage:
		return session.EventTypeAssistant
	case EventKindPlanUpdate:
		return session.EventTypePlan
	case EventKindToolCall:
		return session.EventTypeToolCall
	case EventKindToolResult:
		return session.EventTypeToolResult
	case EventKindParticipant:
		return session.EventTypeParticipant
	case EventKindHandoff:
		return session.EventTypeHandoff
	case EventKindCompact:
		return session.EventTypeCompact
	case EventKindNotice:
		return session.EventTypeNotice
	case EventKindLifecycle, EventKindApprovalRequested, EventKindApprovalReview:
		return session.EventTypeLifecycle
	case EventKindSystemMessage:
		return session.EventTypeSystem
	default:
		return session.EventTypeCustom
	}
}

func inferACPEventsFromGatewayEvent(base eventstream.Envelope, ev Event) []eventstream.Envelope {
	switch ev.Kind {
	case EventKindUserMessage:
		text := ""
		if ev.Narrative != nil {
			text = ev.Narrative.Text
		}
		return singleTextACPUpdate(base, schema.UpdateUserMessage, text, true)
	case EventKindAssistantMessage:
		return inferACPEventsFromNarrative(base, ev)
	case EventKindToolCall:
		if ev.ToolCall == nil {
			return nil
		}
		next := base
		next.Kind = eventstream.KindSessionUpdate
		next.Actor = acpEventActor(ev, ev.ToolCall.Actor)
		next.Update = schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    strings.TrimSpace(ev.ToolCall.CallID),
			Title:         firstNonEmpty(ev.ToolCall.ToolTitle, ev.ToolCall.ToolName, ev.ToolCall.ToolKind),
			Kind:          firstNonEmpty(ev.ToolCall.ToolKind, ev.ToolCall.ToolName),
			Status:        normalizeACPToolStatus(string(ev.ToolCall.Status)),
			RawInput:      maps.Clone(ev.ToolCall.RawInput),
			Content:       acpToolCallContent(ev.ToolCall.Content),
			Meta:          acpMetaWithToolName(ev.Meta, ev.ToolCall.ToolName),
		}
		return []eventstream.Envelope{next}
	case EventKindToolResult:
		if ev.ToolResult == nil {
			return nil
		}
		next := base
		next.Kind = eventstream.KindSessionUpdate
		next.Actor = acpEventActor(ev, ev.ToolResult.Actor)
		title := firstNonEmpty(ev.ToolResult.ToolTitle, ev.ToolResult.ToolName, ev.ToolResult.ToolKind)
		kind := firstNonEmpty(ev.ToolResult.ToolKind, ev.ToolResult.ToolName)
		status := normalizeACPToolStatus(string(ev.ToolResult.Status))
		next.Update = schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    strings.TrimSpace(ev.ToolResult.CallID),
			Title:         stringPtrOrNil(title),
			Kind:          stringPtrOrNil(kind),
			Status:        stringPtrOrNil(status),
			RawInput:      maps.Clone(ev.ToolResult.RawInput),
			RawOutput:     maps.Clone(ev.ToolResult.RawOutput),
			Content:       acpToolCallContent(ev.ToolResult.Content),
			Meta:          acpMetaWithToolName(ev.Meta, ev.ToolResult.ToolName),
		}
		return []eventstream.Envelope{next}
	case EventKindPlanUpdate:
		if ev.Plan == nil {
			return nil
		}
		entries := make([]schema.PlanEntry, 0, len(ev.Plan.Entries))
		for _, item := range ev.Plan.Entries {
			entries = append(entries, schema.PlanEntry{
				Content:  strings.TrimSpace(item.Content),
				Status:   strings.TrimSpace(item.Status),
				Priority: strings.TrimSpace(item.Priority),
			})
		}
		if len(entries) == 0 {
			return nil
		}
		next := base
		next.Kind = eventstream.KindSessionUpdate
		next.Update = schema.PlanUpdate{SessionUpdate: schema.UpdatePlan, Entries: entries}
		return []eventstream.Envelope{next}
	case EventKindApprovalRequested:
		if permission := acpPermissionEnvelopeFromApprovalPayload(base, ev.ApprovalPayload); permission != nil {
			return []eventstream.Envelope{*permission}
		}
		return nil
	case EventKindApprovalReview:
		if ev.ApprovalPayload == nil || !strings.EqualFold(strings.TrimSpace(ev.ApprovalPayload.DecisionSource), string(ApprovalModeAutoReview)) {
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
	case EventKindParticipant:
		if ev.Participant == nil {
			return nil
		}
		next := base
		next.Kind = eventstream.KindParticipant
		next.Actor = acpEventActor(ev, ev.Participant.Actor)
		next.Participant = &eventstream.Participant{State: strings.TrimSpace(string(ev.Participant.Action))}
		return []eventstream.Envelope{next}
	case EventKindLifecycle:
		if ev.Lifecycle == nil {
			return nil
		}
		next := base
		next.Kind = eventstream.KindLifecycle
		next.Actor = acpEventActor(ev, ev.Lifecycle.Actor)
		next.Lifecycle = &eventstream.Lifecycle{
			State:  strings.ToLower(strings.TrimSpace(string(ev.Lifecycle.Status))),
			Reason: strings.TrimSpace(ev.Lifecycle.Reason),
		}
		return []eventstream.Envelope{next}
	case EventKindNotice, EventKindSystemMessage:
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

func acpPermissionEnvelopeFromApprovalPayload(base eventstream.Envelope, payload *ApprovalPayload) *eventstream.Envelope {
	if payload == nil {
		return nil
	}
	toolName := strings.TrimSpace(payload.ToolName)
	toolCallID := strings.TrimSpace(payload.ToolCallID)
	rawInput := acpApprovalRawInput(payload)
	if toolName == "" && toolCallID == "" && len(rawInput) == 0 && len(payload.Options) == 0 {
		return nil
	}
	out := base
	out.Kind = eventstream.KindRequestPermission
	out.Permission = &schema.RequestPermissionRequest{
		SessionID: strings.TrimSpace(base.SessionID),
		ToolCall: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    toolCallID,
			Title:         stringPtrOrNil(toolName),
			Kind:          stringPtrOrNil(toolName),
			Status:        stringPtrOrNil(normalizeACPToolStatus(string(payload.Status))),
			RawInput:      rawInput,
			Meta:          acpMetaWithToolName(base.Meta, toolName),
		},
		Options: acpPermissionOptionsFromApprovalPayload(payload),
	}
	return &out
}

func acpPermissionOptionsFromApprovalPayload(payload *ApprovalPayload) []schema.PermissionOption {
	if payload == nil || len(payload.Options) == 0 {
		return nil
	}
	options := make([]schema.PermissionOption, 0, len(payload.Options))
	for _, item := range payload.Options {
		options = append(options, schema.PermissionOption{
			OptionID: strings.TrimSpace(item.ID),
			Name:     strings.TrimSpace(item.Name),
			Kind:     strings.TrimSpace(item.Kind),
		})
	}
	return options
}

func acpApprovalRawInput(payload *ApprovalPayload) map[string]any {
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

func inferACPEventsFromNarrative(base eventstream.Envelope, ev Event) []eventstream.Envelope {
	if ev.Narrative == nil {
		return nil
	}
	base.Actor = acpEventActor(ev, ev.Narrative.Actor)
	base.Final = ev.Narrative.Final
	switch ev.Narrative.Role {
	case NarrativeRoleUser:
		return singleTextACPUpdate(base, schema.UpdateUserMessage, ev.Narrative.Text, true)
	case NarrativeRoleAssistant:
		out := make([]eventstream.Envelope, 0, 2)
		if ev.Narrative.ReasoningText != "" {
			out = append(out, singleACPTextEvent(base, schema.UpdateAgentThought, ev.Narrative.ReasoningText))
		}
		if ev.Narrative.Text != "" {
			out = append(out, singleACPTextEvent(base, schema.UpdateAgentMessage, ev.Narrative.Text))
		}
		return out
	case NarrativeRoleSystem, NarrativeRoleNotice:
		if strings.TrimSpace(ev.Narrative.Text) == "" {
			return nil
		}
		next := base
		next.Kind = eventstream.KindNotice
		next.Notice = strings.TrimSpace(ev.Narrative.Text)
		return []eventstream.Envelope{next}
	default:
		return nil
	}
}

func singleTextACPUpdate(base eventstream.Envelope, updateType string, text string, final bool) []eventstream.Envelope {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	base.Final = final
	return []eventstream.Envelope{singleACPTextEvent(base, updateType, text)}
}

func singleACPTextEvent(base eventstream.Envelope, updateType string, text string) eventstream.Envelope {
	base.Kind = eventstream.KindSessionUpdate
	base.Update = schema.ContentChunk{
		SessionUpdate: strings.TrimSpace(updateType),
		Content:       schema.TextContent{Type: "text", Text: text},
	}
	return base
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

func normalizeACPToolStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "started", "running", "waiting_approval", "in_progress":
		return schema.ToolStatusInProgress
	case "completed", "success", "succeeded":
		return schema.ToolStatusCompleted
	case "failed", "error":
		return schema.ToolStatusFailed
	case "pending":
		return schema.ToolStatusPending
	default:
		return strings.TrimSpace(status)
	}
}

func acpEventScope(ev Event) eventstream.Scope {
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

func acpEventScopeID(ev Event) string {
	if ev.Origin != nil && strings.TrimSpace(ev.Origin.ScopeID) != "" {
		return strings.TrimSpace(ev.Origin.ScopeID)
	}
	if sessionID := strings.TrimSpace(ev.SessionRef.SessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(ev.TurnID)
}

func acpEventActor(ev Event, fallback string) string {
	if ev.Origin != nil {
		if actor := strings.TrimSpace(ev.Origin.Actor); actor != "" {
			return actor
		}
	}
	return strings.TrimSpace(fallback)
}

func acpEventParticipantID(ev Event) string {
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

func acpEventFinal(ev Event) bool {
	if ev.Narrative != nil {
		return ev.Narrative.Final
	}
	return false
}

func mergeACPEventMeta(base map[string]any, overlay map[string]any) map[string]any {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	if len(base) == 0 {
		return maps.Clone(overlay)
	}
	out := maps.Clone(base)
	for key, value := range overlay {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overlayMap, ok := value.(map[string]any); ok {
				out[key] = mergeACPEventMeta(baseMap, overlayMap)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func acpMetaWithToolName(meta map[string]any, toolName string) map[string]any {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return maps.Clone(meta)
	}
	return withCaelisRuntimeSection(meta, EventMetaRuntimeTool, map[string]any{
		EventMetaRuntimeToolName: toolName,
	})
}

func stringPtrOrNil(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
