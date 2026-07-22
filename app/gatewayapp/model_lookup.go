package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

var errAmbiguousModelAlias = errors.New("ambiguous model alias")

type modelLookup struct {
	mu                sync.RWMutex
	configs           map[string]ModelConfig
	providerEndpoints map[string]ProviderEndpointConfig
	contextWindow     int
	defaultID         string
	resolveHTTPClient func(context.Context, ModelConfig) (*http.Client, error)
	resolveAPIKey     func(context.Context, string) (string, error)
}

func newModelLookup(store *appConfigStore, cfg ModelConfig, contextWindow int) (*modelLookup, error) {
	lookup := &modelLookup{
		configs:           map[string]ModelConfig{},
		providerEndpoints: map[string]ProviderEndpointConfig{},
		contextWindow:     contextWindow,
	}
	if store != nil {
		doc, err := store.Load()
		if err != nil {
			return nil, err
		}
		for _, item := range doc.Models.ProviderEndpoints {
			if _, err := lookup.UpsertProviderEndpoint(item); err != nil {
				return nil, err
			}
		}
		defaultFallback := ""
		for _, item := range doc.Models.Configs {
			id, err := lookup.upsert(item, false)
			if err != nil {
				return nil, err
			}
			if defaultFallback == "" {
				defaultFallback = id
			}
		}
		if strings.TrimSpace(doc.Models.DefaultID) != "" {
			lookup.SetDefault(doc.Models.DefaultID)
		} else if strings.TrimSpace(doc.Models.DefaultAlias) != "" {
			lookup.SetDefault(doc.Models.DefaultAlias)
		} else if defaultFallback != "" {
			lookup.SetDefault(defaultFallback)
		}
	}
	cfg = normalizeModelConfig(cfg)
	if cfg.Provider != "" && cfg.Model != "" {
		if _, err := lookup.Upsert(cfg); err != nil {
			return nil, err
		}
	}
	return lookup, nil
}

func (l *modelLookup) DefaultAlias() string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	cfg, ok := l.configs[strings.ToLower(strings.TrimSpace(l.defaultID))]
	if !ok {
		return ""
	}
	return cfg.Alias
}

func (l *modelLookup) DefaultID() string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.defaultID
}

func (l *modelLookup) ListModelAliases() []string {
	choices := l.ListModelChoices()
	aliases := make([]string, 0, len(choices))
	for _, choice := range choices {
		aliases = append(aliases, choice.Alias)
	}
	return dedupeNonEmptyStrings(aliases)
}

func (l *modelLookup) ListModelChoices() []ModelChoice {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	choices := make([]ModelChoice, 0, len(l.configs))
	if l.defaultID != "" {
		if cfg, ok := l.configs[strings.ToLower(l.defaultID)]; ok {
			choices = append(choices, l.modelChoiceLocked(cfg))
		}
	}
	rest := make([]ModelConfig, 0, len(l.configs))
	for id, cfg := range l.configs {
		if strings.EqualFold(id, l.defaultID) {
			continue
		}
		rest = append(rest, cfg)
	}
	sort.Slice(rest, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(rest[i].Alias + " " + rest[i].ID))
		right := strings.ToLower(strings.TrimSpace(rest[j].Alias + " " + rest[j].ID))
		return left < right
	})
	for _, cfg := range rest {
		choices = append(choices, l.modelChoiceLocked(cfg))
	}
	return choices
}

func (l *modelLookup) modelChoiceLocked(cfg ModelConfig) ModelChoice {
	cfg = l.hydrateModelConfigLocked(cfg)
	return ModelChoice{
		ID:                 cfg.ID,
		Alias:              cfg.Alias,
		Provider:           cfg.Provider,
		Model:              cfg.Model,
		ProviderEndpointID: cfg.ProviderEndpointID,
		EndpointID:         cfg.EndpointID,
		BaseURL:            cfg.BaseURL,
		Detail:             modelChoiceDetail(cfg),
	}
}

