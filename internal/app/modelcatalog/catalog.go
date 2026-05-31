package modelcatalog

import (
	"slices"
	"sort"
	"strings"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
)

const (
	ReasoningModeNone   = "none"
	ReasoningModeToggle = "toggle"
	ReasoningModeEffort = "effort"
	ReasoningModeFixed  = "fixed"
)

type CapabilityInfo struct {
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

type ProviderInfo struct {
	ID         string
	ModelCount int
}

type catalogEntry struct {
	provider string
	pattern  string
	caps     CapabilityInfo
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

var builtinCapabilities = []catalogEntry{
	{provider: "deepseek", pattern: "deepseek-v4", caps: CapabilityInfo{ContextWindowTokens: 1048576, MaxOutputTokens: 393216, DefaultMaxOutputTokens: 32768, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, ReasoningEfforts: []string{"high", "max"}, DefaultReasoningEffort: "high", SupportsJSONOutput: true}},
	{provider: "gemini", pattern: "gemini-2.5", caps: CapabilityInfo{ContextWindowTokens: 1048576, MaxOutputTokens: 65536, DefaultMaxOutputTokens: 8192, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, SupportsJSONOutput: true}},
	{provider: "minimax", pattern: "MiniMax-M2.7", caps: CapabilityInfo{ContextWindowTokens: 204800, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeFixed, SupportsJSONOutput: true}},
	{provider: "minimax", pattern: "MiniMax-M2.5", caps: CapabilityInfo{ContextWindowTokens: 204800, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeFixed, SupportsJSONOutput: true}},
	{provider: "minimax", pattern: "MiniMax-M2.1", caps: CapabilityInfo{ContextWindowTokens: 204800, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeFixed, SupportsJSONOutput: true}},
	{provider: "minimax", pattern: "MiniMax-M2", caps: CapabilityInfo{ContextWindowTokens: 204800, MaxOutputTokens: 8192, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeFixed, SupportsJSONOutput: true}},
	{provider: "openai", pattern: "o", caps: CapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 100000, DefaultMaxOutputTokens: 32768, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, DefaultReasoningEffort: "medium", SupportsJSONOutput: true}},
	{provider: "openai-compatible", pattern: "o", caps: CapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 100000, DefaultMaxOutputTokens: 32768, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, DefaultReasoningEffort: "medium", SupportsJSONOutput: true}},
	{provider: "openai", pattern: "gpt-4o", caps: CapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 16384, DefaultMaxOutputTokens: 4096, SupportsImages: true, SupportsToolCalls: true, SupportsJSONOutput: true}},
	{provider: "openai-compatible", pattern: "gpt-4o", caps: CapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 16384, DefaultMaxOutputTokens: 4096, SupportsImages: true, SupportsToolCalls: true, SupportsJSONOutput: true}},
	{provider: "anthropic", pattern: "claude", caps: CapabilityInfo{ContextWindowTokens: 200000, MaxOutputTokens: 32000, DefaultMaxOutputTokens: 4096, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, SupportsJSONOutput: true}},
	{provider: "anthropic-compatible", pattern: "claude", caps: CapabilityInfo{ContextWindowTokens: 200000, MaxOutputTokens: 32000, DefaultMaxOutputTokens: 4096, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, SupportsJSONOutput: true}},
	{provider: "codefree", pattern: "", caps: CapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 8000, DefaultMaxOutputTokens: 8000, SupportsToolCalls: true, SupportsJSONOutput: true}},
	{provider: "openrouter", pattern: "", caps: CapabilityInfo{ContextWindowTokens: 262144, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "medium", "high"}, SupportsJSONOutput: true}},
	{provider: "volcengine", pattern: "doubao-seed-2.0", caps: CapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "volcengine-coding-plan", pattern: "doubao-seed-2.0-code", caps: CapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 32768, DefaultMaxOutputTokens: 8192, SupportsToolCalls: true, SupportsJSONOutput: true}},
	{provider: "xiaomi", pattern: "mimo-v2.5-pro", caps: CapabilityInfo{ContextWindowTokens: 1048576, MaxOutputTokens: 131072, DefaultMaxOutputTokens: 32768, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "xiaomi", pattern: "mimo-v2-pro", caps: CapabilityInfo{ContextWindowTokens: 1048576, MaxOutputTokens: 131072, DefaultMaxOutputTokens: 32768, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "xiaomi", pattern: "mimo-v2.5", caps: CapabilityInfo{ContextWindowTokens: 1048576, MaxOutputTokens: 131072, DefaultMaxOutputTokens: 32768, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "xiaomi", pattern: "mimo-v2-omni", caps: CapabilityInfo{ContextWindowTokens: 262144, MaxOutputTokens: 131072, DefaultMaxOutputTokens: 32768, SupportsImages: true, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "xiaomi", pattern: "mimo-v2-flash", caps: CapabilityInfo{ContextWindowTokens: 262144, MaxOutputTokens: 65536, DefaultMaxOutputTokens: 16384, SupportsToolCalls: true, SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, SupportsJSONOutput: true}},
	{provider: "ollama", pattern: "", caps: CapabilityInfo{ContextWindowTokens: 128000, MaxOutputTokens: 8192, DefaultMaxOutputTokens: 4096, SupportsToolCalls: true, SupportsJSONOutput: true}},
}

func NormalizeKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func Providers() []ProviderInfo {
	out := make([]ProviderInfo, 0, len(catalogModels))
	for provider, models := range catalogModels {
		out = append(out, ProviderInfo{ID: NormalizeKey(provider), ModelCount: len(models)})
	}
	sort.Slice(out, func(i int, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func Models(provider string) []string {
	return slices.Clone(catalogModels[NormalizeKey(provider)])
}

func ModelCount(provider string) int {
	return len(catalogModels[NormalizeKey(provider)])
}

func DefaultCapabilities() CapabilityInfo {
	return CapabilityInfo{
		ContextWindowTokens:    128000,
		MaxOutputTokens:        32768,
		DefaultMaxOutputTokens: 4096,
		SupportsToolCalls:      true,
		ReasoningMode:          ReasoningModeNone,
		SupportsJSONOutput:     true,
	}
}

func LookupCapabilities(provider string, modelName string) (CapabilityInfo, bool) {
	provider = NormalizeKey(provider)
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	for _, entry := range builtinCapabilities {
		if NormalizeKey(entry.provider) != provider {
			continue
		}
		pattern := strings.ToLower(strings.TrimSpace(entry.pattern))
		if pattern == "" || modelName == pattern || strings.HasPrefix(modelName, pattern) {
			return NormalizeCapabilities(entry.caps), true
		}
	}
	return CapabilityInfo{}, false
}

func CapabilitiesFromModelInfo(info coremodel.ModelInfo) (CapabilityInfo, bool) {
	caps := CapabilityInfo{
		ContextWindowTokens:    info.ContextWindowTokens,
		MaxOutputTokens:        info.MaxOutputTokens,
		DefaultMaxOutputTokens: info.MaxOutputTokens,
		SupportsImages:         info.SupportsImages,
		SupportsToolCalls:      info.SupportsToolCalls,
		ReasoningEfforts:       slices.Clone(info.ReasoningEfforts),
		DefaultReasoningEffort: strings.ToLower(strings.TrimSpace(info.DefaultReasoningEffort)),
		SupportsJSONOutput:     info.SupportsJSON,
	}
	if len(caps.ReasoningEfforts) == 0 && caps.DefaultReasoningEffort != "" {
		caps.ReasoningEfforts = []string{caps.DefaultReasoningEffort}
	}
	caps.SupportsReasoning = len(caps.ReasoningEfforts) > 0 || caps.DefaultReasoningEffort != ""
	if caps.SupportsReasoning {
		caps.ReasoningMode = ReasoningModeEffort
	}
	caps = NormalizeCapabilities(caps)
	known := caps.ContextWindowTokens > 0 ||
		caps.MaxOutputTokens > 0 ||
		caps.SupportsImages ||
		caps.SupportsToolCalls ||
		caps.SupportsReasoning ||
		caps.SupportsJSONOutput
	return caps, known
}

func NormalizeCapabilities(in CapabilityInfo) CapabilityInfo {
	out := in
	out.ReasoningMode = strings.ToLower(strings.TrimSpace(out.ReasoningMode))
	if out.ReasoningMode == "" {
		if out.SupportsReasoning {
			out.ReasoningMode = ReasoningModeToggle
		} else {
			out.ReasoningMode = ReasoningModeNone
		}
	}
	out.ReasoningEfforts = NormalizeReasoningEfforts(out.ReasoningEfforts)
	out.DefaultReasoningEffort = strings.ToLower(strings.TrimSpace(out.DefaultReasoningEffort))
	if out.DefaultMaxOutputTokens <= 0 && out.MaxOutputTokens > 0 {
		out.DefaultMaxOutputTokens = out.MaxOutputTokens
	}
	if out.MaxOutputTokens <= 0 && out.DefaultMaxOutputTokens > 0 {
		out.MaxOutputTokens = out.DefaultMaxOutputTokens
	}
	return out
}

func ReasoningLevelsFromCapabilities(caps CapabilityInfo) []string {
	caps = NormalizeCapabilities(caps)
	if !caps.SupportsReasoning {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(caps.ReasoningMode)) {
	case ReasoningModeFixed, ReasoningModeNone:
		return nil
	}
	levels := NormalizeReasoningEfforts(caps.ReasoningEfforts)
	if len(levels) == 0 {
		return []string{"none"}
	}
	out := make([]string, 0, len(levels)+1)
	out = append(out, "none")
	out = append(out, levels...)
	return out
}

func NormalizeReasoningEfforts(levels []string) []string {
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
