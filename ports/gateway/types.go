package gateway

import (
	"context"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/approval"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
)

type BeginTurnRequest struct {
	SessionRef   session.SessionRef
	Input        string
	ContentParts []model.ContentPart
	ModeName     string
	ModelHint    string
	Surface      string
	Metadata     map[string]any
	Request      agent.ModelRequestOptions
}

type TurnIntent = BeginTurnRequest

type StartSessionRequest struct {
	AppName            string
	UserID             string
	Workspace          session.WorkspaceRef
	PreferredSessionID string
	Title              string
	Metadata           map[string]any
	BindingKey         string
	Binding            BindingDescriptor
}

type LoadSessionRequest struct {
	SessionRef       session.SessionRef
	Limit            int
	IncludeTransient bool
	BindingKey       string
	Binding          BindingDescriptor
}

type ForkSessionRequest struct {
	SourceSessionRef   session.SessionRef
	PreferredSessionID string
	Title              string
	Metadata           map[string]any
	BindingKey         string
	Binding            BindingDescriptor
}

type ResumeSessionRequest struct {
	AppName          string
	UserID           string
	Workspace        session.WorkspaceRef
	SessionID        string
	ExcludeSessionID string
	Limit            int
	IncludeTransient bool
	BindingKey       string
	Binding          BindingDescriptor
}

type ListSessionsRequest struct {
	AppName      string
	UserID       string
	WorkspaceKey string
	Cursor       string
	Limit        int
}

type InterruptRequest struct {
	SessionRef session.SessionRef
	BindingKey string
	Reason     string
}

type BindingDescriptor struct {
	Surface   string    `json:"surface,omitempty"`
	ActorKind string    `json:"actor_kind,omitempty"`
	ActorID   string    `json:"actor_id,omitempty"`
	Owner     string    `json:"owner,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

type BindSessionRequest struct {
	SessionRef session.SessionRef `json:"session_ref"`
	BindingKey string             `json:"binding_key,omitempty"`
	Binding    BindingDescriptor  `json:"binding,omitempty"`
}

type ReplayEventsRequest struct {
	SessionRef session.SessionRef `json:"session_ref"`
	BindingKey string             `json:"binding_key,omitempty"`
	// Cursor accepts a durable replay cursor/projection_id or a legacy source
	// session event id. Live eventstream cursors are stream-local; clients
	// bridging from live to replay should pass projection_id when present.
	Cursor string `json:"cursor,omitempty"`
	// Limit caps source session events, not projected envelopes, so one source
	// event may still expand to multiple semantic ACP envelopes.
	Limit            int  `json:"limit,omitempty"`
	IncludeTransient bool `json:"include_transient,omitempty"`
}

type HandoffControllerRequest struct {
	SessionRef session.SessionRef
	BindingKey string
	Kind       session.ControllerKind
	Agent      string
	Source     string
	Reason     string
}

// AttachParticipantRequest attaches one ACP-backed participant to the current
// session control plane without replacing the main controller.
type AttachParticipantRequest struct {
	SessionRef session.SessionRef
	BindingKey string
	Agent      string
	Role       session.ParticipantRole
	Source     string
	Label      string
}

// DetachParticipantRequest removes one attached participant from the current
// session control plane.
type DetachParticipantRequest struct {
	SessionRef    session.SessionRef
	BindingKey    string
	ParticipantID string
	Source        string
}

type PromptParticipantRequest struct {
	SessionRef    session.SessionRef
	BindingKey    string
	ParticipantID string
	Input         string
	DisplayInput  string
	DisplayTitle  string
	ContentParts  []model.ContentPart
	Source        string
}

type ControlPlaneStateRequest struct {
	SessionRef session.SessionRef
	BindingKey string
}

type BindingStateRequest struct {
	BindingKey string `json:"binding_key,omitempty"`
}

type ActiveTurnState struct {
	SessionRef session.SessionRef `json:"session_ref"`
	Kind       ActiveTurnKind     `json:"kind,omitempty"`
	HandleID   string             `json:"handle_id,omitempty"`
	RunID      string             `json:"run_id,omitempty"`
	TurnID     string             `json:"turn_id,omitempty"`
	StartedAt  time.Time          `json:"started_at,omitempty"`
}

type ActiveTurnKind string

const (
	ActiveTurnKindKernel      ActiveTurnKind = "kernel"
	ActiveTurnKindParticipant ActiveTurnKind = "participant"
)

type ControllerState struct {
	Kind            session.ControllerKind `json:"kind,omitempty"`
	ControllerID    string                 `json:"controller_id,omitempty"`
	AgentName       string                 `json:"agent_name,omitempty"`
	Label           string                 `json:"label,omitempty"`
	EpochID         string                 `json:"epoch_id,omitempty"`
	RemoteSessionID string                 `json:"remote_session_id,omitempty"`
	ContextSyncSeq  int                    `json:"context_sync_seq,omitempty"`
	AttachedAt      time.Time              `json:"attached_at,omitempty"`
	Source          string                 `json:"source,omitempty"`
}

type ParticipantState struct {
	ID             string                  `json:"id,omitempty"`
	Kind           session.ParticipantKind `json:"kind,omitempty"`
	Role           session.ParticipantRole `json:"role,omitempty"`
	AgentName      string                  `json:"agent_name,omitempty"`
	Label          string                  `json:"label,omitempty"`
	SessionID      string                  `json:"session_id,omitempty"`
	Source         string                  `json:"source,omitempty"`
	ParentTurnID   string                  `json:"parent_turn_id,omitempty"`
	DelegationID   string                  `json:"delegation_id,omitempty"`
	ContextSyncSeq int                     `json:"context_sync_seq,omitempty"`
	AttachedAt     time.Time               `json:"attached_at,omitempty"`
	ControllerRef  string                  `json:"controller_ref,omitempty"`
}

type ACPProjectionState struct {
	Cursor    string `json:"cursor,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	EventType string `json:"event_type,omitempty"`
}

