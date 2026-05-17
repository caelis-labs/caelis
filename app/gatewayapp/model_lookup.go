package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/modelregistry"
	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/kernel"
)

var errAmbiguousModelAlias = errors.New("ambiguous model alias")

type modelLookup struct {
	mu            sync.RWMutex
	configs       map[string]ModelConfig
	profiles      map[string]ModelProfileConfig
	contextWindow int
	defaultID     string
}

func newModelLookup(store *appConfigStore, cfg ModelConfig, contextWindow int) (*modelLookup, error) {
	lookup := &modelLookup{
		configs:       map[string]ModelConfig{},
		profiles:      map[string]ModelProfileConfig{},
		contextWindow: contextWindow,
	}
	if store != nil {
		doc, err := store.Load()
		if err != nil {
			return nil, err
		}
		for _, item := range doc.Models.Profiles {
			if _, err := lookup.UpsertProfile(item); err != nil {
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
		ID:         cfg.ID,
		Alias:      cfg.Alias,
		Provider:   cfg.Provider,
		Model:      cfg.Model,
		ProfileID:  cfg.ProfileID,
		EndpointID: cfg.EndpointID,
		BaseURL:    cfg.BaseURL,
		Detail:     modelChoiceDetail(cfg),
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

func (l *modelLookup) ResolveModel(ctx context.Context, alias string, contextWindow int) (kernel.ModelResolution, error) {
	if l == nil {
		return kernel.ModelResolution{}, fmt.Errorf("gatewayapp: model lookup is nil")
	}
	l.mu.RLock()
	ref := firstNonEmpty(strings.TrimSpace(alias), l.defaultID)
	if ref == "" || len(l.configs) == 0 {
		l.mu.RUnlock()
		return kernel.ModelResolution{}, fmt.Errorf("gatewayapp: no model configured; use /connect")
	}
	cfg, ok, resolveErr := l.resolveConfigLocked(ref)
	fallbackContextWindow := l.contextWindow
	l.mu.RUnlock()
	if resolveErr != nil {
		return kernel.ModelResolution{}, resolveErr
	}
	if !ok {
		return kernel.ModelResolution{}, fmt.Errorf("gatewayapp: unknown model alias %q", alias)
	}
	effectiveContextWindow := fallbackContextWindow
	if cfg.ContextWindowTokens > 0 {
		effectiveContextWindow = cfg.ContextWindowTokens
	}
	if contextWindow > 0 {
		effectiveContextWindow = contextWindow
	}
	factory := providers.NewFactory()
	record := providers.Config{
		Alias:                     cfg.ID,
		Provider:                  cfg.Provider,
		API:                       cfg.API,
		Model:                     cfg.Model,
		BaseURL:                   cfg.BaseURL,
		HTTPClient:                cfg.HTTPClient,
		Timeout:                   cfg.Timeout,
		MaxOutputTok:              cfg.MaxOutputTok,
		ContextWindowTokens:       effectiveContextWindow,
		ReasoningLevels:           append([]string(nil), cfg.ReasoningLevels...),
		ReasoningMode:             cfg.ReasoningMode,
		DefaultReasoningEffort:    cfg.DefaultReasoningEffort,
		ReasoningEffort:           cfg.ReasoningEffort,
		SupportedReasoningEfforts: append([]string(nil), cfg.ReasoningLevels...),
		Auth: providers.AuthConfig{
			Type:      cfg.AuthType,
			Token:     cfg.Token,
			TokenEnv:  cfg.TokenEnv,
			HeaderKey: cfg.HeaderKey,
		},
	}
	if err := factory.Register(record); err != nil {
		return kernel.ModelResolution{}, err
	}
	llm, err := factory.NewByAlias(cfg.ID)
	if err != nil {
		return kernel.ModelResolution{}, err
	}
	return kernel.ModelResolution{
		Model:                  llm,
		ReasoningEffort:        cfg.ReasoningEffort,
		DefaultReasoningEffort: cfg.DefaultReasoningEffort,
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

func (l *modelLookup) UpsertProfile(profile ModelProfileConfig) (string, error) {
	if l == nil {
		return "", fmt.Errorf("gatewayapp: model lookup is nil")
	}
	profile = normalizeModelProfileConfig(profile)
	if profile.Provider == "" {
		return "", fmt.Errorf("gatewayapp: provider is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.profiles == nil {
		l.profiles = map[string]ModelProfileConfig{}
	}
	l.profiles[strings.ToLower(profile.ID)] = profile
	return profile.ID, nil
}

func (l *modelLookup) Upsert(cfg ModelConfig) (string, error) {
	return l.upsert(cfg, true)
}

func (l *modelLookup) upsert(cfg ModelConfig, setDefault bool) (string, error) {
	if l == nil {
		return "", fmt.Errorf("gatewayapp: model lookup is nil")
	}
	updatesProfileAuth := modelConfigCarriesProfileAuth(cfg)
	cfg = normalizeModelConfig(cfg)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.configs == nil {
		l.configs = map[string]ModelConfig{}
	}
	if l.profiles == nil {
		l.profiles = map[string]ModelProfileConfig{}
	}
	profile, ok := l.profiles[strings.ToLower(strings.TrimSpace(cfg.ProfileID))]
	if ok {
		cfg.Provider = firstNonEmpty(cfg.Provider, profile.Provider)
		cfg.EndpointID = firstNonEmpty(cfg.EndpointID, profile.EndpointID)
		cfg = normalizeModelConfig(cfg)
	}
	if cfg.Provider == "" || cfg.Model == "" {
		return "", fmt.Errorf("gatewayapp: provider and model are required")
	}
	if !ok || updatesProfileAuth {
		profile = modelProfileFromModelConfig(cfg)
	}
	l.profiles[strings.ToLower(profile.ID)] = profile
	cfg.ProfileID = profile.ID
	cfg = mergeModelConfigProfile(cfg, profile)
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
	if !l.profileReferencedLocked(cfg.ProfileID) {
		delete(l.profiles, strings.ToLower(strings.TrimSpace(cfg.ProfileID)))
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
	profiles := make([]ModelProfileConfig, 0, len(l.profiles))
	for _, profile := range l.profiles {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(profiles[i].ID)) < strings.ToLower(strings.TrimSpace(profiles[j].ID))
	})
	defaultAlias := ""
	if cfg, ok := l.configs[strings.ToLower(strings.TrimSpace(l.defaultID))]; ok {
		defaultAlias = cfg.Alias
	}
	return persistedModelConfig{
		DefaultAlias: defaultAlias,
		DefaultID:    l.defaultID,
		Profiles:     profiles,
		Configs:      configs,
	}
}

func (l *modelLookup) Restore(snapshot persistedModelConfig, contextWindow int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.configs = map[string]ModelConfig{}
	l.profiles = map[string]ModelProfileConfig{}
	for _, profile := range snapshot.Profiles {
		profile = normalizeModelProfileConfig(profile)
		if profile.ID != "" {
			l.profiles[strings.ToLower(profile.ID)] = profile
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

func (l *modelLookup) profileReferencedLocked(profileID string) bool {
	profileID = strings.ToLower(strings.TrimSpace(profileID))
	if profileID == "" {
		return false
	}
	for _, cfg := range l.configs {
		if strings.EqualFold(strings.TrimSpace(cfg.ProfileID), profileID) {
			return true
		}
	}
	return false
}

func (l *modelLookup) hydrateModelConfigLocked(cfg ModelConfig) ModelConfig {
	cfg = normalizeModelConfig(cfg)
	if l == nil || strings.TrimSpace(cfg.ProfileID) == "" {
		return cfg
	}
	profile, ok := l.profiles[strings.ToLower(strings.TrimSpace(cfg.ProfileID))]
	if !ok {
		return cfg
	}
	return mergeModelConfigProfile(cfg, profile)
}

func modelChoiceDetail(cfg ModelConfig) string {
	return modelregistry.ChoiceDetail(cfg)
}

func modelChoiceFromConfig(cfg ModelConfig) ModelChoice {
	return modelregistry.ChoiceFromConfig(cfg)
}

func dedupeModelChoices(choices []ModelChoice) []ModelChoice {
	return modelregistry.DedupeChoices(choices)
}

func normalizeModelConfig(cfg ModelConfig) ModelConfig {
	return modelregistry.NormalizeConfig(cfg)
}

func normalizeModelProfileConfig(profile ModelProfileConfig) ModelProfileConfig {
	return modelregistry.NormalizeProfileConfig(profile)
}

func modelProfileFromModelConfig(cfg ModelConfig) ModelProfileConfig {
	return modelregistry.ProfileFromConfig(cfg)
}

func modelConfigCarriesProfileFields(cfg ModelConfig) bool {
	return modelregistry.ConfigCarriesProfileFields(cfg)
}

func modelConfigCarriesProfileAuth(cfg ModelConfig) bool {
	return modelregistry.ConfigCarriesProfileAuth(cfg)
}

func mergeModelConfigProfile(cfg ModelConfig, profile ModelProfileConfig) ModelConfig {
	return modelregistry.MergeConfigProfile(cfg, profile)
}

func modelConfigSupportsReasoningEffort(cfg ModelConfig, effort string) bool {
	return modelregistry.SupportsReasoningEffort(cfg, effort)
}

func defaultModelAPIForProvider(provider string) providers.APIType {
	return modelregistry.DefaultAPIForProvider(provider)
}

func sanitizePersistedModelConfig(cfg ModelConfig) ModelConfig {
	return modelregistry.SanitizePersistedConfig(cfg)
}

func sanitizePersistedModelProfile(profile ModelProfileConfig) ModelProfileConfig {
	return modelregistry.SanitizePersistedProfile(profile)
}

func defaultAuthTypeForProvider(provider string) providers.AuthType {
	return modelregistry.DefaultAuthTypeForProvider(provider)
}

func buildAlias(provider string, modelName string) string {
	return modelregistry.BuildAlias(provider, modelName)
}

func buildProfileID(provider string, endpointID string, baseURL string) string {
	return modelregistry.BuildProfileID(provider, endpointID, baseURL)
}

func buildModelID(profileID string, alias string) string {
	return modelregistry.BuildModelID(profileID, alias)
}

func normalizeEndpointID(provider string, endpointID string, baseURL string, api providers.APIType) string {
	return modelregistry.NormalizeEndpointID(provider, endpointID, baseURL, api)
}

func firstNonEmptyAPI(values ...providers.APIType) providers.APIType {
	return modelregistry.FirstNonEmptyAPI(values...)
}

func firstNonEmptyAuthType(values ...providers.AuthType) providers.AuthType {
	return modelregistry.FirstNonEmptyAuthType(values...)
}
