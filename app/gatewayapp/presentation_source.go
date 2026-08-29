package gatewayapp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelconfig"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

const (
	acpConfigModeID      = "mode"
	acpConfigModelID     = "model"
	acpConfigReasoningID = "reasoning_effort"
)

type gatewayPresentationSource struct {
	sessions           session.Reader
	appName            string
	userID             string
	fullAccessModeFn   func() bool
	runtimeStateFn     func(context.Context, session.SessionRef) (SessionRuntimeState, error)
	modelSnapshotFn    func() persistedModelConfig
	bindingStatusFn    func(context.Context) (agentbinding.Status, error)
	controllerStatusFn func(context.Context, session.SessionRef) (controller.ControllerStatus, bool, error)
	listAgentsFn       func() []ACPAgentInfo
	fallbackModes      presentationModeReader
	useFallbackModes   bool
	fallbackConfig     presentationConfigReader
}

type presentationModeReader interface {
	SessionModes(context.Context, session.Session) (*acpsdk.SessionModeState, error)
}

type presentationConfigReader interface {
	SessionConfigOptions(context.Context, session.Session) ([]acpsdk.SessionConfigOption, error)
}

type gatewayPresentationSourceDeps struct {
	sessions           session.Reader
	appName            string
	userID             string
	fullAccessModeFn   func() bool
	runtimeStateFn     func(context.Context, session.SessionRef) (SessionRuntimeState, error)
	modelSnapshotFn    func() persistedModelConfig
	bindingStatusFn    func(context.Context) (agentbinding.Status, error)
	controllerStatusFn func(context.Context, session.SessionRef) (controller.ControllerStatus, bool, error)
	listAgentsFn       func() []ACPAgentInfo
}

func newGatewayPresentationSource(deps gatewayPresentationSourceDeps, fallbackModes presentationModeReader, useFallbackModes bool, fallbackConfig presentationConfigReader) gatewayPresentationSource {
	return gatewayPresentationSource{
		sessions:           deps.sessions,
		appName:            deps.appName,
		userID:             deps.userID,
		fullAccessModeFn:   deps.fullAccessModeFn,
		runtimeStateFn:     deps.runtimeStateFn,
		modelSnapshotFn:    deps.modelSnapshotFn,
		bindingStatusFn:    deps.bindingStatusFn,
		controllerStatusFn: deps.controllerStatusFn,
		listAgentsFn:       deps.listAgentsFn,
		fallbackModes:      fallbackModes,
		useFallbackModes:   useFallbackModes && fallbackModes != nil,
		fallbackConfig:     fallbackConfig,
	}
}

func (p gatewayPresentationSource) SessionModes(ctx context.Context, session session.Session) (*appserver.PresentationModeState, error) {
	if p.fullAccessMode() {
		return &appserver.PresentationModeState{CurrentModeID: dangerouslySkipPermissionsModeLabel}, nil
	}
	if p.useFallbackModes {
		modes, err := p.fallbackModes.SessionModes(ctx, session)
		return presentationModesFromACP(modes), err
	}
	if p.runtimeStateFn == nil {
		return nil, fmt.Errorf("gatewayapp: Session Runtime state is unavailable")
	}
	state, err := p.runtimeStateFn(ctx, session.SessionRef)
	if err != nil {
		return nil, err
	}
	return &appserver.PresentationModeState{
		CurrentModeID: normalizeSessionModeOrDefault(state.SessionMode),
		AvailableModes: []appserver.PresentationMode{
			{ID: "auto-review", Name: "Auto Review", Description: "Use automatic AI approval review for sensitive requests."},
			{ID: "manual", Name: "Manual", Description: "Prompt the client for sensitive approval requests."},
		},
	}, nil
}

func (p gatewayPresentationSource) SessionConfigOptions(ctx context.Context, session session.Session) ([]appserver.PresentationConfigOption, error) {
	options := []appserver.PresentationConfigOption{}
	modeOption, err := p.modeConfigOption(ctx, session)
	if err != nil {
		return nil, err
	}
	if modeOption.ID != "" {
		options = append(options, modeOption)
	}
	modelOptions, err := p.modelConfigOptions(ctx, session)
	if err != nil {
		return nil, err
	}
	options = append(options, modelOptions...)
	if p.fallbackConfig != nil {
		fallback, err := p.fallbackConfig.SessionConfigOptions(ctx, session)
		if err != nil {
			return nil, err
		}
		if p.fullAccessMode() {
			fallback = slices.DeleteFunc(fallback, func(option acpsdk.SessionConfigOption) bool {
				id, category := acpConfigOptionIdentity(option)
				return strings.EqualFold(id, acpConfigModeID) || strings.EqualFold(category, acpConfigModeID)
			})
		}
		options = append(options, presentationConfigOptionsFromACP(fallback)...)
	}
	return options, nil
}

