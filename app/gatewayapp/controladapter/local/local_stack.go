package local

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type ModelConfig = controladapter.ModelConfig
type ModelChoice = controladapter.ModelChoice
type SessionRuntimeState = controladapter.SessionRuntimeState
type SandboxStatus = controladapter.SandboxStatus
type DoctorRequest = controladapter.DoctorRequest
type DoctorReport = controladapter.DoctorReport
type ACPAgentInfo = controladapter.ACPAgentInfo

func controlRuntimeDeps(stack *gatewayapp.Stack) *controladapter.ControlRuntimeDeps {
	if stack == nil {
		return nil
	}
	return controlRuntimeDepsFromView(stack.ControlRuntimeView())
}

func controlRuntimeDepsFromView(view *gatewayapp.ControlRuntimeView) *controladapter.ControlRuntimeDeps {
	if view == nil {
		return nil
	}
	return &controladapter.ControlRuntimeDeps{
		Gateway: controladapter.GatewayRuntimeDeps{
			TurnServiceFn: func() controladapter.GatewayTurnService {
				return view.TurnStateFn()
			},
			ControlPlaneServiceFn: func() controladapter.GatewayControlPlaneService {
				return view.ControlPlaneStateFn()
			},
		},
		Session: controladapter.SessionRuntimeDeps{
			Store:     view.Sessions,
			AppName:   view.AppName,
			UserID:    view.UserID,
			Workspace: view.Workspace,
		},
		Status: controladapter.StatusRuntimeDeps{
			RuntimeStateFn: func(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
				return toRuntimeSessionRuntimeState(view.RuntimeStateFn(ctx, ref))
			},
			ConfigurationRevisionFn: view.ConfigurationRevisionFn,
			DoctorFn: func(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
				return toRuntimeDoctorReport(view.DoctorFn(ctx, toGatewayDoctorRequest(req)))
			},
		},
		Agent: controladapter.AgentRuntimeDeps{
			ControllerStatusFn:     view.ControllerStatusFn,
			DisconnectCandidatesFn: view.DisconnectCandidatesFn,
			ListFn:                 func() []ACPAgentInfo { return toRuntimeACPAgents(view.ListAgentsFn()) },
		},
		Model: controladapter.ModelRuntimeDeps{
			EffectiveAliasFn:  view.EffectiveModelAliasFn,
			EffectiveEffortFn: view.EffectiveModelEffortFn,
			ConfigFn: func(alias string) (ModelConfig, bool) {
				return view.ModelConfigFn(alias)
			},
			SessionUsageSnapshotFn: view.SessionUsageSnapshotFn,
			ProviderUsageFn:        view.ProviderUsageFn,
			ListAliasesFn:          view.ListModelAliasesFn,
			ListChoicesFn: func(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
				return toRuntimeModelChoices(view.ListModelChoicesFn(ctx, ref))
			},
			HasReusableAuthFn: view.HasReusableAuthFn,
		},
		Skill: controladapter.SkillRuntimeDeps{
			SnapshotFn: view.SkillCatalogFn,
		},
		Sandbox: controladapter.SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus { return toRuntimeSandboxStatus(view.SandboxFn()) },
		},
		Plugin: controladapter.PluginRuntimeDeps{
			ListPluginsFn: func(ctx context.Context) ([]controlprompt.PluginSnapshot, error) {
				return toRuntimePluginSnapshots(view.ListPluginsFn(ctx))
			},
			ListMarketplacesFn: func(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
				return toRuntimeMarketplaceSnapshots(view.ListMarketplacesFn(ctx))
			},
			InspectPluginFn: func(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
				return toRuntimePluginSnapshotWithError(view.InspectPluginFn(ctx, id))
			},
		},
	}
}

func controlRuntimeDepsForWorkspace(stack *gatewayapp.Stack, workspace session.WorkspaceRef) *controladapter.ControlRuntimeDeps {
	deps := controlRuntimeDeps(stack)
	if deps == nil || stack == nil {
		return deps
	}
	deps.Session.Workspace = workspace
	deps.Sandbox.StatusFn = func() SandboxStatus {
		return toRuntimeSandboxStatus(stack.SandboxStatusForWorkspace(workspace))
	}
	deps.Status.DoctorFn = func(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
		return toRuntimeDoctorReport(stack.DoctorForWorkspace(ctx, workspace, toGatewayDoctorRequest(req)))
	}
	return deps
}

