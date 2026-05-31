package eventbridge

import (
	"maps"
	"strings"

	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/kernel"
	acpsession "github.com/OnslaughtSnail/caelis/ports/session"
)

func KernelEnvelopeFromAppEvent(env appviewmodel.SessionEventEnvelope) (kernel.EventEnvelope, bool) {
	if strings.TrimSpace(env.Error) != "" {
		return kernel.EventEnvelope{
			Err: &kernel.Error{Kind: kernel.KindInternal, Code: kernel.CodeInternal, Message: env.Error},
		}, true
	}
	if env.Canonical == nil {
		return kernel.EventEnvelope{}, false
	}
	return KernelEnvelopeFromCore(coreruntime.EventEnvelope{
		Cursor: coresession.Cursor(strings.TrimSpace(env.Cursor)),
		Event:  coresession.CloneEvent(*env.Canonical),
	})
}

func KernelEnvelopeFromCore(env coreruntime.EventEnvelope) (kernel.EventEnvelope, bool) {
	if strings.TrimSpace(env.Err) != "" {
		return kernel.EventEnvelope{
			Err: &kernel.Error{Kind: kernel.KindInternal, Code: kernel.CodeInternal, Message: env.Err},
		}, true
	}
	if env.Event.Type == "" {
		return kernel.EventEnvelope{}, false
	}
	event := KernelEventFromCore(env.Event)
	cursor := strings.TrimSpace(string(env.Cursor))
	if cursor == "" {
		cursor = strings.TrimSpace(env.Event.ID)
	}
	return kernel.EventEnvelope{Cursor: cursor, Event: event}, true
}

func KernelEventFromCore(event coresession.Event) kernel.Event {
	ref := acpsession.SessionRef{SessionID: strings.TrimSpace(event.SessionID)}
	scope := coreEventScope(event)
	participantID := coreEventParticipantID(event)
	actor := coreEventActor(event)
	out := kernel.Event{
		Kind:       kernelEventKind(event.Type),
		TurnID:     coreEventTurnID(event),
		OccurredAt: event.Time,
		SessionRef: ref,
		Origin:     coreEventOrigin(event, scope, participantID, actor),
		Meta:       coreEventMeta(event),
	}
	if out.Meta == nil {
		out.Meta = map[string]any{}
	}
	text := coresession.EventText(event)
	switch event.Type {
	case coresession.EventUser:
		out.Narrative = &kernel.NarrativePayload{Role: kernel.NarrativeRoleUser, Actor: actor, Text: text, Final: true, Scope: scope, ParticipantID: participantID}
	case coresession.EventAssistant:
		out.Narrative = &kernel.NarrativePayload{Role: kernel.NarrativeRoleAssistant, Actor: actor, Text: text, Final: true, Scope: scope, ParticipantID: participantID}
	case coresession.EventSystem:
		out.Narrative = &kernel.NarrativePayload{Role: kernel.NarrativeRoleSystem, Actor: actor, Text: text, Final: true, Scope: scope, ParticipantID: participantID}
	case coresession.EventNotice:
		out.Narrative = &kernel.NarrativePayload{Role: kernel.NarrativeRoleNotice, Actor: actor, Text: text, Final: true, Scope: scope, ParticipantID: participantID}
	case coresession.EventToolCall:
		out.ToolCall = coreToolCallPayload(event)
	case coresession.EventToolResult:
		out.ToolResult = coreToolResultPayload(event)
	case coresession.EventApproval:
		out.ApprovalPayload = coreApprovalPayload(event)
	case coresession.EventPlan:
		out.Plan = corePlanPayload(event)
	case coresession.EventLifecycle:
		out.Lifecycle = coreLifecyclePayload(event)
	case coresession.EventParticipant:
		out.Participant = coreParticipantPayload(event)
	}
	return out
}

func coreEventScope(event coresession.Event) kernel.EventScope {
	if event.Scope == nil {
		return kernel.EventScopeMain
	}
	participant := event.Scope.Participant
	if strings.TrimSpace(participant.ID) == "" {
		return kernel.EventScopeMain
	}
	if participant.Kind == coresession.ParticipantSubagent {
		return kernel.EventScopeSubagent
	}
	return kernel.EventScopeParticipant
}

func coreEventParticipantID(event coresession.Event) string {
	if event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.Participant.ID)
}

func coreEventActor(event coresession.Event) string {
	if event.Scope != nil && strings.TrimSpace(event.Scope.Participant.ID) != "" {
		participant := event.Scope.Participant
		return firstNonEmpty(participant.Label, participant.AgentName, participant.ID)
	}
	return firstNonEmpty(event.Actor.Name, event.Actor.ID, string(event.Actor.Kind))
}

