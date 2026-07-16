package controladapter

import (
	"context"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	"github.com/caelis-labs/caelis/control/modelconfig"
	controlsystemagent "github.com/caelis-labs/caelis/control/systemagent"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
)

// GatewayTurnService exposes the turn operations used by Adapter.
type GatewayTurnService interface {
	BeginTurn(context.Context, gateway.BeginTurnRequest) (gateway.BeginTurnResult, error)
	SubmitActiveTurn(context.Context, gateway.SubmitActiveTurnRequest) error
	Interrupt(context.Context, gateway.InterruptRequest) error
	ActiveTurns() []gateway.ActiveTurnState
}

// GatewaySessionService exposes the session operations used by Adapter.
type GatewaySessionService interface {
	ResumeSession(context.Context, gateway.ResumeSessionRequest) (session.LoadedSession, error)
	ListSessions(context.Context, gateway.ListSessionsRequest) (session.SessionList, error)
}

// GatewayControlPlaneService exposes controller and participant operations used by Adapter.
type GatewayControlPlaneService interface {
	ControlPlaneState(context.Context, gateway.ControlPlaneStateRequest) (gateway.ControlPlaneState, error)
	HandoffController(context.Context, gateway.HandoffControllerRequest) (session.Session, error)
	AttachParticipant(context.Context, gateway.AttachParticipantRequest) (session.Session, error)
	PromptParticipant(context.Context, gateway.PromptParticipantRequest) (gateway.BeginTurnResult, error)
	StartParticipant(context.Context, gateway.StartParticipantRequest) (gateway.BeginTurnResult, error)
	DetachParticipant(context.Context, gateway.DetachParticipantRequest) (session.Session, error)
}

// GatewayStreamProvider exposes stream subscription access used by Adapter.
type GatewayStreamProvider interface {
	gateway.StreamProvider
}

type ModelConfig = modelconfig.Config

type ModelChoice struct {
	ID         string
	Alias      string
	Provider   string
	Model      string
	ProfileID  string
	EndpointID string
	BaseURL    string
	Detail     string
}

type SystemAgentRuntimeDeps struct {
	StatusFn func(context.Context) (controlsystemagent.Status, error)
	BindFn   func(context.Context, controlsystemagent.BindRequest) (controlsystemagent.Status, error)
	ResetFn  func(context.Context, controlsystemagent.ID) (controlsystemagent.Status, error)
}

type SessionRuntimeState struct {
	ModelID         string
	ModelAlias      string
	ReasoningEffort string
	SessionMode     string
	SandboxMode     string
}

type SandboxStatus struct {
	RequestedBackend         string
	ResolvedBackend          string
	Route                    string
	FallbackReason           string
	InstallHint              string
	Setup                    sandbox.SetupStatus
	SetupRequired            bool
	SetupError               string
	SetupMarkerCurrent       bool
	SetupMarkerReason        string
	SecuritySummary          string
	GlobalSetupCurrent       bool
	GlobalSetupRequired      bool
	GlobalSetupReason        string
	WorkspaceSetupCurrent    bool
	WorkspaceSetupRequired   bool
	WorkspaceSetupReason     string
	WorkspaceSetupRoot       string
	WorkspaceSetupWriteRoots int
	WorkspaceSetupPolicyHash string
	WorkspaceSetupUpdatedAt  time.Time
}

type DoctorRequest struct {
	SessionRef session.SessionRef
	SessionID  string
	BindingKey string
}

