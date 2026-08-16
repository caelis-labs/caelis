package controladapter

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func newAssemblerFromGatewayAppSession(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string, bindingKey string, modelText string) (*assembler, error) {
	active, err := startGatewayAppSessionForTest(ctx, stack, preferredSessionID)
	if err != nil {
		return nil, err
	}
	return newAssemblerForSession(ctx, gatewayAppStackForRuntimeTest(stack), active, bindingKey, modelText)
}

func gatewayAppStackForRuntimeTest(stack *gatewayapp.Stack) *RuntimeStack {
	runtimeStack := NewRuntimeStackFromGatewayApp(stack.ControlRuntimeView(), RuntimeStackGatewayAppAdapters{
		SandboxStatus:        testRuntimeSandboxStatus,
		SessionRuntimeState:  testRuntimeSessionRuntimeState,
		ModelChoices:         testRuntimeModelChoices,
		DoctorRequest:        testGatewayDoctorRequest,
		DoctorReport:         testRuntimeDoctorReport,
		ACPAgents:            testRuntimeACPAgents,
		PluginSnapshots:      testRuntimePluginSnapshots,
		PluginSnapshot:       testRuntimePluginSnapshotWithError,
		MarketplaceSnapshots: testRuntimeMarketplaceSnapshots,
	})
	return runtimeStack
}

func startGatewayAppSessionForTest(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string) (session.Session, error) {
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: stack.UserID})
	if err != nil {
		return session.Session{}, err
	}
	result, err := client.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "adapter-test-session-" + uuid.NewString()},
		PreferredSessionID: preferredSessionID,
		WorkspaceKey:       stack.Workspace.Key,
		CWD:                stack.Workspace.CWD,
	})
	if err != nil {
		return session.Session{}, err
	}
	return stack.Sessions.Session(ctx, session.SessionRef{SessionID: result.SessionID})
}

func TestGatewayAppStackForRuntimeTestWiresFullRuntimeSurface(t *testing.T) {
	t.Parallel()

	stack := gatewayAppStackForRuntimeTest(&gatewayapp.Stack{})
	if stack == nil {
		t.Fatal("gatewayAppStackForRuntimeTest() returned nil")
	}

	gatewayHooks := map[string]bool{
		"turn":          stack.Gateway.TurnServiceFn != nil,
		"session":       stack.Gateway.SessionServiceFn != nil,
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

func testRuntimeSandboxStatus(status gatewayapp.SandboxStatus) SandboxStatus {
	return SandboxStatus{
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

func testRuntimeSessionRuntimeState(state gatewayapp.SessionRuntimeState, err error) (SessionRuntimeState, error) {
	return SessionRuntimeState{
		ModelID:         state.ModelID,
		ModelAlias:      state.ModelAlias,
		ReasoningEffort: state.ReasoningEffort,
		SessionMode:     state.SessionMode,
		SandboxMode:     state.SandboxMode,
	}, err
}

func testRuntimeModelChoices(choices []gatewayapp.ModelChoice, err error) ([]ModelChoice, error) {
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

func testGatewayDoctorRequest(req DoctorRequest) gatewayapp.DoctorRequest {
	return gatewayapp.DoctorRequest{
		SessionRef: req.SessionRef,
		SessionID:  req.SessionID,
		BindingKey: req.BindingKey,
	}
}

func testRuntimeDoctorReport(report gatewayapp.DoctorReport, err error) (DoctorReport, error) {
	return DoctorReport{
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

func testRuntimeACPAgents(agents []gatewayapp.ACPAgentInfo) []ACPAgentInfo {
	out := make([]ACPAgentInfo, 0, len(agents))
	for _, agent := range agents {
		out = append(out, ACPAgentInfo{
			Name:        agent.Name,
			Description: agent.Description,
		})
	}
	return out
}
