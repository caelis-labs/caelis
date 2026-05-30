// Package session defines durable canonical session and event contracts.
package session

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
)

var (
	ErrNotFound = errors.New("core/session: session not found")
	ErrInvalid  = errors.New("core/session: invalid session request")
)

type Ref struct {
	AppName      string `json:"app_name,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	WorkspaceKey string `json:"workspace_key,omitempty"`
}

type Workspace struct {
	Key string `json:"key,omitempty"`
	CWD string `json:"cwd,omitempty"`
}

type ControllerKind string

const (
	ControllerBuiltin ControllerKind = "builtin"
	ControllerACP     ControllerKind = "acp"
)

type ParticipantKind string

const (
	ParticipantACP      ParticipantKind = "acp"
	ParticipantSubagent ParticipantKind = "subagent"
)

type ParticipantRole string

const (
	ParticipantSidecar   ParticipantRole = "sidecar"
	ParticipantDelegated ParticipantRole = "delegated"
	ParticipantObserver  ParticipantRole = "observer"
)

type ControllerBinding struct {
	Kind            ControllerKind `json:"kind,omitempty"`
	ID              string         `json:"id,omitempty"`
	AgentName       string         `json:"agent_name,omitempty"`
	Label           string         `json:"label,omitempty"`
	EpochID         string         `json:"epoch_id,omitempty"`
	RemoteSessionID string         `json:"remote_session_id,omitempty"`
	ContextSyncSeq  int            `json:"context_sync_seq,omitempty"`
	AttachedAt      time.Time      `json:"attached_at,omitempty"`
	Source          string         `json:"source,omitempty"`
}

type ParticipantBinding struct {
	ID             string          `json:"id,omitempty"`
	Kind           ParticipantKind `json:"kind,omitempty"`
	Role           ParticipantRole `json:"role,omitempty"`
	AgentName      string          `json:"agent_name,omitempty"`
	Label          string          `json:"label,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	Source         string          `json:"source,omitempty"`
	ParentTurnID   string          `json:"parent_turn_id,omitempty"`
	DelegationID   string          `json:"delegation_id,omitempty"`
	ContextSyncSeq int             `json:"context_sync_seq,omitempty"`
	AttachedAt     time.Time       `json:"attached_at,omitempty"`
	ControllerRef  string          `json:"controller_ref,omitempty"`
}

type Session struct {
	Ref
	Workspace    Workspace            `json:"workspace,omitempty"`
	Title        string               `json:"title,omitempty"`
	Meta         map[string]any       `json:"meta,omitempty"`
	Controller   ControllerBinding    `json:"controller,omitempty"`
	Participants []ParticipantBinding `json:"participants,omitempty"`
	CreatedAt    time.Time            `json:"created_at,omitempty"`
	UpdatedAt    time.Time            `json:"updated_at,omitempty"`
}

type State map[string]any

type Snapshot struct {
	Session Session `json:"session"`
	Events  []Event `json:"events,omitempty"`
	State   State   `json:"state,omitempty"`
	Cursor  Cursor  `json:"cursor,omitempty"`
}

type Cursor string

type StartRequest struct {
	AppName            string         `json:"app_name,omitempty"`
	UserID             string         `json:"user_id,omitempty"`
	Workspace          Workspace      `json:"workspace,omitempty"`
	PreferredSessionID string         `json:"preferred_session_id,omitempty"`
	Title              string         `json:"title,omitempty"`
	Meta               map[string]any `json:"meta,omitempty"`
}

type EventQuery struct {
	Ref              Ref    `json:"ref"`
	After            Cursor `json:"after,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	IncludeTransient bool   `json:"include_transient,omitempty"`
}

type EventPage struct {
	Events     []Event `json:"events,omitempty"`
	NextCursor Cursor  `json:"next_cursor,omitempty"`
}

type StatePatch func(State) (State, error)

type Store interface {
	Create(context.Context, StartRequest) (Session, error)
	Load(context.Context, Ref) (Snapshot, error)
	Append(context.Context, Ref, []Event) (Cursor, error)
	Events(context.Context, EventQuery) (EventPage, error)
	UpdateState(context.Context, Ref, StatePatch) error
}

type EventType string

const (
	EventUser        EventType = "user"
	EventAssistant   EventType = "assistant"
	EventSystem      EventType = "system"
	EventToolCall    EventType = "tool_call"
	EventToolResult  EventType = "tool_result"
	EventApproval    EventType = "approval"
	EventPlan        EventType = "plan"
	EventCompact     EventType = "compact"
	EventLifecycle   EventType = "lifecycle"
	EventParticipant EventType = "participant"
	EventHandoff     EventType = "handoff"
	EventNotice      EventType = "notice"
)

type Visibility string

const (
	VisibilityCanonical Visibility = "canonical"
	VisibilityUIOnly    Visibility = "ui_only"
	VisibilityOverlay   Visibility = "overlay"
)

type ActorKind string

const (
	ActorUser        ActorKind = "user"
	ActorController  ActorKind = "controller"
	ActorParticipant ActorKind = "participant"
	ActorTool        ActorKind = "tool"
	ActorSystem      ActorKind = "system"
)

type ActorRef struct {
	Kind ActorKind `json:"kind,omitempty"`
	ID   string    `json:"id,omitempty"`
	Role string    `json:"role,omitempty"`
	Name string    `json:"name,omitempty"`
}

type EventScope struct {
	TurnID      string             `json:"turn_id,omitempty"`
	Source      string             `json:"source,omitempty"`
	Controller  ControllerBinding  `json:"controller,omitempty"`
	Participant ParticipantBinding `json:"participant,omitempty"`
	ACP         ACPRef             `json:"acp,omitempty"`
}

type ACPRef struct {
	SessionID string `json:"session_id,omitempty"`
	EventType string `json:"event_type,omitempty"`
}

type ToolStatus string

const (
	ToolStarted         ToolStatus = "started"
	ToolRunning         ToolStatus = "running"
	ToolWaitingApproval ToolStatus = "waiting_approval"
	ToolCompleted       ToolStatus = "completed"
	ToolFailed          ToolStatus = "failed"
	ToolCancelled       ToolStatus = "cancelled"
)

type ToolEvent struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	Title     string         `json:"title,omitempty"`
	Status    ToolStatus     `json:"status,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Output    map[string]any `json:"output,omitempty"`
	Content   []ToolContent  `json:"content,omitempty"`
	Locations []ToolLocation `json:"locations,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

type ToolLocation struct {
	Path string `json:"path,omitempty"`
	Line *int   `json:"line,omitempty"`
}

type ToolContent struct {
	Type       string `json:"type,omitempty"`
	Text       string `json:"text,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
	Path       string `json:"path,omitempty"`
}

