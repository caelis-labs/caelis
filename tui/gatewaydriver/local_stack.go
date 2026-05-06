package gatewaydriver

import (
	"context"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func NewLocalDriver(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string, bindingKey string, modelText string) (*GatewayDriver, error) {
	return NewGatewayDriver(ctx, driverStack(stack), preferredSessionID, bindingKey, modelText)
}

func driverStack(stack *gatewayapp.Stack) *DriverStack {
	if stack == nil {
		return nil
	}
	models := stack.Models()
	agents := stack.Agents()
	status := stack.Status()
	return &DriverStack{
		Gateway:   stack.Gateway,
		Sessions:  stack.Sessions,
		AppName:   stack.AppName,
		UserID:    stack.UserID,
		Workspace: stack.Workspace,

		StartSessionFn:        stack.StartSession,
		ACPControllerStatusFn: agents.ControllerStatus,
		DefaultModelAliasFn:   models.DefaultAlias,
		SandboxStatusFn:       func() SandboxStatus { return toRuntimeSandboxStatus(status.Sandbox()) },
		SessionRuntimeStateFn: func(ctx context.Context, ref sdksession.SessionRef) (SessionRuntimeState, error) {
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
		UnregisterACPAgentFn: agents.Unregister,
		ListModelAliasesFn:   models.ListAliases,
		ListProviderModelsFn: models.ListProviderModels,
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
		Alias:                  cfg.Alias,
		Provider:               cfg.Provider,
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
		Alias:                  cfg.Alias,
		Provider:               cfg.Provider,
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

func toRuntimeSandboxStatus(status gatewayapp.SandboxStatus) SandboxStatus {
	return SandboxStatus{
		RequestedBackend: status.RequestedBackend,
		ResolvedBackend:  status.ResolvedBackend,
		Route:            status.Route,
		FallbackReason:   status.FallbackReason,
		SecuritySummary:  status.SecuritySummary,
	}
}

func toRuntimeSandboxStatusWithError(status gatewayapp.SandboxStatus, err error) (SandboxStatus, error) {
	return toRuntimeSandboxStatus(status), err
}

func toRuntimeSessionRuntimeState(state gatewayapp.SessionRuntimeState, err error) (SessionRuntimeState, error) {
	return SessionRuntimeState{
		ModelAlias:      state.ModelAlias,
		ReasoningEffort: state.ReasoningEffort,
		SessionMode:     state.SessionMode,
		SandboxMode:     state.SandboxMode,
	}, err
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
		StoreDir:                report.StoreDir,
		SessionID:               report.SessionID,
		SessionMode:             report.SessionMode,
		ActiveModelAlias:        report.ActiveModelAlias,
		ActiveProvider:          report.ActiveProvider,
		ActiveModel:             report.ActiveModel,
		MissingAPIKey:           report.MissingAPIKey,
		SandboxRequestedBackend: report.SandboxRequestedBackend,
		SandboxResolvedBackend:  report.SandboxResolvedBackend,
		SandboxRoute:            report.SandboxRoute,
		SandboxFallbackReason:   report.SandboxFallbackReason,
		SandboxSecuritySummary:  report.SandboxSecuritySummary,
		HostExecution:           report.HostExecution,
		FullAccessMode:          report.FullAccessMode,
		ConfigPermissionsSecure: report.ConfigPermissionsSecure,
		Warnings:                append([]string(nil), report.Warnings...),
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
