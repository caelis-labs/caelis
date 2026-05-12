package gatewaydriver

import (
	"context"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func newGatewayDriverFromGatewayAppStack(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string, bindingKey string, modelText string) (*GatewayDriver, error) {
	return NewGatewayDriver(ctx, gatewayAppStackForRuntimeTest(stack), preferredSessionID, bindingKey, modelText)
}

func gatewayAppStackForRuntimeTest(stack *gatewayapp.Stack) *DriverStack {
	if stack == nil {
		return nil
	}
	return &DriverStack{
		GatewayFn: func() GatewayService { return stack.CurrentGateway() },
		Sessions:  stack.Sessions,
		AppName:   stack.AppName,
		UserID:    stack.UserID,
		Workspace: stack.Workspace,

		StartSessionFn:        stack.StartSession,
		ACPControllerStatusFn: stack.ACPControllerStatus,
		DefaultModelAliasFn:   stack.DefaultModelAlias,
		SandboxStatusFn:       func() SandboxStatus { return testRuntimeSandboxStatus(stack.SandboxStatus()) },
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
		SetSandboxBackendFn: func(ctx context.Context, backend string) (SandboxStatus, error) {
			return testRuntimeSandboxStatusWithError(stack.SetSandboxBackend(ctx, backend))
		},
		SetACPControllerModeFn: stack.SetACPControllerMode,
		SetSessionModeFn:       stack.SetSessionMode,
		RegisterBuiltinACPAgentWithOptionsFn: func(ctx context.Context, target string, opts RegisterBuiltinACPAgentOptions) error {
			return stack.RegisterBuiltinACPAgentWithOptions(ctx, target, gatewayapp.RegisterBuiltinACPAgentOptions{Install: opts.Install})
		},
		UnregisterACPAgentFn: stack.UnregisterACPAgent,
		ListModelAliasesFn:   stack.ListModelAliases,
		ListModelChoicesFn: func(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
			return testRuntimeModelChoices(stack.ListModelChoices(ctx, ref))
		},
		ListProviderModelsFn: stack.ListProviderModels,
		ListBuiltinACPAgentAddOptionsFn: func() []ACPAgentAddOption {
			return testRuntimeACPAgentAddOptions(stack.ListBuiltinACPAgentAddOptions())
		},
		ListInstallableACPAgentOptionsFn: func() []ACPAgentAddOption {
			return testRuntimeACPAgentAddOptions(stack.ListInstallableACPAgentOptions())
		},
		ListACPAgentsFn: func() []ACPAgentInfo { return testRuntimeACPAgents(stack.ListACPAgents()) },
	}
}

func testRuntimeModelConfigWithOK(cfg gatewayapp.ModelConfig, ok bool) (ModelConfig, bool) {
	if !ok {
		return ModelConfig{}, false
	}
	return testRuntimeModelConfig(cfg), true
}

func testRuntimeModelConfig(cfg gatewayapp.ModelConfig) ModelConfig {
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

func testGatewayModelConfig(cfg ModelConfig) gatewayapp.ModelConfig {
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

func testRuntimeSandboxStatus(status gatewayapp.SandboxStatus) SandboxStatus {
	return SandboxStatus{
		RequestedBackend: status.RequestedBackend,
		ResolvedBackend:  status.ResolvedBackend,
		Route:            status.Route,
		FallbackReason:   status.FallbackReason,
		SecuritySummary:  status.SecuritySummary,
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
		StoreDir:                 report.StoreDir,
		SessionID:                report.SessionID,
		SessionMode:              report.SessionMode,
		ActiveModelAlias:         report.ActiveModelAlias,
		ActiveProvider:           report.ActiveProvider,
		ActiveModel:              report.ActiveModel,
		MissingAPIKey:            report.MissingAPIKey,
		SandboxRequestedBackend:  report.SandboxRequestedBackend,
		SandboxResolvedBackend:   report.SandboxResolvedBackend,
		SandboxRoute:             report.SandboxRoute,
		SandboxFallbackReason:    report.SandboxFallbackReason,
		SandboxSecuritySummary:   report.SandboxSecuritySummary,
		HostExecution:            report.HostExecution,
		FullAccessMode:           report.FullAccessMode,
		PermissionGrantCount:     report.PermissionGrantCount,
		PermissionGrantNetwork:   report.PermissionGrantNetwork,
		PermissionReadRootCount:  report.PermissionReadRootCount,
		PermissionWriteRootCount: report.PermissionWriteRootCount,
		ConfigPermissionsSecure:  report.ConfigPermissionsSecure,
		Warnings:                 append([]string(nil), report.Warnings...),
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
