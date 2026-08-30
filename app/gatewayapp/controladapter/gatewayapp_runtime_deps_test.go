package controladapter

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func newAssemblerFromGatewayAppSession(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string, bindingKey string, modelText string) (*assembler, error) {
	active, err := startGatewayAppSessionForTest(ctx, stack, preferredSessionID)
	if err != nil {
		return nil, err
	}
	return newAssemblerForSession(ctx, gatewayAppRuntimeDepsForTest(stack), active, bindingKey, modelText)
}

func gatewayAppRuntimeDepsForTest(stack *gatewayapp.Stack) *runtimeDeps {
	kernelReads := stack.ControlKernelReads()
	status := stack.ControlStatus()
	agents := stack.Agents()
	models := stack.Models()
	skills := stack.Skills()
	plugins := stack.ControlPluginReads()
	deps := &runtimeDeps{
		Gateway: GatewayRuntimeDeps{
			TurnServiceFn: func() GatewayTurnService {
				return kernelReads.TurnState()
			},
			ControlPlaneServiceFn: func() GatewayControlPlaneService {
				return kernelReads.ControlPlaneState()
			},
		},
		Session: SessionRuntimeDeps{
			Store:     stack.Sessions(),
			AppName:   stack.AppName(),
			UserID:    stack.UserID(),
			Workspace: stack.Workspace(),
			ListSessionsFn: func(ctx context.Context, req kernel.ListSessionsRequest) (session.SessionList, error) {
				return stack.ControlClient().ListSessions(ctx, appserver.Principal{ID: stack.UserID()}, appserver.ListSessionsRequest{
					WorkspaceKey: req.WorkspaceKey,
					CWD:          req.CWD,
					Cursor:       req.Cursor,
					Limit:        req.Limit,
				})
			},
		},
		Status: StatusRuntimeDeps{
			RuntimeStateFn:          status.SessionRuntimeState,
			ConfigurationRevisionFn: status.ConfigurationRevision,
			DoctorFn: func(ctx context.Context, req DoctorRequest) (DoctorStatusProjection, error) {
				return testDoctorStatusProjection(status.Doctor(ctx, req))
			},
		},
		Agent: AgentRuntimeDeps{
			ControllerStatusFn:     agents.ControllerStatus,
			DisconnectCandidatesFn: agents.DisconnectCandidates,
			ListFn:                 agents.List,
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
			ListChoicesFn:          models.ListChoices,
			HasReusableAuthFn:      models.HasReusableAuth,
		},
		Skill: SkillRuntimeDeps{
			SnapshotFn: skills.Snapshot,
		},
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatusProjection { return testSandboxStatusProjection(status.Sandbox()) },
		},
		Plugin: PluginRuntimeDeps{
			ListPluginsFn: func(ctx context.Context) ([]controlprompt.PluginSnapshot, error) {
				return testRuntimePluginSnapshots(plugins.List(ctx))
			},
			ListMarketplacesFn: func(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
				return testRuntimeMarketplaceSnapshots(plugins.ListMarketplaces(ctx))
			},
			InspectPluginFn: func(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
				return testRuntimePluginSnapshotWithError(plugins.Inspect(ctx, id))
			},
		},
	}
	return deps
}

func startGatewayAppSessionForTest(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string) (session.Session, error) {
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: stack.UserID()})
	if err != nil {
		return session.Session{}, err
	}
	result, err := client.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "adapter-test-session-" + uuid.NewString()},
		PreferredSessionID: preferredSessionID,
		WorkspaceKey:       stack.Workspace().Key,
		CWD:                stack.Workspace().CWD,
	})
	if err != nil {
		return session.Session{}, err
	}
	return stack.Sessions().Session(ctx, session.SessionRef{SessionID: result.SessionID})
}

