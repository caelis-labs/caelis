package gatewaydriver

import (
	"context"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/config"
	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	coresandbox "github.com/OnslaughtSnail/caelis/core/sandbox"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func BindAppServices(stack *DriverStack, svc appservices.Services) *DriverStack {
	if stack == nil {
		stack = &DriverStack{}
	}
	runtimeCfg := svc.Runtime()
	gateway := newAppServiceGateway(svc)
	applyRuntimeDefaults(stack, runtimeCfg)
	stack.BeginTurnFn = gateway.BeginCoreTurn
	stack.SubmitActiveTurnFn = gateway.SubmitCoreActiveTurn
	stack.InterruptFn = gateway.InterruptCore
	stack.ActiveTurnsFn = gateway.ActiveCoreTurns
	stack.ControlPlaneStateFn = gateway.CoreControlPlaneState
	stack.PromptParticipantFn = gateway.PromptCoreParticipant
	stack.StartSessionFn = func(ctx context.Context, preferredSessionID string, _ string) (coresession.Session, error) {
		active, err := svc.Sessions().Start(ctx, appservices.StartSessionRequest{
			PreferredSessionID: strings.TrimSpace(preferredSessionID),
			Workspace: coresession.Workspace{
				Key: runtimeCfg.WorkspaceKey,
				CWD: runtimeCfg.WorkspaceCWD,
			},
		})
		if err != nil {
			return coresession.Session{}, err
		}
		return active, nil
	}
	stack.ResumeSessionFn = func(ctx context.Context, req ResumeSessionRequest) (coresession.Session, error) {
		snapshot, err := svc.Sessions().Load(ctx, coresession.Ref{
			SessionID: strings.TrimSpace(req.SessionID),
		})
		if err != nil {
			return coresession.Session{}, err
		}
		return snapshot.Session, nil
	}
	stack.ListSessionCandidatesFn = func(ctx context.Context, req ListSessionCandidatesRequest) ([]ResumeCandidate, error) {
		workspace := req.Workspace
		if strings.TrimSpace(workspace.Key) == "" {
			workspace.Key = runtimeCfg.WorkspaceKey
		}
		if strings.TrimSpace(workspace.CWD) == "" {
			workspace.CWD = runtimeCfg.WorkspaceCWD
		}
		page, err := svc.Sessions().List(ctx, appservices.ListSessionsRequest{
			Workspace: workspace,
			Limit:     req.Limit,
		})
		if err != nil {
			return nil, err
		}
		out := make([]ResumeCandidate, 0, len(page.Sessions))
		for _, summary := range page.Sessions {
			candidate := resumeCandidateFromCoreSummary(summary)
			if snapshot, err := svc.Sessions().Load(ctx, summary.Session.Ref); err == nil {
				candidate = enrichResumeCandidateFromCoreSnapshot(candidate, snapshot)
			}
			if strings.TrimSpace(candidate.Prompt) == "" && strings.TrimSpace(candidate.Title) == "" {
				continue
			}
			out = append(out, candidate)
		}
		return out, nil
	}
	stack.AppStatusViewFn = func(ctx context.Context, ref coresession.Ref, includeDiagnostics bool) (appviewmodel.StatusView, error) {
		return svc.Status().View(ctx, appservices.StatusRequest{
			SessionRef:         ref,
			IncludeDiagnostics: includeDiagnostics,
		})
	}
	stack.HomeViewFn = func(ctx context.Context, ref coresession.Ref, version string) (appviewmodel.HomeView, error) {
		return svc.Views().Home(ctx, appservices.HomeRequest{SessionRef: ref, Version: version})
	}
	stack.SettingsPanelFn = func(ctx context.Context, ref coresession.Ref) (appviewmodel.SettingsPanelView, error) {
		return svc.Settings().Panel(ctx, appservices.SettingsPanelRequest{SessionRef: ref})
	}
	stack.ReplaySessionEventsFn = func(ctx context.Context, ref coresession.Ref) ([]appviewmodel.SessionEventEnvelope, error) {
		events, err := svc.Events().Replay(ctx, appservices.EventReplayRequest{
			SessionRef: ref,
		})
		if err != nil {
			return nil, err
		}
		out := make([]appviewmodel.SessionEventEnvelope, 0)
		for env := range events {
			out = append(out, appviewmodel.CloneSessionEventEnvelope(env))
		}
		return out, nil
	}
	stack.CommandCatalogFn = func(ctx context.Context) (appviewmodel.CommandCatalogView, error) {
		return svc.Commands().Available(ctx, appservices.CommandCatalogRequest{})
	}
	stack.ExecuteCommandFn = func(ctx context.Context, ref coresession.Ref, input string, parts []coremodel.ContentPart) (CommandExecutionView, error) {
		return svc.Commands().Execute(ctx, appservices.CommandExecutionRequest{
			SessionRef:   ref,
			Input:        input,
			ContentParts: coremodel.CloneContentParts(parts),
		})
	}
	stack.ModelConfigFn = func(ref string) (ModelConfig, bool) {
		cfg, err := svc.Models().Resolve(context.Background(), ref)
		if err != nil {
			return ModelConfig{}, false
		}
		return modelConfigFromApp(cfg), true
	}
	stack.ListModelChoicesFn = func(ctx context.Context, _ coresession.Ref) ([]ModelChoice, error) {
		choices, err := svc.Models().List(ctx)
		if err != nil {
			return nil, err
		}
		return modelChoicesFromApp(choices), nil
	}
	stack.ReasoningLevelsForModelFn = func(provider string, modelName string) []string {
		return svc.Models().ReasoningLevels(provider, modelName)
	}
	stack.DiscoverSkillsFn = func(ctx context.Context, _ string) ([]plugin.SkillDescriptor, error) {
		catalog, err := svc.Resources().Catalog(ctx)
		if err != nil {
			return nil, err
		}
		return catalog.Skills, nil
	}
	stack.ConnectProviderCandidatesFn = func(_ context.Context, query string, limit int) ([]SlashArgCandidate, error) {
		return slashCandidatesFromAppConnect(svc.Models().ConnectProviderCandidates(query, limit)), nil
	}
	stack.ConnectBaseURLCandidatesFn = func(ctx context.Context, provider string, query string, limit int) ([]SlashArgCandidate, error) {
		return slashCandidatesFromAppConnect(svc.Models().ConnectEndpointCandidates(ctx, provider, query, limit)), nil
	}
	stack.ConnectTimeoutCandidatesFn = func(_ context.Context, query string, limit int) ([]SlashArgCandidate, error) {
		return slashCandidatesFromAppConnect(svc.Models().ConnectTimeoutCandidates(query, limit)), nil
	}
	stack.ConnectModelCandidatesFn = func(ctx context.Context, cfg ModelConfig, query string, limit int) ([]SlashArgCandidate, error) {
		candidates, err := svc.Models().ConnectModelCandidates(ctx, modelConfigToApp(cfg), query, limit)
		return slashCandidatesFromAppConnect(candidates), err
	}
	stack.ConnectDefaultsFn = func(ctx context.Context, cfg ModelConfig) (appservices.ConnectModelDefaults, error) {
		return svc.Models().ConnectDefaults(ctx, modelConfigToApp(cfg))
	}
	stack.ACPControllerStatusFn = func(ctx context.Context, ref coresession.Ref) (appviewmodel.ControllerStatus, bool, error) {
		status, ok, err := svc.Controllers().Status(ctx, ref)
		return controllerStatusFromApp(status), ok, err
	}
	stack.SetACPControllerModelFn = func(ctx context.Context, ref coresession.Ref, modelRef string, reasoning string) (appviewmodel.ControllerStatus, error) {
		status, err := svc.Controllers().SetModel(ctx, ref, modelRef, reasoning)
		return controllerStatusFromApp(status), err
	}
	stack.SetACPControllerModeFn = func(ctx context.Context, ref coresession.Ref, mode string) (appviewmodel.ControllerStatus, error) {
		status, err := svc.Controllers().SetMode(ctx, ref, mode)
		return controllerStatusFromApp(status), err
	}
	stack.PreflightSandboxFn = func(ctx context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
		status, err := svc.Sandbox().Preflight(ctx, allowNonElevatedRepair)
		return sandboxStatusFromApp(status), err
	}
	stack.ListACPAgentsFn = func() []ACPAgentInfo {
		agents, err := svc.Agents().List(context.Background())
		if err != nil {
			return nil
		}
		return acpAgentsFromApp(agents)
	}
	stack.ListBuiltinACPAgentAddOptionsFn = func() []ACPAgentAddOption {
		agents, err := svc.Agents().ListBuiltins(context.Background())
		if err != nil {
			return nil
		}
		return builtinAgentOptionsFromApp(agents)
	}
	stack.ListInstallableACPAgentOptionsFn = func() []ACPAgentAddOption {
		options, err := svc.Agents().ListInstallableBuiltins(context.Background())
		if err != nil {
			return nil
		}
		return installableAgentOptionsFromApp(options)
	}
	stack.CompactSessionFn = func(ctx context.Context, ref coresession.Ref) error {
		_, err := svc.Compaction().Compact(ctx, appservices.CompactSessionRequest{
			SessionRef: ref,
			Trigger:    "manual",
		})
		return err
	}
	stack.ListTasksFn = func(ctx context.Context, ref coresession.Ref, opts TaskListOptions) (TaskListView, error) {
		return svc.Tasks().List(ctx, appservices.ListTasksRequest{
			SessionRef:     ref,
			Limit:          opts.Limit,
			IncludeHistory: opts.IncludeHistory,
		})
	}
	stack.TailTaskFn = func(ctx context.Context, opts TaskOutputOptions) (TaskOutputView, error) {
		return svc.Tasks().Tail(ctx, appservices.TaskOutputRequest{
			TaskID:       opts.TaskID,
			StdoutCursor: opts.StdoutCursor,
			StderrCursor: opts.StderrCursor,
		})
	}
	stack.StartTaskFn = func(ctx context.Context, opts TaskStartOptions) (TaskOutputView, error) {
		return svc.Tasks().Start(ctx, appservices.TaskStartRequest{
			Command: opts.Command,
			Args:    append([]string(nil), opts.Args...),
			Dir:     opts.Dir,
			Env:     maps.Clone(opts.Env),
		})
	}
	stack.WaitTaskFn = func(ctx context.Context, opts TaskWaitOptions) (TaskOutputView, error) {
		return svc.Tasks().Wait(ctx, appservices.TaskWaitRequest{
			TaskOutputRequest: appservices.TaskOutputRequest{
				TaskID:       opts.TaskID,
				StdoutCursor: opts.StdoutCursor,
				StderrCursor: opts.StderrCursor,
			},
			YieldTimeMS: opts.YieldTimeMS,
		})
	}
	stack.WriteTaskFn = func(ctx context.Context, opts TaskWriteOptions) (TaskOutputView, error) {
		return svc.Tasks().Write(ctx, appservices.TaskWriteRequest{
			TaskOutputRequest: appservices.TaskOutputRequest{
				TaskID:       opts.TaskID,
				StdoutCursor: opts.StdoutCursor,
				StderrCursor: opts.StderrCursor,
			},
			Input:       opts.Input,
			YieldTimeMS: opts.YieldTimeMS,
		})
	}
	stack.CancelTaskFn = func(ctx context.Context, opts TaskOutputOptions) (TaskOutputView, error) {
		return svc.Tasks().Cancel(ctx, appservices.TaskCancelRequest{
			TaskOutputRequest: appservices.TaskOutputRequest{
				TaskID:       opts.TaskID,
				StdoutCursor: opts.StdoutCursor,
				StderrCursor: opts.StderrCursor,
			},
		})
	}
	stack.ReleaseTaskFn = func(ctx context.Context, opts TaskOutputOptions) error {
		return svc.Tasks().Release(ctx, appservices.TaskOutputRequest{
			TaskID:       opts.TaskID,
			StdoutCursor: opts.StdoutCursor,
			StderrCursor: opts.StderrCursor,
		})
	}
	return stack
}

func applyRuntimeDefaults(stack *DriverStack, runtimeCfg config.Runtime) {
	if strings.TrimSpace(stack.AppName) == "" {
		stack.AppName = strings.TrimSpace(runtimeCfg.AppName)
	}
	if strings.TrimSpace(stack.UserID) == "" {
		stack.UserID = strings.TrimSpace(runtimeCfg.UserID)
	}
	if strings.TrimSpace(stack.Workspace.Key) == "" {
		stack.Workspace.Key = strings.TrimSpace(runtimeCfg.WorkspaceKey)
	}
	if strings.TrimSpace(stack.Workspace.CWD) == "" {
		stack.Workspace.CWD = strings.TrimSpace(runtimeCfg.WorkspaceCWD)
	}
}

func controllerStatusFromApp(status appservices.ControllerStatus) appviewmodel.ControllerStatus {
	return appviewmodel.ControllerStatus{
		SessionRef:      coresession.NormalizeRef(status.SessionRef),
		Agent:           strings.TrimSpace(status.Agent),
		RemoteSessionID: strings.TrimSpace(status.RemoteSessionID),
		Model:           strings.TrimSpace(status.Model),
		ModelOptions:    controllerConfigChoicesFromApp(status.ModelOptions),
		ReasoningEffort: strings.TrimSpace(status.ReasoningEffort),
		EffortOptions:   controllerConfigChoicesFromApp(status.EffortOptions),
		Mode:            strings.TrimSpace(status.Mode),
		ModeOptions:     controllerModesFromApp(status.ModeOptions),
		ConfigOptions:   controllerConfigOptionsFromApp(status.ConfigOptions),
		Lifecycle:       controllerLifecycleFromApp(status.Lifecycle),
		Diagnostics:     controllerDiagnosticsFromApp(status.Diagnostics),
	}
}

func controllerLifecycleFromApp(lifecycle *appservices.ControllerLifecycle) *appviewmodel.ControllerLifecycle {
	if lifecycle == nil {
		return nil
	}
	return &appviewmodel.ControllerLifecycle{
		RunID:           strings.TrimSpace(lifecycle.RunID),
		Phase:           strings.TrimSpace(lifecycle.Phase),
		TurnID:          strings.TrimSpace(lifecycle.TurnID),
		Running:         lifecycle.Running,
		Active:          lifecycle.Active,
		Recovering:      lifecycle.Recovering,
		RemoteSessionID: strings.TrimSpace(lifecycle.RemoteSessionID),
		Error:           strings.TrimSpace(lifecycle.Error),
		StartedAt:       lifecycle.StartedAt,
		UpdatedAt:       lifecycle.UpdatedAt,
	}
}

func controllerDiagnosticsFromApp(in []appservices.ControllerDiagnostic) []appviewmodel.ControllerDiagnostic {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerDiagnostic, 0, len(in))
	for _, diagnostic := range in {
		out = append(out, appviewmodel.ControllerDiagnostic{
			Severity: strings.TrimSpace(diagnostic.Severity),
			Kind:     strings.TrimSpace(diagnostic.Kind),
			Message:  strings.TrimSpace(diagnostic.Message),
			Meta:     maps.Clone(diagnostic.Meta),
		})
	}
	return out
}