func (l *modelLookup) ListProviderModels(provider string) []string {
	if l == nil {
		return nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	models := make([]string, 0, len(l.configs))
	for _, cfg := range l.configs {
		if strings.EqualFold(strings.TrimSpace(cfg.Provider), provider) && strings.TrimSpace(cfg.Model) != "" {
			models = append(models, strings.TrimSpace(cfg.Model))
		}
	}
	sort.Strings(models)
	return dedupeNonEmptyStrings(models)
}

func (l *modelLookup) ResolveModel(ctx context.Context, alias string, contextWindow int) (kernelimpl.ModelResolution, error) {
	if l == nil {
		return kernelimpl.ModelResolution{}, fmt.Errorf("gatewayapp: model lookup is nil")
	}
	l.mu.RLock()
	ref := firstNonEmpty(strings.TrimSpace(alias), l.defaultID)
	if ref == "" || len(l.configs) == 0 {
		l.mu.RUnlock()
		return kernelimpl.ModelResolution{}, fmt.Errorf("gatewayapp: no model configured; use /connect")
	}
	cfg, ok, resolveErr := l.resolveConfigLocked(ref)
	fallbackContextWindow := l.contextWindow
	l.mu.RUnlock()
	if resolveErr != nil {
		return kernelimpl.ModelResolution{}, resolveErr
	}
	if !ok {
		return kernelimpl.ModelResolution{}, fmt.Errorf("gatewayapp: unknown model alias %q", alias)
	}
	return resolveModelFromConfig(ctx, cfg, fallbackContextWindow, contextWindow, l.resolveHTTPClient, l.resolveAPIKey)
}

func (l *modelLookup) ResolveModelConfig(ctx context.Context, cfg ModelConfig, contextWindow int) (kernelimpl.ModelResolution, error) {
	if l == nil {
		return kernelimpl.ModelResolution{}, fmt.Errorf("gatewayapp: model lookup is nil")
	}
	l.mu.RLock()
	fallbackContextWindow := l.contextWindow
	l.mu.RUnlock()
	return resolveModelFromConfig(ctx, cfg, fallbackContextWindow, contextWindow, l.resolveHTTPClient, l.resolveAPIKey)
}

func resolveModelFromConfig(
	ctx context.Context,
	cfg ModelConfig,
	fallbackContextWindow int,
	contextWindow int,
	resolveHTTPClient func(context.Context, ModelConfig) (*http.Client, error),
	resolveAPIKey func(context.Context, string) (string, error),
) (kernelimpl.ModelResolution, error) {
	if strings.TrimSpace(cfg.CredentialRef) != "" {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(cfg.CredentialRef)), "apikey:") {
			if resolveAPIKey == nil {
				return kernelimpl.ModelResolution{}, fmt.Errorf("gatewayapp: managed model credential %q is unavailable", cfg.CredentialRef)
			}
			token, err := resolveAPIKey(ctx, cfg.CredentialRef)
			if err != nil {
				return kernelimpl.ModelResolution{}, fmt.Errorf("gatewayapp: resolve model credential %q: %w", cfg.CredentialRef, err)
			}
			cfg.Token = token
			cfg.TokenEnv = ""
			cfg.PersistToken = false
		} else {
			if resolveHTTPClient == nil {
				return kernelimpl.ModelResolution{}, fmt.Errorf("gatewayapp: managed model credential %q is unavailable", cfg.CredentialRef)
			}
			client, err := resolveHTTPClient(ctx, cfg)
			if err != nil {
				return kernelimpl.ModelResolution{}, err
			}
			cfg.HTTPClient = client
		}
	}
	resolved, err := modelconfig.BuildModel(cfg, fallbackContextWindow, contextWindow)
	if err != nil {
		return kernelimpl.ModelResolution{}, err
	}
	return kernelimpl.ModelResolution{
		Model:                  resolved.Model,
		ProfileID:              modelprofile.BuildProviderID(cfg.ID),
		ReasoningEffort:        resolved.ReasoningEffort,
		DefaultReasoningEffort: resolved.DefaultReasoningEffort,
	}, nil
}

func (l *modelLookup) HasAlias(alias string) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok, err := l.resolveConfigLocked(alias)
	return ok || errors.Is(err, errAmbiguousModelAlias)
}

