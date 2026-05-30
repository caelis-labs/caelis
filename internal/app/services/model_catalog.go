package services

import (
	"context"
	"slices"
	"sort"
	"strings"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

const (
	ReasoningModeNone   = "none"
	ReasoningModeToggle = "toggle"
	ReasoningModeEffort = "effort"
	ReasoningModeFixed  = "fixed"
)

type ModelCapabilityInfo struct {
	ContextWindowTokens    int      `json:"context_window_tokens,omitempty"`
	MaxOutputTokens        int      `json:"max_output_tokens,omitempty"`
	DefaultMaxOutputTokens int      `json:"default_max_output_tokens,omitempty"`
	SupportsImages         bool     `json:"supports_images,omitempty"`
	SupportsToolCalls      bool     `json:"supports_tool_calls,omitempty"`
	SupportsReasoning      bool     `json:"supports_reasoning,omitempty"`
	ReasoningMode          string   `json:"reasoning_mode,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	SupportsJSONOutput     bool     `json:"supports_json_output,omitempty"`
}

type modelCatalogEntry struct {
	provider string
	pattern  string
	caps     ModelCapabilityInfo
}

type ModelSelectionRequest struct {
	SessionRef session.Ref             `json:"session_ref,omitempty"`
	Provider   string                  `json:"provider,omitempty"`
	Discovery  appsettings.ModelConfig `json:"discovery,omitempty"`
}

var catalogModels = map[string][]string{
	"anthropic":              {"claude-sonnet-4-20250514", "claude-opus-4-20250514"},
	"anthropic-compatible":   {"claude-sonnet-4-20250514", "claude-opus-4-20250514"},
	"codefree":               {"DeepSeek-V3.1-Terminus", "GLM-4.7", "GLM-5.1", "Qwen3.5-122B-A10B"},
	"deepseek":               {"deepseek-v4-flash", "deepseek-v4-pro"},
	"gemini":                 {"gemini-2.5-flash", "gemini-2.5-pro"},
	"minimax":                {"MiniMax-M2", "MiniMax-M2.1", "MiniMax-M2.1-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed", "MiniMax-M2.7", "MiniMax-M2.7-highspeed"},
	"ollama":                 {"deepseek-r1:7b", "gemma3:4b", "llama3.1:8b", "qwen2.5:7b"},
	"openai":                 {"gpt-4o", "gpt-4o-mini", "o3", "o4-mini"},
	"openai-compatible":      {"gpt-4o", "gpt-4o-mini", "o3", "o4-mini"},
	"openrouter":             {"anthropic/claude-sonnet-4", "google/gemini-2.5-flash", "openai/gpt-4o-mini"},
	"volcengine":             {"doubao-seed-2.0-code", "doubao-seed-2.0-pro"},
	"volcengine-coding-plan": {"doubao-seed-2.0-code"},
	"xiaomi":                 {"mimo-v2-flash", "mimo-v2-omni", "mimo-v2-pro", "mimo-v2.5", "mimo-v2.5-pro"},
}

var builtinCapabilities = []modelCatalogEntry{
	{provider: "deepseek", pattern: "deepseek-v4", caps: ModelCapabilityInfo{ContextWindowTokens: 1048576, MaxOutputTokens: 393216, DefaultMaxOutputTokens: 32768, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, ReasoningEfforts: []string{"high", "max"}, DefaultReasoningEffort: "high", SupportsJSONOutput: true}},
	{provider: "gemini", pattern: "gemini-2.5", caps: ModelCapabilityInfo{ContextWindowTokens: 1048576, MaxOutputTokens: 65536, DefaultMaxOutputTokens: 8192, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, SupportsJSONOutput: true}},
	{provider: "minimax", pattern: "MiniMax-M2.7", caps: ModelCapabilityInfo{ContextWindowTokens: 204800, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeFixed, SupportsJSONOutput: true}},
	{provider: "minimax", pattern: "MiniMax-M2.5", caps: ModelCapabilityInfo{ContextWindowTokens: 204800, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeFixed, SupportsJSONOutput: true}},
	{provider: "minimax", pattern: "MiniMax-M2.1", caps: ModelCapabilityInfo{ContextWindowTokens: 204800, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeFixed, SupportsJSONOutput: true}},
	{provider: "minimax", pattern: "MiniMax-M2", caps: ModelCapabilityInfo{ContextWindowTokens: 204800, MaxOutputTokens: 8192, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeFixed, SupportsJSONOutput: true}},
	{provider: "openai", pattern: "o", caps: ModelCapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 100000, DefaultMaxOutputTokens: 32768, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, DefaultReasoningEffort: "medium", SupportsJSONOutput: true}},
	{provider: "openai-compatible", pattern: "o", caps: ModelCapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 100000, DefaultMaxOutputTokens: 32768, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, DefaultReasoningEffort: "medium", SupportsJSONOutput: true}},
	{provider: "openai", pattern: "gpt-4o", caps: ModelCapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 16384, DefaultMaxOutputTokens: 4096, SupportsImages: true, SupportsToolCalls: true, SupportsJSONOutput: true}},
	{provider: "openai-compatible", pattern: "gpt-4o", caps: ModelCapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 16384, DefaultMaxOutputTokens: 4096, SupportsImages: true, SupportsToolCalls: true, SupportsJSONOutput: true}},
	{provider: "anthropic", pattern: "claude", caps: ModelCapabilityInfo{ContextWindowTokens: 200000, MaxOutputTokens: 32000, DefaultMaxOutputTokens: 4096, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, SupportsJSONOutput: true}},
	{provider: "anthropic-compatible", pattern: "claude", caps: ModelCapabilityInfo{ContextWindowTokens: 200000, MaxOutputTokens: 32000, DefaultMaxOutputTokens: 4096, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, SupportsJSONOutput: true}},
	{provider: "codefree", pattern: "", caps: ModelCapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 8000, DefaultMaxOutputTokens: 8000, SupportsToolCalls: true, SupportsJSONOutput: true}},
	{provider: "openrouter", pattern: "", caps: ModelCapabilityInfo{ContextWindowTokens: 262144, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, SupportsJSONOutput: true}},
	{provider: "volcengine", pattern: "doubao-seed-2.0", caps: ModelCapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "volcengine-coding-plan", pattern: "doubao-seed-2.0-code", caps: ModelCapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsJSONOutput: true}},
	{provider: "xiaomi", pattern: "mimo-v2.5-pro", caps: ModelCapabilityInfo{ContextWindowTokens: 1048576, MaxOutputTokens: 131072, DefaultMaxOutputTokens: 32768, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "xiaomi", pattern: "mimo-v2-pro", caps: ModelCapabilityInfo{ContextWindowTokens: 1048576, MaxOutputTokens: 131072, DefaultMaxOutputTokens: 32768, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "xiaomi", pattern: "mimo-v2.5", caps: ModelCapabilityInfo{ContextWindowTokens: 1048576, MaxOutputTokens: 131072, DefaultMaxOutputTokens: 32768, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "xiaomi", pattern: "mimo-v2-omni", caps: ModelCapabilityInfo{ContextWindowTokens: 262144, MaxOutputTokens: 131072, DefaultMaxOutputTokens: 32768, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "xiaomi", pattern: "mimo-v2-flash", caps: ModelCapabilityInfo{ContextWindowTokens: 262144, MaxOutputTokens: 65536, DefaultMaxOutputTokens: 16384, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "ollama", pattern: "", caps: ModelCapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 8192, DefaultMaxOutputTokens: 4096, SupportsToolCalls: true, SupportsJSONOutput: true}},
}

func (s ModelService) ListCatalogModels(provider string) []string {
	models := catalogModels[normalizeModelCatalogKey(provider)]
	return slices.Clone(models)
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
	provider, err := s.services.modelProvider(ctx, cfg)
	if err != nil {
		return uniqueSortedModelNames(out), err
	}
	if provider == nil {
		return uniqueSortedModelNames(out), nil
	}
	remote, err := provider.Models(ctx)
	if err != nil {
		return uniqueSortedModelNames(out), err
	}
	out = append(out, modelInfoNames(remote)...)
	return uniqueSortedModelNames(out), nil
}

func (s ModelService) DefaultCapabilities() ModelCapabilityInfo {
	return ModelCapabilityInfo{
		ContextWindowTokens:    128000,
		MaxOutputTokens:        32768,
		DefaultMaxOutputTokens: 4096,
		SupportsToolCalls:      true,
		ReasoningMode:          ReasoningModeNone,
		SupportsJSONOutput:     true,
	}
}

func (s ModelService) LookupCapabilities(provider string, modelName string) (ModelCapabilityInfo, bool) {
	provider = normalizeModelCatalogKey(provider)
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	for _, entry := range builtinCapabilities {
		if normalizeModelCatalogKey(entry.provider) != provider {
			continue
		}
		pattern := strings.ToLower(strings.TrimSpace(entry.pattern))
		if pattern == "" || modelName == pattern || strings.HasPrefix(modelName, pattern) {
			return normalizeModelCapabilityInfo(entry.caps), true
		}
	}
	return ModelCapabilityInfo{}, false
}

func (s ModelService) ReasoningLevels(provider string, modelName string) []string {
	caps, ok := s.LookupCapabilities(provider, modelName)
	if !ok || !caps.SupportsReasoning {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(caps.ReasoningMode)) {
	case ReasoningModeFixed, ReasoningModeNone:
		return nil
	}
	levels := normalizeReasoningEfforts(caps.ReasoningEfforts)
	if len(levels) == 0 {
		return []string{"none"}
	}
	out := make([]string, 0, len(levels)+1)
	out = append(out, "none")
	out = append(out, levels...)
	return out
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
	remote, err := s.ProviderModels(ctx, discovery)
	if err != nil {
		view.DiscoveryErr = err.Error()
	}
	addModelCandidates(candidates, provider, remote, func(candidate *appviewmodel.ModelCandidate) {
		if !candidate.Configured {
			candidate.Remote = true
		}
	})
	view.Candidates = s.sortedModelCandidates(candidates)
	return view, nil
}

func (s ModelService) modelProviderOptions(choices []appsettings.ModelChoice) []appviewmodel.ModelProviderOption {
	providers := map[string]appviewmodel.ModelProviderOption{}
	for provider, models := range catalogModels {
		key := normalizeModelCatalogKey(provider)
		providers[key] = appviewmodel.ModelProviderOption{
			ID:                key,
			Name:              key,
			Builtin:           true,
			RemoteDiscovery:   s.services.modelProvider != nil,
			CatalogModelCount: len(models),
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
		option.CatalogModelCount = len(s.ListCatalogModels(key))
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

func (s ModelService) sortedModelCandidates(candidates map[string]appviewmodel.ModelCandidate) []appviewmodel.ModelCandidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]appviewmodel.ModelCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if caps, ok := s.LookupCapabilities(candidate.Provider, candidate.Model); ok {
			candidate.CapabilitiesKnown = true
			candidate.Capabilities = modelCapabilityView(caps)
			candidate.ReasoningLevels = s.ReasoningLevels(candidate.Provider, candidate.Model)
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
	return strings.ToLower(strings.TrimSpace(value))
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
	out := in
	out.ReasoningMode = strings.ToLower(strings.TrimSpace(out.ReasoningMode))
	if out.ReasoningMode == "" {
		if out.SupportsReasoning {
			out.ReasoningMode = ReasoningModeToggle
		} else {
			out.ReasoningMode = ReasoningModeNone
		}
	}
	out.ReasoningEfforts = normalizeReasoningEfforts(out.ReasoningEfforts)
	out.DefaultReasoningEffort = strings.ToLower(strings.TrimSpace(out.DefaultReasoningEffort))
	if out.DefaultMaxOutputTokens <= 0 && out.MaxOutputTokens > 0 {
		out.DefaultMaxOutputTokens = out.MaxOutputTokens
	}
	if out.MaxOutputTokens <= 0 && out.DefaultMaxOutputTokens > 0 {
		out.MaxOutputTokens = out.DefaultMaxOutputTokens
	}
	return out
}

func normalizeReasoningEfforts(levels []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(levels))
	for _, level := range levels {
		level = strings.ToLower(strings.TrimSpace(level))
		if level == "" || level == "none" || level == "-" {
			continue
		}
		if _, ok := seen[level]; ok {
			continue
		}
		seen[level] = struct{}{}
		out = append(out, level)
	}
	return out
}
