package projector

import (
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

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

func projectGatewayStandardACPEvents(base eventstream.Envelope, ev gateway.Event) []eventstream.Envelope {
	sessionEvent, ok := sessionEventFromGatewayEvent(base, ev)
	if !ok {
		return nil
	}
	projector := EventProjector{}
	out := make([]eventstream.Envelope, 0, 2)
	if permission, ok, err := projector.ProjectPermissionRequest(sessionEvent); err != nil {
		return []eventstream.Envelope{eventstream.Error(err)}
	} else if ok && permission != nil {
		next := base
		next.Kind = eventstream.KindRequestPermission
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
		next.Update = gatewayCompatibleToolContent(update, ev)
		if len(updateMeta) > 0 {
			next.Meta = mergeACPEventMeta(updateMeta, next.Meta)
		}
		out = append(out, next)
	}
	return out
}

func gatewayCompatibleToolContent(update Update, ev gateway.Event) Update {
	if ev.Protocol != nil || update == nil {
		return update
	}
	switch typed := update.(type) {
	case schema.ToolCall:
		if ev.ToolCall == nil || len(ev.ToolCall.Content) == 0 {
			return update
		}
		typed.Content = acpToolCallContent(ev.ToolCall.Content)
		return typed
	case schema.ToolCallUpdate:
		if ev.ToolResult == nil || len(ev.ToolResult.Content) == 0 {
			return update
		}
		typed.Content = acpToolCallContent(ev.ToolResult.Content)
		return typed
	default:
		return update
	}
}

func sessionEventFromGatewayEvent(base eventstream.Envelope, ev gateway.Event) (*session.Event, bool) {
	out := &session.Event{
		SessionID:  strings.TrimSpace(base.SessionID),
		Type:       sessionTypeFromEventKind(ev.Kind),
		Time:       ev.OccurredAt,
		Visibility: session.VisibilityCanonical,
		Meta:       maps.Clone(ev.Meta),
	}
	if ev.Protocol != nil {
		protocol := session.CloneEventProtocol(*ev.Protocol)
		out.Protocol = &protocol
		if ev.Narrative != nil {
			out.Text = ev.Narrative.Text
		}
		return out, true
	}
	switch ev.Kind {
	case gateway.EventKindUserMessage, gateway.EventKindAssistantMessage:
		return applyGatewayNarrative(out, ev.Narrative)
	case gateway.EventKindToolCall:
		protocol, ok := protocolUpdateFromGatewayToolCall(ev)
		if !ok {
			return nil, false
		}
		out.Protocol = protocol
		return out, true
	case gateway.EventKindToolResult:
		protocol, ok := protocolUpdateFromGatewayToolResult(ev)
		if !ok {
			return nil, false
		}
		out.Protocol = protocol
		return out, true
	case gateway.EventKindPlanUpdate:
		protocol, ok := protocolUpdateFromGatewayPlan(ev.Plan)
		if !ok {
			return nil, false
		}
		out.Protocol = protocol
		return out, true
	case gateway.EventKindApprovalRequested:
		protocol, ok := protocolPermissionFromApprovalPayload(ev.ApprovalPayload)
		if !ok {
			return nil, false
		}
		out.Protocol = protocol
		return out, true
	default:
		return nil, false
	}
}

func applyGatewayNarrative(event *session.Event, narrative *gateway.NarrativePayload) (*session.Event, bool) {
	if event == nil || narrative == nil {
		return nil, false
	}
	switch narrative.Role {
	case gateway.NarrativeRoleUser:
		event.Type = session.EventTypeUser
		event.Text = strings.TrimSpace(narrative.Text)
	case gateway.NarrativeRoleAssistant:
		event.Type = session.EventTypeAssistant
		parts := make([]session.EventPart, 0, 2)
		if narrative.ReasoningText != "" {
			parts = append(parts, session.EventPart{
				Kind: "reasoning",
				Reasoning: &session.EventReasoningPart{
					Text:       narrative.ReasoningText,
					Visibility: "visible",
				},
			})
		}
		if narrative.Text != "" {
			parts = append(parts, session.EventPart{Kind: "text", Text: narrative.Text})
		}
		event.AssistantMessage = &session.EventMessagePayload{
			Role:  "assistant",
			Parts: parts,
		}
		event.Text = narrative.Text
	default:
		return nil, false
	}
	return event, true
}

func protocolUpdateFromGatewayToolCall(ev gateway.Event) (*session.EventProtocol, bool) {
	if ev.ToolCall == nil {
		return nil, false
	}
	toolName := strings.TrimSpace(ev.ToolCall.ToolName)
	update := &session.ProtocolUpdate{
		SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
		ToolCallID:    strings.TrimSpace(ev.ToolCall.CallID),
		Title:         firstNonEmpty(ev.ToolCall.ToolTitle, ev.ToolCall.ToolName, ev.ToolCall.ToolKind),
		Kind:          firstNonEmpty(ev.ToolCall.ToolKind, ev.ToolCall.ToolName),
		Status:        strings.TrimSpace(string(ev.ToolCall.Status)),
		RawInput:      maps.Clone(ev.ToolCall.RawInput),
		Content:       session.CloneProtocolToolCallContent(ev.ToolCall.Content),
		Meta:          acpMetaWithToolName(ev.Meta, toolName),
	}
	return &session.EventProtocol{Update: update}, true
}

func protocolUpdateFromGatewayToolResult(ev gateway.Event) (*session.EventProtocol, bool) {
	if ev.ToolResult == nil {
		return nil, false
	}
	toolName := strings.TrimSpace(ev.ToolResult.ToolName)
	update := &session.ProtocolUpdate{
		SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
		ToolCallID:    strings.TrimSpace(ev.ToolResult.CallID),
		Title:         firstNonEmpty(ev.ToolResult.ToolTitle, ev.ToolResult.ToolName, ev.ToolResult.ToolKind),
		Kind:          firstNonEmpty(ev.ToolResult.ToolKind, ev.ToolResult.ToolName),
		Status:        strings.TrimSpace(string(ev.ToolResult.Status)),
		RawInput:      maps.Clone(ev.ToolResult.RawInput),
		RawOutput:     maps.Clone(ev.ToolResult.RawOutput),
		Content:       session.CloneProtocolToolCallContent(ev.ToolResult.Content),
		Meta:          acpMetaWithToolName(ev.Meta, toolName),
	}
	return &session.EventProtocol{Update: update}, true
}

func protocolUpdateFromGatewayPlan(plan *gateway.PlanPayload) (*session.EventProtocol, bool) {
	if plan == nil || len(plan.Entries) == 0 {
		return nil, false
	}
	entries := make([]session.ProtocolPlanEntry, 0, len(plan.Entries))
	for _, item := range plan.Entries {
		entries = append(entries, session.ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: strings.TrimSpace(item.Priority),
		})
	}
	return &session.EventProtocol{Update: &session.ProtocolUpdate{
		SessionUpdate: string(session.ProtocolUpdateTypePlan),
		Entries:       entries,
	}}, true
}

