package controladapter

import (
	"context"
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/ports/agentprofile"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func newAdapterFromGatewayAppStack(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string, bindingKey string, modelText string) (*Adapter, error) {
	return NewAdapter(ctx, gatewayAppStackForRuntimeTest(stack), preferredSessionID, bindingKey, modelText)
}

func gatewayAppStackForRuntimeTest(stack *gatewayapp.Stack) *RuntimeStack {
	if stack == nil {
		return nil
	}
	profiles := stack.AgentProfiles()
	return &RuntimeStack{
		GatewayFn: func() GatewayService { return stack.CurrentGateway() },
		Sessions:  stack.Sessions,
		AppName:   stack.AppName,
		UserID:    stack.UserID,
		Workspace: stack.Workspace,

		StartSessionFn:        stack.StartSession,
		ACPControllerStatusFn: stack.ACPControllerStatus,
		DefaultModelAliasFn:   stack.DefaultModelAlias,
		Sandbox: SandboxRuntimeDeps{
			StatusFn: func() SandboxStatus { return testRuntimeSandboxStatus(stack.SandboxStatus()) },
			SetBackendFn: func(ctx context.Context, backend string) (SandboxStatus, error) {
				return testRuntimeSandboxStatusWithError(stack.SetSandboxBackend(ctx, backend))
			},
			PrepareFn: func(ctx context.Context) (SandboxStatus, error) {
				return testRuntimeSandboxStatusWithError(stack.PrepareSandbox(ctx))
			},
		},
		SessionRuntimeStateFn: func(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
			return testRuntimeSessionRuntimeState(stack.SessionRuntimeState(ctx, ref))
		},
		DoctorFn: func(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
			return testRuntimeDoctorReport(stack.Doctor(ctx, testGatewayDoctorRequest(req)))
		},
		ModelConfigFn:           func(alias string) (ModelConfig, bool) { return testRuntimeModelConfigWithOK(stack.ModelConfig(alias)) },
		SessionUsageSnapshotFn:  stack.SessionUsageSnapshot,
		CompactSessionFn:        stack.CompactSession,
		ConnectFn:               func(cfg ModelConfig) (string, error) { return stack.Connect(testGatewayModelConfig(cfg)) },
		UseModelFn:              stack.UseModel,
		DeleteModelFn:           stack.DeleteModel,
		SetACPControllerModelFn: stack.SetACPControllerModel,
		CycleSessionModeFn:      stack.CycleSessionMode,
		SetACPControllerModeFn:  stack.SetACPControllerMode,
		SetSessionModeFn:        stack.SetSessionMode,
		RegisterBuiltinACPAgentWithOptionsFn: func(ctx context.Context, target string, opts RegisterBuiltinACPAgentOptions) error {
			return stack.RegisterBuiltinACPAgentWithOptions(ctx, target, gatewayapp.RegisterBuiltinACPAgentOptions{Install: opts.Install})
		},
		UnregisterACPAgentFn: stack.UnregisterACPAgent,
		ListModelAliasesFn:   stack.ListModelAliases,
		ListModelChoicesFn: func(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
			return testRuntimeModelChoices(stack.ListModelChoices(ctx, ref))
		},
		ModelCatalog: testRuntimeModelCatalog{stack: stack},
		EnsureCodeFreeAuthFn: func(ctx context.Context, req CodeFreeAuthRequest) error {
			return stack.Models().EnsureCodeFreeAuth(ctx, gatewayapp.CodeFreeAuthRequest{
				BaseURL:         req.BaseURL,
				OpenBrowser:     req.OpenBrowser,
				CallbackTimeout: req.CallbackTimeout,
			})
		},
		EnsureCodeFreeModelSelectionAuthFn: func(ctx context.Context, req CodeFreeAuthRequest) error {
			return stack.Models().EnsureCodeFreeModelSelectionAuth(ctx, gatewayapp.CodeFreeAuthRequest{
				BaseURL:         req.BaseURL,
				OpenBrowser:     req.OpenBrowser,
				CallbackTimeout: req.CallbackTimeout,
			})
		},
		DiscoverSkillsFn: stack.Skills().Discover,
		ListBuiltinACPAgentAddOptionsFn: func() []ACPAgentAddOption {
			return testRuntimeACPAgentAddOptions(stack.ListBuiltinACPAgentAddOptions())
		},
		ListInstallableACPAgentOptionsFn: func() []ACPAgentAddOption {
			return testRuntimeACPAgentAddOptions(stack.ListInstallableACPAgentOptions())
		},
		ListACPAgentsFn: func() []ACPAgentInfo { return testRuntimeACPAgents(stack.ListACPAgents()) },
		AgentProfileStatusFn: func(ctx context.Context) (AgentProfileStatusSnapshot, error) {
			return testRuntimeAgentProfileStatus(profiles.Status(ctx))
		},
		BindAgentProfileFn: func(ctx context.Context, cfg AgentProfileBindingConfig) (AgentProfileStatusSnapshot, error) {
			return testRuntimeAgentProfileStatus(profiles.Bind(ctx, gatewayapp.AgentProfileBindingConfig{
				ProfileID:       cfg.ProfileID,
				Target:          agentprofile.BindingTargetKind(strings.TrimSpace(cfg.Target)),
				Model:           cfg.Model,
				ACPAgent:        cfg.ACPAgent,
				ACPModel:        cfg.ACPModel,
				ReasoningEffort: cfg.ReasoningEffort,
			}))
		},
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

func testRuntimeModelConfigWithOK(cfg gatewayapp.ModelConfig, ok bool) (ModelConfig, bool) {
	if !ok {
		return ModelConfig{}, false
	}
	return testRuntimeModelConfig(cfg), true
}

type testRuntimeModelCatalog struct {
	stack *gatewayapp.Stack
}

func (c testRuntimeModelCatalog) ListProviderModels(provider string) []string {
	return c.stack.ListProviderModels(provider)
}

func (c testRuntimeModelCatalog) ListCatalogModels(provider string) []string {
	return c.stack.Models().ListCatalogModels(provider)
}

func (c testRuntimeModelCatalog) ListModelDirectoryModels(provider string) []string {
	return c.stack.Models().ListModelDirectoryModels(provider)
}

func (c testRuntimeModelCatalog) DefaultCapabilities() ModelCapabilityInfo {
	return testRuntimeModelCapabilities(c.stack.Models().DefaultCapabilities())
}

func (c testRuntimeModelCatalog) LookupCapabilities(provider string, modelName string) (ModelCapabilityInfo, bool) {
	return testRuntimeModelCapabilitiesWithOK(c.stack.Models().LookupCapabilities(provider, modelName))
}

func (c testRuntimeModelCatalog) ReasoningLevels(provider string, modelName string) []string {
	return c.stack.Models().ReasoningLevels(provider, modelName)
}

func testRuntimeModelConfig(cfg gatewayapp.ModelConfig) ModelConfig {
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

func testGatewayModelConfig(cfg ModelConfig) gatewayapp.ModelConfig {
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

func testRuntimeModelCapabilitiesWithOK(caps gatewayapp.ModelCapabilityInfo, ok bool) (ModelCapabilityInfo, bool) {
	return testRuntimeModelCapabilities(caps), ok
}

func testRuntimeModelCapabilities(caps gatewayapp.ModelCapabilityInfo) ModelCapabilityInfo {
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

func testRuntimeSandboxStatusWithError(status gatewayapp.SandboxStatus, err error) (SandboxStatus, error) {
	return testRuntimeSandboxStatus(status), err
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
