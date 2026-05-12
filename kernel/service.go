package kernel

import (
	"context"

	"github.com/OnslaughtSnail/caelis/internal/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type Service interface {
	SessionService
	TurnService
	ControlPlaneService
}

type SessionService interface {
	StartSession(context.Context, StartSessionRequest) (Session, error)
	LoadSession(context.Context, LoadSessionRequest) (LoadedSession, error)
	ResumeSession(context.Context, ResumeSessionRequest) (LoadedSession, error)
	ForkSession(context.Context, ForkSessionRequest) (Session, error)
	ListSessions(context.Context, ListSessionsRequest) (SessionList, error)
	BindSession(context.Context, BindSessionRequest) error
	LookupBinding(BindingStateRequest) (BindingState, error)
	ReplayEvents(context.Context, ReplayEventsRequest) (ReplayEventsResult, error)
}

type TurnService interface {
	BeginTurn(context.Context, BeginTurnRequest) (BeginTurnResult, error)
	SubmitActiveTurn(context.Context, SubmitActiveTurnRequest) error
	Interrupt(context.Context, InterruptRequest) error
	ActiveTurns() []ActiveTurnState
}

type ControlPlaneService interface {
	ControlPlaneState(context.Context, ControlPlaneStateRequest) (ControlPlaneState, error)
	HandoffController(context.Context, HandoffControllerRequest) (Session, error)
	AttachParticipant(context.Context, AttachParticipantRequest) (Session, error)
	PromptParticipant(context.Context, PromptParticipantRequest) (BeginTurnResult, error)
	DetachParticipant(context.Context, DetachParticipantRequest) (Session, error)
}

type Config = kernel.Config
type Gateway = kernel.Gateway
type AssemblyResolverConfig = kernel.AssemblyResolverConfig
type AssemblyResolver = kernel.AssemblyResolver
type BeginTurnRequest = kernel.BeginTurnRequest
type TurnIntent = kernel.TurnIntent
type StartSessionRequest = kernel.StartSessionRequest
type LoadSessionRequest = kernel.LoadSessionRequest
type ForkSessionRequest = kernel.ForkSessionRequest
type ResumeSessionRequest = kernel.ResumeSessionRequest
type ListSessionsRequest = kernel.ListSessionsRequest
type InterruptRequest = kernel.InterruptRequest
type BindingDescriptor = kernel.BindingDescriptor
type BindSessionRequest = kernel.BindSessionRequest
type ReplayEventsRequest = kernel.ReplayEventsRequest
type HandoffControllerRequest = kernel.HandoffControllerRequest
type AttachParticipantRequest = kernel.AttachParticipantRequest
type PromptParticipantRequest = kernel.PromptParticipantRequest
type DetachParticipantRequest = kernel.DetachParticipantRequest
type ControlPlaneStateRequest = kernel.ControlPlaneStateRequest
type BindingStateRequest = kernel.BindingStateRequest
type ActiveTurnState = kernel.ActiveTurnState
type ActiveTurnKind = kernel.ActiveTurnKind
type ControllerState = kernel.ControllerState
type ParticipantState = kernel.ParticipantState
type ACPProjectionState = kernel.ACPProjectionState
type ContinuityState = kernel.ContinuityState
type ControlPlaneState = kernel.ControlPlaneState
type BindingState = kernel.BindingState
type ReplayEventsResult = kernel.ReplayEventsResult
type ResolvedTurn = kernel.ResolvedTurn
type TurnResolver = kernel.TurnResolver
type RequestPolicy = kernel.RequestPolicy
type SurfaceClass = kernel.SurfaceClass
type EventKind = kernel.EventKind
type UsageSnapshot = kernel.UsageSnapshot
type NarrativeRole = kernel.NarrativeRole
type EventScope = kernel.EventScope
type NarrativePayload = kernel.NarrativePayload
type ToolStatus = kernel.ToolStatus
type ToolCallPayload = kernel.ToolCallPayload
type ToolResultPayload = kernel.ToolResultPayload
type PlanEntryPayload = kernel.PlanEntryPayload
type PlanPayload = kernel.PlanPayload
type ApprovalStatus = kernel.ApprovalStatus
type ApprovalReviewStatus = kernel.ApprovalReviewStatus
type ApprovalOption = kernel.ApprovalOption
type ApprovalPayload = kernel.ApprovalPayload
type ApprovalMode = kernel.ApprovalMode
type ApprovalReviewRequest = kernel.ApprovalReviewRequest
type ApprovalReviewResult = kernel.ApprovalReviewResult
type ApprovalReviewer = kernel.ApprovalReviewer
type ApprovalApprover = kernel.ApprovalApprover
type ParticipantAction = kernel.ParticipantAction
type ParticipantPayload = kernel.ParticipantPayload
type LifecycleStatus = kernel.LifecycleStatus
type LifecyclePayload = kernel.LifecyclePayload
type EventOrigin = kernel.EventOrigin
type Event = kernel.Event
type EventEnvelope = kernel.EventEnvelope
type StreamRequest = kernel.StreamRequest
type SubmissionKind = kernel.SubmissionKind
type CancelStatus = kernel.CancelStatus
type CancelResult = kernel.CancelResult
type ApprovalDecision = kernel.ApprovalDecision
type SubmitRequest = kernel.SubmitRequest
type SubmitActiveTurnRequest = kernel.SubmitActiveTurnRequest
type BeginTurnResult = kernel.BeginTurnResult
type TurnHandle = kernel.TurnHandle
type ErrorKind = kernel.ErrorKind
type Error = kernel.Error
type ModelLookup = kernel.ModelLookup
type ModelResolution = kernel.ModelResolution
type ToolAugmenter = kernel.ToolAugmenter
type ToolAugmentContext = kernel.ToolAugmentContext
type ToolAugmentation = kernel.ToolAugmentation
type SessionRef = session.SessionRef
type WorkspaceRef = session.WorkspaceRef
type Session = session.Session
type LoadedSession = session.LoadedSession
type SessionList = session.SessionList
type ControllerBinding = session.ControllerBinding
type ParticipantBinding = session.ParticipantBinding

