package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strings"
	"sync"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/internal/app/modelcatalog"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

const (
	ReasoningModeNone   = modelcatalog.ReasoningModeNone
	ReasoningModeToggle = modelcatalog.ReasoningModeToggle
	ReasoningModeEffort = modelcatalog.ReasoningModeEffort
	ReasoningModeFixed  = modelcatalog.ReasoningModeFixed
)

type ModelCapabilityInfo = modelcatalog.CapabilityInfo

type ModelSelectionRequest struct {
	SessionRef session.Ref             `json:"session_ref,omitempty"`
	Provider   string                  `json:"provider,omitempty"`
	Discovery  appsettings.ModelConfig `json:"discovery,omitempty"`
}

func (s ModelService) ListCatalogModels(provider string) []string {
	return modelcatalog.Models(provider)
}

func (s ModelService) ConfiguredProviderModels(ctx context.Context, provider string) ([]string, error) {
	choices, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	provider = normalizeModelCatalogKey(provider)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(choices))
	for _, choice := range choices {
		if normalizeModelCatalogKey(choice.Provider) != provider {
			continue
		}
		model := strings.TrimSpace(choice.Model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out, nil
}

func (s ModelService) ProviderModels(ctx context.Context, cfg appsettings.ModelConfig) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = appsettings.NormalizeModelConfig(cfg)
	var out []string
	providerKey := normalizeModelCatalogKey(cfg.Provider)
	if providerKey != "" {
		configured, err := s.ConfiguredProviderModels(ctx, providerKey)
		if err != nil {
			return nil, err
		}
		out = append(out, configured...)
	}
	if providerKey == "" || s.services.modelProvider == nil {
		return uniqueSortedModelNames(out), nil
	}
	remote, err := s.ProviderModelInfos(ctx, cfg)
	if err != nil {
		return uniqueSortedModelNames(out), err
	}
	out = append(out, modelInfoNames(remote)...)
	return uniqueSortedModelNames(out), nil
}

func (s ModelService) ProviderModelInfos(ctx context.Context, cfg appsettings.ModelConfig) ([]coremodel.ModelInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = appsettings.NormalizeModelConfig(cfg)
	if normalizeModelCatalogKey(cfg.Provider) == "" || s.services.modelProvider == nil {
		return nil, nil
	}
	cache := s.services.modelCache
	key := modelDiscoveryCacheKey(cfg)
	if cache != nil {
		if cached, ok := cache.Get(key); ok {
			return cached, nil
		}
	}
	provider, err := s.services.modelProvider(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, nil
	}
	remote, err := provider.Models(ctx)
	if err != nil {
		return nil, err
	}
	remote = normalizeModelInfos(cfg.Provider, remote)
	if cache != nil {
		cache.Put(key, remote)
	}
	return cloneModelInfos(remote), nil
}

func (s ModelService) DefaultCapabilities() ModelCapabilityInfo {
	return modelcatalog.DefaultCapabilities()
}

func (s ModelService) LookupCapabilities(provider string, modelName string) (ModelCapabilityInfo, bool) {
	provider = normalizeModelCatalogKey(provider)
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if caps, ok := modelcatalog.LookupCapabilities(provider, modelName); ok {
		return caps, true
	}
	if caps, ok := s.lookupCachedCapabilities(provider, modelName); ok {
		return caps, true
	}
	return ModelCapabilityInfo{}, false
}

func (s ModelService) ReasoningLevels(provider string, modelName string) []string {
	caps, ok := s.LookupCapabilities(provider, modelName)
	if !ok {
		return nil
	}
	return reasoningLevelsFromCapabilities(caps)
}

func (s ModelService) PromptCapabilities(ctx context.Context) (appviewmodel.PromptCapabilitiesView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.services.settings == nil {
		return appviewmodel.PromptCapabilitiesView{Image: true}, nil
	}
	choices, err := s.List(ctx)
	if err != nil {
		return appviewmodel.PromptCapabilitiesView{}, err
	}
	if len(choices) == 0 {
		return appviewmodel.PromptCapabilitiesView{}, nil
	}
	for _, choice := range choices {
		cfg, err := s.services.settings.ResolveModel(choice.ID)
		if err != nil {
			return appviewmodel.PromptCapabilitiesView{}, err
		}
		caps, ok := s.LookupCapabilities(cfg.Provider, cfg.Model)
		if ok && caps.SupportsImages {
			return appviewmodel.PromptCapabilitiesView{Image: true}, nil
		}
	}
	return appviewmodel.PromptCapabilitiesView{}, nil
}

