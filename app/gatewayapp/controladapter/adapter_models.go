package controladapter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (d *Adapter) Connect(ctx context.Context, cfg ConnectConfig) (StatusSnapshot, error) {
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
		if d.stack.Model.EnsureCodeFreeAuthFn == nil {
			return StatusSnapshot{}, missingRuntimeDependency("codefree auth")
		}
		if err := d.stack.Model.EnsureCodeFreeAuthFn(ctx, CodeFreeAuthRequest{
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
	firstEventTimeout := time.Duration(cfg.StreamFirstEventTimeoutSeconds) * time.Second
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
	if d.stack.Model.ConnectFn == nil {
		return StatusSnapshot{}, missingRuntimeDependency("connect")
	}
	alias, err := d.stack.Model.ConnectFn(ModelConfig{
		Provider:                strings.TrimSpace(cfg.Provider),
		EndpointID:              strings.TrimSpace(cfg.EndpointID),
		API:                     api,
		Model:                   cfg.Model,
		BaseURL:                 baseURL,
		Token:                   cfg.APIKey,
		TokenEnv:                cfg.TokenEnv,
		PersistToken:            persistToken,
		AuthType:                authType,
		ContextWindowTokens:     cfg.ContextWindowTokens,
		DefaultReasoningEffort:  defaultReasoningEffort,
		ReasoningEffort:         defaultReasoningEffort,
		ReasoningLevels:         reasoningLevels,
		MaxOutputTok:            cfg.MaxOutputTokens,
		Timeout:                 timeout,
		StreamFirstEventTimeout: firstEventTimeout,
	})
	if err != nil {
		return StatusSnapshot{}, err
	}
	if activeSession, ok := d.currentSession(); ok && alias != "" {
		if d.stack.Model.UseFn == nil {
			return StatusSnapshot{}, missingRuntimeDependency("use model")
		}
		if err := d.stack.Model.UseFn(ctx, activeSession.SessionRef, alias); err != nil {
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

func (d *Adapter) hasReusableConnectAuth(ctx context.Context, provider string, baseURL string) bool {
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
	choices, err := listModelChoices(ctx, d.stack.Model, ref)
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

func (d *Adapter) UseModel(ctx context.Context, model string, reasoningEffort ...string) (StatusSnapshot, error) {
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
		if d.stack.Agent.SetControllerModelFn == nil {
			return StatusSnapshot{}, missingRuntimeDependency("ACP controller model")
		}
		status, err := d.stack.Agent.SetControllerModelFn(ctx, activeSession.SessionRef, strings.TrimSpace(model), reasoning)
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
		return StatusSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: model alias is required")
	}
	reasoning := ""
	if len(reasoningEffort) > 0 {
		reasoning = strings.TrimSpace(reasoningEffort[0])
		if reasoning != "" && !d.modelAliasSupportsReasoningLevel(alias, reasoning) {
			return StatusSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: model %q does not support reasoning level %q", alias, reasoning)
		}
	}
	if d.stack.Model.UseFn == nil {
		return StatusSnapshot{}, missingRuntimeDependency("use model")
	}
	if err := d.stack.Model.UseFn(ctx, activeSession.SessionRef, alias, reasoning); err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.modelText = alias
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *Adapter) DeleteModel(ctx context.Context, alias string) error {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return err
	}
	resolved, err := d.resolveStoredModelAlias(ctx, strings.TrimSpace(alias))
	if err != nil {
		return err
	}
	if d.stack.Model.DeleteFn == nil {
		return missingRuntimeDependency("delete model")
	}
	if err := d.stack.Model.DeleteFn(ctx, activeSession.SessionRef, resolved); err != nil {
		return err
	}
	d.mu.Lock()
	if d.stack.Model.DefaultAliasFn != nil {
		d.defaultModelText = strings.TrimSpace(d.stack.Model.DefaultAliasFn())
	} else {
		d.defaultModelText = ""
	}
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, activeSession)
	return nil
}

func (d *Adapter) CycleSessionMode(ctx context.Context) (StatusSnapshot, error) {
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
		if d.stack.Agent.SetControllerModeFn == nil {
			return StatusSnapshot{}, missingRuntimeDependency("ACP controller mode")
		}
		if _, err := d.stack.Agent.SetControllerModeFn(ctx, activeSession.SessionRef, next.ID); err != nil {
			return StatusSnapshot{}, err
		}
		return d.Status(ctx)
	}
	if d.stack.Status.CycleModeFn == nil {
		return StatusSnapshot{}, missingRuntimeDependency("cycle mode")
	}
	normalized, err := d.stack.Status.CycleModeFn(ctx, activeSession.SessionRef)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sessionMode = normalized
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *Adapter) SetSandboxBackend(ctx context.Context, backend string) (StatusSnapshot, error) {
	if d.stack.Sandbox.SetBackendFn == nil {
		return StatusSnapshot{}, missingRuntimeDependency("sandbox backend")
	}
	status, err := d.stack.Sandbox.SetBackendFn(ctx, backend)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sandboxType = firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, d.sandboxType)
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *Adapter) PrepareSandbox(ctx context.Context) (StatusSnapshot, error) {
	if d.stack.Sandbox.PrepareFn == nil {
		return StatusSnapshot{}, missingRuntimeDependency("sandbox prepare")
	}
	status, err := d.stack.Sandbox.PrepareFn(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sandboxType = firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, d.sandboxType)
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *Adapter) RepairSandbox(ctx context.Context) (StatusSnapshot, error) {
	if d.stack.Sandbox.RepairFn == nil {
		return StatusSnapshot{}, missingRuntimeDependency("sandbox repair")
	}
	status, err := d.stack.Sandbox.RepairFn(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sandboxType = firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, d.sandboxType)
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *Adapter) ResetSandbox(ctx context.Context) (StatusSnapshot, error) {
	if d.stack.Sandbox.ResetFn == nil {
		return StatusSnapshot{}, missingRuntimeDependency("sandbox reset")
	}
	status, err := d.stack.Sandbox.ResetFn(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	d.mu.Lock()
	d.sandboxType = firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, d.sandboxType)
	d.mu.Unlock()
	return d.Status(ctx)
}

func (d *Adapter) SetSessionMode(ctx context.Context, mode string) (StatusSnapshot, error) {
	activeSession, err := d.ensureSession(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	if d.stack.Status.SetSessionModeFn == nil {
		return StatusSnapshot{}, missingRuntimeDependency("session mode")
	}
	normalized, err := d.stack.Status.SetSessionModeFn(ctx, activeSession.SessionRef, mode)
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
	status.Session.SessionMode = normalized
	status.Session.ModeLabel = normalized
	return status, nil
}