func coreEventOrigin(event coresession.Event, scope kernel.EventScope, participantID string, actor string) *kernel.EventOrigin {
	if scope == kernel.EventScopeMain && strings.TrimSpace(participantID) == "" {
		return nil
	}
	origin := &kernel.EventOrigin{
		Scope:         scope,
		ScopeID:       strings.TrimSpace(participantID),
		Source:        "core",
		Actor:         strings.TrimSpace(actor),
		ParticipantID: strings.TrimSpace(participantID),
	}
	if event.Scope != nil {
		origin.Source = firstNonEmpty(event.Scope.Source, origin.Source)
		participant := event.Scope.Participant
		origin.ParticipantKind = strings.TrimSpace(string(participant.Kind))
		origin.ParticipantSessionID = strings.TrimSpace(participant.SessionID)
	}
	return origin
}

func kernelEventKind(kind coresession.EventType) kernel.EventKind {
	switch kind {
	case coresession.EventUser:
		return kernel.EventKindUserMessage
	case coresession.EventAssistant:
		return kernel.EventKindAssistantMessage
	case coresession.EventSystem:
		return kernel.EventKindSystemMessage
	case coresession.EventToolCall:
		return kernel.EventKindToolCall
	case coresession.EventToolResult:
		return kernel.EventKindToolResult
	case coresession.EventApproval:
		return kernel.EventKindApprovalRequested
	case coresession.EventPlan:
		return kernel.EventKindPlanUpdate
	case coresession.EventCompact:
		return kernel.EventKindCompact
	case coresession.EventLifecycle:
		return kernel.EventKindLifecycle
	case coresession.EventParticipant:
		return kernel.EventKindParticipant
	case coresession.EventHandoff:
		return kernel.EventKindHandoff
	case coresession.EventNotice:
		return kernel.EventKindNotice
	default:
		return kernel.EventKindNotice
	}
}

func coreEventTurnID(event coresession.Event) string {
	if event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.TurnID)
}

func coreToolCallPayload(event coresession.Event) *kernel.ToolCallPayload {
	if event.Tool == nil {
		return nil
	}
	scope := coreEventScope(event)
	return &kernel.ToolCallPayload{
		CallID:        strings.TrimSpace(event.Tool.ID),
		ToolName:      strings.TrimSpace(event.Tool.Name),
		ToolKind:      strings.TrimSpace(event.Tool.Kind),
		ToolTitle:     strings.TrimSpace(event.Tool.Title),
		RawInput:      maps.Clone(event.Tool.Input),
		Content:       coreToolContent(event.Tool.Content),
		Status:        coreToolStatus(event.Tool.Status),
		Actor:         coreEventActor(event),
		Scope:         scope,
		ParticipantID: coreEventParticipantID(event),
	}
}

func coreToolResultPayload(event coresession.Event) *kernel.ToolResultPayload {
	if event.Tool == nil {
		return nil
	}
	scope := coreEventScope(event)
	return &kernel.ToolResultPayload{
		CallID:        strings.TrimSpace(event.Tool.ID),
		ToolName:      strings.TrimSpace(event.Tool.Name),
		ToolKind:      strings.TrimSpace(event.Tool.Kind),
		ToolTitle:     strings.TrimSpace(event.Tool.Title),
		RawInput:      maps.Clone(event.Tool.Input),
		RawOutput:     maps.Clone(event.Tool.Output),
		Content:       coreToolContent(event.Tool.Content),
		Status:        coreToolStatus(event.Tool.Status),
		Error:         event.Tool.Status == coresession.ToolFailed,
		Actor:         coreEventActor(event),
		Scope:         scope,
		ParticipantID: coreEventParticipantID(event),
	}
}

func coreToolContent(content []coresession.ToolContent) []acpsession.ProtocolToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]acpsession.ProtocolToolCallContent, 0, len(content))
	for _, item := range content {
		contentType := strings.TrimSpace(item.Type)
		switch contentType {
		case "":
			if strings.TrimSpace(item.Text) != "" {
				contentType = "content"
			}
		case "text":
			contentType = "content"
		}
		var payload any
		if strings.TrimSpace(item.Text) != "" {
			payload = acpsession.ProtocolTextContent(item.Text)
		}
		out = append(out, acpsession.ProtocolToolCallContent{
			Type:       contentType,
			Content:    payload,
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
		})
	}
	return out
}

func coreEventMeta(event coresession.Event) map[string]any {
	meta := maps.Clone(event.Meta)
	if event.Tool != nil {
		meta = mergeCoreMeta(meta, event.Tool.Meta)
	}
	return meta
}

