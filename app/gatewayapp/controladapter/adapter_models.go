package controladapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

func (d *Adapter) Connect(ctx context.Context, cfg ConnectConfig) (StatusSnapshot, error) {
	if d == nil || d.stack == nil {
		return StatusSnapshot{}, missingRuntimeDependency("stack")
	}
	previousDefault := ""
	if d.stack.Model.DefaultAliasFn != nil {
		previousDefault = strings.TrimSpace(d.stack.Model.DefaultAliasFn())
	} else {
		d.mu.Lock()
		previousDefault = strings.TrimSpace(d.defaultModelText)
		d.mu.Unlock()
	}
	hadConfiguredModel := previousDefault != ""

	assembled, err := modelconfig.AssembleConnect(ctx, modelconfig.ConnectRequest{
		Provider:                       cfg.Provider,
		EndpointID:                     cfg.EndpointID,
		Models:                         connectModelSelections(cfg),
		BaseURL:                        cfg.BaseURL,
		TimeoutSeconds:                 cfg.TimeoutSeconds,
		StreamFirstEventTimeoutSeconds: cfg.StreamFirstEventTimeoutSeconds,
		APIKey:                         cfg.APIKey,
		TokenEnv:                       cfg.TokenEnv,
		AuthType:                       cfg.AuthType,
	}, modelconfig.ConnectOptions{
		HasReusableAuth: d.hasReusableConnectAuth,
		Authenticate:    d.stack.Model.AuthenticateFn,
	})
	if err != nil {
		return StatusSnapshot{}, err
	}
	if d.stack.Model.ConnectModelsFn == nil {
		return StatusSnapshot{}, missingRuntimeDependency("connect")
	}
	profiles, err := d.stack.Model.ConnectModelsFn(assembled)
	if err != nil {
		return StatusSnapshot{}, err
	}
	if len(profiles) == 0 {
		return StatusSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: connect returned no model profiles")
	}
	alias := ""
	if profiles[0].Backend.Provider != nil {
		alias = profiles[0].Backend.Provider.ModelConfigID
	}
	if alias == "" {
		return StatusSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: provider connect returned a non-provider profile")
	}
	if activeSession, ok := d.currentSession(); ok && alias != "" && !hadConfiguredModel {
		if d.stack.Model.UseFn == nil {
			return StatusSnapshot{}, missingRuntimeDependency("use model")
		}
		if err := d.stack.Model.UseFn(ctx, activeSession.SessionRef, alias); err != nil {
			return StatusSnapshot{}, err
		}
	}
	d.mu.Lock()
	if alias != "" && !hadConfiguredModel {
		d.defaultModelText = alias
		d.modelText = alias
	} else if d.stack.Model.DefaultAliasFn != nil {
		d.defaultModelText = strings.TrimSpace(d.stack.Model.DefaultAliasFn())
	}
	d.mu.Unlock()
	return d.Status(ctx)
}

func connectModelSelections(cfg ConnectConfig) []modelconfig.ModelSelection {
	names := strings.Split(cfg.Model, ",")
	selections := make([]modelconfig.ModelSelection, 0, len(names))
	seen := map[string]struct{}{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		levels := append([]string(nil), cfg.ReasoningLevels...)
		if cfg.ReasoningLevels != nil && levels == nil {
			levels = []string{}
		}
		selections = append(selections, modelconfig.ModelSelection{
			Name:                name,
			ContextWindowTokens: cfg.ContextWindowTokens,
			MaxOutputTokens:     cfg.MaxOutputTokens,
			ReasoningEffort:     cfg.ReasoningEffort,
			ReasoningLevels:     levels,
		})
	}
	return selections
}

func (d *Adapter) hasReusableConnectAuth(ctx context.Context, provider string, baseURL string) bool {
	if d == nil || d.stack == nil {
		return false
	}
	normalizedBaseURL := modelconfig.NormalizeBaseURL(baseURL)
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
		if modelconfig.NormalizeBaseURL(choice.BaseURL) == normalizedBaseURL {
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
