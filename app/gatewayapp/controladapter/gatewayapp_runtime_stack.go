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
}

func NewRuntimeStackFromGatewayApp(view *gatewayapp.ControlRuntimeView, adapters RuntimeStackGatewayAppAdapters) *RuntimeStack {
	if view == nil {
		return nil
	}
	return &RuntimeStack{
		Gateway: GatewayRuntimeDeps{
			TurnServiceFn: func() GatewayTurnService {
				return view.TurnStateFn()
			},
			SessionServiceFn: func() GatewaySessionService {
				return view.SessionStateFn()
			},
			ControlPlaneServiceFn: func() GatewayControlPlaneService {
				return view.ControlPlaneStateFn()
			},
		},
		Session: SessionRuntimeDeps{
			Store:     view.Sessions,
			AppName:   view.AppName,
			UserID:    view.UserID,
			Workspace: view.Workspace,
		},
		Status: StatusRuntimeDeps{
			RuntimeStateFn: func(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
				return adapters.SessionRuntimeState(view.RuntimeStateFn(ctx, ref))
			},
			ConfigurationRevisionFn: view.ConfigurationRevisionFn,
			DoctorFn: func(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
				return adapters.DoctorReport(view.DoctorFn(ctx, adapters.DoctorRequest(req)))
			},
		},
		Agent: AgentRuntimeDeps{
			ControllerStatusFn:     view.ControllerStatusFn,
			DisconnectCandidatesFn: view.DisconnectCandidatesFn,
			ListFn:                 func() []ACPAgentInfo { return adapters.ACPAgents(view.ListAgentsFn()) },
		},
		Model: ModelRuntimeDeps{
			EffectiveAliasFn:  view.EffectiveModelAliasFn,
			EffectiveEffortFn: view.EffectiveModelEffortFn,
			ConfigFn: func(alias string) (ModelConfig, bool) {
				return view.ModelConfigFn(alias)
			},
			SessionUsageSnapshotFn: view.SessionUsageSnapshotFn,
			ProviderUsageFn:        view.ProviderUsageFn,
			ListAliasesFn:          view.ListModelAliasesFn,
			ListChoicesFn: func(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
				return adapters.ModelChoices(view.ListModelChoicesFn(ctx, ref))
			},
			HasReusableAuthFn: view.HasReusableAuthFn,
		},
		Skill: SkillRuntimeDeps{
			SnapshotFn: view.SkillCatalogFn,
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus { return adapters.SandboxStatus(view.SandboxFn()) },
		},
		Plugin: PluginRuntimeDeps{
			ListPluginsFn: func(ctx context.Context) ([]controlprompt.PluginSnapshot, error) {
				return adapters.PluginSnapshots(view.ListPluginsFn(ctx))
			},
			ListMarketplacesFn: func(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
				return adapters.MarketplaceSnapshots(view.ListMarketplacesFn(ctx))
			},
			InspectPluginFn: func(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
				return adapters.PluginSnapshot(view.InspectPluginFn(ctx, id))
			},
		},
	}
}
