package local

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type ModelConfig = controladapter.ModelConfig
type SandboxStatusProjection = controladapter.SandboxStatusProjection
type DoctorRequest = controladapter.DoctorRequest
type DoctorStatusProjection = controladapter.DoctorStatusProjection

type controlAssemblyDeps struct {
	Status     controladapter.StatusAssemblyDeps
	Agent      controladapter.AgentAssemblyDeps
	Completion controladapter.CompletionAssemblyDeps
	Plugin     controladapter.PluginAssemblyDeps
}

func controlAssemblyDepsFromView(view *gatewayapp.ControlRuntimeView) *controlAssemblyDeps {
	if view == nil {
		return nil
	}
	gateway := controladapter.GatewayRuntimeDeps{
		TurnServiceFn: func() controladapter.GatewayTurnService {
			return view.TurnStateFn()
		},
		ControlPlaneServiceFn: func() controladapter.GatewayControlPlaneService {
			return view.ControlPlaneStateFn()
		},
	}
	sessionDeps := controladapter.SessionRuntimeDeps{
		Store: view.Sessions, AppName: view.AppName, UserID: view.UserID, Workspace: view.Workspace,
	}
	status := controladapter.StatusRuntimeDeps{
		RuntimeStateFn:          view.RuntimeStateFn,
		ConfigurationRevisionFn: view.ConfigurationRevisionFn,
		DoctorFn: func(ctx context.Context, req DoctorRequest) (DoctorStatusProjection, error) {
			return toDoctorStatusProjection(view.DoctorFn(ctx, req))
		},
	}
	agent := controladapter.AgentRuntimeDeps{
		ControllerStatusFn: view.ControllerStatusFn, DisconnectCandidatesFn: view.DisconnectCandidatesFn,
		ListFn: view.ListAgentsFn,
	}
	model := controladapter.ModelRuntimeDeps{
		EffectiveAliasFn: view.EffectiveModelAliasFn, EffectiveEffortFn: view.EffectiveModelEffortFn,
		ConfigFn:               func(alias string) (ModelConfig, bool) { return view.ModelConfigFn(alias) },
		SessionUsageSnapshotFn: view.SessionUsageSnapshotFn, ProviderUsageFn: view.ProviderUsageFn,
		ListAliasesFn:     view.ListModelAliasesFn,
		ListChoicesFn:     view.ListModelChoicesFn,
		HasReusableAuthFn: view.HasReusableAuthFn,
	}
	skill := controladapter.SkillRuntimeDeps{SnapshotFn: view.SkillCatalogFn}
	sandboxDeps := controladapter.SandboxRuntimeDeps{
		StatusFn: func() SandboxStatusProjection { return toSandboxStatusProjection(view.SandboxFn()) },
	}
	plugin := controladapter.PluginRuntimeDeps{
		ListPluginsFn: func(ctx context.Context) ([]controlprompt.PluginSnapshot, error) {
			return toRuntimePluginSnapshots(view.ListPluginsFn(ctx))
		},
		ListMarketplacesFn: func(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
			return toRuntimeMarketplaceSnapshots(view.ListMarketplacesFn(ctx))
		},
		InspectPluginFn: func(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
			return toRuntimePluginSnapshotWithError(view.InspectPluginFn(ctx, id))
		},
	}
	return &controlAssemblyDeps{
		Status: controladapter.StatusAssemblyDeps{
			Gateway: gateway, Session: sessionDeps, Status: status, Agent: agent, Model: model, Sandbox: sandboxDeps,
		},
		Agent: controladapter.AgentAssemblyDeps{Gateway: gateway, Session: sessionDeps, Agent: agent},
		Completion: controladapter.CompletionAssemblyDeps{
			Session: sessionDeps, Status: status, Agent: agent, Model: model, Skill: skill, Plugin: plugin,
		},
		Plugin: controladapter.PluginAssemblyDeps{Plugin: plugin},
	}
}

func toSandboxStatusProjection(status gatewayapp.SandboxStatus) SandboxStatusProjection {
	return SandboxStatusProjection{
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

func toDoctorStatusProjection(report gatewayapp.DoctorReport, err error) (DoctorStatusProjection, error) {
	return DoctorStatusProjection{
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
