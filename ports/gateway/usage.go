package gateway

import (
	"encoding/json"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func AssistantText(event Event) string {
	if event.Narrative != nil && event.Narrative.Role == NarrativeRoleAssistant {
		return strings.TrimSpace(event.Narrative.Text)
	}
	return ""
}

func PromptTokens(event Event) int {
	if event.Usage == nil {
		return 0
	}
	return event.Usage.PromptTokens
}

func CachedInputTokens(event Event) int {
	if event.Usage == nil {
		return 0
	}
	return event.Usage.CachedInputTokens
}

// UsageSnapshotFromSessionEvent projects provider token usage from a durable
// session event into the canonical gateway usage contract.
func UsageSnapshotFromSessionEvent(event *session.Event) *UsageSnapshot {
	if event == nil {
		return nil
	}
	for _, meta := range []map[string]any{semanticUsageMetadata(event), event.Meta} {
		if len(meta) == 0 {
			continue
		}
		raw, ok := meta["usage"]
		if ok {
			payload, ok := raw.(map[string]any)
			if !ok {
				return nil
			}
			return usageSnapshotFromPayload(payload)
		}
		if raw := nestedAny(meta, "caelis", "sdk", "usage"); raw != nil {
			if payload, ok := raw.(map[string]any); ok {
				if usage := usageSnapshotFromPayload(payload); usage != nil {
					return usage
				}
			}
		}
		if usage := usageSnapshotFromPayload(meta); usage != nil {
			return usage
		}
	}
	return nil
}

// UsageSnapshotFromMap projects one provider-style usage payload into the
// canonical gateway usage contract.
func UsageSnapshotFromMap(payload map[string]any) *UsageSnapshot {
	return usageSnapshotFromPayload(payload)
}

func usageSnapshotFromPayload(payload map[string]any) *UsageSnapshot {
	if payload == nil {
		return nil
	}
	promptTokens := firstNonZeroInt(intValue(payload["prompt_tokens"]), intValue(payload["input_tokens"]))
	completionTokens := firstNonZeroInt(intValue(payload["completion_tokens"]), intValue(payload["output_tokens"]))
	totalTokens := intValue(payload["total_tokens"])
	if totalTokens == 0 && (promptTokens != 0 || completionTokens != 0) {
		totalTokens = promptTokens + completionTokens
	}
	usage := &UsageSnapshot{
		PromptTokens:      promptTokens,
		CachedInputTokens: cachedInputTokensFromPayload(payload),
		CompletionTokens:  completionTokens,
		ReasoningTokens:   reasoningTokensFromPayload(payload),
		TotalTokens:       totalTokens,
	}
	if usage.PromptTokens == 0 && usage.CachedInputTokens == 0 && usage.CompletionTokens == 0 && usage.ReasoningTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	return usage
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
		intValue(nestedAny(payload, "input_tokens_details", "cached_tokens")),
		intValue(nestedAny(payload, "prompt_tokens_details", "cached_tokens")),
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
		intValue(nestedAny(payload, "usage_metadata", "thoughts_token_count")),
		intValue(nestedAny(payload, "usageMetadata", "thoughtsTokenCount")),
	)
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
