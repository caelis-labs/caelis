// Package viewmodel defines surface-neutral DTOs shared by the TUI and future
// APP. It projects canonical session events without depending on UI packages.
package viewmodel

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
)

type SessionView struct {
	Ref              session.Ref       `json:"ref"`
	Title            string            `json:"title,omitempty"`
	Workspace        session.Workspace `json:"workspace,omitempty"`
	Status           string            `json:"status,omitempty"`
	UpdatedAt        time.Time         `json:"updated_at,omitempty"`
	Transcript       []TranscriptItem  `json:"transcript,omitempty"`
	PendingApprovals []ApprovalItem    `json:"pending_approvals,omitempty"`
	Participants     []ParticipantItem `json:"participants,omitempty"`
}

type TranscriptItem struct {
	ID          string    `json:"id,omitempty"`
	Type        string    `json:"type,omitempty"`
	Actor       string    `json:"actor,omitempty"`
	Text        string    `json:"text,omitempty"`
	Time        time.Time `json:"time,omitempty"`
	TurnID      string    `json:"turn_id,omitempty"`
	ToolName    string    `json:"tool_name,omitempty"`
	ToolStatus  string    `json:"tool_status,omitempty"`
	Participant string    `json:"participant,omitempty"`
	Controller  string    `json:"controller,omitempty"`
}

type ApprovalItem struct {
	ID      string                   `json:"id,omitempty"`
	EventID string                   `json:"event_id,omitempty"`
	TurnID  string                   `json:"turn_id,omitempty"`
	Tool    string                   `json:"tool,omitempty"`
	Command string                   `json:"command,omitempty"`
	Status  string                   `json:"status,omitempty"`
	Reason  string                   `json:"reason,omitempty"`
	Options []session.ApprovalOption `json:"options,omitempty"`
}

type ParticipantItem struct {
	ID        string `json:"id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Role      string `json:"role,omitempty"`
	Name      string `json:"name,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Source    string `json:"source,omitempty"`
}

func FromSnapshot(snapshot session.Snapshot) SessionView {
	active := session.CloneSession(snapshot.Session)
	view := SessionView{
		Ref:       active.Ref,
		Title:     strings.TrimSpace(active.Title),
		Workspace: active.Workspace,
		Status:    "idle",
		UpdatedAt: active.UpdatedAt,
	}
	for _, participant := range active.Participants {
		view.Participants = append(view.Participants, participantItem(participant))
	}
	seenParticipants := map[string]struct{}{}
	for _, item := range view.Participants {
		if item.ID != "" {
			seenParticipants[item.ID] = struct{}{}
		}
	}
	for _, event := range snapshot.Events {
		if session.IsTransient(event) {
			continue
		}
		if event.Scope != nil && strings.TrimSpace(event.Scope.Participant.ID) != "" {
			item := participantItem(event.Scope.Participant)
			if _, ok := seenParticipants[item.ID]; !ok {
				view.Participants = append(view.Participants, item)
				seenParticipants[item.ID] = struct{}{}
			}
		}
		if item, ok := transcriptItem(event); ok {
			view.Transcript = append(view.Transcript, item)
		}
		if approval := pendingApproval(event); approval != nil {
			view.PendingApprovals = append(view.PendingApprovals, *approval)
			view.Status = "waiting_approval"
			continue
		}
		if event.Lifecycle != nil {
			view.Status = strings.TrimSpace(string(event.Lifecycle.Status))
		}
	}
	return view
}

func transcriptItem(event session.Event) (TranscriptItem, bool) {
	text := session.EventText(event)
	item := TranscriptItem{
		ID:    strings.TrimSpace(event.ID),
		Type:  strings.TrimSpace(string(event.Type)),
		Actor: actorName(event.Actor),
		Text:  text,
		Time:  event.Time,
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
	switch event.Type {
	case session.EventUser, session.EventAssistant, session.EventSystem, session.EventToolCall, session.EventToolResult, session.EventApproval, session.EventPlan, session.EventLifecycle, session.EventParticipant, session.EventHandoff, session.EventNotice:
		return item, item.Text != "" || item.ToolName != "" || event.Approval != nil || len(event.Plan) > 0 || event.Lifecycle != nil
	default:
		return TranscriptItem{}, false
	}
}

func pendingApproval(event session.Event) *ApprovalItem {
	if event.Approval == nil || event.Approval.Status != session.ApprovalPending {
		return nil
	}
	item := ApprovalItem{
		ID:      strings.TrimSpace(event.Approval.ID),
		EventID: strings.TrimSpace(event.ID),
		Status:  strings.TrimSpace(string(event.Approval.Status)),
		Reason:  strings.TrimSpace(event.Approval.Reason),
		Options: append([]session.ApprovalOption(nil), event.Approval.Options...),
	}
	if event.Scope != nil {
		item.TurnID = strings.TrimSpace(event.Scope.TurnID)
	}
	tool := event.Approval.Tool
	if tool == nil {
		tool = event.Tool
	}
	if tool != nil {
		item.Tool = strings.TrimSpace(tool.Name)
		item.Command = commandText(tool.Input)
	}
	return &item
}

func participantItem(binding session.ParticipantBinding) ParticipantItem {
	return ParticipantItem{
		ID:        strings.TrimSpace(binding.ID),
		Kind:      strings.TrimSpace(string(binding.Kind)),
		Role:      strings.TrimSpace(string(binding.Role)),
		Name:      firstNonEmpty(binding.Label, binding.AgentName, binding.ID),
		SessionID: strings.TrimSpace(binding.SessionID),
		Source:    strings.TrimSpace(binding.Source),
	}
}

func actorName(actor session.ActorRef) string {
	return firstNonEmpty(actor.Name, actor.ID, string(actor.Kind))
}

func commandText(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	if value, ok := input["command"].(string); ok {
		return strings.TrimSpace(value)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return string(raw)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
