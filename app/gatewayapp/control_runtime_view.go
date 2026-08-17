package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
)

// ControlRuntimeView is the focused, read-only Runtime projection consumed by
// Host-private AppServer adapters. It deliberately contains no concrete Stack
// handle, mutation service, lifecycle owner, or Runtime assembly capability.
type ControlRuntimeView struct {
	Sessions interface {
		session.Reader
		session.StateReader
	}
	AppName   string
	UserID    string
	Workspace session.WorkspaceRef

	TurnStateFn         func() KernelTurnReader
	ControlPlaneStateFn func() KernelControlPlaneReader

	RuntimeStateFn          func(context.Context, session.SessionRef) (SessionRuntimeState, error)
	ConfigurationRevisionFn func(context.Context) (uint64, error)
	DoctorFn                func(context.Context, DoctorRequest) (DoctorReport, error)

	ControllerStatusFn     func(context.Context, session.SessionRef) (controller.ControllerStatus, bool, error)
	DisconnectCandidatesFn func(context.Context) ([]controlagents.DisconnectCandidate, error)
	ListAgentsFn           func() []ACPAgentInfo

	EffectiveModelAliasFn  func() string
	EffectiveModelEffortFn func() string
	ModelConfigFn          func(string) (ModelConfig, bool)
	SessionUsageSnapshotFn func(context.Context, session.SessionRef, string) (compact.UsageSnapshot, error)
	ProviderUsageFn        func(context.Context, string) (providerusage.Snapshot, bool, error)
	ListModelAliasesFn     func(context.Context, session.SessionRef) ([]string, error)
	ListModelChoicesFn     func(context.Context, session.SessionRef) ([]ModelChoice, error)
	HasReusableAuthFn      func(context.Context, string, string) bool

	SkillCatalogFn func() skill.Catalog
	SandboxFn      func() SandboxStatus

	ListPluginsFn      func(context.Context) ([]PluginInfo, error)
	ListMarketplacesFn func(context.Context) ([]MarketplaceInfo, error)
	InspectPluginFn    func(context.Context, string) (PluginInfo, error)
}

// ControlRuntimeView returns the adapter projection for this composition. The
// view is app-private glue and must not be exposed to presentation surfaces.
func (s *runtimeComposition) ControlRuntimeView() *ControlRuntimeView {
	if s == nil {
		return nil
	}
	models := s.Models()
	agents := s.Agents()
	skills := s.Skills()
	status := s.Status()
	plugins := s.Plugins()
	return &ControlRuntimeView{
		Sessions:  s.sessions,
		AppName:   s.authorities.appName,
		UserID:    s.authorities.userID,
		Workspace: s.workspace,

		TurnStateFn:         s.KernelTurnState,
		ControlPlaneStateFn: s.KernelControlPlaneState,

		RuntimeStateFn:          status.SessionRuntimeState,
		ConfigurationRevisionFn: s.ConfigurationRevision,
		DoctorFn:                status.Doctor,

		ControllerStatusFn:     agents.ControllerStatus,
		DisconnectCandidatesFn: agents.DisconnectCandidates,
		ListAgentsFn:           agents.List,

		EffectiveModelAliasFn:  models.EffectiveAlias,
		EffectiveModelEffortFn: models.EffectiveEffort,
		ModelConfigFn:          models.Config,
		SessionUsageSnapshotFn: models.UsageSnapshot,
		ProviderUsageFn:        models.ProviderUsage,
		ListModelAliasesFn:     models.ListAliases,
		ListModelChoicesFn:     models.ListChoices,
		HasReusableAuthFn:      models.HasReusableAuth,

		SkillCatalogFn: skills.Snapshot,
		SandboxFn:      status.Sandbox,

		ListPluginsFn:      plugins.List,
		ListMarketplacesFn: plugins.ListMarketplaces,
		InspectPluginFn:    plugins.Inspect,
	}
}