type ContinuityState struct {
	LastEventCursor    string             `json:"last_event_cursor,omitempty"`
	ControllerCursor   string             `json:"controller_cursor,omitempty"`
	ParticipantCursors map[string]string  `json:"participant_cursors,omitempty"`
	ACPProjection      ACPProjectionState `json:"acp_projection,omitempty"`
}

type ControlPlaneState struct {
	SessionRef    session.SessionRef `json:"session_ref"`
	Controller    ControllerState    `json:"controller"`
	Participants  []ParticipantState `json:"participants,omitempty"`
	Continuity    ContinuityState    `json:"continuity,omitempty"`
	RunState      agent.RunState     `json:"run_state,omitempty"`
	HasActiveTurn bool               `json:"has_active_turn,omitempty"`
}

type BindingState struct {
	BindingKey      string             `json:"binding_key,omitempty"`
	SessionRef      session.SessionRef `json:"session_ref"`
	Surface         string             `json:"surface,omitempty"`
	ActorKind       string             `json:"actor_kind,omitempty"`
	ActorID         string             `json:"actor_id,omitempty"`
	Owner           string             `json:"owner,omitempty"`
	BoundAt         time.Time          `json:"bound_at,omitempty"`
	UpdatedAt       time.Time          `json:"updated_at,omitempty"`
	ExpiresAt       time.Time          `json:"expires_at,omitempty"`
	LastHandleID    string             `json:"last_handle_id,omitempty"`
	LastRunID       string             `json:"last_run_id,omitempty"`
	LastTurnID      string             `json:"last_turn_id,omitempty"`
	LastEventCursor string             `json:"last_event_cursor,omitempty"`
	HasActiveTurn   bool               `json:"has_active_turn,omitempty"`
}

