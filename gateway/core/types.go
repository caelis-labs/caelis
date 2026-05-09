package core

import (
	"context"
	"strings"
	"time"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

type BeginTurnRequest struct {
	SessionRef   sdksession.SessionRef
	Input        string
	ContentParts []sdkmodel.ContentPart
	ModeName     string
	ModelHint    string
	Surface      string
	Metadata     map[string]any
	Request      sdkruntime.ModelRequestOptions
}

type TurnIntent = BeginTurnRequest

type StartSessionRequest struct {
	AppName            string
	UserID             string
	Workspace          sdksession.WorkspaceRef
	PreferredSessionID string
	Title              string
	Metadata           map[string]any
	BindingKey         string
	Binding            BindingDescriptor
}

type LoadSessionRequest struct {
	SessionRef       sdksession.SessionRef
	Limit            int
	IncludeTransient bool
	BindingKey       string
	Binding          BindingDescriptor
}

type ForkSessionRequest struct {
	SourceSessionRef   sdksession.SessionRef
	PreferredSessionID string
	Title              string
	Metadata           map[string]any
	BindingKey         string
	Binding            BindingDescriptor
}

type ResumeSessionRequest struct {
	AppName          string
	UserID           string
	Workspace        sdksession.WorkspaceRef
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
	SessionRef sdksession.SessionRef
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
	SessionRef sdksession.SessionRef `json:"session_ref"`
	BindingKey string                `json:"binding_key,omitempty"`
	Binding    BindingDescriptor     `json:"binding,omitempty"`
}

type ReplayEventsRequest struct {
	SessionRef       sdksession.SessionRef `json:"session_ref"`
	BindingKey       string                `json:"binding_key,omitempty"`
	Cursor           string                `json:"cursor,omitempty"`
	Limit            int                   `json:"limit,omitempty"`
	IncludeTransient bool                  `json:"include_transient,omitempty"`
}

type HandoffControllerRequest struct {
	SessionRef sdksession.SessionRef
	BindingKey string
	Kind       sdksession.ControllerKind
	Agent      string
	Source     string
	Reason     string
}

// AttachParticipantRequest attaches one ACP-backed participant to the current
// session control plane without replacing the main controller.
type AttachParticipantRequest struct {
	SessionRef sdksession.SessionRef
	BindingKey string
	Agent      string
	Role       sdksession.ParticipantRole
	Source     string
	Label      string
}

// DetachParticipantRequest removes one attached participant from the current
// session control plane.
type DetachParticipantRequest struct {
	SessionRef    sdksession.SessionRef
	BindingKey    string
	ParticipantID string
	Source        string
}

type PromptParticipantRequest struct {
	SessionRef    sdksession.SessionRef
	BindingKey    string
	ParticipantID string
	Input         string
	ContentParts  []sdkmodel.ContentPart
	Source        string
}

type ControlPlaneStateRequest struct {
	SessionRef sdksession.SessionRef
	BindingKey string
}

type BindingStateRequest struct {
	BindingKey string `json:"binding_key,omitempty"`
}

type ActiveTurnState struct {
	SessionRef sdksession.SessionRef `json:"session_ref"`
	Kind       ActiveTurnKind        `json:"kind,omitempty"`
	HandleID   string                `json:"handle_id,omitempty"`
	RunID      string                `json:"run_id,omitempty"`
	TurnID     string                `json:"turn_id,omitempty"`
	StartedAt  time.Time             `json:"started_at,omitempty"`
}

type ActiveTurnKind string

const (
	ActiveTurnKindKernel      ActiveTurnKind = "kernel"
	ActiveTurnKindParticipant ActiveTurnKind = "participant"
)

type ControllerState struct {
	Kind            sdksession.ControllerKind `json:"kind,omitempty"`
	ControllerID    string                    `json:"controller_id,omitempty"`
	AgentName       string                    `json:"agent_name,omitempty"`
	Label           string                    `json:"label,omitempty"`
	EpochID         string                    `json:"epoch_id,omitempty"`
	RemoteSessionID string                    `json:"remote_session_id,omitempty"`
	ContextSyncSeq  int                       `json:"context_sync_seq,omitempty"`
	AttachedAt      time.Time                 `json:"attached_at,omitempty"`
	Source          string                    `json:"source,omitempty"`
}

type ParticipantState struct {
	ID             string                     `json:"id,omitempty"`
	Kind           sdksession.ParticipantKind `json:"kind,omitempty"`
	Role           sdksession.ParticipantRole `json:"role,omitempty"`
	AgentName      string                     `json:"agent_name,omitempty"`
	Label          string                     `json:"label,omitempty"`
	SessionID      string                     `json:"session_id,omitempty"`
	Source         string                     `json:"source,omitempty"`
	ParentTurnID   string                     `json:"parent_turn_id,omitempty"`
	DelegationID   string                     `json:"delegation_id,omitempty"`
	ContextSyncSeq int                        `json:"context_sync_seq,omitempty"`
	AttachedAt     time.Time                  `json:"attached_at,omitempty"`
	ControllerRef  string                     `json:"controller_ref,omitempty"`
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
	SessionRef    sdksession.SessionRef `json:"session_ref"`
	Controller    ControllerState       `json:"controller"`
	Participants  []ParticipantState    `json:"participants,omitempty"`
	Continuity    ContinuityState       `json:"continuity,omitempty"`
	RunState      sdkruntime.RunState   `json:"run_state,omitempty"`
	HasActiveTurn bool                  `json:"has_active_turn,omitempty"`
}

type BindingState struct {
	BindingKey      string                `json:"binding_key,omitempty"`
	SessionRef      sdksession.SessionRef `json:"session_ref"`
	Surface         string                `json:"surface,omitempty"`
	ActorKind       string                `json:"actor_kind,omitempty"`
	ActorID         string                `json:"actor_id,omitempty"`
	Owner           string                `json:"owner,omitempty"`
	BoundAt         time.Time             `json:"bound_at,omitempty"`
	UpdatedAt       time.Time             `json:"updated_at,omitempty"`
	ExpiresAt       time.Time             `json:"expires_at,omitempty"`
	LastHandleID    string                `json:"last_handle_id,omitempty"`
	LastRunID       string                `json:"last_run_id,omitempty"`
	LastTurnID      string                `json:"last_turn_id,omitempty"`
	LastEventCursor string                `json:"last_event_cursor,omitempty"`
	HasActiveTurn   bool                  `json:"has_active_turn,omitempty"`
}

type ReplayEventsResult struct {
	SessionRef    sdksession.SessionRef `json:"session_ref"`
	Events        []EventEnvelope       `json:"events,omitempty"`
	NextCursor    string                `json:"next_cursor,omitempty"`
	Durable       bool                  `json:"durable,omitempty"`
	HasLiveHandle bool                  `json:"has_live_handle,omitempty"`
	ControlPlane  ControlPlaneState     `json:"control_plane"`
}

type ResolvedTurn struct {
	RunRequest sdkruntime.RunRequest
}

type TurnResolver interface {
	ResolveTurn(context.Context, TurnIntent) (ResolvedTurn, error)
}

type RequestPolicy interface {
	ResolveTurnRequest(BeginTurnRequest) sdkruntime.ModelRequestOptions
}

type SurfaceClass string

const (
	SurfaceClassInteractive SurfaceClass = "interactive"
	SurfaceClassBatch       SurfaceClass = "batch"
)

func ClassifySurface(surface string) SurfaceClass {
	normalized := strings.ToLower(strings.TrimSpace(surface))
	switch {
	case normalized == "":
		return SurfaceClassInteractive
	case strings.HasPrefix(normalized, "headless"),
		strings.HasPrefix(normalized, "batch"),
		strings.HasPrefix(normalized, "cron"),
		strings.HasPrefix(normalized, "export"),
		strings.HasPrefix(normalized, "script"):
		return SurfaceClassBatch
	default:
		return SurfaceClassInteractive
	}
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

type UsageSnapshot struct {
	PromptTokens      int `json:"prompt_tokens,omitempty"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	CompletionTokens  int `json:"completion_tokens,omitempty"`
	TotalTokens       int `json:"total_tokens,omitempty"`
}

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
	CallID        string         `json:"call_id,omitempty"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolKind      string         `json:"tool_kind,omitempty"`
	ToolTitle     string         `json:"tool_title,omitempty"`
	RawInput      map[string]any `json:"raw_input,omitempty"`
	Status        ToolStatus     `json:"status,omitempty"`
	Actor         string         `json:"actor,omitempty"`
	Scope         EventScope     `json:"scope,omitempty"`
	ParticipantID string         `json:"participant_id,omitempty"`
}

type ToolResultPayload struct {
	CallID        string         `json:"call_id,omitempty"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolKind      string         `json:"tool_kind,omitempty"`
	ToolTitle     string         `json:"tool_title,omitempty"`
	RawInput      map[string]any `json:"raw_input,omitempty"`
	RawOutput     map[string]any `json:"raw_output,omitempty"`
	Status        ToolStatus     `json:"status,omitempty"`
	Error         bool           `json:"error,omitempty"`
	Actor         string         `json:"actor,omitempty"`
	Scope         EventScope     `json:"scope,omitempty"`
	ParticipantID string         `json:"participant_id,omitempty"`
}

type PlanEntryPayload struct {
	Content  string `json:"content,omitempty"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

type PlanPayload struct {
	Entries []PlanEntryPayload `json:"entries,omitempty"`
}

type ApprovalOption struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

type ApprovalStatus string

const (
	ApprovalStatusPending  ApprovalStatus = "pending"
	ApprovalStatusApproved ApprovalStatus = "approved"
	ApprovalStatusRejected ApprovalStatus = "rejected"
	ApprovalStatusSelected ApprovalStatus = "selected"
)

type ApprovalReviewStatus string

const (
	ApprovalReviewStatusInProgress ApprovalReviewStatus = "in_progress"
	ApprovalReviewStatusApproved   ApprovalReviewStatus = "approved"
	ApprovalReviewStatusDenied     ApprovalReviewStatus = "denied"
	ApprovalReviewStatusTimedOut   ApprovalReviewStatus = "timed_out"
	ApprovalReviewStatusFailed     ApprovalReviewStatus = "failed"
)

type ApprovalPayload struct {
	ToolCallID            string               `json:"tool_call_id,omitempty"`
	ToolName              string               `json:"tool_name,omitempty"`
	RawInput              map[string]any       `json:"raw_input,omitempty"`
	Reason                string               `json:"reason,omitempty"`
	Justification         string               `json:"justification,omitempty"`
	SandboxPermissions    string               `json:"sandbox_permissions,omitempty"`
	AdditionalPermissions map[string]any       `json:"additional_permissions,omitempty"`
	Status                ApprovalStatus       `json:"status,omitempty"`
	Options               []ApprovalOption     `json:"options,omitempty"`
	ReviewID              string               `json:"review_id,omitempty"`
	ReviewStatus          ApprovalReviewStatus `json:"review_status,omitempty"`
	ReviewText            string               `json:"review_text,omitempty"`
	Risk                  string               `json:"risk,omitempty"`
	Authorization         string               `json:"authorization,omitempty"`
	DecisionSource        string               `json:"decision_source,omitempty"`
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

type Event struct {
	Kind       EventKind             `json:"kind"`
	HandleID   string                `json:"handle_id,omitempty"`
	RunID      string                `json:"run_id,omitempty"`
	TurnID     string                `json:"turn_id,omitempty"`
	OccurredAt time.Time             `json:"occurred_at,omitempty"`
	SessionRef sdksession.SessionRef `json:"session_ref,omitempty"`
	Origin     *EventOrigin          `json:"origin,omitempty"`
	Meta       map[string]any        `json:"_meta,omitempty"`
	// Protocol is the canonical ACP-shaped event payload. UI layers should use
	// this plus _meta.caelis, not gateway display fallbacks, as their semantic
	// source of truth.
	Protocol *sdksession.EventProtocol `json:"protocol,omitempty"`
	// Usage is the canonical token snapshot projected from one runtime/session
	// event. It is stable across UI, headless, and adapter boundaries.
	Usage *UsageSnapshot `json:"usage,omitempty"`
	// Narrative carries stable user/assistant/system/notice text. Assistant
	// reasoning remains separate in ReasoningText so UIs can preserve the answer
	// boundary without re-parsing raw provider events.
	Narrative *NarrativePayload `json:"narrative,omitempty"`
	// ToolCall is the stable canonical tool invocation view with normalized
	// status and raw ACP-compatible input.
	ToolCall *ToolCallPayload `json:"tool_call,omitempty"`
	// ToolResult is the stable canonical tool result/update view with normalized
	// status and raw ACP-compatible output.
	ToolResult *ToolResultPayload `json:"tool_result,omitempty"`
	// Plan is the stable canonical plan snapshot for one event.
	Plan *PlanPayload `json:"plan,omitempty"`
	// ApprovalPayload is the stable canonical approval request summary for one
	// event.
	ApprovalPayload *ApprovalPayload `json:"approval,omitempty"`
	// Participant is the stable canonical participant lifecycle payload.
	Participant *ParticipantPayload `json:"participant,omitempty"`
	// Lifecycle is the stable canonical run/session lifecycle payload.
	Lifecycle *LifecyclePayload `json:"lifecycle,omitempty"`
}

type EventEnvelope struct {
	Cursor string `json:"cursor,omitempty"`
	Event  Event  `json:"event"`
	Err    *Error `json:"err,omitempty"`
}

type SubmissionKind string

const (
	SubmissionKindConversation SubmissionKind = "conversation"
	SubmissionKindOverlay      SubmissionKind = "overlay"
	SubmissionKindApproval     SubmissionKind = "approval"
)

type ApprovalDecision struct {
	Outcome    string
	OptionID   string
	Approved   bool
	Reason     string
	ReviewText string
}

type SubmitRequest struct {
	Kind     SubmissionKind
	Text     string
	Metadata map[string]any
	Approval *ApprovalDecision
}

type SubmitActiveTurnRequest struct {
	SessionRef sdksession.SessionRef
	Kind       SubmissionKind
	Text       string
	Metadata   map[string]any
	Approval   *ApprovalDecision
}

type BeginTurnResult struct {
	Session sdksession.Session
	Handle  TurnHandle
}

type TurnHandle interface {
	HandleID() string
	RunID() string
	TurnID() string
	SessionRef() sdksession.SessionRef
	CreatedAt() time.Time
	Events() <-chan EventEnvelope
	EventsAfter(string) ([]EventEnvelope, string, error)
	Submit(context.Context, SubmitRequest) error
	Cancel() bool
	Close() error
}