const (
	KindValidation  = kernel.KindValidation
	KindConflict    = kernel.KindConflict
	KindNotFound    = kernel.KindNotFound
	KindInternal    = kernel.KindInternal
	KindApproval    = kernel.KindApproval
	KindUnsupported = kernel.KindUnsupported
)

const (
	CodeNotImplemented          = kernel.CodeNotImplemented
	CodeInternal                = kernel.CodeInternal
	CodeActiveRunConflict       = kernel.CodeActiveRunConflict
	CodeInvalidRequest          = kernel.CodeInvalidRequest
	CodeCursorNotFound          = kernel.CodeCursorNotFound
	CodeSubmissionUnsupported   = kernel.CodeSubmissionUnsupported
	CodeApprovalNotPending      = kernel.CodeApprovalNotPending
	CodeSessionNotFound         = kernel.CodeSessionNotFound
	CodeSessionAmbiguous        = kernel.CodeSessionAmbiguous
	CodeBindingNotFound         = kernel.CodeBindingNotFound
	CodeNoResumableSession      = kernel.CodeNoResumableSession
	CodeNoActiveRun             = kernel.CodeNoActiveRun
	CodeModeNotFound            = kernel.CodeModeNotFound
	CodeControlPlaneUnsupported = kernel.CodeControlPlaneUnsupported
)

const (
	StateCurrentModelAlias      = kernel.StateCurrentModelAlias
	StateCurrentSandboxMode     = kernel.StateCurrentSandboxMode
	StateCurrentSessionMode     = kernel.StateCurrentSessionMode
	StateCurrentReasoningEffort = kernel.StateCurrentReasoningEffort
	StateUsageAccounting        = kernel.StateUsageAccounting
)

const (
	EventMetaRoot                      = kernel.EventMetaRoot
	EventMetaVersion                   = kernel.EventMetaVersion
	EventMetaTransient                 = kernel.EventMetaTransient
	EventMetaRuntime                   = kernel.EventMetaRuntime
	EventMetaRuntimeTool               = kernel.EventMetaRuntimeTool
	EventMetaRuntimeToolName           = kernel.EventMetaRuntimeToolName
	EventMetaRuntimeToolAction         = kernel.EventMetaRuntimeToolAction
	EventMetaRuntimeToolInput          = kernel.EventMetaRuntimeToolInput
	EventMetaRuntimeTargetKind         = kernel.EventMetaRuntimeTargetKind
	EventMetaRuntimeTargetID           = kernel.EventMetaRuntimeTargetID
	EventMetaRuntimeStream             = kernel.EventMetaRuntimeStream
	EventMetaRuntimeStreamMode         = kernel.EventMetaRuntimeStreamMode
	EventMetaRuntimeStreamParentCallID = kernel.EventMetaRuntimeStreamParentCallID
	EventMetaRuntimeStreamParentTool   = kernel.EventMetaRuntimeStreamParentTool
	EventMetaRuntimeStreamParentTaskID = kernel.EventMetaRuntimeStreamParentTaskID
)

const (
	ApprovalModeAutoReview = kernel.ApprovalModeAutoReview
	ApprovalModeManual     = kernel.ApprovalModeManual
)

const (
	SurfaceClassInteractive = kernel.SurfaceClassInteractive
	SurfaceClassBatch       = kernel.SurfaceClassBatch
)