func (p gatewayPresentationSource) SessionModels(ctx context.Context, session session.Session) (*appserver.PresentationModelState, error) {
	snapshot := p.modelSnapshot()
	if len(snapshot.Configs) == 0 {
		return nil, nil
	}
	current, _, ok, err := p.currentModelConfig(ctx, session)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	models := make([]appserver.PresentationModel, 0, len(snapshot.Configs))
	for _, cfg := range snapshot.Configs {
		models = append(models, appserver.PresentationModel{
			ID:          cfg.ID,
			Name:        cfg.Alias,
			Description: modelDescription(cfg),
		})
	}
	return &appserver.PresentationModelState{
		CurrentModelID:  current,
		AvailableModels: models,
	}, nil
}

func (p gatewayPresentationSource) PromptCapabilities(context.Context) (appserver.PresentationCapabilities, error) {
	image := false
	for _, cfg := range p.modelSnapshot().Configs {
		if modelConfigSupportsImages(cfg) {
			image = true
			break
		}
	}
	return appserver.PresentationCapabilities{
		Audio:           false,
		EmbeddedContext: false,
		Image:           image,
	}, nil
}

func (p gatewayPresentationSource) AvailableCommands(ctx context.Context, sessionID string) ([]appserver.PresentationCommand, error) {
	var activeSession session.Session
	hasActiveSession := false
	if p.sessions != nil && strings.TrimSpace(sessionID) != "" {
		var err error
		activeSession, err = p.session(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		hasActiveSession = true
	}
	var bindingStatus agentbinding.Status
	boundProfiles := map[string]agentbinding.HandleStatus{}
	if p.bindingStatusFn != nil {
		if status, err := p.bindingStatusFn(ctx); err == nil {
			bindingStatus = status
			for _, handle := range agentbinding.BoundDirectHandles(status) {
				boundProfiles[string(handle.Definition.Handle)] = handle
			}
		}
	}
	commands := make([]appserver.PresentationCommand, 0, len(controlprompt.DefaultACPSpecs()))
	seen := map[string]struct{}{}
	coreNames := controlprompt.DefaultACPNames()
	if hasActiveSession && activeSession.Controller.Kind == session.ControllerKindACP {
		coreNames = controlprompt.WithoutNames(coreNames, "compact")
	}
	for _, name := range agentbinding.ProjectBoundDirectNames(coreNames, bindingStatus) {
		spec, known := controlprompt.LookupACP(name)
		cmd := appserver.PresentationCommand{
			Name: name,
		}
		if known {
			cmd.Description = spec.Description
			if hint := availableCommandHint(spec.Usage); hint != "" {
				cmd.Input = commandInput(hint)
			}
		}
		if profile, isProfile := boundProfiles[name]; isProfile {
			cmd.Description = availableProfileDescription(profile)
			if !known {
				cmd.Input = commandInput("prompt")
			}
		}
		commands = append(commands, cmd)
		seen[name] = struct{}{}
	}
	if hasActiveSession {
		runs := make([]controlagents.Run, 0, len(activeSession.Participants))
		for _, participant := range activeSession.Participants {
			runs = append(runs, controlagents.DirectRunFromParticipant(
				participant.Label,
				string(participant.Kind),
				string(participant.Role),
				participant.Source,
			))
		}
		for _, name := range controlagents.AppendRunNames(nil, runs, nil) {
			if _, exists := seen[name]; exists {
				continue
			}
			agent, _, _ := controlagents.ParseRunName(name)
			commands = append(commands, appserver.PresentationCommand{
				Name:        name,
				Description: "Continue the " + agent + " Agent run",
				Input:       commandInput("prompt"),
			})
			seen[name] = struct{}{}
		}
		if remote, active, err := p.controllerStatus(ctx, activeSession.SessionRef); err != nil {
			return nil, err
		} else if active {
			hiddenRosterNames := make(map[string]struct{})
			for _, agent := range p.listAgents() {
				if name := controlagents.NormalizeName(agent.Name); name != "" {
					hiddenRosterNames[name] = struct{}{}
				}
			}
			for _, command := range remote.Commands {
				name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(command.Name, "/")))
				if fields := strings.Fields(name); len(fields) > 0 {
					name = fields[0]
				}
				if name == "" || reservedSlashCommandName(name) {
					continue
				}
				baseName := name
				if agent, _, ok := controlagents.ParseRunName(name); ok {
					baseName = agent
				}
				if _, hidden := hiddenRosterNames[baseName]; hidden {
					continue
				}
				if _, exists := seen[name]; exists {
					continue
				}
				commands = append(commands, appserver.PresentationCommand{Name: name, Description: strings.TrimSpace(command.Description), Input: commandInput("prompt")})
				seen[name] = struct{}{}
			}
		}
	}
	return commands, nil
}

