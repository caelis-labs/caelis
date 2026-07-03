package gateway

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/ports/session"
)

// UsageSnapshotFromSessionEvent projects provider token usage from a durable
// session event into the canonical gateway usage contract.
func UsageSnapshotFromSessionEvent(event *session.Event) *UsageSnapshot {
	if event == nil {
		return nil
	}
	provider := usageProviderFromSessionEvent(event)
	for _, meta := range []map[string]any{semanticUsageMetadata(event), event.Meta} {
		if len(meta) == 0 {
			continue
		}
		provider = firstNonEmptyString(provider, usageProviderFromMetadata(meta))
		raw, ok := meta["usage"]
		if ok {
			payload, ok := raw.(map[string]any)
			if !ok {
				return nil
			}
			return usageSnapshotFromPayload(payload, firstNonEmptyString(usageProviderFromMetadata(payload), provider))
		}
		if raw := nestedAny(meta, "caelis", "sdk", "usage"); raw != nil {
			if payload, ok := raw.(map[string]any); ok {
				if usage := usageSnapshotFromPayload(payload, firstNonEmptyString(usageProviderFromMetadata(payload), provider)); usage != nil {
					return usage
				}
			}
		}
		if usage := usageSnapshotFromPayload(meta, provider); usage != nil {
			return usage
		}
	}
	return nil
}

// UsageSnapshotFromMap projects one provider-style usage payload into the
// canonical gateway usage contract.
func UsageSnapshotFromMap(payload map[string]any) *UsageSnapshot {
	return usageSnapshotFromPayload(payload, usageProviderFromMetadata(payload))
}

// UsageSnapshotFromMapForProvider projects usage while applying provider-specific
// parsing rules that are unavailable from the payload alone.
func UsageSnapshotFromMapForProvider(payload map[string]any, provider string) *UsageSnapshot {
	return usageSnapshotFromPayload(payload, firstNonEmptyString(provider, usageProviderFromMetadata(payload)))
}

// UsageProviderFromSessionEvent extracts the provider used for usage accounting
// from invocation metadata, sdk metadata, or provider-style usage payloads.
func UsageProviderFromSessionEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	provider := usageProviderFromSessionEvent(event)
	for _, meta := range []map[string]any{semanticUsageMetadata(event), event.Meta} {
		if len(meta) == 0 {
			continue
		}
		provider = firstNonEmptyString(provider, usageProviderFromMetadata(meta))
		if raw, ok := meta["usage"].(map[string]any); ok {
			provider = firstNonEmptyString(usageProviderFromMetadata(raw), provider)
		}
		if raw := nestedAny(meta, "caelis", "sdk", "usage"); raw != nil {
			if payload, ok := raw.(map[string]any); ok {
				provider = firstNonEmptyString(usageProviderFromMetadata(payload), provider)
			}
		}
	}
	return provider
}

func usageSnapshotFromPayload(payload map[string]any, provider string) *UsageSnapshot {
	if payload == nil {
		return nil
	}
	promptTokens := firstNonZeroInt(
		intValue(payload["prompt_tokens"]),
		intValue(payload["input_tokens"]),
		intValue(payload["prompt_token_count"]),
		intValue(payload["promptTokenCount"]),
		intValue(nestedAny(payload, "usage_metadata", "prompt_token_count")),
		intValue(nestedAny(payload, "usageMetadata", "promptTokenCount")),
	)
	cacheCreationTokens := firstNonZeroInt(
		intValue(payload["cache_creation_input_tokens"]),
		intValue(nestedAny(payload, "usage_metadata", "cache_creation_input_tokens")),
		intValue(nestedAny(payload, "usageMetadata", "cacheCreationInputTokens")),
	)
	promptTokens += cacheCreationTokens
	completionTokens := firstNonZeroInt(
		intValue(payload["completion_tokens"]),
		intValue(payload["output_tokens"]),
		intValue(payload["candidates_token_count"]),
		intValue(payload["candidatesTokenCount"]),
		intValue(payload["response_token_count"]),
		intValue(payload["responseTokenCount"]),
		intValue(nestedAny(payload, "usage_metadata", "candidates_token_count")),
		intValue(nestedAny(payload, "usageMetadata", "candidatesTokenCount")),
		intValue(nestedAny(payload, "usage_metadata", "response_token_count")),
		intValue(nestedAny(payload, "usageMetadata", "responseTokenCount")),
	)
	totalTokens := firstNonZeroInt(
		intValue(payload["total_tokens"]),
		intValue(payload["total_token_count"]),
		intValue(payload["totalTokenCount"]),
		intValue(nestedAny(payload, "usage_metadata", "total_token_count")),
		intValue(nestedAny(payload, "usageMetadata", "totalTokenCount")),
	)
	cachedTokens := cachedInputTokensFromPayload(payload)
	reasoningTokens := reasoningTokensFromPayload(payload)
	cacheSeparated := ProviderSeparatesCachedInputForUsage(provider, payload)
	if totalTokens == 0 && (promptTokens != 0 || cachedTokens != 0 || completionTokens != 0 || reasoningTokens != 0) {
		totalTokens = promptTokens + completionTokens
		if cacheSeparated {
			totalTokens += cachedTokens
		} else {
			totalTokens += explicitCacheReadInputTokens(payload)
		}
	}
	minimumTotal := promptTokens + completionTokens
	if cacheSeparated {
		minimumTotal += cachedTokens
	} else {
		minimumTotal += explicitCacheReadInputTokens(payload)
	}
	if minimumTotal > 0 && totalTokens < minimumTotal {
		totalTokens = minimumTotal
	}
	usage := &UsageSnapshot{
		PromptTokens:      promptTokens,
		CachedInputTokens: cachedTokens,
		CompletionTokens:  completionTokens,
		ReasoningTokens:   reasoningTokens,
		TotalTokens:       totalTokens,
	}
	if usage.PromptTokens == 0 && usage.CachedInputTokens == 0 && usage.CompletionTokens == 0 && usage.ReasoningTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	return usage
}

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