func controllerConfigChoicesFromApp(choices []appservices.ControllerConfigChoice) []appviewmodel.ControllerConfigChoice {
	if len(choices) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerConfigChoice, 0, len(choices))
	for _, choice := range choices {
		out = append(out, appviewmodel.ControllerConfigChoice{
			Value:       strings.TrimSpace(choice.Value),
			Name:        strings.TrimSpace(choice.Name),
			Description: strings.TrimSpace(choice.Description),
		})
	}
	return out
}

func controllerModesFromApp(modes []appservices.ControllerMode) []appviewmodel.ControllerMode {
	if len(modes) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerMode, 0, len(modes))
	for _, mode := range modes {
		out = append(out, appviewmodel.ControllerMode{
			ID:          strings.TrimSpace(mode.ID),
			Name:        strings.TrimSpace(mode.Name),
			Description: strings.TrimSpace(mode.Description),
		})
	}
	return out
}

func controllerConfigOptionsFromApp(options []appservices.ControllerConfigOption) []appviewmodel.ControllerConfigOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerConfigOption, 0, len(options))
	for _, option := range options {
		out = append(out, appviewmodel.ControllerConfigOption{
			ID:           strings.TrimSpace(option.ID),
			Name:         strings.TrimSpace(option.Name),
			Type:         strings.TrimSpace(option.Type),
			Category:     strings.TrimSpace(option.Category),
			Description:  strings.TrimSpace(option.Description),
			CurrentValue: strings.TrimSpace(option.CurrentValue),
			Options:      controllerConfigChoicesFromApp(option.Options),
		})
	}
	return out
}

