package viewmodel

import (
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
)

type SessionEventEnvelope struct {
	Cursor      string              `json:"cursor,omitempty"`
	Error       string              `json:"error,omitempty"`
	Event       *SessionEventItem   `json:"event,omitempty"`
	Transcript  *TranscriptItem     `json:"transcript,omitempty"`
	Approval    *ApprovalItem       `json:"approval,omitempty"`
	Participant *ParticipantItem    `json:"participant,omitempty"`
	Lifecycle   *LifecycleItem      `json:"lifecycle,omitempty"`
	Plan        []session.PlanEntry `json:"plan,omitempty"`
	Canonical   *session.Event      `json:"canonical,omitempty"`
}

type SessionEventItem struct {
	ID          string    `json:"id,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	Type        string    `json:"type,omitempty"`
	Visibility  string    `json:"visibility,omitempty"`
	Actor       string    `json:"actor,omitempty"`
	Text        string    `json:"text,omitempty"`
	Time        time.Time `json:"time,omitempty"`
	TurnID      string    `json:"turn_id,omitempty"`
	ToolName    string    `json:"tool_name,omitempty"`
	ToolStatus  string    `json:"tool_status,omitempty"`
	Participant string    `json:"participant,omitempty"`
	Controller  string    `json:"controller,omitempty"`
}

type LifecycleItem struct {
	Status string `json:"status,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func EventEnvelopeFromSession(cursor string, event session.Event) SessionEventEnvelope {
	event = session.CloneEvent(event)
	out := SessionEventEnvelope{
		Cursor:    strings.TrimSpace(cursor),
		Event:     eventItem(event),
		Canonical: &event,
	}
	if out.Cursor == "" {
		out.Cursor = strings.TrimSpace(event.ID)
	}
	if item, ok := transcriptItem(event); ok {
		out.Transcript = &item
	}
	if approval := pendingApproval(event); approval != nil {
		out.Approval = approval
	}
	if event.Scope != nil && strings.TrimSpace(event.Scope.Participant.ID) != "" {
		item := participantItem(event.Scope.Participant)
		out.Participant = &item
	}
	if event.Type == session.EventPlan {
		out.Plan = append([]session.PlanEntry(nil), event.Plan...)
	}
	if event.Lifecycle != nil {
		out.Lifecycle = &LifecycleItem{
			Status: strings.TrimSpace(string(event.Lifecycle.Status)),
			Reason: strings.TrimSpace(event.Lifecycle.Reason),
		}
	}
	return out
}

func EventEnvelopeFromError(err string) SessionEventEnvelope {
	return SessionEventEnvelope{Error: strings.TrimSpace(err)}
}

func CloneSessionEventEnvelope(in SessionEventEnvelope) SessionEventEnvelope {
	out := in
	if in.Event != nil {
		event := *in.Event
		out.Event = &event
	}
	if in.Transcript != nil {
		transcript := *in.Transcript
		transcript.Actions = append([]TranscriptAction(nil), in.Transcript.Actions...)
		out.Transcript = &transcript
	}
	if in.Approval != nil {
		approval := *in.Approval
		approval.Options = append([]session.ApprovalOption(nil), in.Approval.Options...)
		approval.Actions = append([]ApprovalAction(nil), in.Approval.Actions...)
		out.Approval = &approval
	}
	if in.Participant != nil {
		participant := *in.Participant
		out.Participant = &participant
	}
	if in.Lifecycle != nil {
		lifecycle := *in.Lifecycle
		out.Lifecycle = &lifecycle
	}
	out.Plan = append([]session.PlanEntry(nil), in.Plan...)
	if in.Canonical != nil {
		canonical := session.CloneEvent(*in.Canonical)
		out.Canonical = &canonical
	}
	return out
}

func eventItem(event session.Event) *SessionEventItem {
	item := &SessionEventItem{
		ID:         strings.TrimSpace(event.ID),
		SessionID:  strings.TrimSpace(event.SessionID),
		Type:       strings.TrimSpace(string(event.Type)),
		Visibility: strings.TrimSpace(string(event.Visibility)),
		Actor:      actorName(event.Actor),
		Text:       session.EventText(event),
		Time:       event.Time,
	}
	if event.Scope != nil {
		item.TurnID = strings.TrimSpace(event.Scope.TurnID)
		item.Controller = firstNonEmpty(event.Scope.Controller.Label, event.Scope.Controller.AgentName, event.Scope.Controller.ID)
		item.Participant = firstNonEmpty(event.Scope.Participant.Label, event.Scope.Participant.AgentName, event.Scope.Participant.ID)
	}
	if event.Tool != nil {
		item.ToolName = strings.TrimSpace(event.Tool.Name)
		item.ToolStatus = strings.TrimSpace(string(event.Tool.Status))
	}
	return item
}
