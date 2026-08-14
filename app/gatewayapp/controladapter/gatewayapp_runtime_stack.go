package controladapter

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type RuntimeStackGatewayAppAdapters struct {
	SandboxStatus        func(gatewayapp.SandboxStatus) SandboxStatus
	SessionRuntimeState  func(gatewayapp.SessionRuntimeState, error) (SessionRuntimeState, error)
	ModelChoices         func([]gatewayapp.ModelChoice, error) ([]ModelChoice, error)
	DoctorRequest        func(DoctorRequest) gatewayapp.DoctorRequest
	DoctorReport         func(gatewayapp.DoctorReport, error) (DoctorReport, error)
	ACPAgents            func([]gatewayapp.ACPAgentInfo) []ACPAgentInfo
	PluginSnapshots      func([]gatewayapp.PluginInfo, error) ([]controlprompt.PluginSnapshot, error)
	PluginSnapshot       func(gatewayapp.PluginInfo, error) (controlprompt.PluginSnapshot, error)
	MarketplaceSnapshots func([]gatewayapp.MarketplaceInfo, error) ([]controlprompt.MarketplaceSnapshot, error)
	MarketplaceSnapshot  func(gatewayapp.MarketplaceInfo, error) (controlprompt.MarketplaceSnapshot, error)
}

func NewRuntimeStackFromGatewayApp(stack *gatewayapp.Stack, adapters RuntimeStackGatewayAppAdapters) *RuntimeStack {
	if stack == nil {
		return nil
	}
	models := stack.Models()
	agents := stack.Agents()
	skills := stack.Skills()
	status := stack.Status()
	plugins := stack.Plugins()
	return &RuntimeStack{
		Gateway: gatewayDepsFromStack(stack),
		Session: SessionRuntimeDeps{
			Store:     stack.Sessions,
			AppName:   stack.AppName,
			UserID:    stack.UserID,
			Workspace: stack.Workspace,
		},
		Status: StatusRuntimeDeps{
			RuntimeStateFn: func(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
				return adapters.SessionRuntimeState(status.SessionRuntimeState(ctx, ref))
			},
			ConfigurationRevisionFn: stack.ConfigurationRevision,
			DoctorFn: func(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
				return adapters.DoctorReport(status.Doctor(ctx, adapters.DoctorRequest(req)))
			},
		},
		Agent: AgentRuntimeDeps{
			ControllerStatusFn:     agents.ControllerStatus,
			DisconnectCandidatesFn: agents.DisconnectCandidates,
			ListFn:                 func() []ACPAgentInfo { return adapters.ACPAgents(agents.List()) },
		},
		Model: ModelRuntimeDeps{
			EffectiveAliasFn:  models.EffectiveAlias,
			EffectiveEffortFn: models.EffectiveEffort,
			ConfigFn: func(alias string) (ModelConfig, bool) {
				return models.Config(alias)
			},
			SessionUsageSnapshotFn: models.UsageSnapshot,
			ProviderUsageFn:        models.ProviderUsage,
			ListAliasesFn:          models.ListAliases,
			ListChoicesFn: func(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
				return adapters.ModelChoices(models.ListChoices(ctx, ref))
			},
			HasReusableAuthFn: models.HasReusableAuth,
		},
		Skill: SkillRuntimeDeps{
			SnapshotFn: skills.Snapshot,
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus { return adapters.SandboxStatus(status.Sandbox()) },
		},
		Plugin: PluginRuntimeDeps{
			ListPluginsFn: func(ctx context.Context) ([]controlprompt.PluginSnapshot, error) {
				return adapters.PluginSnapshots(plugins.List(ctx))
			},
			ListMarketplacesFn: func(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
				return adapters.MarketplaceSnapshots(plugins.ListMarketplaces(ctx))
			},
			InspectPluginFn: func(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
				return adapters.PluginSnapshot(plugins.Inspect(ctx, id))
			},
		},
	}
}

func gatewayDepsFromStack(stack *gatewayapp.Stack) GatewayRuntimeDeps {
	return GatewayRuntimeDeps{
		TurnServiceFn:         func() GatewayTurnService { return stack.KernelTurnState() },
		SessionServiceFn:      func() GatewaySessionService { return stack.KernelSessionState() },
		ControlPlaneServiceFn: func() GatewayControlPlaneService { return stack.KernelControlPlaneState() },
	}
}