func (s ModelService) Selection(ctx context.Context, req ModelSelectionRequest) (appviewmodel.ModelSelectionView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	choices, err := s.List(ctx)
	if err != nil {
		return appviewmodel.ModelSelectionView{}, err
	}
	view := appviewmodel.ModelSelectionView{
		Configured:    statusModelChoices(choices),
		RemoteEnabled: s.services.modelProvider != nil,
	}
	if len(choices) > 0 {
		current, ok, err := s.Current(ctx, req.SessionRef)
		if err != nil {
			return appviewmodel.ModelSelectionView{}, err
		}
		if ok {
			choice := statusModelChoice(appsettings.ModelChoiceFromConfig(current, modelChoiceIsDefault(choices, current.ID)))
			view.Current = &choice
		}
	}
	provider := firstNonEmpty(req.Provider, req.Discovery.Provider)
	provider = normalizeModelCatalogKey(provider)
	view.Provider = provider
	view.Providers = s.modelProviderOptions(choices)
	if provider == "" {
		return view, nil
	}
	candidates := map[string]appviewmodel.ModelCandidate{}
	addModelCandidates(candidates, provider, s.ListCatalogModels(provider), func(candidate *appviewmodel.ModelCandidate) {
		candidate.Catalog = true
	})
	configured, err := s.ConfiguredProviderModels(ctx, provider)
	if err != nil {
		return appviewmodel.ModelSelectionView{}, err
	}
	addModelCandidates(candidates, provider, configured, func(candidate *appviewmodel.ModelCandidate) {
		candidate.Configured = true
	})
	discovery := req.Discovery
	discovery.Provider = firstNonEmpty(discovery.Provider, provider)
	remote, err := s.ProviderModelInfos(ctx, discovery)
	if err != nil {
		view.DiscoveryErr = err.Error()
	}
	addModelInfoCandidates(candidates, provider, remote, func(candidate *appviewmodel.ModelCandidate, info coremodel.ModelInfo) {
		if !candidate.Configured {
			candidate.Remote = true
		}
		if caps, ok := modelInfoCapabilities(info); ok {
			candidate.CapabilitiesKnown = true
			candidate.Capabilities = modelCapabilityView(caps)
			candidate.ReasoningLevels = reasoningLevelsFromCapabilities(caps)
		}
	})
	view.Candidates = s.sortedModelCandidates(candidates)
	return view, nil
}

func (s ModelService) modelProviderOptions(choices []appsettings.ModelChoice) []appviewmodel.ModelProviderOption {
	providers := map[string]appviewmodel.ModelProviderOption{}
	for _, provider := range modelcatalog.Providers() {
		key := normalizeModelCatalogKey(provider.ID)
		providers[key] = appviewmodel.ModelProviderOption{
			ID:                key,
			Name:              key,
			Builtin:           true,
			RemoteDiscovery:   s.services.modelProvider != nil,
			CatalogModelCount: provider.ModelCount,
		}
	}
	for _, alias := range s.services.resources.ModelProviders {
		key := normalizeModelCatalogKey(alias.Name)
		if key == "" {
			continue
		}
		option := providers[key]
		option.ID = key
		option.Name = key
		option.Description = strings.TrimSpace(alias.Description)
		option.Uses = strings.TrimSpace(alias.Uses)
		option.Plugin = true
		option.RemoteDiscovery = s.services.modelProvider != nil
		providers[key] = option
	}
	for _, choice := range choices {
		key := normalizeModelCatalogKey(choice.Provider)
		if key == "" {
			continue
		}
		option := providers[key]
		option.ID = key
		option.Name = key
		option.Configured = true
		option.RemoteDiscovery = s.services.modelProvider != nil
		option.ConfiguredModelCount++
		option.CatalogModelCount = modelcatalog.ModelCount(key)
		providers[key] = option
	}
	out := make([]appviewmodel.ModelProviderOption, 0, len(providers))
	for _, option := range providers {
		out = append(out, option)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
	})
	return out
}

