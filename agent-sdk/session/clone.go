package session

import (
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
)

// NormalizeSessionRef returns one normalized session ref.
func NormalizeSessionRef(ref SessionRef) SessionRef {
	return SessionRef{
		AppName:      strings.TrimSpace(ref.AppName),
		UserID:       strings.TrimSpace(ref.UserID),
		SessionID:    strings.TrimSpace(ref.SessionID),
		WorkspaceKey: strings.TrimSpace(ref.WorkspaceKey),
	}
}

// CloneSession returns one deep copy of one session.
func CloneSession(in Session) Session {
	out := in
	out.SessionRef = NormalizeSessionRef(in.SessionRef)
	out.CWD = strings.TrimSpace(in.CWD)
	out.Title = strings.TrimSpace(in.Title)
	out.Metadata = jsonvalue.CloneMap(in.Metadata)
	out.Controller = CloneControllerBinding(in.Controller)
	out.Participants = CloneParticipantBindings(in.Participants)
	return out
}

// CloneEvent returns one deep copy of one event.
func CloneEvent(in *Event) *Event {
	if in == nil {
		return nil
	}
	out := *in
	out.ID = strings.TrimSpace(in.ID)
	out.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	out.SessionID = strings.TrimSpace(in.SessionID)
	out.Text = in.Text
	out.Meta = cloneProtocolAnyMap(in.Meta)
	out.Actor = CloneActorRef(in.Actor)
	if in.Invocation != nil {
		invocation := CloneEventInvocation(*in.Invocation)
		out.Invocation = &invocation
	}
	if in.Scope != nil {
		scope := CloneEventScope(*in.Scope)
		out.Scope = &scope
	}
	if in.ChildOrigin != nil {
		origin := CloneEventChildOrigin(*in.ChildOrigin)
		out.ChildOrigin = &origin
	}
	if in.PlanPayload != nil {
		payload := cloneEventPlanPayload(*in.PlanPayload)
		out.PlanPayload = &payload
	}
	if in.Notice != nil {
		notice := *in.Notice
		notice.Level = strings.TrimSpace(strings.ToLower(notice.Level))
		notice.Text = strings.TrimSpace(notice.Text)
		notice.Kind = strings.TrimSpace(notice.Kind)
		notice.Meta = cloneProtocolAnyMap(notice.Meta)
		out.Notice = &notice
	}
	if in.Lifecycle != nil {
		lifecycle := *in.Lifecycle
		lifecycle.Status = strings.TrimSpace(lifecycle.Status)
		lifecycle.Reason = strings.TrimSpace(lifecycle.Reason)
		lifecycle.Meta = cloneProtocolAnyMap(lifecycle.Meta)
		out.Lifecycle = &lifecycle
	}
	if in.Journal != nil {
		journal := CloneExecutionJournalEntry(*in.Journal)
		out.Journal = &journal
	}
	if in.Protocol != nil {
		protocol := CloneEventProtocol(*in.Protocol)
		out.Protocol = &protocol
	}
	if in.Message != nil {
		message := model.CloneMessage(*in.Message)
		out.Message = &message
	}
	if in.Tool != nil {
		tool := CloneEventTool(*in.Tool)
		out.Tool = &tool
	}
	return &out
}

// CloneEvents returns one deep copy of one event list.
func CloneEvents(events []*Event) []*Event {
	out := make([]*Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		out = append(out, CloneEvent(event))
	}
	return out
}

// FilterEvents returns one filtered event slice for one history query.
func FilterEvents(events []*Event, limit int, includeTransient bool) []*Event {
	filtered := make([]*Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if !includeTransient && !IsCanonicalHistoryEvent(event) {
			continue
		}
		filtered = append(filtered, CanonicalizeEvent(event))
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return filtered
}

// CloneState returns one recursively isolated copy of one session state map.
func CloneState(state map[string]any) map[string]any {
	return jsonvalue.CloneMap(state)
}

// CloneControllerBinding returns one normalized controller binding copy.
func CloneControllerBinding(in ControllerBinding) ControllerBinding {
	return ControllerBinding{
		Kind:            in.Kind,
		ControllerID:    strings.TrimSpace(in.ControllerID),
		AgentName:       strings.TrimSpace(in.AgentName),
		Label:           strings.TrimSpace(in.Label),
		EpochID:         strings.TrimSpace(in.EpochID),
		RemoteSessionID: strings.TrimSpace(in.RemoteSessionID),
		ContextSyncSeq:  in.ContextSyncSeq,
		AttachedAt:      in.AttachedAt,
		Source:          strings.TrimSpace(in.Source),
	}
}

// CloneParticipantBinding returns one normalized participant binding copy.
func CloneParticipantBinding(in ParticipantBinding) ParticipantBinding {
	return ParticipantBinding{
		ID:                   strings.TrimSpace(in.ID),
		Kind:                 in.Kind,
		Role:                 in.Role,
		AgentName:            strings.TrimSpace(in.AgentName),
		Label:                strings.TrimSpace(in.Label),
		Placement:            placement.Normalize(in.Placement),
		SessionID:            strings.TrimSpace(in.SessionID),
		Source:               strings.TrimSpace(in.Source),
		ParentTurnID:         strings.TrimSpace(in.ParentTurnID),
		DelegationID:         strings.TrimSpace(in.DelegationID),
		AttachmentGeneration: strings.TrimSpace(in.AttachmentGeneration),
		ContextSyncSeq:       in.ContextSyncSeq,
		AttachedAt:           in.AttachedAt,
		ControllerRef:        strings.TrimSpace(in.ControllerRef),
	}
}

// CloneParticipantBindings returns one normalized participant binding list.
func CloneParticipantBindings(in []ParticipantBinding) []ParticipantBinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]ParticipantBinding, 0, len(in))
	for _, item := range in {
		out = append(out, CloneParticipantBinding(item))
	}
	return out
}