func mergeCoreMeta(base map[string]any, extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return maps.Clone(base)
	}
	out := maps.Clone(base)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range extra {
		if value == nil {
			continue
		}
		if existing, ok := out[key]; ok {
			existingMap, existingOK := existing.(map[string]any)
			valueMap, valueOK := value.(map[string]any)
			if existingOK && valueOK {
				out[key] = mergeCoreMeta(existingMap, valueMap)
			}
			continue
		}
		out[key] = cloneCoreMetaValue(value)
	}
	return out
}

func cloneCoreMetaValue(value any) any {
	mapped, ok := value.(map[string]any)
	if !ok {
		return value
	}
	return mergeCoreMeta(nil, mapped)
}

func coreToolStatus(status coresession.ToolStatus) kernel.ToolStatus {
	switch status {
	case coresession.ToolStarted:
		return kernel.ToolStatusStarted
	case coresession.ToolRunning:
		return kernel.ToolStatusRunning
	case coresession.ToolWaitingApproval:
		return kernel.ToolStatusWaitingApproval
	case coresession.ToolCompleted:
		return kernel.ToolStatusCompleted
	case coresession.ToolFailed:
		return kernel.ToolStatusFailed
	case coresession.ToolCancelled:
		return kernel.ToolStatusCancelled
	default:
		return kernel.ToolStatusRunning
	}
}

func coreApprovalPayload(event coresession.Event) *kernel.ApprovalPayload {
	if event.Approval == nil {
		return nil
	}
	payload := &kernel.ApprovalPayload{
		Reason:  strings.TrimSpace(event.Approval.Reason),
		Status:  kernel.ApprovalStatus(event.Approval.Status),
		Options: coreApprovalOptions(event.Approval.Options),
	}
	if tool := event.Approval.Tool; tool != nil {
		payload.ToolCallID = strings.TrimSpace(tool.ID)
		payload.ToolName = strings.TrimSpace(tool.Name)
		payload.RawInput = maps.Clone(tool.Input)
	} else if event.Tool != nil {
		payload.ToolCallID = strings.TrimSpace(event.Tool.ID)
		payload.ToolName = strings.TrimSpace(event.Tool.Name)
		payload.RawInput = maps.Clone(event.Tool.Input)
	}
	return payload
}

func coreApprovalOptions(in []coresession.ApprovalOption) []kernel.ApprovalOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]kernel.ApprovalOption, 0, len(in))
	for _, option := range in {
		out = append(out, kernel.ApprovalOption{
			ID:   strings.TrimSpace(option.ID),
			Name: strings.TrimSpace(option.Name),
			Kind: strings.TrimSpace(option.Kind),
		})
	}
	return out
}

func corePlanPayload(event coresession.Event) *kernel.PlanPayload {
	if len(event.Plan) == 0 {
		return nil
	}
	out := &kernel.PlanPayload{Entries: make([]kernel.PlanEntryPayload, 0, len(event.Plan))}
	for _, entry := range event.Plan {
		out.Entries = append(out.Entries, kernel.PlanEntryPayload{
			Content: strings.TrimSpace(entry.Content),
			Status:  strings.TrimSpace(entry.Status),
		})
	}
	return out
}

func coreLifecyclePayload(event coresession.Event) *kernel.LifecyclePayload {
	if event.Lifecycle == nil {
		return nil
	}
	return &kernel.LifecyclePayload{
		Status:        kernel.LifecycleStatus(event.Lifecycle.Status),
		Reason:        strings.TrimSpace(event.Lifecycle.Reason),
		Actor:         coreEventActor(event),
		Scope:         coreEventScope(event),
		ParticipantID: coreEventParticipantID(event),
	}
}

func coreParticipantPayload(event coresession.Event) *kernel.ParticipantPayload {
	if event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == "" {
		return nil
	}
	participant := event.Scope.Participant
	return &kernel.ParticipantPayload{
		ParticipantID:   strings.TrimSpace(participant.ID),
		ParticipantKind: strings.TrimSpace(string(participant.Kind)),
		Role:            strings.TrimSpace(string(participant.Role)),
		Label:           firstNonEmpty(participant.Label, participant.AgentName, participant.ID),
		Action:          coreParticipantAction(event),
		SessionID:       strings.TrimSpace(participant.SessionID),
		ParentTurnID:    strings.TrimSpace(participant.ParentTurnID),
		DelegationID:    strings.TrimSpace(participant.DelegationID),
		Scope:           coreEventScope(event),
	}
}

func coreParticipantAction(event coresession.Event) kernel.ParticipantAction {
	action := strings.ToLower(strings.TrimSpace(coreEventMetaString(event.Meta, "action")))
	switch action {
	case "attached":
		return kernel.ParticipantActionAttached
	case "detached":
		return kernel.ParticipantActionDetached
	default:
		return ""
	}
}

func coreEventMetaString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	value, _ := meta[strings.TrimSpace(key)].(string)
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