func modelConfigFromApp(cfg appsettings.ModelConfig) ModelConfig {
	return ModelConfig{
		ID:                     strings.TrimSpace(cfg.ID),
		Alias:                  strings.TrimSpace(cfg.Alias),
		Provider:               strings.TrimSpace(cfg.Provider),
		ProfileID:              strings.TrimSpace(cfg.ProfileID),
		EndpointID:             strings.TrimSpace(cfg.EndpointID),
		Model:                  strings.TrimSpace(cfg.Model),
		BaseURL:                strings.TrimSpace(cfg.BaseURL),
		Token:                  strings.TrimSpace(cfg.Token),
		TokenEnv:               strings.TrimSpace(cfg.TokenEnv),
		PersistToken:           cfg.PersistToken,
		AuthType:               modelAuthTypeFromApp(cfg.AuthType),
		HeaderKey:              strings.TrimSpace(cfg.HeaderKey),
		ContextWindowTokens:    cfg.ContextWindowTokens,
		ReasoningEffort:        strings.TrimSpace(cfg.ReasoningEffort),
		DefaultReasoningEffort: strings.TrimSpace(cfg.DefaultReasoningEffort),
		ReasoningLevels:        append([]string(nil), cfg.ReasoningLevels...),
		ReasoningMode:          strings.TrimSpace(cfg.ReasoningMode),
		MaxOutputTok:           cfg.MaxOutputTokens,
		Timeout:                cfg.Timeout,
	}
}