// CloneActorRef returns one normalized actor ref copy.
func CloneActorRef(in ActorRef) ActorRef {
	return ActorRef{
		Kind: in.Kind,
		ID:   strings.TrimSpace(in.ID),
		Role: strings.TrimSpace(in.Role),
		Name: strings.TrimSpace(in.Name),
	}
}

// CloneEventInvocation returns one normalized invocation context copy.
func CloneEventInvocation(in EventInvocation) EventInvocation {
	return EventInvocation{
		Provider:            strings.TrimSpace(in.Provider),
		Model:               strings.TrimSpace(in.Model),
		ContextWindowTokens: in.ContextWindowTokens,
	}
}

// CloneEventScope returns one normalized event scope copy.
func CloneEventScope(in EventScope) EventScope {
	return EventScope{
		TurnID:   strings.TrimSpace(in.TurnID),
		Source:   strings.TrimSpace(in.Source),
		Executor: CloneActorRef(in.Executor),
		Controller: ControllerRef{
			Kind:    in.Controller.Kind,
			ID:      strings.TrimSpace(in.Controller.ID),
			EpochID: strings.TrimSpace(in.Controller.EpochID),
		},
		Participant: ParticipantRef{
			ID:           strings.TrimSpace(in.Participant.ID),
			Kind:         in.Participant.Kind,
			Role:         in.Participant.Role,
			DelegationID: strings.TrimSpace(in.Participant.DelegationID),
		},
		ACP: ACPRef{
			SessionID: strings.TrimSpace(in.ACP.SessionID),
			EventType: strings.TrimSpace(in.ACP.EventType),
		},
	}
}

// CloneEventTool returns one normalized copy of a durable tool payload.
func CloneEventTool(in EventTool) EventTool {
	out := EventTool{
		ID:     strings.TrimSpace(in.ID),
		Name:   strings.TrimSpace(in.Name),
		Kind:   strings.TrimSpace(in.Kind),
		Title:  strings.TrimSpace(in.Title),
		Status: strings.TrimSpace(in.Status),
		Input:  jsonvalue.CloneMap(in.Input),
		Output: jsonvalue.CloneMap(in.Output),
	}
	if len(in.Content) > 0 {
		out.Content = make([]EventToolContent, 0, len(in.Content))
		for _, item := range in.Content {
			var oldText *string
			if item.OldText != nil {
				value := *item.OldText
				oldText = &value
			}
			out.Content = append(out.Content, EventToolContent{
				Type:       strings.TrimSpace(item.Type),
				Text:       item.Text,
				TerminalID: strings.TrimSpace(item.TerminalID),
				Path:       strings.TrimSpace(item.Path),
				OldText:    oldText,
				NewText:    item.NewText,
			})
		}
	}
	if len(in.Locations) > 0 {
		out.Locations = make([]EventToolLocation, 0, len(in.Locations))
		for _, item := range in.Locations {
			var line *int
			if item.Line != nil {
				value := *item.Line
				line = &value
			}
			out.Locations = append(out.Locations, EventToolLocation{
				Path: strings.TrimSpace(item.Path),
				Line: line,
			})
		}
	}
	return out
}

// CloneSessionSummaries returns one copy of one session summary slice.
func CloneSessionSummaries(items []SessionSummary) []SessionSummary {
	if len(items) == 0 {
		return nil
	}
	out := slices.Clone(items)
	for i := range out {
		out[i].SessionRef = NormalizeSessionRef(out[i].SessionRef)
		out[i].CWD = strings.TrimSpace(out[i].CWD)
		out[i].Title = strings.TrimSpace(out[i].Title)
		out[i].Metadata = CloneState(out[i].Metadata)
	}
	return out
}