func availableProfileDescription(profile agentbinding.HandleStatus) string {
	description := strings.TrimSpace(profile.Definition.Description)
	if strings.TrimSpace(profile.Binding.ProfileID) == "" {
		return firstNonEmpty(description+" Unbound; configure it with /subagent bind.", description)
	}
	target := strings.TrimSpace(firstNonEmpty(profile.Profile.DisplayName, profile.Binding.ProfileID))
	if effort := strings.TrimSpace(profile.Binding.Effort); effort != "" {
		target += " [" + effort + "]"
	}
	if description == "" {
		return target
	}
	return description + " · " + target
}

func (p gatewayPresentationSource) modeConfigOption(ctx context.Context, session session.Session) (appserver.PresentationConfigOption, error) {
	modes, err := p.SessionModes(ctx, session)
	if err != nil {
		return appserver.PresentationConfigOption{}, err
	}
	if modes == nil || len(modes.AvailableModes) == 0 {
		return appserver.PresentationConfigOption{}, nil
	}
	return appserver.PresentationConfigOption{
		Type:         "select",
		ID:           acpConfigModeID,
		Name:         "Approval Mode",
		Description:  "Choose how approval requests are resolved for this session",
		Category:     "mode",
		CurrentValue: modes.CurrentModeID,
		Options:      modeSelectOptions(modes.AvailableModes),
	}, nil
}

func (p gatewayPresentationSource) modelConfigOptions(ctx context.Context, session session.Session) ([]appserver.PresentationConfigOption, error) {
	snapshot := p.modelSnapshot()
	if len(snapshot.Configs) == 0 {
		return nil, nil
	}
	current, cfg, ok, err := p.currentModelConfig(ctx, session)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	options := []appserver.PresentationConfigOption{{
		Type:         "select",
		ID:           acpConfigModelID,
		Name:         "Model",
		Description:  "Choose which configured model Caelis should use",
		Category:     "model",
		CurrentValue: current,
		Options:      modelSelectOptions(snapshot.Configs),
	}}
	reasoningLevels := reasoningLevelsForACPModel(cfg)
	if len(reasoningLevels) > 0 {
		options = append(options, appserver.PresentationConfigOption{
			Type:         "select",
			ID:           acpConfigReasoningID,
			Name:         "Reasoning Effort",
			Description:  "Choose how much reasoning effort the model should use",
			Category:     "thought_level",
			CurrentValue: p.currentReasoningEffort(ctx, session, cfg, reasoningLevels),
			Options:      reasoningSelectOptions(reasoningLevels),
		})
	}
	return options, nil
}

func (p gatewayPresentationSource) currentModelConfig(ctx context.Context, session session.Session) (string, ModelConfig, bool, error) {
	snapshot := p.modelSnapshot()
	if len(snapshot.Configs) == 0 {
		return "", ModelConfig{}, false, nil
	}
	if p.runtimeStateFn == nil {
		return "", ModelConfig{}, false, fmt.Errorf("gatewayapp: Session Runtime state is unavailable")
	}
	state, err := p.runtimeStateFn(ctx, session.SessionRef)
	if err != nil {
		return "", ModelConfig{}, false, err
	}
	ref := firstNonEmpty(state.ModelID, state.ModelAlias, snapshot.DefaultID, snapshot.DefaultAlias)
	if cfg, ok := configByRef(snapshot.Configs, ref); ok {
		return cfg.ID, cfg, true, nil
	}
	cfg := snapshot.Configs[0]
	return cfg.ID, cfg, true, nil
}

