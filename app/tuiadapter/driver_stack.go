package tuiadapter

import (
	"context"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	tuiruntime "github.com/OnslaughtSnail/caelis/gateway/adapter/tui/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func NewGatewayDriver(ctx context.Context, stack *gatewayapp.Stack, preferredSessionID string, bindingKey string, modelText string) (*tuiruntime.GatewayDriver, error) {
	return tuiruntime.NewGatewayDriver(ctx, DriverStack(stack), preferredSessionID, bindingKey, modelText)
}

func DriverStack(stack *gatewayapp.Stack) *tuiruntime.DriverStack {
	if stack == nil {
		return nil
	}
	models := stack.Models()
	agents := stack.Agents()
	status := stack.Status()
	return &tuiruntime.DriverStack{
		Gateway:   stack.Gateway,
		Sessions:  stack.Sessions,
		AppName:   stack.AppName,
		UserID:    stack.UserID,
		Workspace: stack.Workspace,

		StartSessionFn:        stack.StartSession,
		ACPControllerStatusFn: agents.ControllerStatus,
		DefaultModelAliasFn:   models.DefaultAlias,
		SandboxStatusFn:       func() tuiruntime.SandboxStatus { return toRuntimeSandboxStatus(status.Sandbox()) },
		SessionRuntimeStateFn: func(ctx context.Context, ref sdksession.SessionRef) (tuiruntime.SessionRuntimeState, error) {
			return toRuntimeSessionRuntimeState(status.SessionRuntimeState(ctx, ref))
		},
		DoctorFn: func(ctx context.Context, req tuiruntime.DoctorRequest) (tuiruntime.DoctorReport, error) {
			return toRuntimeDoctorReport(status.Doctor(ctx, toGatewayDoctorRequest(req)))
		},
		ModelConfigFn: func(alias string) (tuiruntime.ModelConfig, bool) {
			return toRuntimeModelConfigWithOK(models.Config(alias))
		},
		SessionUsageSnapshotFn:  models.UsageSnapshot,
		CompactSessionFn:        stack.CompactSession,
		ConnectFn:               func(cfg tuiruntime.ModelConfig) (string, error) { return models.Connect(toGatewayModelConfig(cfg)) },
		UseModelFn:              models.Use,
		DeleteModelFn:           models.Delete,
		SetACPControllerModelFn: agents.SetControllerModel,
		CycleSessionModeFn:      status.CycleSessionMode,
		SetSandboxBackendFn: func(ctx context.Context, backend string) (tuiruntime.SandboxStatus, error) {
			return toRuntimeSandboxStatusWithError(status.SetSandboxBackend(ctx, backend))
		},
		SetACPControllerModeFn: agents.SetControllerMode,
		SetSessionModeFn:       status.SetSessionMode,
		RegisterBuiltinACPAgentWithOptionsFn: func(ctx context.Context, target string, opts tuiruntime.RegisterBuiltinACPAgentOptions) error {
			return agents.RegisterBuiltinWithOptions(ctx, target, gatewayapp.RegisterBuiltinACPAgentOptions{Install: opts.Install})
		},
		UnregisterACPAgentFn: agents.Unregister,
		ListModelAliasesFn:   models.ListAliases,
		ListProviderModelsFn: models.ListProviderModels,
		ListBuiltinACPAgentAddOptionsFn: func() []tuiruntime.ACPAgentAddOption {
			return toRuntimeACPAgentAddOptions(agents.BuiltinAddOptions())
		},
		ListInstallableACPAgentOptionsFn: func() []tuiruntime.ACPAgentAddOption {
			return toRuntimeACPAgentAddOptions(agents.InstallableOptions())
		},
		ListACPAgentsFn: func() []tuiruntime.ACPAgentInfo { return toRuntimeACPAgents(agents.List()) },
	}
}

func toRuntimeModelConfigWithOK(cfg gatewayapp.ModelConfig, ok bool) (tuiruntime.ModelConfig, bool) {
	if !ok {
		return tuiruntime.ModelConfig{}, false
	}
	return toRuntimeModelConfig(cfg), true
}

func toRuntimeModelConfig(cfg gatewayapp.ModelConfig) tuiruntime.ModelConfig {
	return tuiruntime.ModelConfig{
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

func toGatewayModelConfig(cfg tuiruntime.ModelConfig) gatewayapp.ModelConfig {
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

func toRuntimeSandboxStatus(status gatewayapp.SandboxStatus) tuiruntime.SandboxStatus {
	return tuiruntime.SandboxStatus{
		RequestedBackend: status.RequestedBackend,
		ResolvedBackend:  status.ResolvedBackend,
		Route:            status.Route,
		FallbackReason:   status.FallbackReason,
		SecuritySummary:  status.SecuritySummary,
	}
}

func toRuntimeSandboxStatusWithError(status gatewayapp.SandboxStatus, err error) (tuiruntime.SandboxStatus, error) {
	return toRuntimeSandboxStatus(status), err
}

func toRuntimeSessionRuntimeState(state gatewayapp.SessionRuntimeState, err error) (tuiruntime.SessionRuntimeState, error) {
	return tuiruntime.SessionRuntimeState{
		ModelAlias:      state.ModelAlias,
		ReasoningEffort: state.ReasoningEffort,
		SessionMode:     state.SessionMode,
		SandboxMode:     state.SandboxMode,
	}, err
}

func toGatewayDoctorRequest(req tuiruntime.DoctorRequest) gatewayapp.DoctorRequest {
	return gatewayapp.DoctorRequest{
		SessionRef: req.SessionRef,
		SessionID:  req.SessionID,
		BindingKey: req.BindingKey,
	}
}

func toRuntimeDoctorReport(report gatewayapp.DoctorReport, err error) (tuiruntime.DoctorReport, error) {
	return tuiruntime.DoctorReport{
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

func toRuntimeACPAgentAddOptions(options []gatewayapp.ACPAgentAddOption) []tuiruntime.ACPAgentAddOption {
	out := make([]tuiruntime.ACPAgentAddOption, 0, len(options))
	for _, option := range options {
		out = append(out, tuiruntime.ACPAgentAddOption{
			Value:   option.Value,
			Display: option.Display,
			Detail:  option.Detail,
		})
	}
	return out
}

func toRuntimeACPAgents(agents []gatewayapp.ACPAgentInfo) []tuiruntime.ACPAgentInfo {
	out := make([]tuiruntime.ACPAgentInfo, 0, len(agents))
	for _, agent := range agents {
		out = append(out, tuiruntime.ACPAgentInfo{
			Name:        agent.Name,
			Description: agent.Description,
		})
	}
	return out
}