type ReplayEventsResult struct {
	SessionRef    session.SessionRef     `json:"session_ref"`
	Events        []eventstream.Envelope `json:"events,omitempty"`
	NextCursor    string                 `json:"next_cursor,omitempty"`
	Durable       bool                   `json:"durable,omitempty"`
	HasLiveHandle bool                   `json:"has_live_handle,omitempty"`
	ControlPlane  ControlPlaneState      `json:"control_plane"`
}

type ResolvedTurn struct {
	RunRequest agent.RunRequest
}

type EventKind string

const (
	EventKindUserMessage       EventKind = "user_message"
	EventKindAssistantMessage  EventKind = "assistant_message"
	EventKindPlanUpdate        EventKind = "plan_update"
	EventKindToolCall          EventKind = "tool_call"
	EventKindToolResult        EventKind = "tool_result"
	EventKindParticipant       EventKind = "participant"
	EventKindHandoff           EventKind = "handoff"
	EventKindCompact           EventKind = "compact"
	EventKindNotice            EventKind = "notice"
	EventKindSystemMessage     EventKind = "system_message"
	EventKindApprovalRequested EventKind = "approval_requested"
	EventKindApprovalReview    EventKind = "approval_review"
	EventKindLifecycle         EventKind = "lifecycle"
)

type UsageSnapshot = approval.UsageSnapshot

type NarrativeRole string

const (
	NarrativeRoleUser      NarrativeRole = "user"
	NarrativeRoleAssistant NarrativeRole = "assistant"
	NarrativeRoleReasoning NarrativeRole = "reasoning"
	NarrativeRoleSystem    NarrativeRole = "system"
	NarrativeRoleNotice    NarrativeRole = "notice"
)

type EventScope string

const (
	EventScopeMain        EventScope = "main"
	EventScopeParticipant EventScope = "participant"
	EventScopeSubagent    EventScope = "subagent"
)

type NarrativePayload struct {
	Role          NarrativeRole `json:"role,omitempty"`
	Actor         string        `json:"actor,omitempty"`
	Text          string        `json:"text,omitempty"`
	ReasoningText string        `json:"reasoning_text,omitempty"`
	Final         bool          `json:"final,omitempty"`
	Visibility    string        `json:"visibility,omitempty"`
	UpdateType    string        `json:"update_type,omitempty"`
	Scope         EventScope    `json:"scope,omitempty"`
	ParticipantID string        `json:"participant_id,omitempty"`
}

type ToolStatus string

const (
	ToolStatusStarted         ToolStatus = "started"
	ToolStatusRunning         ToolStatus = "running"
	ToolStatusWaitingApproval ToolStatus = "waiting_approval"
	ToolStatusCompleted       ToolStatus = "completed"
	ToolStatusFailed          ToolStatus = "failed"
	ToolStatusInterrupted     ToolStatus = "interrupted"
	ToolStatusCancelled       ToolStatus = "cancelled"
)

type ToolCallPayload struct {
	CallID        string                            `json:"call_id,omitempty"`
	ToolName      string                            `json:"tool_name,omitempty"`
	ToolKind      string                            `json:"tool_kind,omitempty"`
	ToolTitle     string                            `json:"tool_title,omitempty"`
	RawInput      map[string]any                    `json:"raw_input,omitempty"`
	Content       []session.ProtocolToolCallContent `json:"content,omitempty"`
	Status        ToolStatus                        `json:"status,omitempty"`
	Actor         string                            `json:"actor,omitempty"`
	Scope         EventScope                        `json:"scope,omitempty"`
	ParticipantID string                            `json:"participant_id,omitempty"`
}

type ToolResultPayload struct {
	CallID        string                            `json:"call_id,omitempty"`
	ToolName      string                            `json:"tool_name,omitempty"`
	ToolKind      string                            `json:"tool_kind,omitempty"`
	ToolTitle     string                            `json:"tool_title,omitempty"`
	RawInput      map[string]any                    `json:"raw_input,omitempty"`
	RawOutput     map[string]any                    `json:"raw_output,omitempty"`
	Content       []session.ProtocolToolCallContent `json:"content,omitempty"`
	Status        ToolStatus                        `json:"status,omitempty"`
	Error         bool                              `json:"error,omitempty"`
	Actor         string                            `json:"actor,omitempty"`
	Scope         EventScope                        `json:"scope,omitempty"`
	ParticipantID string                            `json:"participant_id,omitempty"`
}

