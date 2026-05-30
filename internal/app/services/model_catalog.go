package services

import (
	"context"
	"slices"
	"sort"
	"strings"
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

func normalizeModelCatalogKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
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