type DoctorReport struct {
	StoreDir                        string
	SessionID                       string
	SessionMode                     string
	ActiveModelAlias                string
	ActiveProvider                  string
	ActiveModel                     string
	MissingAPIKey                   bool
	SandboxRequestedBackend         string
	SandboxResolvedBackend          string
	SandboxRoute                    string
	SandboxFallbackReason           string
	SandboxInstallHint              string
	SandboxSetup                    *sandbox.SetupStatus
	SandboxSetupRequired            bool
	SandboxSetupError               string
	SandboxSetupMarkerCurrent       bool
	SandboxSetupMarkerReason        string
	SandboxSecuritySummary          string
	SandboxGlobalSetupCurrent       bool
	SandboxGlobalSetupRequired      bool
	SandboxGlobalSetupReason        string
	SandboxWorkspaceSetupCurrent    bool
	SandboxWorkspaceSetupRequired   bool
	SandboxWorkspaceSetupReason     string
	SandboxWorkspaceSetupRoot       string
	SandboxWorkspaceSetupWriteRoots int
	SandboxWorkspaceSetupPolicyHash string
	SandboxWorkspaceSetupUpdatedAt  time.Time
	HostExecution                   bool
	FullAccessMode                  bool
	ConfigPermissionsSecure         bool
	Warnings                        []string
}

type ACPAgentInfo struct {
	Name        string
	Description string
}

// PluginRuntimeDeps carries plugin and marketplace commands. Each hook fails
// when its command is invoked but absent.
type PluginRuntimeDeps struct {
	ListPluginsFn       func(context.Context) ([]PluginSnapshot, error)
	AddMarketplaceFn    func(context.Context, string) (MarketplaceSnapshot, error)
	ListMarketplacesFn  func(context.Context) ([]MarketplaceSnapshot, error)
	UpdateMarketplaceFn func(context.Context, string) (MarketplaceSnapshot, error)
	RemoveMarketplaceFn func(context.Context, string) error
	AddPluginPathFn     func(context.Context, string) (PluginSnapshot, error)
	InstallPluginFn     func(context.Context, string) (PluginSnapshot, error)
	EnablePluginFn      func(context.Context, string) (PluginSnapshot, error)
	DisablePluginFn     func(context.Context, string) (PluginSnapshot, error)
	RemovePluginFn      func(context.Context, string) error
	InspectPluginFn     func(context.Context, string) (PluginSnapshot, error)
}

// GatewayRuntimeDeps is required for turn/session stream operations.
type GatewayRuntimeDeps struct {
	TurnServiceFn         func() GatewayTurnService
	SessionServiceFn      func() GatewaySessionService
	ControlPlaneServiceFn func() GatewayControlPlaneService
	StreamProviderFn      func() GatewayStreamProvider
}

// SessionRuntimeDeps owns durable session identity and storage dependencies.
// Store is optional for lightweight adapters; StartFn and CompactFn are
// required only when the corresponding session operation is invoked.
type SessionRuntimeDeps struct {
	Store     session.Service
	AppName   string
	UserID    string
	Workspace session.WorkspaceRef
	StartFn   func(context.Context, string, string) (session.Session, error)
	CompactFn func(context.Context, session.SessionRef) error
}

// StatusRuntimeDeps carries runtime state lookups. Read-only status hooks may
// be omitted for lightweight adapters; mutating mode hooks fail when absent.
type StatusRuntimeDeps struct {
	RuntimeStateFn   func(context.Context, session.SessionRef) (SessionRuntimeState, error)
	DoctorFn         func(context.Context, DoctorRequest) (DoctorReport, error)
	CycleModeFn      func(context.Context, session.SessionRef) (string, error)
	SetSessionModeFn func(context.Context, session.SessionRef, string) (string, error)
}

// AgentRuntimeDeps carries ACP controller and registered-agent capabilities.
// ControllerStatusFn is optional and degrades to the session binding; command
// and mutation hooks fail when invoked but absent.
type AgentRuntimeDeps struct {
	ControllerStatusFn     func(context.Context, session.SessionRef) (controller.ControllerStatus, bool, error)
	SetControllerModelFn   func(context.Context, session.SessionRef, string, string) (controller.ControllerStatus, error)
	SetControllerModeFn    func(context.Context, session.SessionRef, string) (controller.ControllerStatus, error)
	DiscoverConnectionFn   func(context.Context, controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error)
	ConnectFn              func(context.Context, controlagents.ConnectRequest) (controlagents.ConnectResult, error)
	DisconnectCandidatesFn func(context.Context) ([]controlagents.DisconnectCandidate, error)
	DisconnectFn           func(context.Context, string) (controlagents.DisconnectResult, error)
	ListFn                 func() []ACPAgentInfo
}

