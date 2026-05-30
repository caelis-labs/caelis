package gatewaydriver

import (
	"context"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/config"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
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
	stack.DeleteModelFn = func(ctx context.Context, _ portsession.SessionRef, modelRef string) error {
		return svc.Models().Delete(ctx, modelRef)
	}
	stack.ListACPAgentsFn = func() []ACPAgentInfo {
		agents, err := svc.Agents().List(context.Background())
		if err != nil {
			return nil
		}
		return acpAgentsFromApp(agents)
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
