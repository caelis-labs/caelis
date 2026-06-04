package kernel

import (
	"context"
	"fmt"
	"time"

	kernelimpl "github.com/OnslaughtSnail/caelis/internal/kernel"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/approval"
	assemblyapi "github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/ports/tool"
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

type Config struct {
	Sessions            session.Service
	Runtime             agent.Runtime
	Resolver            TurnResolver
	RequestPolicy       RequestPolicy
	DefaultApprovalMode ApprovalMode
	ApprovalApprover    approval.Approver
	ApprovalReviewer    ApprovalReviewer
	Clock               func() time.Time
}

type Gateway struct {
	inner    *kernelimpl.Gateway
	resolver *AssemblyResolver
}

type AssemblyResolverConfig struct {
	Sessions interface {
		SnapshotState(context.Context, session.SessionRef) (map[string]any, error)
	}
	Assembly          assemblyapi.ResolvedAssembly
	DefaultModelAlias string
	ContextWindow     int
	ModelLookup       ModelLookup
	Tools             []tool.Tool
	AgentName         string
	BaseMetadata      map[string]any
	ToolAugmenter     ToolAugmenter
}

type AssemblyResolver struct {
	inner *kernelimpl.AssemblyResolver
}

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
type StreamRequest = kernelimpl.StreamRequest
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
	StateCurrentApprovalMode    = kernelimpl.StateCurrentApprovalMode
	StateCurrentPolicyProfile   = kernelimpl.StateCurrentPolicyProfile
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

func New(cfg Config) (*Gateway, error) {
	resolver := cfg.Resolver
	publicResolver, _ := resolver.(*AssemblyResolver)
	if publicResolver != nil && publicResolver.inner != nil {
		resolver = publicResolver.inner
	} else if publicResolver != nil {
		resolver = nil
	}
	inner, err := kernelimpl.New(kernelimpl.Config{
		Sessions:            cfg.Sessions,
		Runtime:             cfg.Runtime,
		Resolver:            resolver,
		RequestPolicy:       cfg.RequestPolicy,
		DefaultApprovalMode: cfg.DefaultApprovalMode,
		ApprovalApprover:    cfg.ApprovalApprover,
		ApprovalReviewer:    cfg.ApprovalReviewer,
		Clock:               cfg.Clock,
	})
	if err != nil {
		return nil, err
	}
	return &Gateway{inner: inner, resolver: publicResolver}, nil
}

func NewAssemblyResolver(cfg AssemblyResolverConfig) (*AssemblyResolver, error) {
	inner, err := kernelimpl.NewAssemblyResolver(kernelimpl.AssemblyResolverConfig{
		Sessions:          cfg.Sessions,
		Assembly:          cfg.Assembly,
		DefaultModelAlias: cfg.DefaultModelAlias,
		ContextWindow:     cfg.ContextWindow,
		ModelLookup:       cfg.ModelLookup,
		Tools:             cfg.Tools,
		AgentName:         cfg.AgentName,
		BaseMetadata:      cfg.BaseMetadata,
		ToolAugmenter:     cfg.ToolAugmenter,
	})
	if err != nil {
		return nil, err
	}
	return &AssemblyResolver{inner: inner}, nil
}

func (g *Gateway) StartSession(ctx context.Context, req StartSessionRequest) (Session, error) {
	if g == nil || g.inner == nil {
		return Session{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.StartSession(ctx, req)
}

func (g *Gateway) LoadSession(ctx context.Context, req LoadSessionRequest) (LoadedSession, error) {
	if g == nil || g.inner == nil {
		return LoadedSession{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.LoadSession(ctx, req)
}

func (g *Gateway) ResumeSession(ctx context.Context, req ResumeSessionRequest) (LoadedSession, error) {
	if g == nil || g.inner == nil {
		return LoadedSession{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.ResumeSession(ctx, req)
}

func (g *Gateway) ForkSession(ctx context.Context, req ForkSessionRequest) (Session, error) {
	if g == nil || g.inner == nil {
		return Session{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.ForkSession(ctx, req)
}

func (g *Gateway) ListSessions(ctx context.Context, req ListSessionsRequest) (SessionList, error) {
	if g == nil || g.inner == nil {
		return SessionList{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.ListSessions(ctx, req)
}

func (g *Gateway) BindSession(ctx context.Context, req BindSessionRequest) error {
	if g == nil || g.inner == nil {
		return fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.BindSession(ctx, req)
}

func (g *Gateway) LookupBinding(req BindingStateRequest) (BindingState, error) {
	if g == nil || g.inner == nil {
		return BindingState{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.LookupBinding(req)
}

func (g *Gateway) ReplayEvents(ctx context.Context, req ReplayEventsRequest) (ReplayEventsResult, error) {
	if g == nil || g.inner == nil {
		return ReplayEventsResult{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.ReplayEvents(ctx, req)
}

func (g *Gateway) BeginTurn(ctx context.Context, req BeginTurnRequest) (BeginTurnResult, error) {
	if g == nil || g.inner == nil {
		return BeginTurnResult{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.BeginTurn(ctx, req)
}

func (g *Gateway) SubmitActiveTurn(ctx context.Context, req SubmitActiveTurnRequest) error {
	if g == nil || g.inner == nil {
		return fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.SubmitActiveTurn(ctx, req)
}

func (g *Gateway) Interrupt(ctx context.Context, req InterruptRequest) error {
	if g == nil || g.inner == nil {
		return fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.Interrupt(ctx, req)
}

func (g *Gateway) ActiveTurns() []ActiveTurnState {
	if g == nil || g.inner == nil {
		return nil
	}
	return g.inner.ActiveTurns()
}

func (g *Gateway) ActiveCounts() (int, int) {
	if g == nil || g.inner == nil {
		return 0, 0
	}
	return g.inner.ActiveCounts()
}

func (g *Gateway) ActiveTurn(sessionID string) (ActiveTurnState, bool) {
	if g == nil || g.inner == nil {
		return ActiveTurnState{}, false
	}
	return g.inner.ActiveTurn(sessionID)
}

func (g *Gateway) CancelActiveTurns() {
	if g == nil || g.inner == nil {
		return
	}
	g.inner.CancelActiveTurns()
}

func (g *Gateway) ControlPlaneState(ctx context.Context, req ControlPlaneStateRequest) (ControlPlaneState, error) {
	if g == nil || g.inner == nil {
		return ControlPlaneState{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.ControlPlaneState(ctx, req)
}

func (g *Gateway) HandoffController(ctx context.Context, req HandoffControllerRequest) (Session, error) {
	if g == nil || g.inner == nil {
		return Session{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.HandoffController(ctx, req)
}

func (g *Gateway) AttachParticipant(ctx context.Context, req AttachParticipantRequest) (Session, error) {
	if g == nil || g.inner == nil {
		return Session{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.AttachParticipant(ctx, req)
}

func (g *Gateway) PromptParticipant(ctx context.Context, req PromptParticipantRequest) (BeginTurnResult, error) {
	if g == nil || g.inner == nil {
		return BeginTurnResult{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.PromptParticipant(ctx, req)
}

func (g *Gateway) DetachParticipant(ctx context.Context, req DetachParticipantRequest) (Session, error) {
	if g == nil || g.inner == nil {
		return Session{}, fmt.Errorf("kernel: gateway is not initialized")
	}
	return g.inner.DetachParticipant(ctx, req)
}

func (g *Gateway) Streams() stream.Service {
	if g == nil || g.inner == nil {
		return nil
	}
	return g.inner.Streams()
}

func (g *Gateway) Resolver() *AssemblyResolver {
	if g == nil || g.inner == nil {
		return nil
	}
	if g.resolver != nil {
		return g.resolver
	}
	if inner := g.inner.Resolver(); inner != nil {
		return &AssemblyResolver{inner: inner}
	}
	return nil
}

func (g *Gateway) ApprovalReviewer() ApprovalReviewer {
	if g == nil || g.inner == nil {
		return nil
	}
	return g.inner.ApprovalReviewer()
}

func (g *Gateway) CurrentSession(bindingKey string) (SessionRef, bool) {
	if g == nil || g.inner == nil {
		return SessionRef{}, false
	}
	return g.inner.CurrentSession(bindingKey)
}

func (g *Gateway) ClearBinding(bindingKey string) {
	if g == nil || g.inner == nil {
		return
	}
	g.inner.ClearBinding(bindingKey)
}

func (r *AssemblyResolver) SetModelLookup(lookup ModelLookup, defaultAlias string) {
	if r == nil || r.inner == nil {
		return
	}
	r.inner.SetModelLookup(lookup, defaultAlias)
}

func (r *AssemblyResolver) ResolveTurn(ctx context.Context, intent TurnIntent) (ResolvedTurn, error) {
	if r == nil || r.inner == nil {
		return ResolvedTurn{}, fmt.Errorf("kernel: assembly resolver is not initialized")
	}
	return r.inner.ResolveTurn(ctx, intent)
}

func (r *AssemblyResolver) ResolveControllerTurn(ctx context.Context, intent TurnIntent) (ResolvedTurn, error) {
	if r == nil || r.inner == nil {
		return ResolvedTurn{}, fmt.Errorf("kernel: assembly resolver is not initialized")
	}
	return r.inner.ResolveControllerTurn(ctx, intent)
}

func (r *AssemblyResolver) ResolveApprovalModel(ctx context.Context, ref session.SessionRef) (model.LLM, error) {
	if r == nil || r.inner == nil {
		return nil, fmt.Errorf("kernel: assembly resolver is not initialized")
	}
	return r.inner.ResolveApprovalModel(ctx, ref)
}

func (r *AssemblyResolver) ListModelAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
	if r == nil || r.inner == nil {
		return nil, fmt.Errorf("kernel: assembly resolver is not initialized")
	}
	return r.inner.ListModelAliases(ctx, ref)
}

var NormalizeApprovalMode = kernelimpl.NormalizeApprovalMode
var CurrentApprovalMode = kernelimpl.CurrentApprovalMode
var CurrentApprovalModeOrDefault = kernelimpl.CurrentApprovalModeOrDefault
var CurrentModelAlias = kernelimpl.CurrentModelAlias
var CurrentReasoningEffort = kernelimpl.CurrentReasoningEffort
var CurrentSandboxMode = kernelimpl.CurrentSandboxMode
var CurrentSessionMode = kernelimpl.CurrentSessionMode
var CurrentSessionModeOrDefault = kernelimpl.CurrentSessionModeOrDefault
var CurrentPolicyProfile = kernelimpl.CurrentPolicyProfile
var ClassifySurface = kernelimpl.ClassifySurface
var EventError = kernelimpl.EventError
var As = kernelimpl.As
var StreamRequestFromEvent = kernelimpl.StreamRequestFromEvent
var StreamFrameEvent = kernelimpl.StreamFrameEvent
var StreamFrameEvents = kernelimpl.StreamFrameEvents
var CleanSubagentFinalOutput = kernelimpl.CleanSubagentFinalOutput
var ProjectSessionEvent = kernelimpl.ProjectSessionEvent
var ProjectACPEventEnvelope = kernelimpl.ProjectACPEventEnvelope
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
