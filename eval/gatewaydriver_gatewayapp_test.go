//go:build e2e

package eval

import (
	"context"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/gatewaydriver"
)

func newGatewayDriverFromGatewayAppStack(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string, bindingKey string, modelText string) (*gatewaydriver.GatewayDriver, error) {
	return gatewaydriver.NewGatewayDriver(ctx, evalGatewayDriverStack(stack), preferredSessionID, bindingKey, modelText)
}

func evalGatewayDriverStack(stack *gatewayapp.Stack) *gatewaydriver.DriverStack {
	if stack == nil {
		return nil
	}
	models := stack.Models()
	agents := stack.Agents()
	skills := stack.Skills()
	status := stack.Status()
	return &gatewaydriver.DriverStack{
		GatewayFn: func() gatewaydriver.GatewayService { return stack.CurrentGateway() },
		Sessions:  stack.Sessions,
		AppName:   stack.AppName,
		UserID:    stack.UserID,
		Workspace: stack.Workspace,

		StartSessionFn:        stack.StartSession,
		ACPControllerStatusFn: agents.ControllerStatus,
		DefaultModelAliasFn:   models.DefaultAlias,
		SandboxStatusFn:       func() gatewaydriver.SandboxStatus { return evalRuntimeSandboxStatus(status.Sandbox()) },
		SessionRuntimeStateFn: func(ctx context.Context, ref session.SessionRef) (gatewaydriver.SessionRuntimeState, error) {
			return evalRuntimeSessionRuntimeState(status.SessionRuntimeState(ctx, ref))
		},
		DoctorFn: func(ctx context.Context, req gatewaydriver.DoctorRequest) (gatewaydriver.DoctorReport, error) {
			return evalRuntimeDoctorReport(status.Doctor(ctx, evalGatewayDoctorRequest(req)))
		},
		ModelConfigFn: func(alias string) (gatewaydriver.ModelConfig, bool) {
			return evalRuntimeModelConfigWithOK(models.Config(alias))
		},
		SessionUsageSnapshotFn: models.UsageSnapshot,
		CompactSessionFn:       stack.CompactSession,
		ConnectFn: func(cfg gatewaydriver.ModelConfig) (string, error) {
			return models.Connect(evalGatewayModelConfig(cfg))
		},
		UseModelFn:              models.Use,
		DeleteModelFn:           models.Delete,
		SetACPControllerModelFn: agents.SetControllerModel,
		CycleSessionModeFn:      status.CycleSessionMode,
		SetSandboxBackendFn: func(ctx context.Context, backend string) (gatewaydriver.SandboxStatus, error) {
			return evalRuntimeSandboxStatusWithError(status.SetSandboxBackend(ctx, backend))
		},
		PrepareSandboxFn: func(ctx context.Context) (gatewaydriver.SandboxStatus, error) {
			return evalRuntimeSandboxStatusWithError(status.PrepareSandbox(ctx))
		},
		RepairSandboxFn: func(ctx context.Context) (gatewaydriver.SandboxStatus, error) {
			return evalRuntimeSandboxStatusWithError(status.RepairSandbox(ctx))
		},
		PreflightSandboxFn: func(ctx context.Context, allowNonElevatedRepair bool) (gatewaydriver.SandboxStatus, error) {
			return evalRuntimeSandboxStatusWithError(status.PreflightSandbox(ctx, allowNonElevatedRepair))
		},
		ResetSandboxFn: func(ctx context.Context) (gatewaydriver.SandboxStatus, error) {
			return evalRuntimeSandboxStatusWithError(status.ResetSandbox(ctx))
		},
		SetACPControllerModeFn: agents.SetControllerMode,
		SetSessionModeFn:       status.SetSessionMode,
		RegisterBuiltinACPAgentWithOptionsFn: func(ctx context.Context, target string, opts gatewaydriver.RegisterBuiltinACPAgentOptions) error {
			return agents.RegisterBuiltinWithOptions(ctx, target, gatewayapp.RegisterBuiltinACPAgentOptions{Install: opts.Install})
		},
		RegisterACPAgentFn: func(ctx context.Context, cfg gatewaydriver.CustomAgentConfig) error {
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
		ListModelChoicesFn: func(ctx context.Context, ref session.SessionRef) ([]gatewaydriver.ModelChoice, error) {
			return evalRuntimeModelChoices(models.ListChoices(ctx, ref))
		},
		ListProviderModelsFn: models.ListProviderModels,
		ListCatalogModelsFn:  models.ListCatalogModels,
		DefaultModelCapabilitiesFn: func() gatewaydriver.ModelCapabilityInfo {
			return evalRuntimeModelCapabilities(models.DefaultCapabilities())
		},
		LookupModelCapabilitiesFn: func(provider string, modelName string) (gatewaydriver.ModelCapabilityInfo, bool) {
			return evalRuntimeModelCapabilitiesWithOK(models.LookupCapabilities(provider, modelName))
		},
		ReasoningLevelsForModelFn: models.ReasoningLevels,
		EnsureCodeFreeAuthFn: func(ctx context.Context, req gatewaydriver.CodeFreeAuthRequest) error {
			return models.EnsureCodeFreeAuth(ctx, gatewayapp.CodeFreeAuthRequest{
				BaseURL:         req.BaseURL,
				OpenBrowser:     req.OpenBrowser,
				CallbackTimeout: req.CallbackTimeout,
			})
		},
		EnsureCodeFreeModelSelectionAuthFn: func(ctx context.Context, req gatewaydriver.CodeFreeAuthRequest) error {
			return models.EnsureCodeFreeModelSelectionAuth(ctx, gatewayapp.CodeFreeAuthRequest{
				BaseURL:         req.BaseURL,
				OpenBrowser:     req.OpenBrowser,
				CallbackTimeout: req.CallbackTimeout,
			})
		},
		DiscoverSkillsFn: skills.Discover,
		ListBuiltinACPAgentAddOptionsFn: func() []gatewaydriver.ACPAgentAddOption {
			return evalRuntimeACPAgentAddOptions(agents.BuiltinAddOptions())
		},
		ListInstallableACPAgentOptionsFn: func() []gatewaydriver.ACPAgentAddOption {
			return evalRuntimeACPAgentAddOptions(agents.InstallableOptions())
		},
		ListACPAgentsFn: func() []gatewaydriver.ACPAgentInfo { return evalRuntimeACPAgents(agents.List()) },
	}
}

func evalRuntimeModelConfigWithOK(cfg gatewayapp.ModelConfig, ok bool) (gatewaydriver.ModelConfig, bool) {
	if !ok {
		return gatewaydriver.ModelConfig{}, false
	}
	return evalRuntimeModelConfig(cfg), true
}

func evalRuntimeModelConfig(cfg gatewayapp.ModelConfig) gatewaydriver.ModelConfig {
	return gatewaydriver.ModelConfig{
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

func evalGatewayModelConfig(cfg gatewaydriver.ModelConfig) gatewayapp.ModelConfig {
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

func evalRuntimeModelCapabilitiesWithOK(caps gatewayapp.ModelCapabilityInfo, ok bool) (gatewaydriver.ModelCapabilityInfo, bool) {
	return evalRuntimeModelCapabilities(caps), ok
}

func evalRuntimeModelCapabilities(caps gatewayapp.ModelCapabilityInfo) gatewaydriver.ModelCapabilityInfo {
	return gatewaydriver.ModelCapabilityInfo{
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

func evalRuntimeSandboxStatus(status gatewayapp.SandboxStatus) gatewaydriver.SandboxStatus {
	return gatewaydriver.SandboxStatus{
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

func evalRuntimeSandboxStatusWithError(status gatewayapp.SandboxStatus, err error) (gatewaydriver.SandboxStatus, error) {
	return evalRuntimeSandboxStatus(status), err
}

func evalRuntimeSessionRuntimeState(state gatewayapp.SessionRuntimeState, err error) (gatewaydriver.SessionRuntimeState, error) {
	return gatewaydriver.SessionRuntimeState{
		ModelID:         state.ModelID,
		ModelAlias:      state.ModelAlias,
		ReasoningEffort: state.ReasoningEffort,
		SessionMode:     state.SessionMode,
		SandboxMode:     state.SandboxMode,
	}, err
}

func evalRuntimeModelChoices(choices []gatewayapp.ModelChoice, err error) ([]gatewaydriver.ModelChoice, error) {
	if err != nil {
		return nil, err
	}
	out := make([]gatewaydriver.ModelChoice, 0, len(choices))
	for _, choice := range choices {
		out = append(out, gatewaydriver.ModelChoice{
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

func evalGatewayDoctorRequest(req gatewaydriver.DoctorRequest) gatewayapp.DoctorRequest {
	return gatewayapp.DoctorRequest{
		SessionRef: req.SessionRef,
		SessionID:  req.SessionID,
		BindingKey: req.BindingKey,
	}
}

func evalRuntimeDoctorReport(report gatewayapp.DoctorReport, err error) (gatewaydriver.DoctorReport, error) {
	return gatewaydriver.DoctorReport{
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
		SandboxSetup:                    evalCloneOptionalSetupStatus(report.SandboxSetup),
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
		PermissionGrantCount:            report.PermissionGrantCount,
		PermissionReadRootCount:         report.PermissionReadRootCount,
		PermissionWriteRootCount:        report.PermissionWriteRootCount,
		ConfigPermissionsSecure:         report.ConfigPermissionsSecure,
		Warnings:                        append([]string(nil), report.Warnings...),
	}, err
}

func evalCloneOptionalSetupStatus(status *sandbox.SetupStatus) *sandbox.SetupStatus {
	if status == nil {
		return nil
	}
	out := sandbox.CloneSetupStatus(*status)
	return &out
}

func evalRuntimeACPAgentAddOptions(options []gatewayapp.ACPAgentAddOption) []gatewaydriver.ACPAgentAddOption {
	out := make([]gatewaydriver.ACPAgentAddOption, 0, len(options))
	for _, option := range options {
		out = append(out, gatewaydriver.ACPAgentAddOption{
			Value:   option.Value,
			Display: option.Display,
			Detail:  option.Detail,
		})
	}
	return out
}

func evalRuntimeACPAgents(agents []gatewayapp.ACPAgentInfo) []gatewaydriver.ACPAgentInfo {
	out := make([]gatewaydriver.ACPAgentInfo, 0, len(agents))
	for _, agent := range agents {
		out = append(out, gatewaydriver.ACPAgentInfo{
			Name:        agent.Name,
			Description: agent.Description,
		})
	}
	return out
}