func TestGatewayAppRuntimeDepsWiresFocusedServices(t *testing.T) {
	t.Parallel()

	stack := gatewayAppRuntimeDepsForTest(&gatewayapp.Stack{})
	if stack == nil {
		t.Fatal("gatewayAppRuntimeDepsForTest() returned nil")
		return
	}

	gatewayHooks := map[string]bool{
		"turn":          stack.Gateway.TurnServiceFn != nil,
		"control-plane": stack.Gateway.ControlPlaneServiceFn != nil,
	}
	for name, ok := range gatewayHooks {
		if !ok {
			t.Fatalf("gateway %s hook is not wired", name)
		}
	}

	if stack.Sandbox.StatusFn == nil {
		t.Fatal("sandbox status hook is not wired")
	}

	pluginHooks := map[string]bool{
		"listPlugins":      stack.Plugin.ListPluginsFn != nil,
		"listMarketplaces": stack.Plugin.ListMarketplacesFn != nil,
		"inspectPlugin":    stack.Plugin.InspectPluginFn != nil,
	}
	for name, ok := range pluginHooks {
		if !ok {
			t.Fatalf("plugin hook %s is not wired", name)
		}
	}
}

func testRuntimePluginSnapshot(info gatewayapp.PluginInfo) controlprompt.PluginSnapshot {
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

func testRuntimePluginSnapshots(list []gatewayapp.PluginInfo, err error) ([]controlprompt.PluginSnapshot, error) {
	if err != nil {
		return nil, err
	}
	out := make([]controlprompt.PluginSnapshot, 0, len(list))
	for _, info := range list {
		out = append(out, testRuntimePluginSnapshot(info))
	}
	return out, nil
}

func testRuntimePluginSnapshotWithError(info gatewayapp.PluginInfo, err error) (controlprompt.PluginSnapshot, error) {
	if err != nil {
		return controlprompt.PluginSnapshot{}, err
	}
	return testRuntimePluginSnapshot(info), nil
}

func testRuntimeMarketplaceSnapshot(info gatewayapp.MarketplaceInfo) controlprompt.MarketplaceSnapshot {
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

func testRuntimeMarketplaceSnapshots(list []gatewayapp.MarketplaceInfo, err error) ([]controlprompt.MarketplaceSnapshot, error) {
	if err != nil {
		return nil, err
	}
	out := make([]controlprompt.MarketplaceSnapshot, 0, len(list))
	for _, info := range list {
		out = append(out, testRuntimeMarketplaceSnapshot(info))
	}
	return out, nil
}

func testSandboxStatusProjection(status gatewayapp.SandboxStatus) SandboxStatusProjection {
	return SandboxStatusProjection{
		RequestedBackend:   status.RequestedBackend,
		ResolvedBackend:    status.ResolvedBackend,
		Route:              status.Route,
		FallbackReason:     status.FallbackReason,
		InstallHint:        status.InstallHint,
		SetupRequired:      status.SetupRequired,
		SetupError:         status.SetupError,
		SetupMarkerCurrent: status.SetupMarkerCurrent,
		SetupMarkerReason:  status.SetupMarkerReason,
		SecuritySummary:    status.SecuritySummary,
		FullAccessMode:     status.FullAccessMode,
	}
}

func testDoctorStatusProjection(report gatewayapp.DoctorReport, err error) (DoctorStatusProjection, error) {
	return DoctorStatusProjection{
		StoreDir:                  report.StoreDir,
		SessionID:                 report.SessionID,
		SessionMode:               report.SessionMode,
		ActiveModelAlias:          report.ActiveModelAlias,
		ActiveProvider:            report.ActiveProvider,
		ActiveModel:               report.ActiveModel,
		MissingAPIKey:             report.MissingAPIKey,
		SandboxRequestedBackend:   report.SandboxRequestedBackend,
		SandboxResolvedBackend:    report.SandboxResolvedBackend,
		SandboxRoute:              report.SandboxRoute,
		SandboxFallbackReason:     report.SandboxFallbackReason,
		SandboxInstallHint:        report.SandboxInstallHint,
		SandboxSetupRequired:      report.SandboxSetupRequired,
		SandboxSetupError:         report.SandboxSetupError,
		SandboxSetupMarkerCurrent: report.SandboxSetupMarkerCurrent,
		SandboxSetupMarkerReason:  report.SandboxSetupMarkerReason,
		SandboxSecuritySummary:    report.SandboxSecuritySummary,
		HostExecution:             report.HostExecution,
		FullAccessMode:            report.FullAccessMode,
		ConfigPermissionsSecure:   report.ConfigPermissionsSecure,
		Warnings:                  append([]string(nil), report.Warnings...),
	}, err
}