func (l *modelLookup) UpsertProviderEndpoint(endpoint ProviderEndpointConfig) (string, error) {
	if l == nil {
		return "", fmt.Errorf("gatewayapp: model lookup is nil")
	}
	endpoint = normalizeProviderEndpointConfig(endpoint)
	if endpoint.Provider == "" {
		return "", fmt.Errorf("gatewayapp: provider is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.providerEndpoints == nil {
		l.providerEndpoints = map[string]ProviderEndpointConfig{}
	}
	l.providerEndpoints[strings.ToLower(endpoint.ID)] = endpoint
	return endpoint.ID, nil
}

func (l *modelLookup) Upsert(cfg ModelConfig) (string, error) {
	return l.upsert(cfg, true)
}

func (l *modelLookup) upsert(cfg ModelConfig, setDefault bool) (string, error) {
	if l == nil {
		return "", fmt.Errorf("gatewayapp: model lookup is nil")
	}
	rawConfig := cfg
	cfg = normalizeModelConfig(cfg)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.configs == nil {
		l.configs = map[string]ModelConfig{}
	}
	if l.providerEndpoints == nil {
		l.providerEndpoints = map[string]ProviderEndpointConfig{}
	}
	endpoint, ok := l.providerEndpoints[strings.ToLower(strings.TrimSpace(cfg.ProviderEndpointID))]
	if ok {
		endpoint = modelconfig.ApplyConfigProviderEndpointFields(endpoint, rawConfig)
		cfg.Provider = firstNonEmpty(cfg.Provider, endpoint.Provider)
		cfg.EndpointID = firstNonEmpty(cfg.EndpointID, endpoint.EndpointID)
		cfg = normalizeModelConfig(cfg)
	}
	if cfg.Provider == "" || cfg.Model == "" {
		return "", fmt.Errorf("gatewayapp: provider and model are required")
	}
	if !ok {
		endpoint = providerEndpointFromModelConfig(cfg)
	}
	l.providerEndpoints[strings.ToLower(endpoint.ID)] = endpoint
	for id, existing := range l.configs {
		if strings.EqualFold(strings.TrimSpace(existing.ProviderEndpointID), endpoint.ID) {
			l.configs[id] = mergeModelConfigProviderEndpoint(existing, endpoint)
		}
	}
	cfg.ProviderEndpointID = endpoint.ID
	cfg = mergeModelConfigProviderEndpoint(cfg, endpoint)
	l.configs[strings.ToLower(cfg.ID)] = cfg
	if setDefault {
		l.defaultID = cfg.ID
	}
	if cfg.ContextWindowTokens > 0 {
		l.contextWindow = cfg.ContextWindowTokens
	}
	return cfg.ID, nil
}

func (l *modelLookup) Delete(alias string) error {
	if l == nil {
		return fmt.Errorf("gatewayapp: model lookup is nil")
	}
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" {
		return fmt.Errorf("gatewayapp: model alias is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cfg, ok, resolveErr := l.resolveConfigLocked(alias)
	if resolveErr != nil {
		return resolveErr
	}
	if !ok {
		return fmt.Errorf("gatewayapp: unknown model alias %q", alias)
	}
	delete(l.configs, strings.ToLower(cfg.ID))
	if !l.providerEndpointReferencedLocked(cfg.ProviderEndpointID) {
		delete(l.providerEndpoints, strings.ToLower(strings.TrimSpace(cfg.ProviderEndpointID)))
	}
	if strings.EqualFold(l.defaultID, cfg.ID) {
		l.defaultID = ""
		ids := make([]string, 0, len(l.configs))
		for id := range l.configs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) > 0 {
			l.defaultID = l.configs[ids[0]].ID
		}
	}
	return nil
}

func (l *modelLookup) SetDefault(alias string) {
	if l == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if cfg, ok, err := l.resolveConfigLocked(alias); err == nil && ok {
		l.defaultID = cfg.ID
		if cfg.ContextWindowTokens > 0 {
			l.contextWindow = cfg.ContextWindowTokens
		}
	}
}

func (l *modelLookup) Snapshot() persistedModelConfig {
	if l == nil {
		return persistedModelConfig{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	configs := make([]ModelConfig, 0, len(l.configs))
	for _, cfg := range l.configs {
		configs = append(configs, cfg)
	}
	sort.Slice(configs, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(configs[i].Alias+" "+configs[i].ID)) < strings.ToLower(strings.TrimSpace(configs[j].Alias+" "+configs[j].ID))
	})
	endpoints := make([]ProviderEndpointConfig, 0, len(l.providerEndpoints))
	for _, endpoint := range l.providerEndpoints {
		endpoints = append(endpoints, endpoint)
	}
	sort.Slice(endpoints, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(endpoints[i].ID)) < strings.ToLower(strings.TrimSpace(endpoints[j].ID))
	})
	defaultAlias := ""
	if cfg, ok := l.configs[strings.ToLower(strings.TrimSpace(l.defaultID))]; ok {
		defaultAlias = cfg.Alias
	}
	return persistedModelConfig{
		DefaultAlias:      defaultAlias,
		DefaultID:         l.defaultID,
		ProviderEndpoints: endpoints,
		Configs:           configs,
	}
}