func toRuntimeSandboxStatus(status gatewayapp.SandboxStatus) SandboxStatus {
	return SandboxStatus{
		RequestedBackend:         status.RequestedBackend,
		ResolvedBackend:          status.ResolvedBackend,
		Route:                    status.Route,
		FallbackReason:           status.FallbackReason,
		InstallHint:              status.InstallHint,
		Setup:                    sandbox.CloneSetupStatus(status.Setup),
		SetupRequired:            status.SetupRequired,
		SetupError:               status.SetupError,
		SetupMarkerCurrent:       status.SetupMarkerCurrent,
		SetupMarkerReason:        status.SetupMarkerReason,
		SecuritySummary:          status.SecuritySummary,
		FullAccessMode:           status.FullAccessMode,
		GlobalSetupCurrent:       status.GlobalSetupCurrent,
		GlobalSetupRequired:      status.GlobalSetupRequired,
		GlobalSetupReason:        status.GlobalSetupReason,
		WorkspaceSetupCurrent:    status.WorkspaceSetupCurrent,
		WorkspaceSetupRequired:   status.WorkspaceSetupRequired,
		WorkspaceSetupReason:     status.WorkspaceSetupReason,
		WorkspaceSetupRoot:       status.WorkspaceSetupRoot,
		WorkspaceSetupWriteRoots: status.WorkspaceSetupWriteRoots,
		WorkspaceSetupPolicyHash: status.WorkspaceSetupPolicyHash,
		WorkspaceSetupUpdatedAt:  status.WorkspaceSetupUpdatedAt,
	}
}

func toRuntimeSessionRuntimeState(state gatewayapp.SessionRuntimeState, err error) (SessionRuntimeState, error) {
	return SessionRuntimeState{
		ModelID:         state.ModelID,
		ModelAlias:      state.ModelAlias,
		ReasoningEffort: state.ReasoningEffort,
		SessionMode:     state.SessionMode,
		SandboxMode:     state.SandboxMode,
	}, err
}

func toRuntimeModelChoices(choices []gatewayapp.ModelChoice, err error) ([]ModelChoice, error) {
	if err != nil {
		return nil, err
	}
	out := make([]ModelChoice, 0, len(choices))
	for _, choice := range choices {
		out = append(out, ModelChoice{
			ID:                 choice.ID,
			Alias:              choice.Alias,
			Provider:           choice.Provider,
			Model:              choice.Model,
			ProviderEndpointID: choice.ProviderEndpointID,
			EndpointID:         choice.EndpointID,
			BaseURL:            choice.BaseURL,
			Detail:             choice.Detail,
		})
	}
	return out, nil
}

func toGatewayDoctorRequest(req DoctorRequest) gatewayapp.DoctorRequest {
	return gatewayapp.DoctorRequest{
		SessionRef: req.SessionRef,
		SessionID:  req.SessionID,
		BindingKey: req.BindingKey,
	}
}

func toRuntimeDoctorReport(report gatewayapp.DoctorReport, err error) (DoctorReport, error) {
	return DoctorReport{
		GoVersion:                       report.GoVersion,
		GOOS:                            report.GOOS,
		GOARCH:                          report.GOARCH,
		StoreDir:                        report.StoreDir,
		ConfigPath:                      report.ConfigPath,
		ConfigDirMode:                   report.ConfigDirMode,
		ConfigFileMode:                  report.ConfigFileMode,
		ConfigDirSecure:                 report.ConfigDirSecure,
		ConfigFileSecure:                report.ConfigFileSecure,
		ConfigPermissionsSecure:         report.ConfigPermissionsSecure,
		SessionID:                       report.SessionID,
		SessionMode:                     report.SessionMode,
		PolicyProfile:                   report.PolicyProfile,
		ActiveModelAlias:                report.ActiveModelAlias,
		ActiveProvider:                  report.ActiveProvider,
		ActiveModel:                     report.ActiveModel,
		MissingAPIKey:                   report.MissingAPIKey,
		TokenSource:                     report.TokenSource,
		PersistedPlaintextToken:         report.PersistedPlaintextToken,
		SandboxRequestedBackend:         report.SandboxRequestedBackend,
		SandboxResolvedBackend:          report.SandboxResolvedBackend,
		SandboxRoute:                    report.SandboxRoute,
		SandboxFallbackReason:           report.SandboxFallbackReason,
		SandboxInstallHint:              report.SandboxInstallHint,
		SandboxSetup:                    cloneOptionalSetupStatus(report.SandboxSetup),
		SandboxSetupRequired:            report.SandboxSetupRequired,
		SandboxSetupError:               report.SandboxSetupError,
		SandboxSetupMarkerCurrent:       report.SandboxSetupMarkerCurrent,
		SandboxSetupMarkerReason:        report.SandboxSetupMarkerReason,
		SandboxSecuritySummary:          report.SandboxSecuritySummary,
		SandboxGlobalSetupCurrent:       report.SandboxGlobalSetupCurrent,
		SandboxGlobalSetupRequired:      report.SandboxGlobalSetupRequired,
		SandboxGlobalSetupReason:        report.SandboxGlobalSetupReason,
		SandboxWorkspaceSetupCurrent:    report.SandboxWorkspaceSetupCurrent,
		SandboxWorkspaceSetupRequired:   report.SandboxWorkspaceSetupRequired,
		SandboxWorkspaceSetupReason:     report.SandboxWorkspaceSetupReason,
		SandboxWorkspaceSetupRoot:       report.SandboxWorkspaceSetupRoot,
		SandboxWorkspaceSetupWriteRoots: report.SandboxWorkspaceSetupWriteRoots,
		SandboxWorkspaceSetupPolicyHash: report.SandboxWorkspaceSetupPolicyHash,
		SandboxWorkspaceSetupUpdatedAt:  report.SandboxWorkspaceSetupUpdatedAt,
		HostExecution:                   report.HostExecution,
		FullAccessMode:                  report.FullAccessMode,
		ActiveTurnSessions:              append([]string(nil), report.ActiveTurnSessions...),
		Warnings:                        append([]string(nil), report.Warnings...),
	}, err
}

