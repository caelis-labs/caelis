package gatewaydriver

import (
	"context"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	coresandbox "github.com/OnslaughtSnail/caelis/core/sandbox"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	portscontroller "github.com/OnslaughtSnail/caelis/ports/controller"
	portsandbox "github.com/OnslaughtSnail/caelis/ports/sandbox"
	portsession "github.com/OnslaughtSnail/caelis/ports/session"
	portskill "github.com/OnslaughtSnail/caelis/ports/skill"
)

func BindAppServices(stack *DriverStack, svc appservices.Services) *DriverStack {
	if stack == nil {
		stack = &DriverStack{}
	}
	runtimeCfg := svc.Runtime()
	gateway := newAppServiceGateway(svc)
	applyRuntimeDefaults(stack, runtimeCfg)
	stack.GatewayFn = func() GatewayService { return gateway }
	stack.StartSessionFn = func(ctx context.Context, preferredSessionID string, _ string) (portsession.Session, error) {
		active, err := svc.Sessions().Start(ctx, appservices.StartSessionRequest{
			PreferredSessionID: strings.TrimSpace(preferredSessionID),
			Workspace: coresession.Workspace{
				Key: runtimeCfg.WorkspaceKey,
				CWD: runtimeCfg.WorkspaceCWD,
			},
		})
		if err != nil {
			return portsession.Session{}, err
		}
		return portSessionFromCore(active), nil
	}
	stack.AppStatusViewFn = func(ctx context.Context, ref portsession.SessionRef) (appviewmodel.StatusView, error) {
		return svc.Status().View(ctx, appservices.StatusRequest{SessionRef: coreRefFromPort(ref)})
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
	stack.ListModelChoicesFn = func(ctx context.Context, _ portsession.SessionRef) ([]ModelChoice, error) {
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
	stack.DiscoverSkillsFn = func(ctx context.Context, _ string) ([]portskill.Meta, error) {
		catalog, err := svc.Resources().Catalog(ctx)
		if err != nil {
			return nil, err
		}
		return skillMetasFromApp(catalog.Skills), nil
	}
	stack.ConnectFn = func(cfg ModelConfig) (string, error) {
		connected, err := svc.Models().Connect(context.Background(), modelConfigToApp(cfg))
		if err != nil {
			return "", err
		}
		return firstNonEmpty(connected.Alias, connected.ID), nil
	}
	stack.UseModelFn = func(ctx context.Context, ref portsession.SessionRef, modelRef string, reasoning ...string) error {
		effort := ""
		if len(reasoning) > 0 {
			effort = strings.TrimSpace(reasoning[0])
		}
		_, err := svc.Models().Use(ctx, coreRefFromPort(ref), modelRef, effort)
		return err
	}
	stack.ACPControllerStatusFn = func(ctx context.Context, ref portsession.SessionRef) (portscontroller.ControllerStatus, bool, error) {
		status, ok, err := svc.Controllers().Status(ctx, coreRefFromPort(ref))
		return controllerStatusFromApp(status), ok, err
	}
	stack.SetACPControllerModelFn = func(ctx context.Context, ref portsession.SessionRef, modelRef string, reasoning string) (portscontroller.ControllerStatus, error) {
		status, err := svc.Controllers().SetModel(ctx, coreRefFromPort(ref), modelRef, reasoning)
		return controllerStatusFromApp(status), err
	}
	stack.SetACPControllerModeFn = func(ctx context.Context, ref portsession.SessionRef, mode string) (portscontroller.ControllerStatus, error) {
		status, err := svc.Controllers().SetMode(ctx, coreRefFromPort(ref), mode)
		return controllerStatusFromApp(status), err
	}
	stack.DeleteModelFn = func(ctx context.Context, _ portsession.SessionRef, modelRef string) error {
		return svc.Models().Delete(ctx, modelRef)
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
	stack.RegisterBuiltinACPAgentWithOptionsFn = func(ctx context.Context, target string, opts RegisterBuiltinACPAgentOptions) error {
		_, err := svc.Agents().RegisterBuiltinWithOptions(ctx, target, appservices.RegisterBuiltinAgentOptions{Install: opts.Install})
		return err
	}
	stack.RegisterACPAgentFn = func(ctx context.Context, cfg CustomAgentConfig) error {
		_, err := svc.Agents().RegisterCustom(ctx, customAgentToApp(cfg))
		return err
	}
	stack.UnregisterACPAgentFn = func(target string) error {
		return svc.Agents().Remove(context.Background(), target)
	}
	stack.CompactSessionFn = func(ctx context.Context, ref portsession.SessionRef) error {
		_, err := svc.Compaction().Compact(ctx, appservices.CompactSessionRequest{
			SessionRef: coreRefFromPort(ref),
			Trigger:    "manual",
		})
		return err
	}
	stack.SetSessionModeFn = func(ctx context.Context, ref portsession.SessionRef, mode string) (string, error) {
		choice, err := svc.Modes().Set(ctx, coreRefFromPort(ref), mode)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(choice.ID), nil
	}
	stack.CycleSessionModeFn = func(ctx context.Context, ref portsession.SessionRef) (string, error) {
		current, err := svc.Modes().CurrentID(ctx, coreRefFromPort(ref))
		if err != nil {
			return "", err
		}
		next := coreruntime.SessionModeManual
		if current == coreruntime.SessionModeManual {
			next = coreruntime.SessionModeAutoReview
		}
		choice, err := svc.Modes().Set(ctx, coreRefFromPort(ref), next)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(choice.ID), nil
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

func portRefFromCore(ref coresession.Ref) portsession.SessionRef {
	return portsession.SessionRef{
		AppName:      strings.TrimSpace(ref.AppName),
		UserID:       strings.TrimSpace(ref.UserID),
		SessionID:    strings.TrimSpace(ref.SessionID),
		WorkspaceKey: strings.TrimSpace(ref.WorkspaceKey),
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

func controllerStatusFromApp(status appservices.ControllerStatus) portscontroller.ControllerStatus {
	return portscontroller.ControllerStatus{
		SessionRef:      portRefFromCore(status.SessionRef),
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

func controllerConfigChoicesFromApp(choices []appservices.ControllerConfigChoice) []portscontroller.ControllerConfigChoice {
	if len(choices) == 0 {
		return nil
	}
	out := make([]portscontroller.ControllerConfigChoice, 0, len(choices))
	for _, choice := range choices {
		out = append(out, portscontroller.ControllerConfigChoice{
			Value:       strings.TrimSpace(choice.Value),
			Name:        strings.TrimSpace(choice.Name),
			Description: strings.TrimSpace(choice.Description),
		})
	}
	return out
}

func controllerModesFromApp(modes []appservices.ControllerMode) []portscontroller.ControllerMode {
	if len(modes) == 0 {
		return nil
	}
	out := make([]portscontroller.ControllerMode, 0, len(modes))
	for _, mode := range modes {
		out = append(out, portscontroller.ControllerMode{
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
		AuthType:               authTypeFromString(cfg.AuthType),
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

func skillMetasFromApp(skills []plugin.SkillDescriptor) []portskill.Meta {
	if len(skills) == 0 {
		return nil
	}
	out := make([]portskill.Meta, 0, len(skills))
	for _, item := range skills {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		out = append(out, portskill.Meta{
			Name:        name,
			Description: strings.TrimSpace(item.Description),
			Path:        firstNonEmpty(item.Paths...),
		})
	}
	return out
}

func sandboxStatusFromApp(status appservices.SandboxStatus) SandboxStatus {
	setup := sandboxSetupStatusFromApp(status.Setup)
	global, hasGlobal := sandboxSetupCheckByScope(setup, portsandbox.SetupScopeGlobal)
	workspace, hasWorkspace := sandboxSetupCheckByScope(setup, portsandbox.SetupScopeWorkspace)
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

func sandboxSetupStatusFromApp(status coresandbox.SetupStatus) portsandbox.SetupStatus {
	out := portsandbox.SetupStatus{
		Required: status.Required,
		Error:    strings.TrimSpace(status.Error),
		Details:  maps.Clone(status.Details),
		Counts:   maps.Clone(status.Counts),
		Checks:   make([]portsandbox.SetupCheck, 0, len(status.Checks)),
	}
	for _, check := range status.Checks {
		out.Checks = append(out.Checks, portsandbox.SetupCheck{
			Name:      strings.TrimSpace(check.Name),
			Scope:     portsandbox.SetupScope(strings.TrimSpace(string(check.Scope))),
			Current:   check.Current,
			Required:  check.Required,
			Reason:    strings.TrimSpace(check.Reason),
			Error:     strings.TrimSpace(check.Error),
			Version:   check.Version,
			Root:      strings.TrimSpace(check.Root),
			UpdatedAt: check.UpdatedAt,
			Details:   maps.Clone(check.Details),
			Counts:    maps.Clone(check.Counts),
		})
	}
	return out
}

func sandboxSetupCheckByScope(status portsandbox.SetupStatus, scope portsandbox.SetupScope) (portsandbox.SetupCheck, bool) {
	for _, check := range status.Checks {
		if check.Scope == scope {
			return check, true
		}
	}
	return portsandbox.SetupCheck{}, false
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

func setupReason(check portsandbox.SetupCheck, ok bool) string {
	if !ok {
		return ""
	}
	return firstNonEmpty(check.Error, check.Reason)
}

func setupRoot(check portsandbox.SetupCheck, ok bool) string {
	if !ok {
		return ""
	}
	return strings.TrimSpace(check.Root)
}

func setupDetail(check portsandbox.SetupCheck, ok bool, key string) string {
	if !ok {
		return ""
	}
	return strings.TrimSpace(check.Details[strings.TrimSpace(key)])
}

func setupCount(check portsandbox.SetupCheck, ok bool, key string) int {
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

func customAgentToApp(cfg CustomAgentConfig) appservices.AgentDescriptor {
	return appservices.AgentDescriptor{
		ID:          strings.ToLower(strings.TrimSpace(cfg.Name)),
		Name:        strings.ToLower(strings.TrimSpace(cfg.Name)),
		Kind:        appservices.AgentKindExternalACP,
		Command:     strings.TrimSpace(cfg.Command),
		Args:        append([]string(nil), cfg.Args...),
		Env:         maps.Clone(cfg.Env),
		WorkDir:     strings.TrimSpace(cfg.WorkDir),
		Description: strings.TrimSpace(cfg.Description),
	}
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
