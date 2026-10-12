package modelcatalog

import (
	"sort"
	"strings"
)

// SpeedMode describes one non-default request-speed level supported by a model.
type SpeedMode struct {
	Level       string `json:"level"`
	Description string `json:"description"`
}

// ModelCapabilities describes known capabilities and limits for a specific model.
type ModelCapabilities struct {
	// ContextWindowTokens is the maximum input context window size.
	ContextWindowTokens int `json:"context_window"`
	// MaxOutputTokens is the maximum output tokens the model can generate.
	MaxOutputTokens int `json:"max_output"`
	// DefaultMaxOutputTokens is the default output token limit if not explicitly set.
	// API providers may use their own default if this is 0.
	DefaultMaxOutputTokens int `json:"default_max_output"`
	// SupportsImages indicates whether the model accepts image inputs.
	SupportsImages bool `json:"images"`
	// SupportsToolCalls indicates whether the model supports function/tool calling.
	SupportsToolCalls bool `json:"tool_calls"`
	// SupportsReasoning indicates whether the model supports thinking/reasoning mode.
	SupportsReasoning bool `json:"reasoning"`
	// ReasoningMode describes how reasoning is controlled: none|toggle|effort|fixed.
	ReasoningMode string `json:"reasoning_mode"`
	// ReasoningEfforts lists supported reasoning effort levels (for example:
	// low|medium|high|xhigh). Empty means the model uses toggle/budget-only
	// reasoning or the effort set is unknown.
	ReasoningEfforts []string `json:"reasoning_efforts"`
	// DefaultReasoningEffort is the recommended default effort when the model
	// uses effort-based reasoning.
	DefaultReasoningEffort string `json:"default_reasoning_effort"`
	// SpeedModes lists non-default request-speed levels and their user-facing
	// tradeoffs. An empty list means no speed selector should be offered.
	SpeedModes []SpeedMode `json:"speed_modes"`
	// SupportsJSONOutput indicates whether the model supports structured JSON output.
	SupportsJSONOutput bool `json:"json_output"`
}

const (
	ReasoningModeNone   = "none"
	ReasoningModeToggle = "toggle"
	ReasoningModeEffort = "effort"
	ReasoningModeFixed  = "fixed"
)

// DefaultModelCapabilities returns conservative defaults for unknown models.
func DefaultModelCapabilities() ModelCapabilities {
	return ModelCapabilities{
		ContextWindowTokens:    128000,
		MaxOutputTokens:        32768,
		DefaultMaxOutputTokens: conservativeDefaultMaxOutputTokens(128000, false),
		SupportsToolCalls:      true,
		ReasoningMode:          ReasoningModeNone,
		SupportsJSONOutput:     true,
	}
}

func conservativeDefaultMaxOutputTokens(contextWindow int, reasoning bool) int {
	defaultTokens := 8192
	if reasoning {
		defaultTokens = 32768
	}
	return capSuggestedDefaultMaxOutput(contextWindow, defaultTokens, reasoning)
}

func capSuggestedDefaultMaxOutput(contextWindow int, suggested int, reasoning bool) int {
	if suggested <= 0 {
		return suggested
	}
	if reasoning || contextWindow <= 0 {
		return suggested
	}
	contextCap := contextWindow / 8
	if contextCap > 0 && contextCap < suggested {
		return contextCap
	}
	return suggested
}

func RecommendedFallbackMaxOutputTokens(contextWindow int, suggested int, reasoning bool) int {
	conservative := conservativeDefaultMaxOutputTokens(contextWindow, reasoning)
	if suggested <= 0 {
		return conservative
	}
	if suggested < conservative {
		return suggested
	}
	return conservative
}

// NormalizeReasoningEffort canonicalizes one reasoning effort value.
// Known aliases:
//
//	very_high, very-high, veryhigh -> xhigh
func NormalizeReasoningEffort(input string) string {
	value := strings.ToLower(strings.TrimSpace(input))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	switch value {
	case "":
		return ""
	case "very_high", "veryhigh":
		return "xhigh"
	default:
		return value
	}
}