// DelegationRuntimeDeps carries the Control-owned fixed profile bindings.
type DelegationRuntimeDeps struct {
	StatusFn func(context.Context) (controldelegation.Status, error)
	BindFn   func(context.Context, controldelegation.BindRequest) (controldelegation.Status, error)
	ResetFn  func(context.Context, controldelegation.Profile) (controldelegation.Status, error)
}

// ModelRuntimeDeps carries model catalog and mutation capabilities. Metadata
// reads can return zero values when absent; connect/use/delete operations fail
// when invoked without their backing hooks.
type ModelRuntimeDeps struct {
	DefaultAliasFn         func() string
	ConfigFn               func(string) (ModelConfig, bool)
	SessionUsageSnapshotFn func(context.Context, session.SessionRef, string) (compact.UsageSnapshot, error)
	ConnectModelsFn        func([]ModelConfig) ([]string, error)
	UseFn                  func(context.Context, session.SessionRef, string, ...string) error
	DeleteFn               func(context.Context, session.SessionRef, string) error
	ListAliasesFn          func(context.Context, session.SessionRef) ([]string, error)
	ListChoicesFn          func(context.Context, session.SessionRef) ([]ModelChoice, error)
	AuthenticateFn         modelconfig.AuthenticateFunc
}

// SkillRuntimeDeps carries access to the current runtime skill catalog used for
// completions.
type SkillRuntimeDeps struct {
	SnapshotFn func() skill.Catalog
}

func (deps SkillRuntimeDeps) Snapshot() skill.Catalog {
	if deps.SnapshotFn == nil {
		return skill.Catalog{}
	}
	return deps.SnapshotFn()
}

// SandboxRuntimeDeps carries sandbox status and lifecycle commands. Status and
// preflight can degrade to zero-value status; mutating lifecycle hooks fail
// when invoked but absent.
type SandboxRuntimeDeps struct {
	StatusFn     func() SandboxStatus
	SetBackendFn func(context.Context, string) (SandboxStatus, error)
	PrepareFn    func(context.Context) (SandboxStatus, error)
	RepairFn     func(context.Context) (SandboxStatus, error)
	PreflightFn  func(context.Context, bool) (SandboxStatus, error)
	ResetFn      func(context.Context) (SandboxStatus, error)
}

type RuntimeStack struct {
	Gateway          GatewayRuntimeDeps
	ControlFeeds     controlclientport.FeedRegistry
	ControlReconnect controlclientport.ReconnectReader
	Session          SessionRuntimeDeps
	Status           StatusRuntimeDeps
	Agent            AgentRuntimeDeps
	Delegation       DelegationRuntimeDeps
	SystemAgent      SystemAgentRuntimeDeps
	Model            ModelRuntimeDeps
	Sandbox          SandboxRuntimeDeps
	Skill            SkillRuntimeDeps
	Plugin           PluginRuntimeDeps
}

func missingRuntimeDependency(name string) error {
	return fmt.Errorf("app/gatewayapp/controladapter: %s dependency is unavailable", name)
}

func listModelChoices(ctx context.Context, deps ModelRuntimeDeps, ref session.SessionRef) ([]ModelChoice, error) {
	if deps.ListChoicesFn != nil {
		return deps.ListChoicesFn(ctx, ref)
	}
	if deps.ListAliasesFn == nil {
		return nil, missingRuntimeDependency("model alias")
	}
	aliases, err := deps.ListAliasesFn(ctx, ref)
	if err != nil {
		return nil, err
	}
	choices := make([]ModelChoice, 0, len(aliases))
	for _, alias := range aliases {
		choices = append(choices, ModelChoice{ID: alias, Alias: alias})
	}
	return choices, nil
}
