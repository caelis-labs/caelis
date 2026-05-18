package gatewaydriver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (d *GatewayDriver) Connect(ctx context.Context, cfg ConnectConfig) (StatusSnapshot, error) {
	tpl, ok := findProviderTemplate(cfg.Provider)
	if !ok {
		return StatusSnapshot{}, fmt.Errorf("provider %q is not supported", strings.TrimSpace(cfg.Provider))
	}
	cfg.Provider = tpl.provider
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.TokenEnv = strings.TrimSpace(cfg.TokenEnv)
	if env, ok := parseTokenEnvSpec(cfg.APIKey); ok {
		cfg.TokenEnv = env
		cfg.APIKey = ""
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = tpl.defaultBaseURL
	}
	endpoint, hasEndpoint := connectEndpointForBaseURL(tpl, cfg.BaseURL)
	if strings.TrimSpace(cfg.EndpointID) == "" && hasEndpoint {
		cfg.EndpointID = endpoint.id
	}
	if err := validateConnectConfig(tpl, cfg); err != nil {
		if !d.hasReusableConnectAuth(ctx, tpl.provider, cfg.BaseURL) {
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
	api := tpl.api
	if hasEndpoint && strings.TrimSpace(string(endpoint.api)) != "" {
		api = endpoint.api
	}
	if tpl.provider == "codefree" {
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
	authType := defaultConnectAuthType(tpl.provider)
	if strings.TrimSpace(cfg.AuthType) != "" {
		authType = authTypeFromString(strings.TrimSpace(cfg.AuthType))
	}
	if tpl.noAuthRequired {
		authType = model.AuthNone
	}
	persistToken := strings.TrimSpace(cfg.APIKey) != "" && strings.TrimSpace(cfg.TokenEnv) == ""
	reasoningLevels := normalizeReasoningLevels(cfg.ReasoningLevels)
	defaultReasoningEffort := strings.TrimSpace(cfg.ReasoningEffort)
	alias, err := d.stack.Connect(ModelConfig{
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
	if err != nil {
		return StatusSnapshot{}, err
	}
	if activeSession, ok := d.currentSession(); ok && alias != "" {
		if err := d.stack.UseModel(ctx, activeSession.SessionRef, alias); err != nil {
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
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
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
		status, err := d.stack.SetACPControllerModel(ctx, activeSession.SessionRef, strings.TrimSpace(model), reasoning)
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
	if err := d.stack.UseModel(ctx, activeSession.SessionRef, alias, reasoning); err != nil {
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
	if err := d.stack.DeleteModel(ctx, activeSession.SessionRef, resolved); err != nil {
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
	normalized, err := d.stack.CycleSessionMode(ctx, activeSession.SessionRef)
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

func (d *GatewayDriver) SetSessionMode(ctx context.Context, mode string) (StatusSnapshot, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	normalized, err := d.stack.SetSessionMode(ctx, activeSession.SessionRef, mode)
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
