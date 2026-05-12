package local

import (
	"context"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/gatewaydriver"
)

type GatewayDriver = gatewaydriver.GatewayDriver
type DriverStack = gatewaydriver.DriverStack
type GatewayService = gatewaydriver.GatewayService
type ModelConfig = gatewaydriver.ModelConfig
type ModelCapabilityInfo = gatewaydriver.ModelCapabilityInfo
type ModelChoice = gatewaydriver.ModelChoice
type SessionRuntimeState = gatewaydriver.SessionRuntimeState
type SandboxStatus = gatewaydriver.SandboxStatus
type DoctorRequest = gatewaydriver.DoctorRequest
type DoctorReport = gatewaydriver.DoctorReport
type RegisterBuiltinACPAgentOptions = gatewaydriver.RegisterBuiltinACPAgentOptions
type ACPAgentInfo = gatewaydriver.ACPAgentInfo
type ACPAgentAddOption = gatewaydriver.ACPAgentAddOption
type CodeFreeAuthRequest = gatewaydriver.CodeFreeAuthRequest
type CustomAgentConfig = gatewaydriver.CustomAgentConfig

func NewLocalDriver(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string, bindingKey string, modelText string) (*GatewayDriver, error) {
	return gatewaydriver.NewGatewayDriver(ctx, driverStack(stack), preferredSessionID, bindingKey, modelText)
}

func driverStack(stack *gatewayapp.Stack) *DriverStack {
	if stack == nil {
		return nil
	}
	models := stack.Models()
	agents := stack.Agents()
	skills := stack.Skills()
	status := stack.Status()
	return &DriverStack{
		GatewayFn: func() GatewayService { return stack.CurrentGateway() },
		Sessions:  stack.Sessions,
		AppName:   stack.AppName,
		UserID:    stack.UserID,
		Workspace: stack.Workspace,

		StartSessionFn:        stack.StartSession,
		ACPControllerStatusFn: agents.ControllerStatus,
		DefaultModelAliasFn:   models.DefaultAlias,
		SandboxStatusFn:       func() SandboxStatus { return toRuntimeSandboxStatus(status.Sandbox()) },
		SessionRuntimeStateFn: func(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
			return toRuntimeSessionRuntimeState(status.SessionRuntimeState(ctx, ref))
		},
		DoctorFn: func(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
			return toRuntimeDoctorReport(status.Doctor(ctx, toGatewayDoctorRequest(req)))
		},
		ModelConfigFn: func(alias string) (ModelConfig, bool) {
			return toRuntimeModelConfigWithOK(models.Config(alias))
		},
		SessionUsageSnapshotFn:  models.UsageSnapshot,
		CompactSessionFn:        stack.CompactSession,
		ConnectFn:               func(cfg ModelConfig) (string, error) { return models.Connect(toGatewayModelConfig(cfg)) },
		UseModelFn:              models.Use,
		DeleteModelFn:           models.Delete,
		SetACPControllerModelFn: agents.SetControllerModel,
		CycleSessionModeFn:      status.CycleSessionMode,
		SetSandboxBackendFn: func(ctx context.Context, backend string) (SandboxStatus, error) {
			return toRuntimeSandboxStatusWithError(status.SetSandboxBackend(ctx, backend))
		},
		SetACPControllerModeFn: agents.SetControllerMode,
		SetSessionModeFn:       status.SetSessionMode,
		RegisterBuiltinACPAgentWithOptionsFn: func(ctx context.Context, target string, opts RegisterBuiltinACPAgentOptions) error {
			return agents.RegisterBuiltinWithOptions(ctx, target, gatewayapp.RegisterBuiltinACPAgentOptions{Install: opts.Install})
		},
		RegisterACPAgentFn: func(ctx context.Context, cfg CustomAgentConfig) error {
			env := make(map[string]string, len(cfg.Env))
			for key, value := range cfg.Env {
				env[key] = value
			}
			return agents.RegisterCustom(ctx, gatewayapp.AgentConfig{
				Name:        cfg.Name,
				Description: cfg.Description,
				Command:     cfg.Command,
				Args:        append([]string(nil), cfg.Args...),
				Env:         env,
				WorkDir:     cfg.WorkDir,
			})
		},
		UnregisterACPAgentFn: agents.Unregister,
		ListModelAliasesFn:   models.ListAliases,
		ListModelChoicesFn: func(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
			return toRuntimeModelChoices(models.ListChoices(ctx, ref))
		},
		ListProviderModelsFn:       models.ListProviderModels,
		ListCatalogModelsFn:        models.ListCatalogModels,
		DefaultModelCapabilitiesFn: func() ModelCapabilityInfo { return toRuntimeModelCapabilities(models.DefaultCapabilities()) },
		LookupModelCapabilitiesFn: func(provider string, modelName string) (ModelCapabilityInfo, bool) {
			return toRuntimeModelCapabilitiesWithOK(models.LookupCapabilities(provider, modelName))
		},
		ReasoningLevelsForModelFn: models.ReasoningLevels,
		EnsureCodeFreeAuthFn: func(ctx context.Context, req CodeFreeAuthRequest) error {
			return models.EnsureCodeFreeAuth(ctx, gatewayapp.CodeFreeAuthRequest{
				BaseURL:         req.BaseURL,
				OpenBrowser:     req.OpenBrowser,
				CallbackTimeout: req.CallbackTimeout,
			})
		},
		EnsureCodeFreeModelSelectionAuthFn: func(ctx context.Context, req CodeFreeAuthRequest) error {
			return models.EnsureCodeFreeModelSelectionAuth(ctx, gatewayapp.CodeFreeAuthRequest{
				BaseURL:         req.BaseURL,
				OpenBrowser:     req.OpenBrowser,
				CallbackTimeout: req.CallbackTimeout,
			})
		},
		DiscoverSkillsFn: skills.Discover,
		ListBuiltinACPAgentAddOptionsFn: func() []ACPAgentAddOption {
			return toRuntimeACPAgentAddOptions(agents.BuiltinAddOptions())
		},
		ListInstallableACPAgentOptionsFn: func() []ACPAgentAddOption {
			return toRuntimeACPAgentAddOptions(agents.InstallableOptions())
		},
		ListACPAgentsFn: func() []ACPAgentInfo { return toRuntimeACPAgents(agents.List()) },
	}
}

