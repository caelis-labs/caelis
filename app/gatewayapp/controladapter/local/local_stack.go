package local

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func gatewayRuntimeDeps(reads gatewayapp.KernelReadService) controladapter.GatewayRuntimeDeps {
	return controladapter.GatewayRuntimeDeps{
		TurnServiceFn: func() controladapter.GatewayTurnService {
			return reads.TurnState()
		},
		ControlPlaneServiceFn: func() controladapter.GatewayControlPlaneService {
			return reads.ControlPlaneState()
		},
	}
}

func sessionRuntimeDeps(
	store gatewayapp.ControlSessionReader,
	appName string,
	userID string,
	workspace session.WorkspaceRef,
) controladapter.SessionRuntimeDeps {
	return controladapter.SessionRuntimeDeps{
		Store: store, AppName: appName, UserID: userID, Workspace: workspace,
	}
}

func statusRuntimeDeps(status gatewayapp.StatusService) controladapter.StatusRuntimeDeps {
	return controladapter.StatusRuntimeDeps{
		RuntimeStateFn:                   status.SessionRuntimeState,
		ConfigurationRevisionFn:          status.ConfigurationRevision,
		WorkspaceTrustFn:                 status.WorkspaceTrust,
		ProjectMCPConfigurationPresentFn: status.ProjectMCPConfigurationPresent,
		TaskEntriesFn:                    status.SessionTasks,
		DoctorFn: func(ctx context.Context, req controladapter.DoctorRequest) (controladapter.DoctorStatusProjection, error) {
			return toDoctorStatusProjection(status.Doctor(ctx, req))
		},
	}
}

func agentRuntimeDeps(agents gatewayapp.AgentService) controladapter.AgentRuntimeDeps {
	return controladapter.AgentRuntimeDeps{
		ControllerStatusFn:     agents.ControllerStatus,
		DisconnectCandidatesFn: agents.DisconnectCandidates,
		ListFn:                 agents.List,
	}
}

func modelRuntimeDeps(models gatewayapp.ModelService) controladapter.ModelRuntimeDeps {
	return controladapter.ModelRuntimeDeps{
		EffectiveAliasFn:       models.EffectiveAlias,
		EffectiveEffortFn:      models.EffectiveEffort,
		EffectiveFastModeFn:    models.EffectiveFastMode,
		ConfigFn:               models.Config,
		SessionUsageSnapshotFn: models.UsageSnapshot,
		ProviderUsageFn:        models.ProviderUsage,
		ListAliasesFn:          models.ListAliases,
		ListChoicesFn:          models.ListChoices,
		HasReusableAuthFn:      models.HasReusableAuth,
	}
}

func skillRuntimeDeps(skills gatewayapp.SkillService) controladapter.SkillRuntimeDeps {
	return controladapter.SkillRuntimeDeps{SnapshotFn: skills.Snapshot}
}

func sandboxRuntimeDeps(status gatewayapp.StatusService) controladapter.SandboxRuntimeDeps {
	return controladapter.SandboxRuntimeDeps{
		StatusFn: func() controladapter.SandboxStatusProjection { return toSandboxStatusProjection(status.Sandbox()) },
	}
}

func pluginRuntimeDeps(plugins gatewayapp.PluginReadService) controladapter.PluginRuntimeDeps {
	return controladapter.PluginRuntimeDeps{
		ListPluginsFn: func(ctx context.Context) ([]controlprompt.PluginSnapshot, error) {
			return toRuntimePluginSnapshots(plugins.List(ctx))
		},
		ListMarketplacesFn: func(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
			return toRuntimeMarketplaceSnapshots(plugins.ListMarketplaces(ctx))
		},
		InspectPluginFn: func(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
			return toRuntimePluginSnapshotWithError(plugins.Inspect(ctx, id))
		},
	}
}

func statusAssemblyDepsFromLease(lease *gatewayapp.ControlRuntimeLease) *controladapter.StatusAssemblyDeps {
	if lease == nil || lease.SessionReads() == nil {
		return nil
	}
	status := lease.Status()
	return &controladapter.StatusAssemblyDeps{
		Gateway: gatewayRuntimeDeps(lease.KernelReads()),
		Session: sessionRuntimeDeps(lease.SessionReads(), lease.AppName(), lease.UserID(), lease.Workspace()),
		Status:  statusRuntimeDeps(status),
		Agent:   agentRuntimeDeps(lease.Agents()),
		Model:   modelRuntimeDeps(lease.Models()),
		Sandbox: sandboxRuntimeDeps(status),
	}
}

func agentAssemblyDepsFromLease(lease *gatewayapp.ControlRuntimeLease) *controladapter.AgentAssemblyDeps {
	if lease == nil || lease.SessionReads() == nil {
		return nil
	}
	return &controladapter.AgentAssemblyDeps{
		Gateway: gatewayRuntimeDeps(lease.KernelReads()),
		Session: sessionRuntimeDeps(lease.SessionReads(), lease.AppName(), lease.UserID(), lease.Workspace()),
		Agent:   agentRuntimeDeps(lease.Agents()),
	}
}

func completionAssemblyDepsFromLease(lease *gatewayapp.ControlRuntimeLease) *controladapter.CompletionAssemblyDeps {
	if lease == nil || lease.SessionReads() == nil {
		return nil
	}
	return &controladapter.CompletionAssemblyDeps{
		Session: sessionRuntimeDeps(lease.SessionReads(), lease.AppName(), lease.UserID(), lease.Workspace()),
		Status:  statusRuntimeDeps(lease.Status()),
		Agent:   agentRuntimeDeps(lease.Agents()),
		Model:   modelRuntimeDeps(lease.Models()),
		Skill:   skillRuntimeDeps(lease.Skills()),
		Plugin:  pluginRuntimeDeps(lease.PluginReads()),
	}
}

func toSandboxStatusProjection(status gatewayapp.SandboxStatus) controladapter.SandboxStatusProjection {
	return controladapter.SandboxStatusProjection{
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

func toDoctorStatusProjection(report gatewayapp.DoctorReport, err error) (controladapter.DoctorStatusProjection, error) {
	return controladapter.DoctorStatusProjection{
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