type PlanEntryPayload struct {
	Content  string `json:"content,omitempty"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

type PlanPayload struct {
	Entries []PlanEntryPayload `json:"entries,omitempty"`
}

type ParticipantAction string

const (
	ParticipantActionAttached ParticipantAction = "attached"
	ParticipantActionDetached ParticipantAction = "detached"
)

type ParticipantPayload struct {
	ParticipantID   string            `json:"participant_id,omitempty"`
	ParticipantKind string            `json:"participant_kind,omitempty"`
	Role            string            `json:"role,omitempty"`
	Label           string            `json:"label,omitempty"`
	Action          ParticipantAction `json:"action,omitempty"`
	SessionID       string            `json:"session_id,omitempty"`
	ParentTurnID    string            `json:"parent_turn_id,omitempty"`
	DelegationID    string            `json:"delegation_id,omitempty"`
	Actor           string            `json:"actor,omitempty"`
	Scope           EventScope        `json:"scope,omitempty"`
}

type LifecycleStatus string

const (
	LifecycleStatusRunning         LifecycleStatus = "running"
	LifecycleStatusWaitingApproval LifecycleStatus = "waiting_approval"
	LifecycleStatusInterrupted     LifecycleStatus = "interrupted"
	LifecycleStatusFailed          LifecycleStatus = "failed"
	LifecycleStatusCompleted       LifecycleStatus = "completed"
)

type LifecyclePayload struct {
	Status        LifecycleStatus `json:"status,omitempty"`
	Reason        string          `json:"reason,omitempty"`
	Actor         string          `json:"actor,omitempty"`
	Scope         EventScope      `json:"scope,omitempty"`
	ParticipantID string          `json:"participant_id,omitempty"`
}

type EventOrigin struct {
	Scope                EventScope `json:"scope,omitempty"`
	ScopeID              string     `json:"scope_id,omitempty"`
	Source               string     `json:"source,omitempty"`
	Actor                string     `json:"actor,omitempty"`
	ParticipantID        string     `json:"participant_id,omitempty"`
	ParticipantKind      string     `json:"participant_kind,omitempty"`
	ParticipantSessionID string     `json:"participant_session_id,omitempty"`
}

type SubmissionKind = agent.SubmissionKind

const (
	SubmissionKindConversation                = agent.SubmissionKindConversation
	SubmissionKindApproval     SubmissionKind = "approval"
)

type CancelStatus = agent.CancelStatus
type CancelResult = agent.CancelResult

const (
	CancelStatusCancelled        = agent.CancelStatusCancelled
	CancelStatusAlreadyCancelled = agent.CancelStatusAlreadyCancelled
)

type ApprovalDecision struct {
	Outcome    string
	OptionID   string
	Approved   bool
	Reason     string
	ReviewText string
}

type SubmitRequest struct {
	Kind         SubmissionKind
	Text         string
	ContentParts []model.ContentPart
	Metadata     map[string]any
	Approval     *ApprovalDecision
}

type SubmitActiveTurnRequest struct {
	SessionRef   session.SessionRef
	Kind         SubmissionKind
	Text         string
	ContentParts []model.ContentPart
	Metadata     map[string]any
	Approval     *ApprovalDecision
}

type BeginTurnResult struct {
	Session session.Session
	Handle  TurnHandle
}

type TurnHandle interface {
	HandleID() string
	RunID() string
	TurnID() string
	SessionRef() session.SessionRef
	CreatedAt() time.Time
	ACPEvents() <-chan eventstream.Envelope
	Submit(context.Context, SubmitRequest) error
	Cancel() agent.CancelResult
	Close() error
}