func protocolPermissionFromApprovalPayload(payload *gateway.ApprovalPayload) (*session.EventProtocol, bool) {
	if payload == nil {
		return nil, false
	}
	toolName := strings.TrimSpace(payload.ToolName)
	toolCallID := strings.TrimSpace(payload.ToolCallID)
	rawInput := acpApprovalRawInput(payload)
	if toolName == "" && toolCallID == "" && len(rawInput) == 0 && len(payload.Options) == 0 {
		return nil, false
	}
	options := make([]session.ProtocolApprovalOption, 0, len(payload.Options))
	for _, item := range payload.Options {
		options = append(options, session.ProtocolApprovalOption{
			ID:   strings.TrimSpace(item.ID),
			Name: strings.TrimSpace(item.Name),
			Kind: strings.TrimSpace(item.Kind),
		})
	}
	approval := &session.ProtocolApproval{
		ToolCall: session.ProtocolToolCall{
			ID:       toolCallID,
			Name:     toolName,
			Kind:     toolName,
			Title:    toolName,
			Status:   strings.TrimSpace(string(payload.Status)),
			RawInput: rawInput,
		},
		Options: options,
	}
	return &session.EventProtocol{Permission: approval, Approval: approval}, true
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
	meta := maps.Clone(ev.Meta)
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
		caelis = maps.Clone(caelis)
	}
	caelis["invocation"] = map[string]any{
		"provider": invocation.Provider,
		"model":    invocation.Model,
	}
	meta["caelis"] = caelis
	return meta
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
		next := base
		next.Kind = eventstream.KindParticipant
		next.Actor = acpEventActor(ev, ev.Participant.Actor)
		next.Participant = &eventstream.Participant{State: strings.TrimSpace(string(ev.Participant.Action))}
		return []eventstream.Envelope{next}
	case gateway.EventKindLifecycle:
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

func gatewayPermissionToolMeta(baseMeta map[string]any, ev gateway.Event, sessionEvent *session.Event) map[string]any {
	toolName := ""
	if ev.ApprovalPayload != nil {
		toolName = strings.TrimSpace(ev.ApprovalPayload.ToolName)
	}
	if toolName == "" && sessionEvent != nil && sessionEvent.Protocol != nil {
		protocol := session.CloneEventProtocol(*sessionEvent.Protocol)
		if protocol.Approval != nil {
			toolName = strings.TrimSpace(protocol.Approval.ToolCall.Name)
		}
	}
	return acpMetaWithToolName(baseMeta, toolName)
}

func acpMetaWithToolName(meta map[string]any, toolName string) map[string]any {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return maps.Clone(meta)
	}
	return withCaelisRuntimeSection(meta, gateway.EventMetaRuntimeTool, map[string]any{
		gateway.EventMetaRuntimeToolName: toolName,
	})
}

func withCaelisRuntimeSection(meta map[string]any, section string, values map[string]any) map[string]any {
	out := maps.Clone(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis, _ := out[gateway.EventMetaRoot].(map[string]any)
	caelis = maps.Clone(caelis)
	if caelis == nil {
		caelis = map[string]any{}
	}
	caelis[gateway.EventMetaVersion] = 1
	runtime, _ := caelis[gateway.EventMetaRuntime].(map[string]any)
	runtime = maps.Clone(runtime)
	if runtime == nil {
		runtime = map[string]any{}
	}
	sectionMap, _ := runtime[section].(map[string]any)
	sectionMap = maps.Clone(sectionMap)
	if sectionMap == nil {
		sectionMap = map[string]any{}
	}
	for key, value := range values {
		sectionMap[key] = value
	}
	runtime[section] = sectionMap
	caelis[gateway.EventMetaRuntime] = runtime
	out[gateway.EventMetaRoot] = caelis
	return out
}