func modelAuthTypeFromApp(value string) coremodel.AuthType {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return authTypeFromString(value)
}

func modelConfigToApp(cfg ModelConfig) appsettings.ModelConfig {
	return appsettings.ModelConfig{
		ID:                     strings.TrimSpace(cfg.ID),
		Alias:                  strings.TrimSpace(cfg.Alias),
		ProfileID:              strings.TrimSpace(cfg.ProfileID),
		Provider:               strings.TrimSpace(cfg.Provider),
		EndpointID:             strings.TrimSpace(cfg.EndpointID),
		Model:                  strings.TrimSpace(cfg.Model),
		BaseURL:                strings.TrimSpace(cfg.BaseURL),
		Token:                  strings.TrimSpace(cfg.Token),
		TokenEnv:               strings.TrimSpace(cfg.TokenEnv),
		PersistToken:           cfg.PersistToken,
		AuthType:               strings.TrimSpace(string(cfg.AuthType)),
		HeaderKey:              strings.TrimSpace(cfg.HeaderKey),
		ContextWindowTokens:    cfg.ContextWindowTokens,
		MaxOutputTokens:        cfg.MaxOutputTok,
		ReasoningEffort:        strings.TrimSpace(cfg.ReasoningEffort),
		DefaultReasoningEffort: strings.TrimSpace(cfg.DefaultReasoningEffort),
		ReasoningMode:          strings.TrimSpace(cfg.ReasoningMode),
		ReasoningLevels:        append([]string(nil), cfg.ReasoningLevels...),
		Timeout:                cfg.Timeout,
	}
}