func cloneOptionalSetupStatus(status *sandbox.SetupStatus) *sandbox.SetupStatus {
	if status == nil {
		return nil
	}
	out := sandbox.CloneSetupStatus(*status)
	return &out
}

func toRuntimeACPAgents(agents []gatewayapp.ACPAgentInfo) []ACPAgentInfo {
	out := make([]ACPAgentInfo, 0, len(agents))
	for _, agent := range agents {
		out = append(out, ACPAgentInfo{
			Name:        agent.Name,
			Description: agent.Description,
		})
	}
	return out
}

func toRuntimePluginSnapshot(info gatewayapp.PluginInfo) controlprompt.PluginSnapshot {
	mcpSnapshots := make([]controlprompt.MCPServerSnapshot, 0, len(info.MCPServers))
	for _, mcpInfo := range info.MCPServers {
		mcpSnapshots = append(mcpSnapshots, controlprompt.MCPServerSnapshot{
			Name:    mcpInfo.Name,
			Status:  mcpInfo.Status,
			Tools:   append([]string(nil), mcpInfo.Tools...),
			Warning: mcpInfo.Warning,
		})
	}
	return controlprompt.PluginSnapshot{
		ID:          info.ID,
		Name:        info.Name,
		Version:     info.Version,
		Description: info.Description,
		Root:        info.Root,
		Enabled:     info.Enabled,
		Skills:      append([]string(nil), info.Skills...),
		Hooks:       append([]string(nil), info.Hooks...),
		Agents:      append([]string(nil), info.Agents...),
		MCPServers:  mcpSnapshots,
		Status:      info.Status,
		Warning:     info.Warning,
	}
}

func toRuntimePluginSnapshots(list []gatewayapp.PluginInfo, err error) ([]controlprompt.PluginSnapshot, error) {
	if err != nil {
		return nil, err
	}
	out := make([]controlprompt.PluginSnapshot, 0, len(list))
	for _, info := range list {
		out = append(out, toRuntimePluginSnapshot(info))
	}
	return out, nil
}

func toRuntimePluginSnapshotWithError(info gatewayapp.PluginInfo, err error) (controlprompt.PluginSnapshot, error) {
	if err != nil {
		return controlprompt.PluginSnapshot{}, err
	}
	return toRuntimePluginSnapshot(info), nil
}

func toRuntimeMarketplaceSnapshot(info gatewayapp.MarketplaceInfo) controlprompt.MarketplaceSnapshot {
	return controlprompt.MarketplaceSnapshot{
		Name:                              info.Name,
		Description:                       info.Description,
		Owner:                             info.Owner,
		Source:                            info.Source,
		Root:                              info.Root,
		Version:                           info.Version,
		PluginRoot:                        info.PluginRoot,
		AllowCrossMarketplaceDependencies: append([]string(nil), info.AllowCrossMarketplaceDependencies...),
		PluginCount:                       info.PluginCount,
	}
}

func toRuntimeMarketplaceSnapshots(list []gatewayapp.MarketplaceInfo, err error) ([]controlprompt.MarketplaceSnapshot, error) {
	if err != nil {
		return nil, err
	}
	out := make([]controlprompt.MarketplaceSnapshot, 0, len(list))
	for _, info := range list {
		out = append(out, toRuntimeMarketplaceSnapshot(info))
	}
	return out, nil
}
