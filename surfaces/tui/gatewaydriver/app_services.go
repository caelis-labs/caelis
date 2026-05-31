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
	portsession "github.com/OnslaughtSnail/caelis/ports/session"
)

func BindAppServices(stack *DriverStack, svc appservices.Services) *DriverStack {
	if stack == nil {
		stack = &DriverStack{}
	}
	runtimeCfg := svc.Runtime()
	gateway := newAppServiceGateway(svc)
	applyRuntimeDefaults(stack, runtimeCfg)
	stack.GatewayFn = func() GatewayService { return gateway }
	stack.BeginTurnFn = gateway.BeginCoreTurn
	stack.SubmitActiveTurnFn = gateway.SubmitCoreActiveTurn
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
	stack.AppStatusViewFn = func(ctx context.Context, ref coresession.Ref) (appviewmodel.StatusView, error) {
		return svc.Status().View(ctx, appservices.StatusRequest{SessionRef: ref})
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
	stack.SandboxStatusFn = func() SandboxStatus {
		status, err := svc.Sandbox().Status(context.Background())
		if err != nil {
			return SandboxStatus{}
		}
		return sandboxStatusFromApp(status)
	}
	stack.DefaultModelAliasFn = func() string {
		cfg, ok, err := svc.Models().Current(context.Background(), coresession.Ref{})
		if err != nil || !ok {
			return strings.TrimSpace(runtimeCfg.Model)
		}
		return firstNonEmpty(cfg.Alias, cfg.ID)
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
	stack.ListProviderModelsFn = func(provider string) []string {
		models, err := svc.Models().ConfiguredProviderModels(context.Background(), provider)
		if err != nil {
			return nil
		}
		return models
	}
	stack.ListProviderModelsForConfigFn = func(ctx context.Context, cfg ModelConfig) ([]string, error) {
		return svc.Models().ProviderModels(ctx, modelConfigToApp(cfg))
	}
	stack.ListCatalogModelsFn = func(provider string) []string {
		return svc.Models().ListCatalogModels(provider)
	}
	stack.DefaultModelCapabilitiesFn = func() ModelCapabilityInfo {
		return modelCapabilityInfoFromApp(svc.Models().DefaultCapabilities())
	}
	stack.LookupModelCapabilitiesFn = func(provider string, modelName string) (ModelCapabilityInfo, bool) {
		caps, ok := svc.Models().LookupCapabilities(provider, modelName)
		return modelCapabilityInfoFromApp(caps), ok
	}
	stack.ReasoningLevelsForModelFn = func(provider string, modelName string) []string {
		return svc.Models().ReasoningLevels(provider, modelName)
	}
	stack.EnsureCodeFreeAuthFn = func(ctx context.Context, req CodeFreeAuthRequest) error {
		_, err := svc.Models().EnsureCodeFreeAuth(ctx, codeFreeAuthRequestToApp(req))
		return err
	}
	stack.EnsureCodeFreeModelSelectionAuthFn = func(ctx context.Context, req CodeFreeAuthRequest) error {
		_, err := svc.Models().EnsureCodeFreeModelSelectionAuth(ctx, codeFreeAuthRequestToApp(req))
		return err
	}
	stack.DiscoverSkillsFn = func(ctx context.Context, _ string) ([]plugin.SkillDescriptor, error) {
		catalog, err := svc.Resources().Catalog(ctx)
		if err != nil {
			return nil, err
		}
		return catalog.Skills, nil
	}
	stack.ConnectFn = func(cfg ModelConfig) (string, error) {
		connected, err := svc.Models().Connect(context.Background(), modelConfigToApp(cfg))
		if err != nil {
			return "", err
		}
		if modelAliasIsAmbiguous(context.Background(), svc, connected) {
			return strings.TrimSpace(connected.ID), nil
		}
		return firstNonEmpty(connected.Alias, connected.ID), nil
	}
	stack.PrepareConnectModelConfigFn = func(ctx context.Context, cfg ModelConfig) (ModelConfig, error) {
		prepared, err := svc.Models().PrepareConnectConfig(ctx, modelConfigToApp(cfg))
		if err != nil {
			return ModelConfig{}, err
		}
		return modelConfigFromApp(prepared), nil
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
	stack.ConnectDefaultsFn = func(ctx context.Context, cfg ModelConfig) (connectModelDefaults, error) {
		defaults, err := svc.Models().ConnectDefaults(ctx, modelConfigToApp(cfg))
		return connectModelDefaultsFromApp(defaults), err
	}
	stack.UseModelFn = func(ctx context.Context, ref coresession.Ref, modelRef string, reasoning ...string) error {
		effort := ""
		if len(reasoning) > 0 {
			effort = strings.TrimSpace(reasoning[0])
		}
		_, err := svc.Models().Use(ctx, ref, modelRef, effort)
		return err
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
	stack.DeleteModelFn = func(ctx context.Context, ref coresession.Ref, modelRef string) error {
		deleted, resolveErr := svc.Models().Resolve(ctx, modelRef)
		if err := svc.Models().Delete(ctx, modelRef); err != nil {
			return err
		}
		if resolveErr == nil && strings.TrimSpace(ref.SessionID) != "" {
			snapshot, err := svc.Sessions().Load(ctx, ref)
			if err == nil {
				if currentID, _ := snapshot.State[appservices.StateCurrentModelID].(string); strings.EqualFold(strings.TrimSpace(currentID), strings.TrimSpace(deleted.ID)) {
					_ = svc.Models().ClearSession(ctx, ref)
				}
			}
		}
		return nil
	}
	stack.SetSandboxBackendFn = func(ctx context.Context, backend string) (SandboxStatus, error) {
		if _, err := svc.Settings().SetSandboxBackend(ctx, backend); err != nil {
			status, statusErr := svc.Sandbox().Status(ctx)
			if statusErr != nil {
				return SandboxStatus{}, err
			}
			return sandboxStatusFromApp(status), err
		}
		status, err := svc.Sandbox().Status(ctx)
		if err != nil {
			return SandboxStatus{}, err
		}
		return sandboxStatusFromApp(status), nil
	}
	stack.PrepareSandboxFn = func(ctx context.Context) (SandboxStatus, error) {
		status, err := svc.Sandbox().Prepare(ctx)
		return sandboxStatusFromApp(status), err
	}
	stack.RepairSandboxFn = func(ctx context.Context) (SandboxStatus, error) {
		status, err := svc.Sandbox().Repair(ctx)
		return sandboxStatusFromApp(status), err
	}
	stack.PreflightSandboxFn = func(ctx context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
		status, err := svc.Sandbox().Preflight(ctx, allowNonElevatedRepair)
		return sandboxStatusFromApp(status), err
	}
	stack.ResetSandboxFn = func(ctx context.Context) (SandboxStatus, error) {
		status, err := svc.Sandbox().Reset(ctx)
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
	stack.SetSessionModeFn = func(ctx context.Context, ref coresession.Ref, mode string) (string, error) {
		choice, err := svc.Modes().Set(ctx, ref, mode)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(choice.ID), nil
	}
	stack.CycleSessionModeFn = func(ctx context.Context, ref coresession.Ref) (string, error) {
		choice, err := svc.Modes().Toggle(ctx, ref)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(choice.ID), nil
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

func coreRefFromPort(ref portsession.SessionRef) coresession.Ref {
	return coresession.Ref{
		AppName:      strings.TrimSpace(ref.AppName),
		UserID:       strings.TrimSpace(ref.UserID),
		SessionID:    strings.TrimSpace(ref.SessionID),
		WorkspaceKey: strings.TrimSpace(ref.WorkspaceKey),
	}
}

func coreSessionFromPort(active portsession.Session) coresession.Session {
	return coresession.Session{
		Ref: coreRefFromPort(active.SessionRef),
		Workspace: coresession.Workspace{
			Key: strings.TrimSpace(active.SessionRef.WorkspaceKey),
			CWD: strings.TrimSpace(active.CWD),
		},
		Title:        strings.TrimSpace(active.Title),
		Meta:         maps.Clone(active.Metadata),
		Controller:   coreControllerBindingFromPort(active.Controller),
		Participants: coreParticipantBindingsFromPort(active.Participants),
		CreatedAt:    active.CreatedAt,
		UpdatedAt:    active.UpdatedAt,
	}
}

func portRefFromCore(ref coresession.Ref) portsession.SessionRef {
	return portsession.SessionRef{
		AppName:      strings.TrimSpace(ref.AppName),
		UserID:       strings.TrimSpace(ref.UserID),
		SessionID:    strings.TrimSpace(ref.SessionID),
		WorkspaceKey: strings.TrimSpace(ref.WorkspaceKey),
	}
}

func coreControllerBindingFromPort(in portsession.ControllerBinding) coresession.ControllerBinding {
	kind := coresession.ControllerKind(strings.TrimSpace(string(in.Kind)))
	if kind == "" && strings.TrimSpace(in.AgentName) != "" {
		kind = coresession.ControllerBuiltin
	}
	return coresession.ControllerBinding{
		Kind:            kind,
		ID:              strings.TrimSpace(in.ControllerID),
		AgentName:       strings.TrimSpace(in.AgentName),
		Label:           strings.TrimSpace(in.Label),
		EpochID:         strings.TrimSpace(in.EpochID),
		RemoteSessionID: strings.TrimSpace(in.RemoteSessionID),
		ContextSyncSeq:  in.ContextSyncSeq,
		AttachedAt:      in.AttachedAt,
		Source:          strings.TrimSpace(in.Source),
	}
}

func coreParticipantBindingsFromPort(in []portsession.ParticipantBinding) []coresession.ParticipantBinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]coresession.ParticipantBinding, 0, len(in))
	for _, participant := range in {
		out = append(out, coreParticipantBindingFromPort(participant))
	}
	return out
}

func coreParticipantBindingFromPort(in portsession.ParticipantBinding) coresession.ParticipantBinding {
	return coresession.ParticipantBinding{
		ID:             strings.TrimSpace(in.ID),
		Kind:           coresession.ParticipantKind(strings.TrimSpace(string(in.Kind))),
		Role:           coresession.ParticipantRole(strings.TrimSpace(string(in.Role))),
		AgentName:      strings.TrimSpace(in.AgentName),
		Label:          strings.TrimSpace(in.Label),
		SessionID:      strings.TrimSpace(in.SessionID),
		Source:         strings.TrimSpace(in.Source),
		ParentTurnID:   strings.TrimSpace(in.ParentTurnID),
		DelegationID:   strings.TrimSpace(in.DelegationID),
		ContextSyncSeq: in.ContextSyncSeq,
		AttachedAt:     in.AttachedAt,
		ControllerRef:  strings.TrimSpace(in.ControllerRef),
	}
}

func portSessionFromCore(active coresession.Session) portsession.Session {
	return portsession.Session{
		SessionRef:   portRefFromCore(active.Ref),
		CWD:          strings.TrimSpace(active.Workspace.CWD),
		Title:        strings.TrimSpace(active.Title),
		Metadata:     maps.Clone(active.Meta),
		Controller:   portControllerFromCore(active.Controller),
		Participants: portParticipantsFromCore(active.Participants),
		CreatedAt:    active.CreatedAt,
		UpdatedAt:    active.UpdatedAt,
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
	}
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

func portControllerFromCore(in coresession.ControllerBinding) portsession.ControllerBinding {
	kind := portsession.ControllerKind(in.Kind)
	if in.Kind == coresession.ControllerBuiltin {
		kind = portsession.ControllerKindKernel
	}
	return portsession.ControllerBinding{
		Kind:            kind,
		ControllerID:    strings.TrimSpace(in.ID),
		AgentName:       strings.TrimSpace(in.AgentName),
		Label:           strings.TrimSpace(in.Label),
		EpochID:         strings.TrimSpace(in.EpochID),
		RemoteSessionID: strings.TrimSpace(in.RemoteSessionID),
		ContextSyncSeq:  in.ContextSyncSeq,
		AttachedAt:      in.AttachedAt,
		Source:          strings.TrimSpace(in.Source),
	}
}

func portParticipantsFromCore(in []coresession.ParticipantBinding) []portsession.ParticipantBinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]portsession.ParticipantBinding, 0, len(in))
	for _, participant := range in {
		out = append(out, portParticipantFromCore(participant))
	}
	return out
}

func portParticipantFromCore(in coresession.ParticipantBinding) portsession.ParticipantBinding {
	return portsession.ParticipantBinding{
		ID:             strings.TrimSpace(in.ID),
		Kind:           portsession.ParticipantKind(in.Kind),
		Role:           portsession.ParticipantRole(in.Role),
		AgentName:      strings.TrimSpace(in.AgentName),
		Label:          strings.TrimSpace(in.Label),
		SessionID:      strings.TrimSpace(in.SessionID),
		Source:         strings.TrimSpace(in.Source),
		ParentTurnID:   strings.TrimSpace(in.ParentTurnID),
		DelegationID:   strings.TrimSpace(in.DelegationID),
		ContextSyncSeq: in.ContextSyncSeq,
		AttachedAt:     in.AttachedAt,
		ControllerRef:  strings.TrimSpace(in.ControllerRef),
	}
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

func modelAliasIsAmbiguous(ctx context.Context, svc appservices.Services, cfg appsettings.ModelConfig) bool {
	alias := strings.TrimSpace(cfg.Alias)
	if alias == "" {
		return false
	}
	choices, err := svc.Models().List(ctx)
	if err != nil {
		return false
	}
	count := 0
	for _, choice := range choices {
		if strings.EqualFold(strings.TrimSpace(choice.Alias), alias) {
			count++
		}
	}
	return count > 1
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

func connectModelDefaultsFromApp(defaults appservices.ConnectModelDefaults) connectModelDefaults {
	return connectModelDefaults{
		ContextWindow:          defaults.ContextWindow,
		MaxOutput:              defaults.MaxOutput,
		ReasoningLevels:        append([]string(nil), defaults.ReasoningLevels...),
		DefaultReasoningEffort: strings.TrimSpace(defaults.DefaultReasoningEffort),
	}
}

func modelCapabilityInfoFromApp(caps appservices.ModelCapabilityInfo) ModelCapabilityInfo {
	return ModelCapabilityInfo{
		ContextWindowTokens:    caps.ContextWindowTokens,
		DefaultMaxOutputTokens: caps.DefaultMaxOutputTokens,
		MaxOutputTokens:        caps.MaxOutputTokens,
		ReasoningEfforts:       append([]string(nil), caps.ReasoningEfforts...),
		DefaultReasoningEffort: strings.TrimSpace(caps.DefaultReasoningEffort),
		SupportsReasoning:      caps.SupportsReasoning,
		SupportsToolCalls:      caps.SupportsToolCalls,
		SupportsImages:         caps.SupportsImages,
		SupportsJSON:           caps.SupportsJSONOutput,
	}
}

func codeFreeAuthRequestToApp(req CodeFreeAuthRequest) appservices.CodeFreeAuthRequest {
	return appservices.CodeFreeAuthRequest{
		BaseURL:         strings.TrimSpace(req.BaseURL),
		OpenBrowser:     req.OpenBrowser,
		CallbackTimeout: req.CallbackTimeout,
	}
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
