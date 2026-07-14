package kernel

import gateway "github.com/caelis-labs/caelis/ports/gateway"

type BeginTurnRequest = gateway.BeginTurnRequest
type TurnIntent = gateway.TurnIntent
type StartSessionRequest = gateway.StartSessionRequest
type LoadSessionRequest = gateway.LoadSessionRequest
type ResumeSessionRequest = gateway.ResumeSessionRequest
type ListSessionsRequest = gateway.ListSessionsRequest
type InterruptRequest = gateway.InterruptRequest
type BindingDescriptor = gateway.BindingDescriptor
type BindSessionRequest = gateway.BindSessionRequest
type HandoffControllerRequest = gateway.HandoffControllerRequest
type AttachParticipantRequest = gateway.AttachParticipantRequest
type PromptParticipantRequest = gateway.PromptParticipantRequest
type StartParticipantRequest = gateway.StartParticipantRequest
type DetachParticipantRequest = gateway.DetachParticipantRequest
type ControlPlaneStateRequest = gateway.ControlPlaneStateRequest
type ActiveTurnState = gateway.ActiveTurnState
type ActiveTurnKind = gateway.ActiveTurnKind
type ControllerState = gateway.ControllerState
type ParticipantState = gateway.ParticipantState
type ACPProjectionState = gateway.ACPProjectionState
type ContinuityState = gateway.ContinuityState
type ControlPlaneState = gateway.ControlPlaneState
type ResolvedTurn = gateway.ResolvedTurn
type TurnResolver = gateway.TurnResolver
type ControllerTurnResolver = gateway.ControllerTurnResolver
type RequestPolicy = gateway.RequestPolicy
type SurfaceClass = gateway.SurfaceClass
type EventKind = gateway.EventKind
type UsageSnapshot = gateway.UsageSnapshot
type NarrativeRole = gateway.NarrativeRole
type EventScope = gateway.EventScope
type NarrativePayload = gateway.NarrativePayload
type ToolStatus = gateway.ToolStatus
type ToolCallPayload = gateway.ToolCallPayload
type ToolResultPayload = gateway.ToolResultPayload
type PlanEntryPayload = gateway.PlanEntryPayload
type PlanPayload = gateway.PlanPayload
type ApprovalOption = gateway.ApprovalOption
type ApprovalStatus = gateway.ApprovalStatus
type ApprovalReviewStatus = gateway.ApprovalReviewStatus
type ApprovalPayload = gateway.ApprovalPayload
type ParticipantAction = gateway.ParticipantAction
type ParticipantLifecycle = gateway.ParticipantLifecycle
type ParticipantPayload = gateway.ParticipantPayload
type LifecycleStatus = gateway.LifecycleStatus
type LifecyclePayload = gateway.LifecyclePayload
type EventOrigin = gateway.EventOrigin
type SubmissionKind = gateway.SubmissionKind
type CancelStatus = gateway.CancelStatus
type CancelResult = gateway.CancelResult
type ApprovalDecision = gateway.ApprovalDecision
type SubmitRequest = gateway.SubmitRequest
type SubmitActiveTurnRequest = gateway.SubmitActiveTurnRequest
type BeginTurnResult = gateway.BeginTurnResult
type TurnHandle = gateway.TurnHandle

const (
	ActiveTurnKindKernel      = gateway.ActiveTurnKindKernel
	ActiveTurnKindParticipant = gateway.ActiveTurnKindParticipant
)

const (
	ParticipantLifecyclePersistent = gateway.ParticipantLifecyclePersistent
	ParticipantLifecycleTransient  = gateway.ParticipantLifecycleTransient
)

const (
	SurfaceClassInteractive = gateway.SurfaceClassInteractive
	SurfaceClassBatch       = gateway.SurfaceClassBatch
)

const (
	EventKindUserMessage       = gateway.EventKindUserMessage
	EventKindAssistantMessage  = gateway.EventKindAssistantMessage
	EventKindPlanUpdate        = gateway.EventKindPlanUpdate
	EventKindToolCall          = gateway.EventKindToolCall
	EventKindToolResult        = gateway.EventKindToolResult
	EventKindParticipant       = gateway.EventKindParticipant
	EventKindHandoff           = gateway.EventKindHandoff
	EventKindCompact           = gateway.EventKindCompact
	EventKindNotice            = gateway.EventKindNotice
	EventKindSystemMessage     = gateway.EventKindSystemMessage
	EventKindApprovalRequested = gateway.EventKindApprovalRequested
	EventKindApprovalReview    = gateway.EventKindApprovalReview
	EventKindLifecycle         = gateway.EventKindLifecycle
)

const (
	NarrativeRoleUser      = gateway.NarrativeRoleUser
	NarrativeRoleAssistant = gateway.NarrativeRoleAssistant
	NarrativeRoleReasoning = gateway.NarrativeRoleReasoning
	NarrativeRoleSystem    = gateway.NarrativeRoleSystem
	NarrativeRoleNotice    = gateway.NarrativeRoleNotice
)

const (
	EventScopeMain        = gateway.EventScopeMain
	EventScopeParticipant = gateway.EventScopeParticipant
	EventScopeSubagent    = gateway.EventScopeSubagent
)

const (
	ToolStatusStarted         = gateway.ToolStatusStarted
	ToolStatusRunning         = gateway.ToolStatusRunning
	ToolStatusWaitingApproval = gateway.ToolStatusWaitingApproval
	ToolStatusCompleted       = gateway.ToolStatusCompleted
	ToolStatusFailed          = gateway.ToolStatusFailed
	ToolStatusInterrupted     = gateway.ToolStatusInterrupted
	ToolStatusCancelled       = gateway.ToolStatusCancelled
)

const (
	ApprovalStatusPending  = gateway.ApprovalStatusPending
	ApprovalStatusApproved = gateway.ApprovalStatusApproved
	ApprovalStatusRejected = gateway.ApprovalStatusRejected
	ApprovalStatusSelected = gateway.ApprovalStatusSelected
)

const (
	ApprovalReviewStatusInProgress = gateway.ApprovalReviewStatusInProgress
	ApprovalReviewStatusApproved   = gateway.ApprovalReviewStatusApproved
	ApprovalReviewStatusDenied     = gateway.ApprovalReviewStatusDenied
	ApprovalReviewStatusTimedOut   = gateway.ApprovalReviewStatusTimedOut
	ApprovalReviewStatusFailed     = gateway.ApprovalReviewStatusFailed
)

const (
	ParticipantActionAttached = gateway.ParticipantActionAttached
	ParticipantActionDetached = gateway.ParticipantActionDetached
)

const (
	LifecycleStatusRunning         = gateway.LifecycleStatusRunning
	LifecycleStatusWaitingApproval = gateway.LifecycleStatusWaitingApproval
	LifecycleStatusInterrupted     = gateway.LifecycleStatusInterrupted
	LifecycleStatusFailed          = gateway.LifecycleStatusFailed
	LifecycleStatusCompleted       = gateway.LifecycleStatusCompleted
)

const (
	SubmissionKindConversation = gateway.SubmissionKindConversation
	SubmissionKindApproval     = gateway.SubmissionKindApproval
)

const (
	CancelStatusCancelled        = gateway.CancelStatusCancelled
	CancelStatusAlreadyCancelled = gateway.CancelStatusAlreadyCancelled
)

var ClassifySurface = gateway.ClassifySurface