func toRuntimeModelConfigWithOK(cfg gatewayapp.ModelConfig, ok bool) (ModelConfig, bool) {
	if !ok {
		return ModelConfig{}, false
	}
	return toRuntimeModelConfig(cfg), true
}

func toRuntimeModelConfig(cfg gatewayapp.ModelConfig) ModelConfig {
	return ModelConfig{
		ID:                     cfg.ID,
		Alias:                  cfg.Alias,
		Provider:               cfg.Provider,
		ProfileID:              cfg.ProfileID,
		EndpointID:             cfg.EndpointID,
		API:                    cfg.API,
		Model:                  cfg.Model,
		BaseURL:                cfg.BaseURL,
		HTTPClient:             cfg.HTTPClient,
		Token:                  cfg.Token,
		TokenEnv:               cfg.TokenEnv,
		PersistToken:           cfg.PersistToken,
		AuthType:               cfg.AuthType,
		HeaderKey:              cfg.HeaderKey,
		ContextWindowTokens:    cfg.ContextWindowTokens,
		ReasoningEffort:        cfg.ReasoningEffort,
		DefaultReasoningEffort: cfg.DefaultReasoningEffort,
		ReasoningLevels:        append([]string(nil), cfg.ReasoningLevels...),
		ReasoningMode:          cfg.ReasoningMode,
		MaxOutputTok:           cfg.MaxOutputTok,
		Timeout:                cfg.Timeout,
	}
}

func toGatewayModelConfig(cfg ModelConfig) gatewayapp.ModelConfig {
	return gatewayapp.ModelConfig{
		ID:                     cfg.ID,
		Alias:                  cfg.Alias,
		Provider:               cfg.Provider,
		ProfileID:              cfg.ProfileID,
		EndpointID:             cfg.EndpointID,
		API:                    cfg.API,
		Model:                  cfg.Model,
		BaseURL:                cfg.BaseURL,
		HTTPClient:             cfg.HTTPClient,
		Token:                  cfg.Token,
		TokenEnv:               cfg.TokenEnv,
		PersistToken:           cfg.PersistToken,
		AuthType:               cfg.AuthType,
		HeaderKey:              cfg.HeaderKey,
		ContextWindowTokens:    cfg.ContextWindowTokens,
		ReasoningEffort:        cfg.ReasoningEffort,
		DefaultReasoningEffort: cfg.DefaultReasoningEffort,
		ReasoningLevels:        append([]string(nil), cfg.ReasoningLevels...),
		ReasoningMode:          cfg.ReasoningMode,
		MaxOutputTok:           cfg.MaxOutputTok,
		Timeout:                cfg.Timeout,
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
		RequestedBackend:   status.RequestedBackend,
		ResolvedBackend:    status.ResolvedBackend,
		Route:              status.Route,
		FallbackReason:     status.FallbackReason,
		InstallHint:        status.InstallHint,
		SecuritySummary:    status.SecuritySummary,
		AutoReviewDisabled: status.AutoReviewDisabled,
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
		SandboxSecuritySummary:    report.SandboxSecuritySummary,
		SandboxAutoReviewDisabled: report.SandboxAutoReviewDisabled,
		HostExecution:             report.HostExecution,
		FullAccessMode:            report.FullAccessMode,
		PermissionGrantCount:      report.PermissionGrantCount,
		PermissionGrantNetwork:    report.PermissionGrantNetwork,
		PermissionReadRootCount:   report.PermissionReadRootCount,
		PermissionWriteRootCount:  report.PermissionWriteRootCount,
		ConfigPermissionsSecure:   report.ConfigPermissionsSecure,
		Warnings:                  append([]string(nil), report.Warnings...),
	}, err
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
