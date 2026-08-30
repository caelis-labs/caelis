package controladapter

import (
	"context"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/control/workspacetrust"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// GatewayTurnService exposes the live-turn state read used by server
// assemblers. Turn mutation remains behind typed AppServer clients.
type GatewayTurnService interface {
	ActiveTurns() []kernel.ActiveTurnState
}

// GatewayControlPlaneService exposes the controller and participant state read
// used by status and completion.
type GatewayControlPlaneService interface {
	ControlPlaneState(context.Context, kernel.ControlPlaneStateRequest) (kernel.ControlPlaneState, error)
}

type ModelConfig = modelconfig.Config

type ModelChoice = modelconfig.Choice
type SessionRuntimeState = controlstatus.SessionRuntimeState

// SandboxStatusProjection is the subset of Host sandbox state consumed by the
// AppServer status assembler. Host lifecycle and setup authorities stay out of
// this projection.
type SandboxStatusProjection struct {
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
	FullAccessMode           bool
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

type DoctorRequest = controlstatus.DoctorRequest

// DoctorStatusProjection is the diagnostics subset consumed by the stable
// Control status read model. Host-only setup detail remains in gatewayapp.
type DoctorStatusProjection struct {
	GoVersion                       string
	GOOS                            string
	GOARCH                          string
	StoreDir                        string
	ConfigPath                      string
	ConfigDirMode                   string
	ConfigFileMode                  string
	ConfigDirSecure                 bool
	ConfigFileSecure                bool
	ConfigPermissionsSecure         bool
	SessionID                       string
	SessionMode                     string
	PolicyProfile                   string
	ActiveModelAlias                string
	ActiveProvider                  string
	ActiveModel                     string
	MissingAPIKey                   bool
	TokenSource                     string
	PersistedPlaintextToken         bool
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
	ActiveTurnSessions              []string
	Warnings                        []string
}

type ACPAgentInfo = controlagents.CatalogEntry

// PluginRuntimeDeps carries pure plugin and marketplace reads. Host mutations
// use the shared Control command path instead of assembler hooks.
type PluginRuntimeDeps struct {
	ListPluginsFn      func(context.Context) ([]controlprompt.PluginSnapshot, error)
	ListMarketplacesFn func(context.Context) ([]controlprompt.MarketplaceSnapshot, error)
	InspectPluginFn    func(context.Context, string) (controlprompt.PluginSnapshot, error)
}

// GatewayRuntimeDeps carries only read-only gateway projections needed while
// assembling focused AppServer services.
type GatewayRuntimeDeps struct {
	TurnServiceFn         func() GatewayTurnService
	ControlPlaneServiceFn func() GatewayControlPlaneService
}

// SessionRuntimeDeps owns durable session identity and storage dependencies.
// Store is optional for lightweight assemblers.
type SessionRuntimeDeps struct {
	Store interface {
		session.Reader
		session.StateReader
	}
	AppName string
	// UserID supplies the durable Session identity used by bound assemblers.
	// Visibility-sensitive Session listing must use the principal-bound
	// ListSessionsFn.
	UserID         string
	Workspace      session.WorkspaceRef
	ListSessionsFn func(context.Context, kernel.ListSessionsRequest) (session.SessionList, error)
}

// StatusRuntimeDeps carries read-only runtime state lookups.
type StatusRuntimeDeps struct {
	RuntimeStateFn          func(context.Context, session.SessionRef) (SessionRuntimeState, error)
	ConfigurationRevisionFn func(context.Context) (uint64, error)
	WorkspaceTrustFn        func(context.Context, string) (workspacetrust.Level, error)
	DoctorFn                func(context.Context, DoctorRequest) (DoctorStatusProjection, error)
	TaskEntriesFn           func(context.Context, session.SessionRef) ([]*taskapi.Entry, error)
}

// AgentRuntimeDeps carries ACP controller and registered-agent capabilities.
// ControllerStatusFn is optional and degrades to the session binding.
type AgentRuntimeDeps struct {
	ControllerStatusFn     func(context.Context, session.SessionRef) (controller.ControllerStatus, bool, error)
	DisconnectCandidatesFn func(context.Context) ([]controlagents.DisconnectCandidate, error)
	ListFn                 func() []ACPAgentInfo
}

// ModelRuntimeDeps carries model catalog and authentication capabilities.
// Metadata reads can return zero values when absent. Product mutations are
// exposed only through the focused Configuration command service.
type ModelRuntimeDeps struct {
	EffectiveAliasFn       func() string
	EffectiveEffortFn      func() string
	ConfigFn               func(string) (ModelConfig, bool)
	SessionUsageSnapshotFn func(context.Context, session.SessionRef, string) (compact.UsageSnapshot, error)
	ProviderUsageFn        func(context.Context, string) (providerusage.Snapshot, bool, error)
	ListAliasesFn          func(context.Context, session.SessionRef) ([]string, error)
	ListChoicesFn          func(context.Context, session.SessionRef) ([]ModelChoice, error)
	HasReusableAuthFn      func(context.Context, string, string) bool
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

// SandboxRuntimeDeps carries the read-only sandbox status projection. Product
// lifecycle mutations are exposed only by the principal-bound AppServer
// Configuration capability.
type SandboxRuntimeDeps struct {
	StatusFn func() SandboxStatusProjection
}

// runtimeDeps is the private union used by the shared assembler implementation.
// Public constructors accept only the focused capability groups above.
type runtimeDeps struct {
	Gateway GatewayRuntimeDeps
	Session SessionRuntimeDeps
	Status  StatusRuntimeDeps
	Agent   AgentRuntimeDeps
	Model   ModelRuntimeDeps
	Sandbox SandboxRuntimeDeps
	Skill   SkillRuntimeDeps
	Plugin  PluginRuntimeDeps
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