func modelChoicesFromApp(choices []appsettings.ModelChoice) []ModelChoice {
	if len(choices) == 0 {
		return nil
	}
	out := make([]ModelChoice, 0, len(choices))
	for _, choice := range choices {
		out = append(out, ModelChoice{
			ID:         strings.TrimSpace(choice.ID),
			Alias:      strings.TrimSpace(choice.Alias),
			Provider:   strings.TrimSpace(choice.Provider),
			Model:      strings.TrimSpace(choice.Model),
			ProfileID:  strings.TrimSpace(choice.ProfileID),
			EndpointID: strings.TrimSpace(choice.EndpointID),
			BaseURL:    strings.TrimSpace(choice.BaseURL),
			Detail:     strings.TrimSpace(choice.Detail),
		})
	}
	return out
}

func slashCandidatesFromAppConnect(candidates []appservices.ConnectCandidate) []SlashArgCandidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, SlashArgCandidate{
			Value:   strings.TrimSpace(candidate.Value),
			Display: strings.TrimSpace(candidate.Display),
			Detail:  strings.TrimSpace(candidate.Detail),
			NoAuth:  candidate.NoAuth,
		})
	}
	return out
}

func sandboxStatusFromApp(status appservices.SandboxStatus) SandboxStatus {
	setup := coresandbox.CloneSetupStatus(status.Setup)
	global, hasGlobal := sandboxSetupCheckByScope(setup, coresandbox.SetupGlobal)
	workspace, hasWorkspace := sandboxSetupCheckByScope(setup, coresandbox.SetupWorkspace)
	return SandboxStatus{
		RequestedBackend:         strings.TrimSpace(status.RequestedBackend),
		ResolvedBackend:          strings.TrimSpace(status.ResolvedBackend),
		Route:                    strings.TrimSpace(status.Route),
		FallbackReason:           strings.TrimSpace(status.FallbackReason),
		InstallHint:              strings.TrimSpace(status.FallbackInstallHint),
		Setup:                    setup,
		SetupRequired:            status.SetupRequired,
		SetupError:               strings.TrimSpace(status.SetupError),
		SetupMarkerCurrent:       status.SetupMarkerCurrent,
		SetupMarkerReason:        strings.TrimSpace(status.SetupMarkerReason),
		SecuritySummary:          sandboxSecuritySummary(status),
		GlobalSetupCurrent:       hasGlobal && global.Current,
		GlobalSetupRequired:      hasGlobal && global.Required,
		GlobalSetupReason:        setupReason(global, hasGlobal),
		WorkspaceSetupCurrent:    hasWorkspace && workspace.Current,
		WorkspaceSetupRequired:   hasWorkspace && workspace.Required,
		WorkspaceSetupReason:     setupReason(workspace, hasWorkspace),
		WorkspaceSetupRoot:       setupRoot(workspace, hasWorkspace),
		WorkspaceSetupWriteRoots: setupCount(workspace, hasWorkspace, "write_roots"),
		WorkspaceSetupPolicyHash: setupDetail(workspace, hasWorkspace, "policy_hash"),
		WorkspaceSetupUpdatedAt:  workspace.UpdatedAt,
	}
}

