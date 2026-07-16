package controladapter

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
)

type RuntimeStackGatewayAppAdapters struct {
	SandboxStatus        func(gatewayapp.SandboxStatus) SandboxStatus
	SessionRuntimeState  func(gatewayapp.SessionRuntimeState, error) (SessionRuntimeState, error)
	ModelChoices         func([]gatewayapp.ModelChoice, error) ([]ModelChoice, error)
	DoctorRequest        func(DoctorRequest) gatewayapp.DoctorRequest
	DoctorReport         func(gatewayapp.DoctorReport, error) (DoctorReport, error)
	ACPAgents            func([]gatewayapp.ACPAgentInfo) []ACPAgentInfo
	PluginSnapshots      func([]gatewayapp.PluginInfo, error) ([]PluginSnapshot, error)
	PluginSnapshot       func(gatewayapp.PluginInfo, error) (PluginSnapshot, error)
	MarketplaceSnapshots func([]gatewayapp.MarketplaceInfo, error) ([]MarketplaceSnapshot, error)
	MarketplaceSnapshot  func(gatewayapp.MarketplaceInfo, error) (MarketplaceSnapshot, error)
}

func NewRuntimeStackFromGatewayApp(stack *gatewayapp.Stack, adapters RuntimeStackGatewayAppAdapters) *RuntimeStack {
	if stack == nil {
		return nil
	}
	models := stack.Models()
	agents := stack.Agents()
	delegation := stack.Delegation()
	systemAgents := stack.SystemAgents()
	skills := stack.Skills()
	status := stack.Status()
	plugins := stack.Plugins()
	return &RuntimeStack{
		Gateway:          gatewayDepsFromStack(stack),
		ControlFeeds:     stack.ControlClientFeeds(),
		ControlReconnect: stack.ControlClientReconnect(),
		Session: SessionRuntimeDeps{
			Store:     stack.Sessions,
			AppName:   stack.AppName,
			UserID:    stack.UserID,
			Workspace: stack.Workspace,
			StartFn:   stack.StartSession,
			CompactFn: stack.CompactSession,
		},
		Status: StatusRuntimeDeps{
			RuntimeStateFn: func(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
				return adapters.SessionRuntimeState(status.SessionRuntimeState(ctx, ref))
			},
			DoctorFn: func(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
				return adapters.DoctorReport(status.Doctor(ctx, adapters.DoctorRequest(req)))
			},
			CycleModeFn:      status.CycleSessionMode,
			SetSessionModeFn: status.SetSessionMode,
		},
		Agent: AgentRuntimeDeps{
			ControllerStatusFn:     agents.ControllerStatus,
			SetControllerModelFn:   agents.SetControllerModel,
			SetControllerModeFn:    agents.SetControllerMode,
			DiscoverConnectionFn:   agents.DiscoverConnection,
			ConnectFn:              agents.Connect,
			DisconnectCandidatesFn: agents.DisconnectCandidates,
			DisconnectFn:           agents.Disconnect,
			ListFn:                 func() []ACPAgentInfo { return adapters.ACPAgents(agents.List()) },
		},
		Delegation: DelegationRuntimeDeps{
			StatusFn: delegation.DelegationStatus,
			BindFn:   delegation.BindDelegation,
			ResetFn:  delegation.ResetDelegation,
		},
		SystemAgent: SystemAgentRuntimeDeps{
			StatusFn: systemAgents.SystemAgentStatus,
			BindFn:   systemAgents.BindSystemAgent,
			ResetFn:  systemAgents.ResetSystemAgent,
		},
		Model: ModelRuntimeDeps{
			DefaultAliasFn: models.DefaultAlias,
			ConfigFn: func(alias string) (ModelConfig, bool) {
				return models.Config(alias)
			},
			SessionUsageSnapshotFn: models.UsageSnapshot,
			ConnectModelsFn:        models.ConnectModels,
			UseFn:                  models.Use,
			DeleteFn:               models.Delete,
			ListAliasesFn:          models.ListAliases,
			ListChoicesFn: func(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
				return adapters.ModelChoices(models.ListChoices(ctx, ref))
			},
			AuthenticateFn: models.Authenticate,
		},
		Skill: SkillRuntimeDeps{
			SnapshotFn: skills.Snapshot,
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus { return adapters.SandboxStatus(status.Sandbox()) },
			SetBackendFn: func(ctx context.Context, backend string) (SandboxStatus, error) {
				snapshot, err := status.SetSandboxBackend(ctx, backend)
				return adapters.SandboxStatus(snapshot), err
			},
			PrepareFn: func(ctx context.Context) (SandboxStatus, error) {
				snapshot, err := status.PrepareSandbox(ctx)
				return adapters.SandboxStatus(snapshot), err
			},
			RepairFn: func(ctx context.Context) (SandboxStatus, error) {
				snapshot, err := status.RepairSandbox(ctx)
				return adapters.SandboxStatus(snapshot), err
			},
			PreflightFn: func(ctx context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
				snapshot, err := status.PreflightSandbox(ctx, allowNonElevatedRepair)
				return adapters.SandboxStatus(snapshot), err
			},
			ResetFn: func(ctx context.Context) (SandboxStatus, error) {
				snapshot, err := status.ResetSandbox(ctx)
				return adapters.SandboxStatus(snapshot), err
			},
		},
		Plugin: PluginRuntimeDeps{
			ListPluginsFn: func(ctx context.Context) ([]PluginSnapshot, error) {
				return adapters.PluginSnapshots(plugins.List(ctx))
			},
			AddMarketplaceFn: func(ctx context.Context, source string) (MarketplaceSnapshot, error) {
				return adapters.MarketplaceSnapshot(plugins.AddMarketplace(ctx, source))
			},
			ListMarketplacesFn: func(ctx context.Context) ([]MarketplaceSnapshot, error) {
				return adapters.MarketplaceSnapshots(plugins.ListMarketplaces(ctx))
			},
			UpdateMarketplaceFn: func(ctx context.Context, name string) (MarketplaceSnapshot, error) {
				return adapters.MarketplaceSnapshot(plugins.UpdateMarketplace(ctx, name))
			},
			RemoveMarketplaceFn: func(ctx context.Context, name string) error {
				return plugins.RemoveMarketplace(ctx, name)
			},
			AddPluginPathFn: func(ctx context.Context, path string) (PluginSnapshot, error) {
				return adapters.PluginSnapshot(plugins.AddPath(ctx, path))
			},
			InstallPluginFn: func(ctx context.Context, source string) (PluginSnapshot, error) {
				return adapters.PluginSnapshot(plugins.Install(ctx, source))
			},
			EnablePluginFn: func(ctx context.Context, id string) (PluginSnapshot, error) {
				return adapters.PluginSnapshot(plugins.Enable(ctx, id))
			},
			DisablePluginFn: func(ctx context.Context, id string) (PluginSnapshot, error) {
				return adapters.PluginSnapshot(plugins.Disable(ctx, id))
			},
			RemovePluginFn: func(ctx context.Context, id string) error {
				return plugins.Remove(ctx, id)
			},
			InspectPluginFn: func(ctx context.Context, id string) (PluginSnapshot, error) {
				return adapters.PluginSnapshot(plugins.Inspect(ctx, id))
			},
		},
	}
}

func gatewayDepsFromStack(stack *gatewayapp.Stack) GatewayRuntimeDeps {
	return GatewayRuntimeDeps{
		TurnServiceFn:         func() GatewayTurnService { return stack.KernelTurns() },
		SessionServiceFn:      func() GatewaySessionService { return stack.KernelSessions() },
		ControlPlaneServiceFn: func() GatewayControlPlaneService { return stack.KernelControlPlane() },
		StreamProviderFn:      func() GatewayStreamProvider { return stack.KernelStreams() },
	}
}