func addModelCandidates(candidates map[string]appviewmodel.ModelCandidate, provider string, models []string, update func(*appviewmodel.ModelCandidate)) {
	for _, modelName := range models {
		modelName = strings.TrimSpace(modelName)
		if modelName == "" {
			continue
		}
		key := strings.ToLower(modelName)
		candidate := candidates[key]
		candidate.Provider = provider
		candidate.Model = modelName
		update(&candidate)
		candidates[key] = candidate
	}
}

func addModelInfoCandidates(candidates map[string]appviewmodel.ModelCandidate, provider string, models []coremodel.ModelInfo, update func(*appviewmodel.ModelCandidate, coremodel.ModelInfo)) {
	for _, info := range models {
		modelName := firstNonEmpty(info.ID, info.Name)
		modelName = strings.TrimSpace(modelName)
		if modelName == "" {
			continue
		}
		key := strings.ToLower(modelName)
		candidate := candidates[key]
		candidate.Provider = firstNonEmpty(normalizeModelCatalogKey(info.Provider), provider)
		candidate.Model = modelName
		update(&candidate, info)
		candidates[key] = candidate
	}
}

func (s ModelService) sortedModelCandidates(candidates map[string]appviewmodel.ModelCandidate) []appviewmodel.ModelCandidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]appviewmodel.ModelCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.CapabilitiesKnown {
			if len(candidate.ReasoningLevels) == 0 {
				candidate.ReasoningLevels = reasoningLevelsFromCapabilityView(candidate.Capabilities)
			}
		} else if caps, ok := s.LookupCapabilities(candidate.Provider, candidate.Model); ok {
			candidate.CapabilitiesKnown = true
			candidate.Capabilities = modelCapabilityView(caps)
			candidate.ReasoningLevels = reasoningLevelsFromCapabilities(caps)
		} else {
			candidate.Capabilities = modelCapabilityView(s.DefaultCapabilities())
		}
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		left := candidateRank(out[i])
		right := candidateRank(out[j])
		if left != right {
			return left < right
		}
		return strings.ToLower(out[i].Model) < strings.ToLower(out[j].Model)
	})
	return out
}

func candidateRank(candidate appviewmodel.ModelCandidate) int {
	if candidate.Configured {
		return 0
	}
	if candidate.Catalog {
		return 1
	}
	if candidate.Remote {
		return 2
	}
	return 3
}

func modelCapabilityView(in ModelCapabilityInfo) appviewmodel.ModelCapability {
	in = normalizeModelCapabilityInfo(in)
	return appviewmodel.ModelCapability{
		ContextWindowTokens:    in.ContextWindowTokens,
		MaxOutputTokens:        in.MaxOutputTokens,
		DefaultMaxOutputTokens: in.DefaultMaxOutputTokens,
		SupportsImages:         in.SupportsImages,
		SupportsToolCalls:      in.SupportsToolCalls,
		SupportsReasoning:      in.SupportsReasoning,
		ReasoningMode:          in.ReasoningMode,
		ReasoningEfforts:       slices.Clone(in.ReasoningEfforts),
		DefaultReasoningEffort: in.DefaultReasoningEffort,
		SupportsJSONOutput:     in.SupportsJSONOutput,
	}
}

func normalizeModelCatalogKey(value string) string {
	return modelcatalog.NormalizeKey(value)
}

func modelInfoNames(models []coremodel.ModelInfo) []string {
	if len(models) == 0 {
		return nil
	}
	out := make([]string, 0, len(models))
	for _, item := range models {
		name := firstNonEmpty(item.ID, item.Name)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func normalizeModelInfos(provider string, models []coremodel.ModelInfo) []coremodel.ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]coremodel.ModelInfo, 0, len(models))
	for _, item := range models {
		item.ID = strings.TrimSpace(item.ID)
		item.Name = strings.TrimSpace(item.Name)
		item.Provider = firstNonEmpty(normalizeModelCatalogKey(item.Provider), normalizeModelCatalogKey(provider))
		item.DefaultReasoningEffort = strings.ToLower(strings.TrimSpace(item.DefaultReasoningEffort))
		item.ReasoningEfforts = normalizeReasoningEfforts(item.ReasoningEfforts)
		if item.ID == "" && item.Name == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func cloneModelInfos(models []coremodel.ModelInfo) []coremodel.ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := slices.Clone(models)
	for i := range out {
		out[i].ReasoningEfforts = slices.Clone(models[i].ReasoningEfforts)
	}
	return out
}