func normalizedProviderKey(provider string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(provider, "_", "-")))
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
	if intValue(payload["cache_read_input_tokens"]) > 0 ||
		intValue(payload["cache_creation_input_tokens"]) > 0 ||
		intValue(nestedAny(payload, "usage_metadata", "cache_read_input_tokens")) > 0 ||
		intValue(nestedAny(payload, "usageMetadata", "cacheReadInputTokens")) > 0 {
		return true
	}
	prompt := intValue(payload["prompt_tokens"])
	cached := intValue(payload["cached_input_tokens"])
	completion := intValue(payload["completion_tokens"])
	total := intValue(payload["total_tokens"])
	return cached > 0 && total >= prompt+cached+completion && prompt+completion > 0
}

// NormalizeUsageForDisplay converts provider raw usage into the status-table
// display contract. Reasoning is treated as an output-token breakdown, so it is
// not added to Total unless CompletionTokens is absent.
func NormalizeUsageForDisplay(usage UsageSnapshot, provider string) UsageSnapshot {
	if providerSeparatesCachedInputForSnapshot(provider, usage) && usage.CachedInputTokens > 0 {
		usage.PromptTokens += usage.CachedInputTokens
	}
	minimum := usage.PromptTokens + usage.CompletionTokens
	if usage.CompletionTokens == 0 {
		minimum += usage.ReasoningTokens
	}
	if usage.TotalTokens < minimum {
		usage.TotalTokens = minimum
	}
	return usage
}

func providerSeparatesCachedInputForSnapshot(provider string, usage UsageSnapshot) bool {
	if ProviderSeparatesCachedInput(provider) {
		return true
	}
	if normalizedProviderKey(provider) != "deepseek" || usage.CachedInputTokens <= 0 {
		return false
	}
	return usage.TotalTokens >= usage.PromptTokens+usage.CachedInputTokens+usage.CompletionTokens &&
		usage.PromptTokens+usage.CompletionTokens > 0
}

func semanticUsageMetadata(event *session.Event) map[string]any {
	if event == nil {
		return nil
	}
	return event.Meta
}

func cachedInputTokensFromPayload(payload map[string]any) int {
	return firstNonZeroInt(
		intValue(payload["cached_input_tokens"]),
		intValue(payload["cached_prompt_tokens"]),
		intValue(payload["cached_tokens"]),
		intValue(payload["prompt_cache_hit_tokens"]),
		intValue(payload["cache_read_input_tokens"]),
		intValue(payload["cached_content_token_count"]),
		intValue(payload["cachedContentTokenCount"]),
		intValue(nestedAny(payload, "input_tokens_details", "cached_tokens")),
		intValue(nestedAny(payload, "prompt_tokens_details", "cached_tokens")),
		intValue(nestedAny(payload, "usage_metadata", "cached_content_token_count")),
		intValue(nestedAny(payload, "usageMetadata", "cachedContentTokenCount")),
	)
}

func explicitCacheReadInputTokens(payload map[string]any) int {
	return firstNonZeroInt(
		intValue(payload["cache_read_input_tokens"]),
		intValue(nestedAny(payload, "usage_metadata", "cache_read_input_tokens")),
		intValue(nestedAny(payload, "usageMetadata", "cacheReadInputTokens")),
	)
}

func reasoningTokensFromPayload(payload map[string]any) int {
	return firstNonZeroInt(
		intValue(payload["reasoning_tokens"]),
		intValue(payload["reasoning_output_tokens"]),
		intValue(payload["thinking_tokens"]),
		intValue(payload["thinking_output_tokens"]),
		intValue(payload["thoughts_token_count"]),
		intValue(payload["thoughtsTokenCount"]),
		intValue(nestedAny(payload, "completion_tokens_details", "reasoning_tokens")),
		intValue(nestedAny(payload, "output_tokens_details", "reasoning_tokens")),
		intValue(nestedAny(payload, "output_tokens_details", "thinking_tokens")),
		intValue(nestedAny(payload, "usage_metadata", "thoughts_token_count")),
		intValue(nestedAny(payload, "usageMetadata", "thoughtsTokenCount")),
	)
}

func usageProviderFromSessionEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Invocation != nil {
		return strings.TrimSpace(event.Invocation.Provider)
	}
	return ""
}

func usageProviderFromMetadata(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	return firstNonEmptyString(
		strings.TrimSpace(stringValue(meta["provider"])),
		strings.TrimSpace(stringValue(nestedAny(meta, "caelis", "invocation", "provider"))),
		strings.TrimSpace(stringValue(nestedAny(meta, "caelis", "sdk", "provider"))),
	)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func nestedAny(values map[string]any, path ...string) any {
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

func intValue(v any) int {
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