// CompareReasoningEffort compares canonical reasoning efforts from least to
// most intensive. Known efforts sort before unknown provider-specific values;
// unknown values use their normalized lexical order for determinism.
func CompareReasoningEffort(left, right string) int {
	left = NormalizeReasoningEffort(left)
	right = NormalizeReasoningEffort(right)
	leftRank, leftKnown := reasoningEffortRank(left)
	rightRank, rightKnown := reasoningEffortRank(right)
	switch {
	case leftKnown && !rightKnown:
		return -1
	case !leftKnown && rightKnown:
		return 1
	case leftKnown && rightKnown && leftRank < rightRank:
		return -1
	case leftKnown && rightKnown && leftRank > rightRank:
		return 1
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

// PreferredReasoningEffort selects the canonical default for one supported
// effort set. A reasoning-bearing choice wins over none, and medium wins when
// available. Otherwise it returns the lower middle reasoning-bearing choice
// after sorting by intensity: low/high/xhigh selects high, while
// low/high/xhigh/max also selects high. None is returned only when it is the
// sole supported choice.
func PreferredReasoningEffort(levels []string) string {
	levels = normalizeReasoningEffortList(levels)
	reasoningLevels := make([]string, 0, len(levels))
	hasNone := false
	for _, level := range levels {
		if level == "none" {
			hasNone = true
			continue
		}
		if level == "medium" {
			return level
		}
		reasoningLevels = append(reasoningLevels, level)
	}
	if len(reasoningLevels) == 0 {
		if hasNone {
			return "none"
		}
		return ""
	}
	sort.SliceStable(reasoningLevels, func(i, j int) bool {
		return CompareReasoningEffort(reasoningLevels[i], reasoningLevels[j]) < 0
	})
	return reasoningLevels[(len(reasoningLevels)-1)/2]
}

func reasoningEffortRank(value string) (int, bool) {
	switch value {
	case "none":
		return 0, true
	case "minimal":
		return 1, true
	case "low":
		return 2, true
	case "medium":
		return 3, true
	case "high":
		return 4, true
	case "xhigh":
		return 5, true
	case "max":
		return 6, true
	case "ultra":
		return 7, true
	default:
		return 0, false
	}
}

func NormalizeReasoningMode(input string) string {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case ReasoningModeNone:
		return ReasoningModeNone
	case ReasoningModeToggle, "boolean", "onoff":
		return ReasoningModeToggle
	case ReasoningModeEffort, "levels":
		return ReasoningModeEffort
	case ReasoningModeFixed, "always_on", "always-on", "fixed_on", "fixed-on":
		return ReasoningModeFixed
	default:
		return ""
	}
}

func normalizeReasoningEffortList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, one := range in {
		normalized := NormalizeReasoningEffort(one)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizeModelCapabilitiesReasoning(caps *ModelCapabilities) {
	if caps == nil {
		return
	}
	caps.ReasoningEfforts = normalizeReasoningEffortList(caps.ReasoningEfforts)
	caps.DefaultReasoningEffort = NormalizeReasoningEffort(caps.DefaultReasoningEffort)
	mode := NormalizeReasoningMode(caps.ReasoningMode)
	switch {
	case !caps.SupportsReasoning:
		caps.ReasoningMode = ReasoningModeNone
		caps.ReasoningEfforts = nil
		caps.DefaultReasoningEffort = ""
		return
	case mode != "":
		caps.ReasoningMode = mode
	case len(caps.ReasoningEfforts) > 0:
		caps.ReasoningMode = ReasoningModeEffort
	default:
		caps.ReasoningMode = ReasoningModeToggle
	}
	if caps.ReasoningMode != ReasoningModeEffort && caps.ReasoningMode != ReasoningModeToggle {
		caps.ReasoningEfforts = nil
		caps.DefaultReasoningEffort = ""
		return
	}
	if len(caps.ReasoningEfforts) == 0 {
		caps.DefaultReasoningEffort = ""
		return
	}
	if caps.DefaultReasoningEffort == "" || !SupportsReasoningEffortList(caps.ReasoningEfforts, caps.DefaultReasoningEffort) {
		caps.DefaultReasoningEffort = PreferredReasoningEffort(caps.ReasoningEfforts)
	}
}

func SupportsReasoningEffortList(levels []string, effort string) bool {
	normalized := NormalizeReasoningEffort(effort)
	if normalized == "" {
		return false
	}
	for _, one := range normalizeReasoningEffortList(levels) {
		if one == normalized {
			return true
		}
	}
	return false
}

func reasoningLevelsFromCapabilities(caps ModelCapabilities) []string {
	normalizeModelCapabilitiesReasoning(&caps)
	switch caps.ReasoningMode {
	case ReasoningModeEffort:
		return append([]string(nil), caps.ReasoningEfforts...)
	case ReasoningModeToggle:
		if len(caps.ReasoningEfforts) > 0 {
			out := make([]string, 0, len(caps.ReasoningEfforts)+1)
			out = append(out, "none")
			out = append(out, caps.ReasoningEfforts...)
			return out
		}
		return []string{"none", "high"}
	case ReasoningModeFixed:
		return []string{"low", "medium", "high"}
	default:
		return nil
	}
}