func modelInfoCapabilities(info coremodel.ModelInfo) (ModelCapabilityInfo, bool) {
	return modelcatalog.CapabilitiesFromModelInfo(info)
}

func uniqueSortedModelNames(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

func normalizeModelCapabilityInfo(in ModelCapabilityInfo) ModelCapabilityInfo {
	return modelcatalog.NormalizeCapabilities(in)
}

func reasoningLevelsFromCapabilities(caps ModelCapabilityInfo) []string {
	return modelcatalog.ReasoningLevelsFromCapabilities(caps)
}

func reasoningLevelsFromCapabilityView(caps appviewmodel.ModelCapability) []string {
	return reasoningLevelsFromCapabilities(ModelCapabilityInfo{
		ContextWindowTokens:    caps.ContextWindowTokens,
		MaxOutputTokens:        caps.MaxOutputTokens,
		DefaultMaxOutputTokens: caps.DefaultMaxOutputTokens,
		SupportsImages:         caps.SupportsImages,
		SupportsToolCalls:      caps.SupportsToolCalls,
		SupportsReasoning:      caps.SupportsReasoning,
		ReasoningMode:          caps.ReasoningMode,
		ReasoningEfforts:       slices.Clone(caps.ReasoningEfforts),
		DefaultReasoningEffort: caps.DefaultReasoningEffort,
		SupportsJSONOutput:     caps.SupportsJSONOutput,
	})
}

func normalizeReasoningEfforts(levels []string) []string {
	return modelcatalog.NormalizeReasoningEfforts(levels)
}

func (s ModelService) lookupCachedCapabilities(provider string, modelName string) (ModelCapabilityInfo, bool) {
	cache := s.services.modelCache
	if cache == nil {
		return ModelCapabilityInfo{}, false
	}
	info, ok := cache.Find(provider, modelName)
	if !ok {
		return ModelCapabilityInfo{}, false
	}
	return modelInfoCapabilities(info)
}

type modelDiscoveryCache struct {
	mu      sync.RWMutex
	entries map[string][]coremodel.ModelInfo
}

func newModelDiscoveryCache() *modelDiscoveryCache {
	return &modelDiscoveryCache{entries: map[string][]coremodel.ModelInfo{}}
}

func (c *modelDiscoveryCache) Get(key string) ([]coremodel.ModelInfo, bool) {
	if c == nil || strings.TrimSpace(key) == "" {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	models, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return cloneModelInfos(models), true
}

func (c *modelDiscoveryCache) Put(key string, models []coremodel.ModelInfo) {
	if c == nil || strings.TrimSpace(key) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string][]coremodel.ModelInfo{}
	}
	c.entries[key] = cloneModelInfos(models)
}

func (c *modelDiscoveryCache) Find(provider string, modelName string) (coremodel.ModelInfo, bool) {
	if c == nil {
		return coremodel.ModelInfo{}, false
	}
	provider = normalizeModelCatalogKey(provider)
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if provider == "" || modelName == "" {
		return coremodel.ModelInfo{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, models := range c.entries {
		for _, info := range models {
			if normalizeModelCatalogKey(info.Provider) != provider {
				continue
			}
			if strings.ToLower(firstNonEmpty(info.ID, info.Name)) != modelName {
				continue
			}
			return cloneModelInfo(info), true
		}
	}
	return coremodel.ModelInfo{}, false
}

func cloneModelInfo(info coremodel.ModelInfo) coremodel.ModelInfo {
	info.ReasoningEfforts = slices.Clone(info.ReasoningEfforts)
	return info
}

func modelDiscoveryCacheKey(cfg appsettings.ModelConfig) string {
	cfg = appsettings.NormalizeModelConfig(cfg)
	tokenHash := ""
	if cfg.Token != "" {
		sum := sha256.Sum256([]byte(cfg.Token))
		tokenHash = hex.EncodeToString(sum[:])
	}
	parts := []string{
		normalizeModelCatalogKey(cfg.Provider),
		strings.TrimSpace(cfg.EndpointID),
		strings.TrimSpace(cfg.BaseURL),
		strings.TrimSpace(cfg.AuthType),
		strings.TrimSpace(cfg.HeaderKey),
		strings.TrimSpace(cfg.TokenEnv),
		tokenHash,
	}
	return strings.Join(parts, "\x00")
}
