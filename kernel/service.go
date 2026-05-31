package kernel

import (
	"context"

	kernelimpl "github.com/OnslaughtSnail/caelis/internal/kernel"
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

type Config = kernelimpl.Config
type Gateway = kernelimpl.Gateway
type AssemblyResolverConfig = kernelimpl.AssemblyResolverConfig
type AssemblyResolver = kernelimpl.AssemblyResolver
type BeginTurnRequest = kernelimpl.BeginTurnRequest
type TurnIntent = kernelimpl.TurnIntent
type StartSessionRequest = kernelimpl.StartSessionRequest
type LoadSessionRequest = kernelimpl.LoadSessionRequest
type ForkSessionRequest = kernelimpl.ForkSessionRequest
type ResumeSessionRequest = kernelimpl.ResumeSessionRequest
type ListSessionsRequest = kernelimpl.ListSessionsRequest
type InterruptRequest = kernelimpl.InterruptRequest
type BindingDescriptor = kernelimpl.BindingDescriptor
type BindSessionRequest = kernelimpl.BindSessionRequest
type ReplayEventsRequest = kernelimpl.ReplayEventsRequest
type HandoffControllerRequest = kernelimpl.HandoffControllerRequest
type AttachParticipantRequest = kernelimpl.AttachParticipantRequest
type PromptParticipantRequest = kernelimpl.PromptParticipantRequest
type DetachParticipantRequest = kernelimpl.DetachParticipantRequest
type ControlPlaneStateRequest = kernelimpl.ControlPlaneStateRequest
type BindingStateRequest = kernelimpl.BindingStateRequest
type ActiveTurnState = kernelimpl.ActiveTurnState
type ActiveTurnKind = kernelimpl.ActiveTurnKind
type ControllerState = kernelimpl.ControllerState
type ParticipantState = kernelimpl.ParticipantState
type ACPProjectionState = kernelimpl.ACPProjectionState
type ContinuityState = kernelimpl.ContinuityState
type ControlPlaneState = kernelimpl.ControlPlaneState
type BindingState = kernelimpl.BindingState
type ReplayEventsResult = kernelimpl.ReplayEventsResult
type ResolvedTurn = kernelimpl.ResolvedTurn
type TurnResolver = kernelimpl.TurnResolver
type RequestPolicy = kernelimpl.RequestPolicy
type SurfaceClass = kernelimpl.SurfaceClass
type EventKind = kernelimpl.EventKind
type UsageSnapshot = kernelimpl.UsageSnapshot
type NarrativeRole = kernelimpl.NarrativeRole
type EventScope = kernelimpl.EventScope
type NarrativePayload = kernelimpl.NarrativePayload
type ToolStatus = kernelimpl.ToolStatus
type ToolCallPayload = kernelimpl.ToolCallPayload
type ToolResultPayload = kernelimpl.ToolResultPayload
type PlanEntryPayload = kernelimpl.PlanEntryPayload
type PlanPayload = kernelimpl.PlanPayload
type ApprovalStatus = kernelimpl.ApprovalStatus
type ApprovalReviewStatus = kernelimpl.ApprovalReviewStatus
type ApprovalOption = kernelimpl.ApprovalOption
type ApprovalPayload = kernelimpl.ApprovalPayload
type ApprovalMode = kernelimpl.ApprovalMode
type ApprovalReviewRequest = kernelimpl.ApprovalReviewRequest
type ApprovalReviewResult = kernelimpl.ApprovalReviewResult
type ApprovalReviewer = kernelimpl.ApprovalReviewer
type ApprovalApprover = kernelimpl.ApprovalApprover
type ParticipantAction = kernelimpl.ParticipantAction
type ParticipantPayload = kernelimpl.ParticipantPayload
type LifecycleStatus = kernelimpl.LifecycleStatus
type LifecyclePayload = kernelimpl.LifecyclePayload
type EventOrigin = kernelimpl.EventOrigin
type Event = kernelimpl.Event
type EventEnvelope = kernelimpl.EventEnvelope
type SubmissionKind = kernelimpl.SubmissionKind
type CancelStatus = kernelimpl.CancelStatus
type CancelResult = kernelimpl.CancelResult
type ApprovalDecision = kernelimpl.ApprovalDecision
type SubmitRequest = kernelimpl.SubmitRequest
type SubmitActiveTurnRequest = kernelimpl.SubmitActiveTurnRequest
type BeginTurnResult = kernelimpl.BeginTurnResult
type TurnHandle = kernelimpl.TurnHandle
type ErrorKind = kernelimpl.ErrorKind
type Error = kernelimpl.Error
type ModelLookup = kernelimpl.ModelLookup
type ModelResolution = kernelimpl.ModelResolution
type ToolAugmenter = kernelimpl.ToolAugmenter
type ToolAugmentContext = kernelimpl.ToolAugmentContext
type ToolAugmentation = kernelimpl.ToolAugmentation
type SessionRef = session.SessionRef
type WorkspaceRef = session.WorkspaceRef
type Session = session.Session
type LoadedSession = session.LoadedSession
type SessionList = session.SessionList
type ControllerBinding = session.ControllerBinding
type ParticipantBinding = session.ParticipantBinding

const (
	KindValidation  = kernelimpl.KindValidation
	KindConflict    = kernelimpl.KindConflict
	KindNotFound    = kernelimpl.KindNotFound
	KindInternal    = kernelimpl.KindInternal
	KindApproval    = kernelimpl.KindApproval
	KindUnsupported = kernelimpl.KindUnsupported
)

const (
	CodeNotImplemented          = kernelimpl.CodeNotImplemented
	CodeInternal                = kernelimpl.CodeInternal
	CodeActiveRunConflict       = kernelimpl.CodeActiveRunConflict
	CodeInvalidRequest          = kernelimpl.CodeInvalidRequest
	CodeCursorNotFound          = kernelimpl.CodeCursorNotFound
	CodeSubmissionUnsupported   = kernelimpl.CodeSubmissionUnsupported
	CodeApprovalNotPending      = kernelimpl.CodeApprovalNotPending
	CodeSessionNotFound         = kernelimpl.CodeSessionNotFound
	CodeSessionAmbiguous        = kernelimpl.CodeSessionAmbiguous
	CodeBindingNotFound         = kernelimpl.CodeBindingNotFound
	CodeNoResumableSession      = kernelimpl.CodeNoResumableSession
	CodeNoActiveRun             = kernelimpl.CodeNoActiveRun
	CodeModeNotFound            = kernelimpl.CodeModeNotFound
	CodeControlPlaneUnsupported = kernelimpl.CodeControlPlaneUnsupported
)

const (
	StateCurrentModelAlias      = kernelimpl.StateCurrentModelAlias
	StateCurrentSandboxMode     = kernelimpl.StateCurrentSandboxMode
	StateCurrentSessionMode     = kernelimpl.StateCurrentSessionMode
	StateCurrentReasoningEffort = kernelimpl.StateCurrentReasoningEffort
	StateUsageAccounting        = kernelimpl.StateUsageAccounting
)

const (
	EventMetaRoot                      = kernelimpl.EventMetaRoot
	EventMetaVersion                   = kernelimpl.EventMetaVersion
	EventMetaTransient                 = kernelimpl.EventMetaTransient
	EventMetaRuntime                   = kernelimpl.EventMetaRuntime
	EventMetaRuntimeTool               = kernelimpl.EventMetaRuntimeTool
	EventMetaRuntimeToolName           = kernelimpl.EventMetaRuntimeToolName
	EventMetaRuntimeToolAction         = kernelimpl.EventMetaRuntimeToolAction
	EventMetaRuntimeToolInput          = kernelimpl.EventMetaRuntimeToolInput
	EventMetaRuntimeTargetKind         = kernelimpl.EventMetaRuntimeTargetKind
	EventMetaRuntimeTargetID           = kernelimpl.EventMetaRuntimeTargetID
	EventMetaRuntimeStream             = kernelimpl.EventMetaRuntimeStream
	EventMetaRuntimeStreamMode         = kernelimpl.EventMetaRuntimeStreamMode
	EventMetaRuntimeStreamParentCallID = kernelimpl.EventMetaRuntimeStreamParentCallID
	EventMetaRuntimeStreamParentTool   = kernelimpl.EventMetaRuntimeStreamParentTool
	EventMetaRuntimeStreamParentTaskID = kernelimpl.EventMetaRuntimeStreamParentTaskID
)

const (
	ApprovalModeAutoReview = kernelimpl.ApprovalModeAutoReview
	ApprovalModeManual     = kernelimpl.ApprovalModeManual
)

const (
	SurfaceClassInteractive = kernelimpl.SurfaceClassInteractive
	SurfaceClassBatch       = kernelimpl.SurfaceClassBatch
)

const (
	EventKindUserMessage       = kernelimpl.EventKindUserMessage
	EventKindAssistantMessage  = kernelimpl.EventKindAssistantMessage
	EventKindPlanUpdate        = kernelimpl.EventKindPlanUpdate
	EventKindToolCall          = kernelimpl.EventKindToolCall
	EventKindToolResult        = kernelimpl.EventKindToolResult
	EventKindParticipant       = kernelimpl.EventKindParticipant
	EventKindHandoff           = kernelimpl.EventKindHandoff
	EventKindCompact           = kernelimpl.EventKindCompact
	EventKindNotice            = kernelimpl.EventKindNotice
	EventKindSystemMessage     = kernelimpl.EventKindSystemMessage
	EventKindApprovalRequested = kernelimpl.EventKindApprovalRequested
	EventKindApprovalReview    = kernelimpl.EventKindApprovalReview
	EventKindLifecycle         = kernelimpl.EventKindLifecycle
)

const (
	NarrativeRoleUser      = kernelimpl.NarrativeRoleUser
	NarrativeRoleAssistant = kernelimpl.NarrativeRoleAssistant
	NarrativeRoleReasoning = kernelimpl.NarrativeRoleReasoning
	NarrativeRoleSystem    = kernelimpl.NarrativeRoleSystem
	NarrativeRoleNotice    = kernelimpl.NarrativeRoleNotice
)

const (
	EventScopeMain        = kernelimpl.EventScopeMain
	EventScopeParticipant = kernelimpl.EventScopeParticipant
	EventScopeSubagent    = kernelimpl.EventScopeSubagent
)

const (
	ToolStatusStarted         = kernelimpl.ToolStatusStarted
	ToolStatusRunning         = kernelimpl.ToolStatusRunning
	ToolStatusWaitingApproval = kernelimpl.ToolStatusWaitingApproval
	ToolStatusCompleted       = kernelimpl.ToolStatusCompleted
	ToolStatusFailed          = kernelimpl.ToolStatusFailed
	ToolStatusInterrupted     = kernelimpl.ToolStatusInterrupted
	ToolStatusCancelled       = kernelimpl.ToolStatusCancelled
)

const (
	ApprovalStatusPending  = kernelimpl.ApprovalStatusPending
	ApprovalStatusApproved = kernelimpl.ApprovalStatusApproved
	ApprovalStatusRejected = kernelimpl.ApprovalStatusRejected
	ApprovalStatusSelected = kernelimpl.ApprovalStatusSelected
)

const (
	ApprovalReviewStatusInProgress = kernelimpl.ApprovalReviewStatusInProgress
	ApprovalReviewStatusApproved   = kernelimpl.ApprovalReviewStatusApproved
	ApprovalReviewStatusDenied     = kernelimpl.ApprovalReviewStatusDenied
	ApprovalReviewStatusTimedOut   = kernelimpl.ApprovalReviewStatusTimedOut
	ApprovalReviewStatusFailed     = kernelimpl.ApprovalReviewStatusFailed
)

const (
	ParticipantActionAttached = kernelimpl.ParticipantActionAttached
	ParticipantActionDetached = kernelimpl.ParticipantActionDetached
)

const (
	LifecycleStatusRunning         = kernelimpl.LifecycleStatusRunning
	LifecycleStatusWaitingApproval = kernelimpl.LifecycleStatusWaitingApproval
	LifecycleStatusInterrupted     = kernelimpl.LifecycleStatusInterrupted
	LifecycleStatusFailed          = kernelimpl.LifecycleStatusFailed
	LifecycleStatusCompleted       = kernelimpl.LifecycleStatusCompleted
)

const (
	SubmissionKindConversation = kernelimpl.SubmissionKindConversation
	SubmissionKindApproval     = kernelimpl.SubmissionKindApproval
)

const (
	CancelStatusCancelled        = kernelimpl.CancelStatusCancelled
	CancelStatusAlreadyCancelled = kernelimpl.CancelStatusAlreadyCancelled
)

var New = kernelimpl.New
var NewAssemblyResolver = kernelimpl.NewAssemblyResolver
var NormalizeApprovalMode = kernelimpl.NormalizeApprovalMode
var CurrentApprovalMode = kernelimpl.CurrentApprovalMode
var CurrentModelAlias = kernelimpl.CurrentModelAlias
var CurrentReasoningEffort = kernelimpl.CurrentReasoningEffort
var CurrentSandboxMode = kernelimpl.CurrentSandboxMode
var CurrentSessionMode = kernelimpl.CurrentSessionMode
var ClassifySurface = kernelimpl.ClassifySurface
var EventError = kernelimpl.EventError
var As = kernelimpl.As
var CleanSubagentFinalOutput = kernelimpl.CleanSubagentFinalOutput
var ProjectSessionEvent = kernelimpl.ProjectSessionEvent
var EventMetaString = kernelimpl.EventMetaString
var EventMetaBool = kernelimpl.EventMetaBool
var FormatApprovalReviewText = kernelimpl.FormatApprovalReviewText
var UsageSnapshotFromSessionEvent = kernelimpl.UsageSnapshotFromSessionEvent
var UsageSnapshotFromMap = kernelimpl.UsageSnapshotFromMap
var AssistantText = kernelimpl.AssistantText
var PromptTokens = kernelimpl.PromptTokens
var CachedInputTokens = kernelimpl.CachedInputTokens

const (
	ActiveTurnKindKernel      = kernelimpl.ActiveTurnKindKernel
	ActiveTurnKindParticipant = kernelimpl.ActiveTurnKindParticipant
)
