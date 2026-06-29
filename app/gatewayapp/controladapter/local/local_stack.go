package local

import (
	"context"
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	controladapter "github.com/OnslaughtSnail/caelis/app/gatewayapp/controladapter"
	"github.com/OnslaughtSnail/caelis/ports/agentprofile"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type Adapter = controladapter.Adapter
type RuntimeStack = controladapter.RuntimeStack
type GatewayService = controladapter.GatewayService
type ModelConfig = controladapter.ModelConfig
type ModelCapabilityInfo = controladapter.ModelCapabilityInfo
type ModelChoice = controladapter.ModelChoice
type SessionRuntimeState = controladapter.SessionRuntimeState
type SandboxStatus = controladapter.SandboxStatus
type DoctorRequest = controladapter.DoctorRequest
type DoctorReport = controladapter.DoctorReport
type RegisterBuiltinACPAgentOptions = controladapter.RegisterBuiltinACPAgentOptions
type ACPAgentInfo = controladapter.ACPAgentInfo
type ACPAgentAddOption = controladapter.ACPAgentAddOption
type AgentProfileStatusSnapshot = controladapter.AgentProfileStatusSnapshot
type AgentProfileBindingConfig = controladapter.AgentProfileBindingConfig
type CodeFreeAuthRequest = controladapter.CodeFreeAuthRequest
type CustomAgentConfig = controladapter.CustomAgentConfig

func NewLocalAdapter(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string, bindingKey string, modelText string) (*Adapter, error) {
	return controladapter.NewAdapter(ctx, runtimeStack(stack), preferredSessionID, bindingKey, modelText)
}

func NewLocalAdapterForSession(ctx context.Context, stack *gatewayapp.Stack, activeSession session.Session, bindingKey string, modelText string) (*Adapter, error) {
	return controladapter.NewAdapterForSession(ctx, runtimeStack(stack), activeSession, bindingKey, modelText)
}

func runtimeStack(stack *gatewayapp.Stack) *RuntimeStack {
	return controladapter.NewRuntimeStackFromGatewayApp(stack, controladapter.RuntimeStackGatewayAppAdapters{
		ModelConfig:          toRuntimeModelConfig,
		GatewayModelConfig:   toGatewayModelConfig,
		ModelCapabilities:    toRuntimeModelCapabilities,
		SandboxStatus:        toRuntimeSandboxStatus,
		SessionRuntimeState:  toRuntimeSessionRuntimeState,
		ModelChoices:         toRuntimeModelChoices,
		DoctorRequest:        toGatewayDoctorRequest,
		DoctorReport:         toRuntimeDoctorReport,
		ACPAgentAddOptions:   toRuntimeACPAgentAddOptions,
		ACPAgents:            toRuntimeACPAgents,
		AgentProfileStatus:   toRuntimeAgentProfileStatus,
		AgentProfileBinding:  toGatewayAgentProfileBinding,
		PluginSnapshots:      toRuntimePluginSnapshots,
		PluginSnapshot:       toRuntimePluginSnapshotWithError,
		MarketplaceSnapshots: toRuntimeMarketplaceSnapshots,
		MarketplaceSnapshot:  toRuntimeMarketplaceSnapshotWithError,
	})
}

func toRuntimeModelConfig(cfg gatewayapp.ModelConfig) ModelConfig {
	return ModelConfig{
		ID:                      cfg.ID,
		Alias:                   cfg.Alias,
		Provider:                cfg.Provider,
		ProfileID:               cfg.ProfileID,
		EndpointID:              cfg.EndpointID,
		API:                     cfg.API,
		Model:                   cfg.Model,
		BaseURL:                 cfg.BaseURL,
		HTTPClient:              cfg.HTTPClient,
		Token:                   cfg.Token,
		TokenEnv:                cfg.TokenEnv,
		PersistToken:            cfg.PersistToken,
		AuthType:                cfg.AuthType,
		HeaderKey:               cfg.HeaderKey,
		ContextWindowTokens:     cfg.ContextWindowTokens,
		ReasoningEffort:         cfg.ReasoningEffort,
		DefaultReasoningEffort:  cfg.DefaultReasoningEffort,
		ReasoningLevels:         append([]string(nil), cfg.ReasoningLevels...),
		ReasoningMode:           cfg.ReasoningMode,
		MaxOutputTok:            cfg.MaxOutputTok,
		Timeout:                 cfg.Timeout,
		StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
	}
}

func toGatewayModelConfig(cfg ModelConfig) gatewayapp.ModelConfig {
	return gatewayapp.ModelConfig{
		ID:                      cfg.ID,
		Alias:                   cfg.Alias,
		Provider:                cfg.Provider,
		ProfileID:               cfg.ProfileID,
		EndpointID:              cfg.EndpointID,
		API:                     cfg.API,
		Model:                   cfg.Model,
		BaseURL:                 cfg.BaseURL,
		HTTPClient:              cfg.HTTPClient,
		Token:                   cfg.Token,
		TokenEnv:                cfg.TokenEnv,
		PersistToken:            cfg.PersistToken,
		AuthType:                cfg.AuthType,
		HeaderKey:               cfg.HeaderKey,
		ContextWindowTokens:     cfg.ContextWindowTokens,
		ReasoningEffort:         cfg.ReasoningEffort,
		DefaultReasoningEffort:  cfg.DefaultReasoningEffort,
		ReasoningLevels:         append([]string(nil), cfg.ReasoningLevels...),
		ReasoningMode:           cfg.ReasoningMode,
		MaxOutputTok:            cfg.MaxOutputTok,
		Timeout:                 cfg.Timeout,
		StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
	}
}

func toRuntimeModelCapabilitiesWithOK(caps gatewayapp.ModelCapabilityInfo, ok bool) (ModelCapabilityInfo, bool) {
	return toRuntimeModelCapabilities(caps), ok
}

func toRuntimeModelCapabilities(caps gatewayapp.ModelCapabilityInfo) ModelCapabilityInfo {
	return ModelCapabilityInfo{
		ContextWindowTokens:    caps.ContextWindowTokens,
		DefaultMaxOutputTokens: caps.DefaultMaxOutputTokens,
		MaxOutputTokens:        caps.MaxOutputTokens,
		ReasoningEfforts:       append([]string(nil), caps.ReasoningEfforts...),
		DefaultReasoningEffort: caps.DefaultReasoningEffort,
		SupportsReasoning:      caps.SupportsReasoning,
		SupportsToolCalls:      caps.SupportsToolCalls,
		SupportsImages:         caps.SupportsImages,
		SupportsJSON:           caps.SupportsJSON,
	}
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

func toRuntimeSandboxStatusWithError(status gatewayapp.SandboxStatus, err error) (SandboxStatus, error) {
	return toRuntimeSandboxStatus(status), err
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

func toGatewayDoctorRequest(req DoctorRequest) gatewayapp.DoctorRequest {
	return gatewayapp.DoctorRequest{
		SessionRef: req.SessionRef,
		SessionID:  req.SessionID,
		BindingKey: req.BindingKey,
	}
}

func toRuntimeDoctorReport(report gatewayapp.DoctorReport, err error) (DoctorReport, error) {
	return DoctorReport{
		StoreDir:                        report.StoreDir,
		SessionID:                       report.SessionID,
		SessionMode:                     report.SessionMode,
		ActiveModelAlias:                report.ActiveModelAlias,
		ActiveProvider:                  report.ActiveProvider,
		ActiveModel:                     report.ActiveModel,
		MissingAPIKey:                   report.MissingAPIKey,
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
		ConfigPermissionsSecure:         report.ConfigPermissionsSecure,
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

func toRuntimeACPAgentAddOptions(options []gatewayapp.ACPAgentAddOption) []ACPAgentAddOption {
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

func toRuntimeAgentProfileStatus(status gatewayapp.AgentProfileStatus, err error) (AgentProfileStatusSnapshot, error) {
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
		out.Profiles = append(out.Profiles, toRuntimeAgentProfileSnapshot(snapshot))
	}
	return out, nil
}

func toRuntimeAgentProfileSnapshot(snapshot agentprofile.Snapshot) controladapter.AgentProfileSnapshot {
	profile := agentprofile.NormalizeProfile(snapshot.Profile)
	binding := agentprofile.NormalizeBinding(snapshot.Binding)
	return controladapter.AgentProfileSnapshot{
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
		Source:          agentProfileMetadataString(profile.Metadata, "source"),
		BuiltIn:         agentProfileMetadataBool(profile.Metadata, "built_in"),
		SystemManaged:   agentProfileMetadataBool(profile.Metadata, "system_managed"),
	}
}

func toGatewayAgentProfileBinding(cfg AgentProfileBindingConfig) gatewayapp.AgentProfileBindingConfig {
	return gatewayapp.AgentProfileBindingConfig{
		ProfileID:       cfg.ProfileID,
		Target:          agentprofile.BindingTargetKind(strings.TrimSpace(cfg.Target)),
		Model:           cfg.Model,
		ACPAgent:        cfg.ACPAgent,
		ACPModel:        cfg.ACPModel,
		ReasoningEffort: cfg.ReasoningEffort,
	}
}

func agentProfileMetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func agentProfileMetadataBool(metadata map[string]any, key string) bool {
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

func toRuntimePluginSnapshot(info gatewayapp.PluginInfo) controladapter.PluginSnapshot {
	mcpSnapshots := make([]controladapter.MCPServerSnapshot, 0, len(info.MCPServers))
	for _, mcpInfo := range info.MCPServers {
		mcpSnapshots = append(mcpSnapshots, controladapter.MCPServerSnapshot{
			Name:    mcpInfo.Name,
			Status:  mcpInfo.Status,
			Tools:   append([]string(nil), mcpInfo.Tools...),
			Warning: mcpInfo.Warning,
		})
	}
	return controladapter.PluginSnapshot{
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

func toRuntimePluginSnapshots(list []gatewayapp.PluginInfo, err error) ([]controladapter.PluginSnapshot, error) {
	if err != nil {
		return nil, err
	}
	out := make([]controladapter.PluginSnapshot, 0, len(list))
	for _, info := range list {
		out = append(out, toRuntimePluginSnapshot(info))
	}
	return out, nil
}

func toRuntimePluginSnapshotWithError(info gatewayapp.PluginInfo, err error) (controladapter.PluginSnapshot, error) {
	if err != nil {
		return controladapter.PluginSnapshot{}, err
	}
	return toRuntimePluginSnapshot(info), nil
}

func toRuntimeMarketplaceSnapshot(info gatewayapp.MarketplaceInfo) controladapter.MarketplaceSnapshot {
	return controladapter.MarketplaceSnapshot{
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

func toRuntimeMarketplaceSnapshots(list []gatewayapp.MarketplaceInfo, err error) ([]controladapter.MarketplaceSnapshot, error) {
	if err != nil {
		return nil, err
	}
	out := make([]controladapter.MarketplaceSnapshot, 0, len(list))
	for _, info := range list {
		out = append(out, toRuntimeMarketplaceSnapshot(info))
	}
	return out, nil
}

func toRuntimeMarketplaceSnapshotWithError(info gatewayapp.MarketplaceInfo, err error) (controladapter.MarketplaceSnapshot, error) {
	if err != nil {
		return controladapter.MarketplaceSnapshot{}, err
	}
	return toRuntimeMarketplaceSnapshot(info), nil
}