type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalRejected ApprovalStatus = "rejected"
)

type ApprovalOption struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

type ApprovalEvent struct {
	ID       string           `json:"id,omitempty"`
	Status   ApprovalStatus   `json:"status,omitempty"`
	Tool     *ToolEvent       `json:"tool,omitempty"`
	Options  []ApprovalOption `json:"options,omitempty"`
	Decision string           `json:"decision,omitempty"`
	Reason   string           `json:"reason,omitempty"`
}

type PlanEntry struct {
	Content string `json:"content,omitempty"`
	Status  string `json:"status,omitempty"`
}

type LifecycleStatus string

const (
	LifecycleRunning         LifecycleStatus = "running"
	LifecycleWaitingApproval LifecycleStatus = "waiting_approval"
	LifecycleCompleted       LifecycleStatus = "completed"
	LifecycleFailed          LifecycleStatus = "failed"
	LifecycleCancelled       LifecycleStatus = "cancelled"
)

type LifecycleEvent struct {
	Status LifecycleStatus `json:"status,omitempty"`
	Reason string          `json:"reason,omitempty"`
	Meta   map[string]any  `json:"meta,omitempty"`
}

type Event struct {
	ID         string          `json:"id,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	Type       EventType       `json:"type,omitempty"`
	Visibility Visibility      `json:"visibility,omitempty"`
	Time       time.Time       `json:"time,omitempty"`
	Actor      ActorRef        `json:"actor,omitempty"`
	Scope      *EventScope     `json:"scope,omitempty"`
	Message    *model.Message  `json:"message,omitempty"`
	Tool       *ToolEvent      `json:"tool,omitempty"`
	Approval   *ApprovalEvent  `json:"approval,omitempty"`
	Plan       []PlanEntry     `json:"plan,omitempty"`
	Lifecycle  *LifecycleEvent `json:"lifecycle,omitempty"`
	Meta       map[string]any  `json:"meta,omitempty"`
}

func NormalizeRef(in Ref) Ref {
	return Ref{
		AppName:      strings.TrimSpace(in.AppName),
		UserID:       strings.TrimSpace(in.UserID),
		SessionID:    strings.TrimSpace(in.SessionID),
		WorkspaceKey: strings.TrimSpace(in.WorkspaceKey),
	}
}

func CloneSession(in Session) Session {
	out := in
	out.Ref = NormalizeRef(in.Ref)
	out.Workspace.Key = strings.TrimSpace(in.Workspace.Key)
	out.Workspace.CWD = strings.TrimSpace(in.Workspace.CWD)
	out.Title = strings.TrimSpace(in.Title)
	out.Meta = maps.Clone(in.Meta)
	out.Participants = slices.Clone(in.Participants)
	return out
}

func CloneEvent(in Event) Event {
	out := in
	out.Meta = maps.Clone(in.Meta)
	if in.Scope != nil {
		scope := *in.Scope
		out.Scope = &scope
	}
	if in.Message != nil {
		message := model.CloneMessage(*in.Message)
		out.Message = &message
	}
	if in.Tool != nil {
		tool := CloneToolEvent(*in.Tool)
		out.Tool = &tool
	}
	if in.Approval != nil {
		approval := *in.Approval
		if in.Approval.Tool != nil {
			tool := CloneToolEvent(*in.Approval.Tool)
			approval.Tool = &tool
		}
		approval.Options = slices.Clone(in.Approval.Options)
		out.Approval = &approval
	}
	out.Plan = slices.Clone(in.Plan)
	if in.Lifecycle != nil {
		lifecycle := *in.Lifecycle
		lifecycle.Meta = maps.Clone(in.Lifecycle.Meta)
		out.Lifecycle = &lifecycle
	}
	return out
}

func CloneToolEvent(in ToolEvent) ToolEvent {
	out := in
	out.Input = maps.Clone(in.Input)
	out.Output = maps.Clone(in.Output)
	out.Content = slices.Clone(in.Content)
	out.Locations = slices.Clone(in.Locations)
	out.Meta = maps.Clone(in.Meta)
	return out
}

func IsTransient(event Event) bool {
	return event.Visibility == VisibilityUIOnly || event.Visibility == VisibilityOverlay
}

func EventText(event Event) string {
	if event.Message != nil {
		if text := event.Message.TextContent(); text != "" {
			return text
		}
	}
	if event.Tool != nil {
		for _, item := range event.Tool.Content {
			if text := strings.TrimSpace(item.Text); text != "" {
				return text
			}
		}
	}
	if event.Lifecycle != nil {
		return strings.TrimSpace(event.Lifecycle.Reason)
	}
	return ""
}
