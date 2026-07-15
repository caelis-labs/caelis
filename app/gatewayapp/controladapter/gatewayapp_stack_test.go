package controladapter

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/ports/agentprofile"
)

func newAdapterFromGatewayAppStack(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string, bindingKey string, modelText string) (*Adapter, error) {
	return NewAdapter(ctx, gatewayAppStackForRuntimeTest(stack), preferredSessionID, bindingKey, modelText)
}

func gatewayAppStackForRuntimeTest(stack *gatewayapp.Stack) *RuntimeStack {
	return NewRuntimeStackFromGatewayApp(stack, RuntimeStackGatewayAppAdapters{
		SandboxStatus:        testRuntimeSandboxStatus,
		SessionRuntimeState:  testRuntimeSessionRuntimeState,
		ModelChoices:         testRuntimeModelChoices,
		DoctorRequest:        testGatewayDoctorRequest,
		DoctorReport:         testRuntimeDoctorReport,
		ACPAgentAddOptions:   testRuntimeACPAgentAddOptions,
		ACPAgents:            testRuntimeACPAgents,
		AgentProfileStatus:   testRuntimeAgentProfileStatus,
		AgentProfileBinding:  testGatewayAgentProfileBinding,
		PluginSnapshots:      testRuntimePluginSnapshots,
		PluginSnapshot:       testRuntimePluginSnapshotWithError,
		MarketplaceSnapshots: testRuntimeMarketplaceSnapshots,
		MarketplaceSnapshot:  testRuntimeMarketplaceSnapshotWithError,
	})
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
		"stream":        stack.Gateway.StreamProviderFn != nil,
	}
	for name, ok := range gatewayHooks {
		if !ok {
			t.Fatalf("gateway %s hook is not wired", name)
		}
	}

	sandboxHooks := map[string]bool{
		"status":     stack.Sandbox.StatusFn != nil,
		"setBackend": stack.Sandbox.SetBackendFn != nil,
		"prepare":    stack.Sandbox.PrepareFn != nil,
		"repair":     stack.Sandbox.RepairFn != nil,
		"preflight":  stack.Sandbox.PreflightFn != nil,
		"reset":      stack.Sandbox.ResetFn != nil,
	}
	for name, ok := range sandboxHooks {
		if !ok {
			t.Fatalf("sandbox hook %s is not wired", name)
		}
	}

	pluginHooks := map[string]bool{
		"listPlugins":       stack.Plugin.ListPluginsFn != nil,
		"addMarketplace":    stack.Plugin.AddMarketplaceFn != nil,
		"listMarketplaces":  stack.Plugin.ListMarketplacesFn != nil,
		"updateMarketplace": stack.Plugin.UpdateMarketplaceFn != nil,
		"removeMarketplace": stack.Plugin.RemoveMarketplaceFn != nil,
		"addPluginPath":     stack.Plugin.AddPluginPathFn != nil,
		"installPlugin":     stack.Plugin.InstallPluginFn != nil,
		"enablePlugin":      stack.Plugin.EnablePluginFn != nil,
		"disablePlugin":     stack.Plugin.DisablePluginFn != nil,
		"removePlugin":      stack.Plugin.RemovePluginFn != nil,
		"inspectPlugin":     stack.Plugin.InspectPluginFn != nil,
	}
	for name, ok := range pluginHooks {
		if !ok {
			t.Fatalf("plugin hook %s is not wired", name)
		}
	}
}

func testRuntimeAgentProfileStatus(status gatewayapp.AgentProfileStatus, err error) (AgentProfileStatusSnapshot, error) {
	if err != nil {
		return AgentProfileStatusSnapshot{}, err
	}
	out := AgentProfileStatusSnapshot{}
	for _, warning := range status.Warnings {
		message := strings.TrimSpace(warning.Message)
		if message == "" {
			continue
		}
		if path := strings.TrimSpace(warning.Path); path != "" {
			message = path + ": " + message
		}
		out.Warnings = append(out.Warnings, message)
	}
	for _, snapshot := range status.Profiles {
		profile := agentprofile.NormalizeProfile(snapshot.Profile)
		binding := agentprofile.NormalizeBinding(snapshot.Binding)
		out.Profiles = append(out.Profiles, AgentProfileSnapshot{
			ID:              profile.ID,
			Name:            profile.Name,
			Description:     profile.Description,
			Capabilities:    append([]string(nil), profile.Capabilities...),
			Path:            profile.Path,
			Enabled:         binding.Enabled == nil || *binding.Enabled,
			Target:          string(binding.Target),
			Model:           binding.Model,
			ACPAgent:        binding.ACPAgent,
			ACPModel:        binding.ACPModel,
			ReasoningEffort: binding.ReasoningEffort,
			Status:          string(binding.Status),
			Warning:         binding.Warning,
			Source:          testRuntimeAgentProfileMetadataString(profile.Metadata, "source"),
			BuiltIn:         testRuntimeAgentProfileMetadataBool(profile.Metadata, "built_in"),
			SystemManaged:   testRuntimeAgentProfileMetadataBool(profile.Metadata, "system_managed"),
		})
	}
	return out, nil
}

func testRuntimeAgentProfileMetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func testRuntimeAgentProfileMetadataBool(metadata map[string]any, key string) bool {
	if len(metadata) == 0 {
		return false
	}
	switch value := metadata[key].(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "yes", "1", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func testGatewayAgentProfileBinding(cfg AgentProfileBindingConfig) gatewayapp.AgentProfileBindingConfig {
	return gatewayapp.AgentProfileBindingConfig{
		ProfileID:       cfg.ProfileID,
		Target:          agentprofile.BindingTargetKind(strings.TrimSpace(cfg.Target)),
		Model:           cfg.Model,
		ACPAgent:        cfg.ACPAgent,
		ACPModel:        cfg.ACPModel,
		ReasoningEffort: cfg.ReasoningEffort,
	}
}

func testRuntimePluginSnapshot(info gatewayapp.PluginInfo) PluginSnapshot {
	mcpSnapshots := make([]MCPServerSnapshot, 0, len(info.MCPServers))
	for _, mcpInfo := range info.MCPServers {
		mcpSnapshots = append(mcpSnapshots, MCPServerSnapshot{
			Name:    mcpInfo.Name,
			Status:  mcpInfo.Status,
			Tools:   append([]string(nil), mcpInfo.Tools...),
			Warning: mcpInfo.Warning,
		})
	}
	return PluginSnapshot{
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

func testRuntimePluginSnapshots(list []gatewayapp.PluginInfo, err error) ([]PluginSnapshot, error) {
	if err != nil {
		return nil, err
	}
	out := make([]PluginSnapshot, 0, len(list))
	for _, info := range list {
		out = append(out, testRuntimePluginSnapshot(info))
	}
	return out, nil
}

func testRuntimePluginSnapshotWithError(info gatewayapp.PluginInfo, err error) (PluginSnapshot, error) {
	if err != nil {
		return PluginSnapshot{}, err
	}
	return testRuntimePluginSnapshot(info), nil
}

func testRuntimeMarketplaceSnapshot(info gatewayapp.MarketplaceInfo) MarketplaceSnapshot {
	return MarketplaceSnapshot{
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

func testRuntimeMarketplaceSnapshots(list []gatewayapp.MarketplaceInfo, err error) ([]MarketplaceSnapshot, error) {
	if err != nil {
		return nil, err
	}
	out := make([]MarketplaceSnapshot, 0, len(list))
	for _, info := range list {
		out = append(out, testRuntimeMarketplaceSnapshot(info))
	}
	return out, nil
}

func testRuntimeMarketplaceSnapshotWithError(info gatewayapp.MarketplaceInfo, err error) (MarketplaceSnapshot, error) {
	if err != nil {
		return MarketplaceSnapshot{}, err
	}
	return testRuntimeMarketplaceSnapshot(info), nil
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
			ID:         choice.ID,
			Alias:      choice.Alias,
			Provider:   choice.Provider,
			Model:      choice.Model,
			ProfileID:  choice.ProfileID,
			EndpointID: choice.EndpointID,
			BaseURL:    choice.BaseURL,
			Detail:     choice.Detail,
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

func testRuntimeACPAgentAddOptions(options []gatewayapp.ACPAgentAddOption) []ACPAgentAddOption {
	out := make([]ACPAgentAddOption, 0, len(options))
	for _, option := range options {
		out = append(out, ACPAgentAddOption{
			Value:   option.Value,
			Display: option.Display,
			Detail:  option.Detail,
		})
	}
	return out
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
