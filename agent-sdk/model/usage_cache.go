package model

import (
	"encoding/json"
	"strings"
)

// ProviderSeparatesCachedInput reports providers whose prompt/input token count
// excludes cache-read tokens. Display accounting folds cached input into Input
// for these providers while keeping Cached as the visible cache-read breakdown.
func ProviderSeparatesCachedInput(provider string) bool {
	switch normalizedProviderKey(provider) {
	case "anthropic", "anthropic-compatible", "deepseek-anthropic", "deepseek/anthropic", "minimax":
		return true
	default:
		return false
	}
}

// ProviderSeparatesCachedInputForUsage reports whether this specific usage
// payload follows Anthropic-style cache accounting where cache reads are
// separate from prompt/input tokens.
func ProviderSeparatesCachedInputForUsage(provider string, payload map[string]any) bool {
	if len(payload) > 0 {
		provider = firstNonEmptyString(strings.TrimSpace(stringValue(payload["provider"])), provider)
	}
	if ProviderSeparatesCachedInput(provider) {
		return true
	}
	if normalizedProviderKey(provider) != "deepseek" || len(payload) == 0 {
		return false
	}
	if usageIntValue(payload["cache_read_input_tokens"]) > 0 ||
		usageIntValue(payload["cache_creation_input_tokens"]) > 0 ||
		usageIntValue(nestedUsageAny(payload, "usage_metadata", "cache_read_input_tokens")) > 0 ||
		usageIntValue(nestedUsageAny(payload, "usageMetadata", "cacheReadInputTokens")) > 0 {
		return true
	}
	prompt := usageIntValue(payload["prompt_tokens"])
	cached := usageIntValue(payload["cached_input_tokens"])
	completion := usageIntValue(payload["completion_tokens"])
	total := usageIntValue(payload["total_tokens"])
	return cached > 0 && total >= prompt+cached+completion && prompt+completion > 0
}

func normalizedProviderKey(provider string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(provider, "_", "-")))
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func nestedUsageAny(values map[string]any, path ...string) any {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	return current
}

func usageIntValue(v any) int {
	switch typed := v.(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case json.Number:
		i, err := typed.Int64()
		if err == nil {
			return int(i)
		}
		f, err := typed.Float64()
		if err == nil {
			return int(f)
		}
		return 0
	default:
		return 0
	}
}

func stringValue(v any) string {
	text, _ := v.(string)
	return text
}