func (p gatewayPresentationSource) currentReasoningEffort(ctx context.Context, session session.Session, cfg ModelConfig, levels []string) string {
	if p.runtimeStateFn != nil {
		state, err := p.runtimeStateFn(ctx, session.SessionRef)
		if err == nil {
			if value := modelcatalog.NormalizeReasoningEffort(state.ReasoningEffort); value != "" {
				return value
			}
		}
	}
	for _, value := range []string{
		cfg.ReasoningEffort,
		cfg.DefaultReasoningEffort,
		modelconfig.DefaultReasoningEffortForConfig(cfg),
	} {
		if normalized := modelcatalog.NormalizeReasoningEffort(value); normalized != "" {
			return normalized
		}
	}
	if len(levels) > 0 {
		return levels[0]
	}
	return ""
}

func (p gatewayPresentationSource) session(ctx context.Context, sessionID string) (session.Session, error) {
	if p.sessions == nil {
		return session.Session{}, fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return session.Session{}, fmt.Errorf("gatewayapp: session id is required")
	}
	ref := p.sessionRef(sessionID)
	return p.sessions.Session(ctx, ref)
}

func (p gatewayPresentationSource) sessionRef(sessionID string) session.SessionRef {
	appName := "caelis"
	userID := "acp"
	appName = firstNonEmpty(strings.TrimSpace(p.appName), appName)
	userID = firstNonEmpty(strings.TrimSpace(p.userID), userID)
	return session.SessionRef{
		AppName:   appName,
		UserID:    userID,
		SessionID: strings.TrimSpace(sessionID),
	}
}

func (p gatewayPresentationSource) modelSnapshot() persistedModelConfig {
	if p.modelSnapshotFn == nil {
		return persistedModelConfig{}
	}
	return p.modelSnapshotFn()
}

func (p gatewayPresentationSource) fullAccessMode() bool {
	return p.fullAccessModeFn != nil && p.fullAccessModeFn()
}

func (p gatewayPresentationSource) controllerStatus(ctx context.Context, ref session.SessionRef) (controller.ControllerStatus, bool, error) {
	if p.controllerStatusFn == nil {
		return controller.ControllerStatus{}, false, nil
	}
	return p.controllerStatusFn(ctx, ref)
}

func (p gatewayPresentationSource) listAgents() []ACPAgentInfo {
	if p.listAgentsFn == nil {
		return nil
	}
	return p.listAgentsFn()
}

func modeSelectOptions(modes []appserver.PresentationMode) []appserver.PresentationSelectOption {
	options := make([]appserver.PresentationSelectOption, 0, len(modes))
	for _, mode := range modes {
		options = append(options, appserver.PresentationSelectOption{
			Value:       mode.ID,
			Name:        mode.Name,
			Description: mode.Description,
		})
	}
	return options
}

func modelSelectOptions(configs []ModelConfig) []appserver.PresentationSelectOption {
	options := make([]appserver.PresentationSelectOption, 0, len(configs))
	for _, cfg := range configs {
		options = append(options, appserver.PresentationSelectOption{
			Value:       cfg.ID,
			Name:        cfg.Alias,
			Description: modelDescription(cfg),
		})
	}
	return options
}

func reasoningSelectOptions(levels []string) []appserver.PresentationSelectOption {
	options := make([]appserver.PresentationSelectOption, 0, len(levels))
	for _, level := range levels {
		options = append(options, appserver.PresentationSelectOption{
			Value: level,
			Name:  reasoningDisplayName(level),
		})
	}
	return options
}

func configByRef(configs []ModelConfig, ref string) (ModelConfig, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ModelConfig{}, false
	}
	for _, cfg := range configs {
		if strings.EqualFold(strings.TrimSpace(cfg.ID), ref) {
			return cfg, true
		}
	}
	var match ModelConfig
	matches := 0
	for _, cfg := range configs {
		if strings.EqualFold(strings.TrimSpace(cfg.Alias), ref) {
			match = cfg
			matches++
		}
	}
	return match, matches == 1
}

func reasoningLevelsForACPModel(cfg ModelConfig) []string {
	levels := append([]string(nil), cfg.ReasoningLevels...)
	levels = append(levels, modelconfig.ReasoningLevelsForConfig(cfg)...)
	levels = append(levels, cfg.DefaultReasoningEffort, cfg.ReasoningEffort)
	return modelconfig.NormalizeReasoningLevels(levels)
}

