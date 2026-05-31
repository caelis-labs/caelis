package gatewaydriver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
)

func (d *GatewayDriver) Connect(ctx context.Context, cfg ConnectConfig) (StatusSnapshot, error) {
	if prepared, ok, err := d.prepareConnectModelConfig(ctx, cfg); ok || err != nil {
		if err != nil {
			return StatusSnapshot{}, err
		}
		return d.connectPreparedModelConfig(ctx, prepared)
	}
	tpl, ok := findProviderTemplate(cfg.Provider)
	if !ok {
		return StatusSnapshot{}, fmt.Errorf("provider %q is not supported", strings.TrimSpace(cfg.Provider))
	}
	cfg.Provider = tpl.Provider
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.TokenEnv = strings.TrimSpace(cfg.TokenEnv)
	if env, ok := parseTokenEnvSpec(cfg.APIKey); ok {
		cfg.TokenEnv = env
		cfg.APIKey = ""
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = tpl.DefaultBaseURL
	}
	endpoint, hasEndpoint := connectEndpointForBaseURL(tpl, cfg.BaseURL)
	if strings.TrimSpace(cfg.EndpointID) == "" && hasEndpoint {
		cfg.EndpointID = endpoint.ID
	}
	if err := validateConnectConfig(tpl, cfg); err != nil {
		if !d.hasReusableConnectAuth(ctx, tpl.Provider, cfg.BaseURL) {
			return StatusSnapshot{}, err
		}
	}
	if defaults, err := connectDefaultsForConfigWithStack(ctx, d.stack, cfg); err == nil {
		if cfg.ContextWindowTokens <= 0 {
			cfg.ContextWindowTokens = defaults.ContextWindow
		}
		if cfg.MaxOutputTokens <= 0 {
			cfg.MaxOutputTokens = defaults.MaxOutput
		}
		if len(cfg.ReasoningLevels) == 0 {
			cfg.ReasoningLevels = defaults.ReasoningLevels
		}
		if cfg.ReasoningEffort == "" {
			cfg.ReasoningEffort = defaults.DefaultReasoningEffort
		}
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	api := tpl.API
	if hasEndpoint && strings.TrimSpace(string(endpoint.API)) != "" {
		api = endpoint.API
	}
	if tpl.Provider == "codefree" {
		if err := d.stack.EnsureCodeFreeAuth(ctx, CodeFreeAuthRequest{
			BaseURL:         baseURL,
			OpenBrowser:     true,
			CallbackTimeout: 5 * time.Minute,
		}); err != nil {
			return StatusSnapshot{}, err
		}
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if cfg.TimeoutSeconds <= 0 {
		timeout = 60 * time.Second
	}
	authType := defaultConnectAuthType(tpl.Provider)
	if strings.TrimSpace(cfg.AuthType) != "" {
		authType = authTypeFromString(strings.TrimSpace(cfg.AuthType))
	}
	if tpl.NoAuthRequired {
		authType = model.AuthNone
	}
	persistToken := strings.TrimSpace(cfg.APIKey) != "" && strings.TrimSpace(cfg.TokenEnv) == ""
	reasoningLevels := normalizeReasoningLevels(cfg.ReasoningLevels)
	defaultReasoningEffort := strings.TrimSpace(cfg.ReasoningEffort)
	return d.connectPreparedModelConfig(ctx, ModelConfig{
		Provider:               strings.TrimSpace(cfg.Provider),
		EndpointID:             strings.TrimSpace(cfg.EndpointID),
		API:                    api,
		Model:                  cfg.Model,
		BaseURL:                baseURL,
		Token:                  cfg.APIKey,
		TokenEnv:               cfg.TokenEnv,
		PersistToken:           persistToken,
		AuthType:               authType,
		ContextWindowTokens:    cfg.ContextWindowTokens,
		DefaultReasoningEffort: defaultReasoningEffort,
		ReasoningEffort:        defaultReasoningEffort,
		ReasoningLevels:        reasoningLevels,
		MaxOutputTok:           cfg.MaxOutputTokens,
		Timeout:                timeout,
	})
}

func (d *GatewayDriver) prepareConnectModelConfig(ctx context.Context, cfg ConnectConfig) (ModelConfig, bool, error) {
	if d == nil || d.stack == nil {
		return ModelConfig{}, false, nil
	}
	modelCfg := ModelConfig{
		Provider:            strings.TrimSpace(cfg.Provider),
		EndpointID:          strings.TrimSpace(cfg.EndpointID),
		Model:               strings.TrimSpace(cfg.Model),
		BaseURL:             strings.TrimSpace(cfg.BaseURL),
		Token:               strings.TrimSpace(cfg.APIKey),
		TokenEnv:            strings.TrimSpace(cfg.TokenEnv),
		AuthType:            authTypeFromString(strings.TrimSpace(cfg.AuthType)),
		ContextWindowTokens: cfg.ContextWindowTokens,
		MaxOutputTok:        cfg.MaxOutputTokens,
		ReasoningEffort:     strings.TrimSpace(cfg.ReasoningEffort),
		ReasoningLevels:     append([]string(nil), cfg.ReasoningLevels...),
	}
	if cfg.TimeoutSeconds > 0 {
		modelCfg.Timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	modelCfg.PersistToken = modelCfg.Token != "" && modelCfg.TokenEnv == ""
	return d.stack.PrepareConnectModelConfig(ctx, modelCfg)
}

func (d *GatewayDriver) connectPreparedModelConfig(ctx context.Context, cfg ModelConfig) (StatusSnapshot, error) {
	alias, err := d.stack.Connect(cfg)
	if err != nil {
		return StatusSnapshot{}, err
	}
	if activeSession, ok := d.currentSession(); ok && alias != "" {
		if err := d.stack.UseModel(ctx, activeSession.Ref, alias); err != nil {
			return StatusSnapshot{}, err
		}
	}
	d.mu.Lock()
	if alias != "" {
		d.defaultModelText = alias
		d.modelText = alias
	}
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) hasReusableConnectAuth(ctx context.Context, provider string, baseURL string) bool {
	if d == nil || d.stack == nil {
		return false
	}
	normalizedBaseURL := normalizedConnectBaseURL(baseURL)
	if normalizedBaseURL == "" {
		return false
	}
	ref := coresession.Ref{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.Ref
	}
	choices, err := d.stack.ListModelChoices(ctx, ref)
	if err != nil {
		return false
	}
	for _, choice := range choices {
		if !strings.EqualFold(strings.TrimSpace(choice.Provider), strings.TrimSpace(provider)) {
			continue
		}
		if normalizedConnectBaseURL(choice.BaseURL) == normalizedBaseURL {
			return true
		}
	}
	return false
}

func (d *GatewayDriver) UseModel(ctx context.Context, model string, reasoningEffort ...string) (StatusSnapshot, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	if _, activeACP, err := d.activeACPControllerStatus(ctx); err != nil {
		return StatusSnapshot{}, err
	} else if activeACP {
		reasoning := ""
		if len(reasoningEffort) > 0 {
			reasoning = strings.TrimSpace(reasoningEffort[0])
		}
		status, err := d.stack.SetACPControllerModel(ctx, activeSession.Ref, strings.TrimSpace(model), reasoning)
		if err != nil {
			return StatusSnapshot{}, err
		}
		d.mu.Lock()
		d.modelText = strings.TrimSpace(firstNonEmpty(status.Model, model))
		d.mu.Unlock()
		return d.Status(ctx)
	}
	alias, err := d.resolveStoredModelAlias(ctx, strings.TrimSpace(model))
	if err != nil {
		return StatusSnapshot{}, err
	}
	if alias == "" {
		return StatusSnapshot{}, fmt.Errorf("surfaces/tui/gatewaydriver: model alias is required")
	}
	reasoning := ""
	if len(reasoningEffort) > 0 {
		reasoning = strings.TrimSpace(reasoningEffort[0])
		if reasoning != "" && !d.modelAliasSupportsReasoningLevel(alias, reasoning) {
			return StatusSnapshot{}, fmt.Errorf("surfaces/tui/gatewaydriver: model %q does not support reasoning level %q", alias, reasoning)
		}
	}
	if err := d.stack.UseModel(ctx, activeSession.Ref, alias, reasoning); err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.modelText = alias
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) DeleteModel(ctx context.Context, alias string) error {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return err
	}
	resolved, err := d.resolveStoredModelAlias(ctx, strings.TrimSpace(alias))
	if err != nil {
		return err
	}
	if err := d.stack.DeleteModel(ctx, activeSession.Ref, resolved); err != nil {
		return err
	}
	d.mu.Lock()
	d.defaultModelText = strings.TrimSpace(d.stack.DefaultModelAlias())
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, activeSession)
	return nil
}

func (d *GatewayDriver) CycleSessionMode(ctx context.Context) (StatusSnapshot, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	if controllerStatus, activeACP, err := d.activeACPControllerStatus(ctx); err != nil {
		return StatusSnapshot{}, err
	} else if activeACP {
		next, err := nextACPControllerMode(controllerStatus)
		if err != nil {
			return StatusSnapshot{}, err
		}
		if _, err := d.stack.SetACPControllerMode(ctx, activeSession.Ref, next.ID); err != nil {
			return StatusSnapshot{}, err
		}
		return d.Status(ctx)
	}
	normalized, err := d.stack.CycleSessionMode(ctx, activeSession.Ref)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sessionMode = normalized
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) SetSandboxBackend(ctx context.Context, backend string) (StatusSnapshot, error) {
	status, err := d.stack.SetSandboxBackend(ctx, backend)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sandboxType = firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, d.sandboxType)
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) PrepareSandbox(ctx context.Context) (StatusSnapshot, error) {
	status, err := d.stack.PrepareSandbox(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sandboxType = firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, d.sandboxType)
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) RepairSandbox(ctx context.Context) (StatusSnapshot, error) {
	status, err := d.stack.RepairSandbox(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sandboxType = firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, d.sandboxType)
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) ResetSandbox(ctx context.Context) (StatusSnapshot, error) {
	status, err := d.stack.ResetSandbox(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sandboxType = firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, d.sandboxType)
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *GatewayDriver) SetSessionMode(ctx context.Context, mode string) (StatusSnapshot, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	if _, activeACP, err := d.activeACPControllerStatus(ctx); err != nil {
		return StatusSnapshot{}, err
	} else if activeACP {
		if _, err := d.stack.SetACPControllerMode(ctx, activeSession.Ref, mode); err != nil {
			return StatusSnapshot{}, err
		}
		return d.Status(ctx)
	}
	normalized, err := d.stack.SetSessionMode(ctx, activeSession.Ref, mode)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sessionMode = normalized
	d.mu.Unlock()
	status, err := d.Status(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	status.SessionMode = normalized
	status.ModeLabel = normalized
	return status, nil
}