func sandboxSetupCheckByScope(status coresandbox.SetupStatus, scope coresandbox.SetupScope) (coresandbox.SetupCheck, bool) {
	for _, check := range status.Checks {
		if check.Scope == scope {
			return coresandbox.CloneSetupCheck(check), true
		}
	}
	return coresandbox.SetupCheck{}, false
}

func sandboxSecuritySummary(status appservices.SandboxStatus) string {
	backend := firstNonEmpty(status.ResolvedBackend, status.RequestedBackend)
	switch strings.ToLower(strings.TrimSpace(status.Route)) {
	case "host":
		return "host execution"
	case "sandbox":
		if backend != "" {
			return backend + " sandbox"
		}
		return "sandboxed execution"
	default:
		return strings.TrimSpace(backend)
	}
}

func setupReason(check coresandbox.SetupCheck, ok bool) string {
	if !ok {
		return ""
	}
	return firstNonEmpty(check.Error, check.Reason)
}

func setupRoot(check coresandbox.SetupCheck, ok bool) string {
	if !ok {
		return ""
	}
	return strings.TrimSpace(check.Root)
}

func setupDetail(check coresandbox.SetupCheck, ok bool, key string) string {
	if !ok {
		return ""
	}
	return strings.TrimSpace(check.Details[strings.TrimSpace(key)])
}

func setupCount(check coresandbox.SetupCheck, ok bool, key string) int {
	if !ok {
		return 0
	}
	return check.Counts[strings.TrimSpace(key)]
}

func acpAgentsFromApp(agents []appservices.AgentDescriptor) []ACPAgentInfo {
	if len(agents) == 0 {
		return nil
	}
	out := make([]ACPAgentInfo, 0, len(agents))
	for _, agent := range agents {
		if agent.Kind != appservices.AgentKindExternalACP {
			continue
		}
		id := strings.TrimSpace(agent.ID)
		name := strings.TrimSpace(agent.Name)
		command := strings.TrimSpace(agent.Command)
		if id == "" {
			id = firstNonEmpty(name, command)
		}
		if id == "" {
			continue
		}
		description := strings.TrimSpace(agent.Description)
		if description == "" {
			description = strings.Join(compactAgentDetails([]string{name, command}), " · ")
		}
		out = append(out, ACPAgentInfo{
			Name:        id,
			Description: description,
		})
	}
	return out
}

func builtinAgentOptionsFromApp(agents []appservices.AgentDescriptor) []ACPAgentAddOption {
	if len(agents) == 0 {
		return nil
	}
	out := make([]ACPAgentAddOption, 0, len(agents))
	for _, agent := range agents {
		value := strings.TrimSpace(firstNonEmpty(agent.ID, agent.Name))
		if value == "" {
			continue
		}
		out = append(out, ACPAgentAddOption{
			Value:   value,
			Display: firstNonEmpty(strings.TrimSpace(agent.Name), value),
			Detail:  strings.TrimSpace(agent.Description),
		})
	}
	return out
}

func installableAgentOptionsFromApp(options []appservices.AgentInstallOption) []ACPAgentAddOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]ACPAgentAddOption, 0, len(options))
	for _, option := range options {
		value := strings.TrimSpace(option.Value)
		if value == "" {
			continue
		}
		out = append(out, ACPAgentAddOption{
			Value:   value,
			Display: firstNonEmpty(strings.TrimSpace(option.Display), value),
			Detail:  strings.TrimSpace(option.Detail),
		})
	}
	return out
}

func compactAgentDetails(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}