func modelConfigSupportsImages(cfg ModelConfig) bool {
	return modelconfig.ModelSupportsImages(cfg)
}

func modelDescription(cfg ModelConfig) string {
	switch {
	case strings.TrimSpace(cfg.Provider) != "" && strings.TrimSpace(cfg.Model) != "":
		return strings.TrimSpace(cfg.Provider) + "/" + strings.TrimSpace(cfg.Model)
	case strings.TrimSpace(cfg.Model) != "":
		return strings.TrimSpace(cfg.Model)
	default:
		return ""
	}
}

func reasoningDisplayName(level string) string {
	level = strings.TrimSpace(level)
	if level == "" {
		return ""
	}
	return strings.ToUpper(level[:1]) + level[1:]
}

func commandInput(hint string) *appserver.PresentationCommandInput {
	return &appserver.PresentationCommandInput{Hint: hint}
}

func availableCommandHint(usage string) string {
	usage = strings.TrimSpace(usage)
	if usage == "" {
		return ""
	}
	fields := strings.Fields(usage)
	if len(fields) <= 1 {
		return ""
	}
	return strings.Join(fields[1:], " ")
}

func presentationModesFromACP(modes *acpsdk.SessionModeState) *appserver.PresentationModeState {
	if modes == nil {
		return nil
	}
	result := &appserver.PresentationModeState{CurrentModeID: string(modes.CurrentModeId)}
	for _, mode := range modes.AvailableModes {
		description := ""
		if mode.Description != nil {
			description = *mode.Description
		}
		result.AvailableModes = append(result.AvailableModes, appserver.PresentationMode{
			ID: string(mode.Id), Name: mode.Name, Description: description,
		})
	}
	return result
}

func presentationConfigOptionsFromACP(configs []acpsdk.SessionConfigOption) []appserver.PresentationConfigOption {
	result := make([]appserver.PresentationConfigOption, 0, len(configs))
	for _, config := range configs {
		mapped := appserver.PresentationConfigOption{}
		switch {
		case config.Select != nil:
			selectOption := config.Select
			mapped = appserver.PresentationConfigOption{
				Type: selectOption.Type, ID: string(selectOption.Id), Name: selectOption.Name,
				CurrentValue: string(selectOption.CurrentValue),
			}
			applyPresentationConfigDisplay(&mapped, selectOption.Description, selectOption.Category)
			var choices []acpsdk.SessionConfigSelectOption
			switch {
			case selectOption.Options.Ungrouped != nil:
				choices = append(choices, (*selectOption.Options.Ungrouped)...)
			case selectOption.Options.Grouped != nil:
				for _, group := range *selectOption.Options.Grouped {
					choices = append(choices, group.Options...)
				}
			}
			for _, option := range choices {
				description := ""
				if option.Description != nil {
					description = *option.Description
				}
				mapped.Options = append(mapped.Options, appserver.PresentationSelectOption{
					Value: string(option.Value), Name: option.Name, Description: description,
				})
			}
		case config.Boolean != nil:
			booleanOption := config.Boolean
			mapped = appserver.PresentationConfigOption{
				Type: booleanOption.Type, ID: string(booleanOption.Id), Name: booleanOption.Name,
				CurrentValue: booleanOption.CurrentValue,
			}
			applyPresentationConfigDisplay(&mapped, booleanOption.Description, booleanOption.Category)
		default:
			continue
		}
		result = append(result, mapped)
	}
	return result
}

func applyPresentationConfigDisplay(mapped *appserver.PresentationConfigOption, description *string, category *acpsdk.SessionConfigOptionCategory) {
	if description != nil {
		mapped.Description = *description
	}
	if category != nil {
		mapped.Category = string(*category)
	}
}

func acpConfigOptionIdentity(option acpsdk.SessionConfigOption) (string, string) {
	switch {
	case option.Select != nil:
		category := ""
		if option.Select.Category != nil {
			category = string(*option.Select.Category)
		}
		return strings.TrimSpace(string(option.Select.Id)), strings.TrimSpace(category)
	case option.Boolean != nil:
		category := ""
		if option.Boolean.Category != nil {
			category = string(*option.Boolean.Category)
		}
		return strings.TrimSpace(string(option.Boolean.Id)), strings.TrimSpace(category)
	default:
		return "", ""
	}
}

var _ PresentationSource = gatewayPresentationSource{}