const (
	EventKindUserMessage       = kernel.EventKindUserMessage
	EventKindAssistantMessage  = kernel.EventKindAssistantMessage
	EventKindPlanUpdate        = kernel.EventKindPlanUpdate
	EventKindToolCall          = kernel.EventKindToolCall
	EventKindToolResult        = kernel.EventKindToolResult
	EventKindParticipant       = kernel.EventKindParticipant
	EventKindHandoff           = kernel.EventKindHandoff
	EventKindCompact           = kernel.EventKindCompact
	EventKindNotice            = kernel.EventKindNotice
	EventKindSystemMessage     = kernel.EventKindSystemMessage
	EventKindApprovalRequested = kernel.EventKindApprovalRequested
	EventKindApprovalReview    = kernel.EventKindApprovalReview
	EventKindLifecycle         = kernel.EventKindLifecycle
)

const (
	NarrativeRoleUser      = kernel.NarrativeRoleUser
	NarrativeRoleAssistant = kernel.NarrativeRoleAssistant
	NarrativeRoleReasoning = kernel.NarrativeRoleReasoning
	NarrativeRoleSystem    = kernel.NarrativeRoleSystem
	NarrativeRoleNotice    = kernel.NarrativeRoleNotice
)

const (
	EventScopeMain        = kernel.EventScopeMain
	EventScopeParticipant = kernel.EventScopeParticipant
	EventScopeSubagent    = kernel.EventScopeSubagent
)

const (
	ToolStatusStarted         = kernel.ToolStatusStarted
	ToolStatusRunning         = kernel.ToolStatusRunning
	ToolStatusWaitingApproval = kernel.ToolStatusWaitingApproval
	ToolStatusCompleted       = kernel.ToolStatusCompleted
	ToolStatusFailed          = kernel.ToolStatusFailed
	ToolStatusInterrupted     = kernel.ToolStatusInterrupted
	ToolStatusCancelled       = kernel.ToolStatusCancelled
)

const (
	ApprovalStatusPending  = kernel.ApprovalStatusPending
	ApprovalStatusApproved = kernel.ApprovalStatusApproved
	ApprovalStatusRejected = kernel.ApprovalStatusRejected
	ApprovalStatusSelected = kernel.ApprovalStatusSelected
)

const (
	ApprovalReviewStatusInProgress = kernel.ApprovalReviewStatusInProgress
	ApprovalReviewStatusApproved   = kernel.ApprovalReviewStatusApproved
	ApprovalReviewStatusDenied     = kernel.ApprovalReviewStatusDenied
	ApprovalReviewStatusTimedOut   = kernel.ApprovalReviewStatusTimedOut
	ApprovalReviewStatusFailed     = kernel.ApprovalReviewStatusFailed
)

const (
	ParticipantActionAttached = kernel.ParticipantActionAttached
	ParticipantActionDetached = kernel.ParticipantActionDetached
)

const (
	LifecycleStatusRunning         = kernel.LifecycleStatusRunning
	LifecycleStatusWaitingApproval = kernel.LifecycleStatusWaitingApproval
	LifecycleStatusInterrupted     = kernel.LifecycleStatusInterrupted
	LifecycleStatusFailed          = kernel.LifecycleStatusFailed
	LifecycleStatusCompleted       = kernel.LifecycleStatusCompleted
)

const (
	SubmissionKindConversation = kernel.SubmissionKindConversation
	SubmissionKindApproval     = kernel.SubmissionKindApproval
)

const (
	CancelStatusCancelled        = kernel.CancelStatusCancelled
	CancelStatusAlreadyCancelled = kernel.CancelStatusAlreadyCancelled
)

var New = kernel.New
var NewAssemblyResolver = kernel.NewAssemblyResolver
var NormalizeApprovalMode = kernel.NormalizeApprovalMode
var CurrentApprovalMode = kernel.CurrentApprovalMode
var CurrentModelAlias = kernel.CurrentModelAlias
var CurrentReasoningEffort = kernel.CurrentReasoningEffort
var CurrentSandboxMode = kernel.CurrentSandboxMode
var CurrentSessionMode = kernel.CurrentSessionMode
var ClassifySurface = kernel.ClassifySurface
var EventError = kernel.EventError
var As = kernel.As
var StreamRequestFromEvent = kernel.StreamRequestFromEvent
var StreamFrameEvent = kernel.StreamFrameEvent
var StreamFrameEvents = kernel.StreamFrameEvents
var CleanSubagentFinalOutput = kernel.CleanSubagentFinalOutput
var ProjectSessionEvent = kernel.ProjectSessionEvent
var EventMetaString = kernel.EventMetaString
var EventMetaBool = kernel.EventMetaBool
var FormatApprovalReviewText = kernel.FormatApprovalReviewText
var UsageSnapshotFromSessionEvent = kernel.UsageSnapshotFromSessionEvent
var UsageSnapshotFromMap = kernel.UsageSnapshotFromMap
var AssistantText = kernel.AssistantText
var PromptTokens = kernel.PromptTokens
var CachedInputTokens = kernel.CachedInputTokens

const (
	ActiveTurnKindKernel      = kernel.ActiveTurnKindKernel
	ActiveTurnKindParticipant = kernel.ActiveTurnKindParticipant
)
