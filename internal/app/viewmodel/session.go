// Package viewmodel defines surface-neutral DTOs shared by the TUI and future
// APP. It projects canonical session events without depending on UI packages.
package viewmodel

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
	coretool "github.com/OnslaughtSnail/caelis/core/tool"
)

type SessionView struct {
	Ref              session.Ref         `json:"ref"`
	Title            string              `json:"title,omitempty"`
	Workspace        session.Workspace   `json:"workspace,omitempty"`
	Status           string              `json:"status,omitempty"`
	UpdatedAt        time.Time           `json:"updated_at,omitempty"`
	Transcript       []TranscriptItem    `json:"transcript,omitempty"`
	Plan             []session.PlanEntry `json:"plan,omitempty"`
	PendingApprovals []ApprovalItem      `json:"pending_approvals,omitempty"`
	Participants     []ParticipantItem   `json:"participants,omitempty"`
}

type ResumePanelView struct {
	Workspace  session.Workspace   `json:"workspace,omitempty"`
	Search     string              `json:"search,omitempty"`
	Count      int                 `json:"count,omitempty"`
	Sessions   []ResumeSessionItem `json:"sessions,omitempty"`
	NextCursor session.Cursor      `json:"next_cursor,omitempty"`
}

type ResumeSessionItem struct {
	Ref         session.Ref `json:"ref"`
	SessionID   string      `json:"session_id,omitempty"`
	Title       string      `json:"title,omitempty"`
	Workspace   string      `json:"workspace,omitempty"`
	EventCount  int         `json:"event_count,omitempty"`
	UpdatedAt   time.Time   `json:"updated_at,omitempty"`
	LastEventAt time.Time   `json:"last_event_at,omitempty"`
	Command     string      `json:"command,omitempty"`
}

type TranscriptItem struct {
	ID          string             `json:"id,omitempty"`
	Type        string             `json:"type,omitempty"`
	Actor       string             `json:"actor,omitempty"`
	Text        string             `json:"text,omitempty"`
	Time        time.Time          `json:"time,omitempty"`
	TurnID      string             `json:"turn_id,omitempty"`
	ToolName    string             `json:"tool_name,omitempty"`
	ToolStatus  string             `json:"tool_status,omitempty"`
	Participant string             `json:"participant,omitempty"`
	Controller  string             `json:"controller,omitempty"`
	Actions     []TranscriptAction `json:"actions,omitempty"`
}

type TranscriptAction struct {
	ID            string `json:"id,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Label         string `json:"label,omitempty"`
	Command       string `json:"command,omitempty"`
	TargetID      string `json:"target_id,omitempty"`
	Enabled       bool   `json:"enabled"`
	Destructive   bool   `json:"destructive,omitempty"`
	RequiresInput bool   `json:"requires_input,omitempty"`
}

type ApprovalItem struct {
	ID                 string                   `json:"id,omitempty"`
	EventID            string                   `json:"event_id,omitempty"`
	TurnID             string                   `json:"turn_id,omitempty"`
	Tool               string                   `json:"tool,omitempty"`
	Command            string                   `json:"command,omitempty"`
	Status             string                   `json:"status,omitempty"`
	Reason             string                   `json:"reason,omitempty"`
	Justification      string                   `json:"justification,omitempty"`
	SandboxPermissions string                   `json:"sandbox_permissions,omitempty"`
	Risk               string                   `json:"risk,omitempty"`
	Options            []session.ApprovalOption `json:"options,omitempty"`
	Actions            []ApprovalAction         `json:"actions,omitempty"`
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
		if event.Type == session.EventPlan {
			view.Plan = append([]session.PlanEntry(nil), event.Plan...)
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
	item.Actions = TranscriptActionsFromEvent(event)
	switch event.Type {
	case session.EventUser, session.EventAssistant, session.EventSystem, session.EventToolCall, session.EventToolResult, session.EventApproval, session.EventPlan, session.EventLifecycle, session.EventParticipant, session.EventHandoff, session.EventNotice:
		return item, item.Text != "" || item.ToolName != "" || event.Approval != nil || len(event.Plan) > 0 || event.Lifecycle != nil
	default:
		return TranscriptItem{}, false
	}
}

func TranscriptActionsFromEvent(event session.Event) []TranscriptAction {
	if event.Type != session.EventToolCall && event.Type != session.EventToolResult {
		return nil
	}
	taskMeta := coretool.RuntimeTaskMeta(transcriptEventMeta(event))
	if len(taskMeta) == 0 {
		return nil
	}
	taskID := firstNonEmpty(anyString(taskMeta["task_id"]), anyString(taskMeta["id"]), anyString(taskMeta["target_id"]))
	if taskID == "" {
		return nil
	}
	running := anyBool(taskMeta["running"]) || taskStateRunning(anyString(taskMeta["state"]))
	actions := []TranscriptAction{
		transcriptTaskAction("tail", "Tail", taskID, "/task tail "+taskID, false, false),
	}
	if running {
		actions = append(actions,
			transcriptTaskAction("wait", "Wait", taskID, "/task wait "+taskID, false, false),
			transcriptTaskAction("cancel", "Cancel", taskID, "/task cancel "+taskID, true, false),
		)
		if anyBool(taskMeta["supports_input"]) || anyBool(taskMeta["input_supported"]) {
			actions = append(actions, transcriptTaskAction("write", "Write", taskID, "/task write "+taskID+" -- ", false, true))
		}
		return actions
	}
	return append(actions, transcriptTaskAction("release", "Release", taskID, "/task release "+taskID, false, false))
}

func transcriptTaskAction(kind string, label string, taskID string, command string, destructive bool, requiresInput bool) TranscriptAction {
	return TranscriptAction{
		ID:            "task." + strings.TrimSpace(kind) + ":" + strings.TrimSpace(taskID),
		Kind:          strings.TrimSpace(kind),
		Label:         strings.TrimSpace(label),
		Command:       command,
		TargetID:      strings.TrimSpace(taskID),
		Enabled:       true,
		Destructive:   destructive,
		RequiresInput: requiresInput,
	}
}

func taskStateRunning(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running", "waiting":
		return true
	default:
		return false
	}
}

func transcriptEventMeta(event session.Event) map[string]any {
	out := cloneStringAnyMap(event.Meta)
	if event.Tool == nil || len(event.Tool.Meta) == 0 {
		return out
	}
	for key, value := range event.Tool.Meta {
		out[key] = value
	}
	return out
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
	item.Actions = ApprovalActionsFromOptions(item.Options)
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
		item.Justification = inputString(tool.Input, "justification")
		item.SandboxPermissions = inputString(tool.Input, "sandbox_permissions")
		item.Risk = firstNonEmpty(inputString(tool.Input, "risk"), inputString(tool.Input, "risk_level"))
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

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func anyBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func cloneStringAnyMap(values map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range values {
		out[key] = value
	}
	return out
}

func inputString(input map[string]any, key string) string {
	if len(input) == 0 {
		return ""
	}
	value, _ := input[strings.TrimSpace(key)].(string)
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
