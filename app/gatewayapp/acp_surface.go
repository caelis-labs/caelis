package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/acp"
	modelcatalog "github.com/OnslaughtSnail/caelis/sdk/model/catalog"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

const (
	acpConfigModeID      = "mode"
	acpConfigModelID     = "model"
	acpConfigReasoningID = "reasoning_effort"
)

type gatewayACPSurface struct {
	stack            *Stack
	fallbackModes    acp.ModeProvider
	useFallbackModes bool
	fallbackConfig   acp.ConfigProvider
}

func newGatewayACPSurface(stack *Stack, fallbackModes acp.ModeProvider, useFallbackModes bool, fallbackConfig acp.ConfigProvider) gatewayACPSurface {
	return gatewayACPSurface{
		stack:            stack,
		fallbackModes:    fallbackModes,
		useFallbackModes: useFallbackModes && fallbackModes != nil,
		fallbackConfig:   fallbackConfig,
	}
}

func (p gatewayACPSurface) SessionModes(ctx context.Context, session sdksession.Session) (*acp.SessionModeState, error) {
	if p.useFallbackModes {
		return p.fallbackModes.SessionModes(ctx, session)
	}
	if p.stack == nil {
		return nil, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	state, err := p.stack.SessionRuntimeState(ctx, session.SessionRef)
	if err != nil {
		return nil, err
	}
	return &acp.SessionModeState{
		CurrentModeID: normalizeSessionModeOrDefault(state.SessionMode),
		AvailableModes: []acp.SessionMode{
			{ID: "auto-review", Name: "Auto Review", Description: "Use automatic AI approval review for sensitive requests."},
			{ID: "manual", Name: "Manual", Description: "Prompt for user approval for sensitive requests."},
		},
	}, nil
}

func (p gatewayACPSurface) SetSessionMode(ctx context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	if p.useFallbackModes {
		return p.fallbackModes.SetSessionMode(ctx, req)
	}
	if p.stack == nil {
		return acp.SetSessionModeResponse{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return acp.SetSessionModeResponse{}, fmt.Errorf("gatewayapp: session id is required")
	}
	_, err := p.stack.SetSessionMode(ctx, p.sessionRef(req.SessionID), req.ModeID)
	return acp.SetSessionModeResponse{}, err
}

func (p gatewayACPSurface) SessionConfigOptions(ctx context.Context, session sdksession.Session) ([]acp.SessionConfigOption, error) {
	options := []acp.SessionConfigOption{}
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
		options = append(options, fallback...)
	}
	return options, nil
}

func (p gatewayACPSurface) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	switch strings.TrimSpace(req.ConfigID) {
	case acpConfigModeID:
		value, ok := req.Value.(string)
		if !ok {
			return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("gatewayapp: mode value must be a string")
		}
		if _, err := p.SetSessionMode(ctx, acp.SetSessionModeRequest{
			SessionID: req.SessionID,
			ModeID:    value,
		}); err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
	case acpConfigModelID:
		value, ok := req.Value.(string)
		if !ok {
			return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("gatewayapp: model value must be a string")
		}
		if err := p.setSessionModel(ctx, req.SessionID, value, ""); err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
	case acpConfigReasoningID:
		value, ok := req.Value.(string)
		if !ok {
			return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("gatewayapp: reasoning effort value must be a string")
		}
		session, err := p.session(ctx, req.SessionID)
		if err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
		alias, cfg, ok, err := p.currentModelConfig(ctx, session)
		if err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
		if !ok {
			return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("gatewayapp: no model configured")
		}
		levels := reasoningLevelsForACPModel(cfg)
		value = modelcatalog.NormalizeReasoningEffort(value)
		if len(levels) > 0 && !containsACPStringFold(levels, value) {
			return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("gatewayapp: model %q does not support reasoning level %q", alias, value)
		}
		if err := p.setSessionModel(ctx, req.SessionID, alias, value); err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
	default:
		if p.fallbackConfig == nil {
			return acp.SetSessionConfigOptionResponse{}, acp.ErrCapabilityUnsupported
		}
		return p.fallbackConfig.SetSessionConfigOption(ctx, req)
	}
	session, err := p.session(ctx, req.SessionID)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	options, err := p.SessionConfigOptions(ctx, session)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

func (p gatewayACPSurface) SessionModels(ctx context.Context, session sdksession.Session) (*acp.SessionModelState, error) {
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
	models := make([]acp.ModelInfo, 0, len(snapshot.Configs))
	for _, cfg := range snapshot.Configs {
		models = append(models, acp.ModelInfo{
			ModelID:     cfg.Alias,
			Name:        cfg.Alias,
			Description: modelDescription(cfg),
		})
	}
	return &acp.SessionModelState{
		CurrentModelID:  current,
		AvailableModels: models,
	}, nil
}

func (p gatewayACPSurface) SetSessionModel(ctx context.Context, req acp.SetSessionModelRequest) (acp.SetSessionModelResponse, error) {
	if err := p.setSessionModel(ctx, req.SessionID, req.ModelID, ""); err != nil {
		return acp.SetSessionModelResponse{}, err
	}
	return acp.SetSessionModelResponse{}, nil
}

func (p gatewayACPSurface) PromptCapabilities(context.Context) (acp.PromptCapabilities, error) {
	image := false
	for _, cfg := range p.modelSnapshot().Configs {
		if modelConfigSupportsImages(cfg) {
			image = true
			break
		}
	}
	return acp.PromptCapabilities{
		Audio:           false,
		EmbeddedContext: false,
		Image:           image,
	}, nil
}

func (p gatewayACPSurface) AvailableCommands(context.Context, string) ([]acp.AvailableCommand, error) {
	return []acp.AvailableCommand{
		{Name: "agent", Description: "Manage ACP agents", Input: commandInput("use|add|install|list|remove")},
		{Name: "connect", Description: "Configure a model provider", Input: commandInput("provider model [base-url] [timeout] [token] [context] [max-output] [reasoning-levels]")},
		{Name: "model", Description: "Switch or inspect models", Input: commandInput("use <alias> [reasoning]")},
		{Name: "approval", Description: "Switch approval mode", Input: commandInput("auto-review|manual")},
		{Name: "status", Description: "Show current runtime status", Input: nil},
		{Name: "resume", Description: "Resume a previous session", Input: commandInput("session id")},
		{Name: "compact", Description: "Compact the current conversation", Input: nil},
	}, nil
}

func (p gatewayACPSurface) modeConfigOption(ctx context.Context, session sdksession.Session) (acp.SessionConfigOption, error) {
	modes, err := p.SessionModes(ctx, session)
	if err != nil {
		return acp.SessionConfigOption{}, err
	}
	if modes == nil || len(modes.AvailableModes) == 0 {
		return acp.SessionConfigOption{}, nil
	}
	return acp.SessionConfigOption{
		Type:         "select",
		ID:           acpConfigModeID,
		Name:         "Approval Preset",
		Description:  "Choose an approval and sandboxing preset for this session",
		Category:     "mode",
		CurrentValue: modes.CurrentModeID,
		Options:      modeSelectOptions(modes.AvailableModes),
	}, nil
}

func (p gatewayACPSurface) modelConfigOptions(ctx context.Context, session sdksession.Session) ([]acp.SessionConfigOption, error) {
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
	options := []acp.SessionConfigOption{{
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
		options = append(options, acp.SessionConfigOption{
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

func (p gatewayACPSurface) currentModelConfig(ctx context.Context, session sdksession.Session) (string, ModelConfig, bool, error) {
	snapshot := p.modelSnapshot()
	if len(snapshot.Configs) == 0 {
		return "", ModelConfig{}, false, nil
	}
	state, err := p.stack.SessionRuntimeState(ctx, session.SessionRef)
	if err != nil {
		return "", ModelConfig{}, false, err
	}
	alias := firstNonEmpty(state.ModelAlias, snapshot.DefaultAlias)
	if cfg, ok := configByAlias(snapshot.Configs, alias); ok {
		return cfg.Alias, cfg, true, nil
	}
	cfg := snapshot.Configs[0]
	return cfg.Alias, cfg, true, nil
}

func (p gatewayACPSurface) currentReasoningEffort(ctx context.Context, session sdksession.Session, cfg ModelConfig, levels []string) string {
	state, err := p.stack.SessionRuntimeState(ctx, session.SessionRef)
	if err == nil {
		if value := modelcatalog.NormalizeReasoningEffort(state.ReasoningEffort); value != "" {
			return value
		}
	}
	for _, value := range []string{
		cfg.ReasoningEffort,
		cfg.DefaultReasoningEffort,
		modelcatalog.DefaultReasoningEffortForModel(cfg.Provider, cfg.Model),
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

func (p gatewayACPSurface) setSessionModel(ctx context.Context, sessionID string, alias string, reasoning string) error {
	if p.stack == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("gatewayapp: session id is required")
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("gatewayapp: model id is required")
	}
	return p.stack.UseModel(ctx, p.sessionRef(sessionID), alias, reasoning)
}

func (p gatewayACPSurface) session(ctx context.Context, sessionID string) (sdksession.Session, error) {
	if p.stack == nil || p.stack.Sessions == nil {
		return sdksession.Session{}, fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sdksession.Session{}, fmt.Errorf("gatewayapp: session id is required")
	}
	return p.stack.Sessions.Session(ctx, p.sessionRef(sessionID))
}

func (p gatewayACPSurface) sessionRef(sessionID string) sdksession.SessionRef {
	appName := "caelis"
	userID := "acp"
	if p.stack != nil {
		appName = firstNonEmpty(strings.TrimSpace(p.stack.AppName), appName)
		userID = firstNonEmpty(strings.TrimSpace(p.stack.UserID), userID)
	}
	return sdksession.SessionRef{
		AppName:   appName,
		UserID:    userID,
		SessionID: strings.TrimSpace(sessionID),
	}
}

func (p gatewayACPSurface) modelSnapshot() persistedModelConfig {
	if p.stack == nil || p.stack.lookup == nil {
		return persistedModelConfig{}
	}
	return p.stack.lookup.Snapshot()
}

func modeSelectOptions(modes []acp.SessionMode) []acp.SessionConfigSelectOption {
	options := make([]acp.SessionConfigSelectOption, 0, len(modes))
	for _, mode := range modes {
		options = append(options, acp.SessionConfigSelectOption{
			Value:       mode.ID,
			Name:        mode.Name,
			Description: mode.Description,
		})
	}
	return options
}

func modelSelectOptions(configs []ModelConfig) []acp.SessionConfigSelectOption {
	options := make([]acp.SessionConfigSelectOption, 0, len(configs))
	for _, cfg := range configs {
		options = append(options, acp.SessionConfigSelectOption{
			Value:       cfg.Alias,
			Name:        cfg.Alias,
			Description: modelDescription(cfg),
		})
	}
	return options
}

func reasoningSelectOptions(levels []string) []acp.SessionConfigSelectOption {
	options := make([]acp.SessionConfigSelectOption, 0, len(levels))
	for _, level := range levels {
		options = append(options, acp.SessionConfigSelectOption{
			Value: level,
			Name:  reasoningDisplayName(level),
		})
	}
	return options
}

func configByAlias(configs []ModelConfig, alias string) (ModelConfig, bool) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ModelConfig{}, false
	}
	for _, cfg := range configs {
		if strings.EqualFold(strings.TrimSpace(cfg.Alias), alias) {
			return cfg, true
		}
	}
	return ModelConfig{}, false
}

func reasoningLevelsForACPModel(cfg ModelConfig) []string {
	levels := append([]string(nil), cfg.ReasoningLevels...)
	levels = append(levels, modelcatalog.ReasoningLevelsForModel(cfg.Provider, cfg.Model)...)
	levels = append(levels, cfg.DefaultReasoningEffort, cfg.ReasoningEffort)
	for i, level := range levels {
		levels[i] = modelcatalog.NormalizeReasoningEffort(level)
	}
	return dedupeNonEmptyStrings(levels)
}

func modelConfigSupportsImages(cfg ModelConfig) bool {
	caps, ok := modelcatalog.LookupModelCapabilities(cfg.Provider, cfg.Model)
	if !ok {
		caps, ok = modelcatalog.LookupSuggestedModelCapabilities(cfg.Provider, cfg.Model)
	}
	return ok && caps.SupportsImages
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

func commandInput(hint string) *acp.AvailableCommandInput {
	return &acp.AvailableCommandInput{Hint: hint}
}

func containsACPStringFold(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}

var _ acp.ModeProvider = gatewayACPSurface{}
var _ acp.ConfigProvider = gatewayACPSurface{}
var _ acp.ModelProvider = gatewayACPSurface{}
var _ acp.PromptCapabilitiesProvider = gatewayACPSurface{}
var _ acp.CommandProvider = gatewayACPSurface{}