func (l *modelLookup) Restore(snapshot persistedModelConfig, contextWindow int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.configs = map[string]ModelConfig{}
	l.providerEndpoints = map[string]ProviderEndpointConfig{}
	for _, endpoint := range snapshot.ProviderEndpoints {
		endpoint = normalizeProviderEndpointConfig(endpoint)
		if endpoint.ID != "" {
			l.providerEndpoints[strings.ToLower(endpoint.ID)] = endpoint
		}
	}
	for _, cfg := range snapshot.Configs {
		cfg = normalizeModelConfig(cfg)
		if cfg.ID != "" {
			l.configs[strings.ToLower(cfg.ID)] = cfg
		}
	}
	l.defaultID = strings.TrimSpace(snapshot.DefaultID)
	l.contextWindow = contextWindow
	if l.defaultID == "" && strings.TrimSpace(snapshot.DefaultAlias) != "" {
		if cfg, ok, err := l.resolveConfigLocked(snapshot.DefaultAlias); err == nil && ok {
			l.defaultID = cfg.ID
		}
	}
}

func (l *modelLookup) Config(alias string) (ModelConfig, bool) {
	if l == nil {
		return ModelConfig{}, false
	}
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" {
		return ModelConfig{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	cfg, ok, err := l.resolveConfigLocked(key)
	if err != nil || !ok {
		return ModelConfig{}, false
	}
	return cfg, true
}

func (l *modelLookup) ResolveConfig(alias string) (ModelConfig, error) {
	if l == nil {
		return ModelConfig{}, fmt.Errorf("gatewayapp: model lookup is nil")
	}
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" {
		return ModelConfig{}, fmt.Errorf("gatewayapp: model alias is required")
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	cfg, ok, err := l.resolveConfigLocked(key)
	if err != nil {
		return ModelConfig{}, err
	}
	if !ok {
		return ModelConfig{}, fmt.Errorf("gatewayapp: unknown model alias %q", alias)
	}
	return cfg, nil
}

func (l *modelLookup) resolveConfigLocked(ref string) (ModelConfig, bool, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" {
		return ModelConfig{}, false, nil
	}
	if cfg, ok := l.configs[ref]; ok {
		return l.hydrateModelConfigLocked(cfg), true, nil
	}
	var match ModelConfig
	matches := 0
	for _, cfg := range l.configs {
		if strings.EqualFold(strings.TrimSpace(cfg.Alias), ref) {
			match = cfg
			matches++
		}
	}
	if matches > 1 {
		return ModelConfig{}, false, fmt.Errorf("gatewayapp: %w %q; use a profile-qualified model id", errAmbiguousModelAlias, ref)
	}
	if matches == 0 {
		return ModelConfig{}, false, nil
	}
	return l.hydrateModelConfigLocked(match), true, nil
}

func (l *modelLookup) providerEndpointReferencedLocked(profileID string) bool {
	profileID = strings.ToLower(strings.TrimSpace(profileID))
	if profileID == "" {
		return false
	}
	for _, cfg := range l.configs {
		if strings.EqualFold(strings.TrimSpace(cfg.ProviderEndpointID), profileID) {
			return true
		}
	}
	return false
}

func (l *modelLookup) hydrateModelConfigLocked(cfg ModelConfig) ModelConfig {
	cfg = normalizeModelConfig(cfg)
	if l == nil || strings.TrimSpace(cfg.ProviderEndpointID) == "" {
		return cfg
	}
	endpoint, ok := l.providerEndpoints[strings.ToLower(strings.TrimSpace(cfg.ProviderEndpointID))]
	if !ok {
		return cfg
	}
	return mergeModelConfigProviderEndpoint(cfg, endpoint)
}

func modelChoiceDetail(cfg ModelConfig) string {
	return modelconfig.ChoiceDetail(cfg)
}

func modelChoiceFromConfig(cfg ModelConfig) ModelChoice {
	return modelconfig.ChoiceFromConfig(cfg)
}

func dedupeModelChoices(choices []ModelChoice) []ModelChoice {
	return modelconfig.DedupeChoices(choices)
}

func normalizeModelConfig(cfg ModelConfig) ModelConfig {
	return modelconfig.NormalizeConfig(cfg)
}

func normalizeProviderEndpointConfig(endpoint ProviderEndpointConfig) ProviderEndpointConfig {
	return modelconfig.NormalizeProviderEndpoint(endpoint)
}

func providerEndpointFromModelConfig(cfg ModelConfig) ProviderEndpointConfig {
	return modelconfig.ProviderEndpointFromConfig(cfg)
}

func mergeModelConfigProviderEndpoint(cfg ModelConfig, endpoint ProviderEndpointConfig) ModelConfig {
	return modelconfig.MergeConfigProviderEndpoint(cfg, endpoint)
}

func modelConfigSupportsReasoningEffort(cfg ModelConfig, effort string) bool {
	return modelconfig.SupportsReasoningEffort(cfg, effort)
}
