package gateway

import "testing"

func TestUsageSnapshotFromMapAcceptsGeminiUsageMetadata(t *testing.T) {
	usage := UsageSnapshotFromMap(map[string]any{
		"usageMetadata": map[string]any{
			"promptTokenCount":        11,
			"cachedContentTokenCount": 7,
			"candidatesTokenCount":    2,
			"thoughtsTokenCount":      5,
			"toolUsePromptTokenCount": 3,
			"totalTokenCount":         21,
		},
	})

	if usage == nil {
		t.Fatal("UsageSnapshotFromMap() = nil, want usage")
	}
	if usage.PromptTokens != 11 {
		t.Fatalf("PromptTokens = %d, want Gemini promptTokenCount", usage.PromptTokens)
	}
	if usage.CachedInputTokens != 7 {
		t.Fatalf("CachedInputTokens = %d, want Gemini cachedContentTokenCount", usage.CachedInputTokens)
	}
	if usage.CompletionTokens != 2 {
		t.Fatalf("CompletionTokens = %d, want Gemini candidatesTokenCount", usage.CompletionTokens)
	}
	if usage.ReasoningTokens != 5 {
		t.Fatalf("ReasoningTokens = %d, want Gemini thoughtsTokenCount", usage.ReasoningTokens)
	}
	if usage.TotalTokens != 21 {
		t.Fatalf("TotalTokens = %d, want Gemini totalTokenCount", usage.TotalTokens)
	}
}

func TestUsageSnapshotFromMapAcceptsAnthropicCacheReadUsage(t *testing.T) {
	usage := UsageSnapshotFromMap(map[string]any{
		"input_tokens":                11,
		"cache_creation_input_tokens": 3,
		"cache_read_input_tokens":     7,
		"output_tokens":               2,
	})

	if usage == nil {
		t.Fatal("UsageSnapshotFromMap() = nil, want usage")
	}
	if usage.PromptTokens != 14 {
		t.Fatalf("PromptTokens = %d, want input plus cache creation", usage.PromptTokens)
	}
	if usage.CachedInputTokens != 7 {
		t.Fatalf("CachedInputTokens = %d, want cache read tokens", usage.CachedInputTokens)
	}
	if usage.CompletionTokens != 2 {
		t.Fatalf("CompletionTokens = %d, want output tokens", usage.CompletionTokens)
	}
	if usage.TotalTokens != 23 {
		t.Fatalf("TotalTokens = %d, want prompt plus cache read plus output", usage.TotalTokens)
	}
}

func TestUsageSnapshotFromMapForProviderDoesNotTrustUndercountedCacheTotal(t *testing.T) {
	usage := UsageSnapshotFromMapForProvider(map[string]any{
		"input_tokens":            10,
		"cache_read_input_tokens": 100,
		"output_tokens":           5,
		"total_tokens":            15,
	}, "deepseek")

	if usage == nil {
		t.Fatal("UsageSnapshotFromMapForProvider() = nil, want usage")
	}
	if usage.TotalTokens != 115 {
		t.Fatalf("TotalTokens = %d, want max(explicit total, prompt+cached+output)", usage.TotalTokens)
	}
}

func TestDeepSeekOpenAICompatCacheHitDoesNotDoubleCount(t *testing.T) {
	usage := UsageSnapshotFromMapForProvider(map[string]any{
		"prompt_tokens":           42,
		"prompt_cache_hit_tokens": 31,
		"completion_tokens":       8,
		"total_tokens":            50,
	}, "deepseek")

	if usage == nil {
		t.Fatal("UsageSnapshotFromMapForProvider() = nil, want usage")
	}
	normalized := NormalizeUsageForDisplay(*usage, "deepseek")
	if normalized.PromptTokens != 42 {
		t.Fatalf("PromptTokens = %d, want OpenAI-compatible prompt tokens unchanged", normalized.PromptTokens)
	}
	if normalized.CachedInputTokens != 31 {
		t.Fatalf("CachedInputTokens = %d, want cache-hit breakdown", normalized.CachedInputTokens)
	}
	if normalized.TotalTokens != 50 {
		t.Fatalf("TotalTokens = %d, want provider total without adding cache hit twice", normalized.TotalTokens)
	}
}

func TestDeepSeekAnthropicUsageProviderFoldsCacheReadForDisplay(t *testing.T) {
	usage := UsageSnapshotFromMapForProvider(map[string]any{
		"provider":            "deepseek-anthropic",
		"prompt_tokens":       42,
		"cached_input_tokens": 31,
		"completion_tokens":   8,
		"total_tokens":        81,
	}, "deepseek")

	if usage == nil {
		t.Fatal("UsageSnapshotFromMapForProvider() = nil, want usage")
	}
	normalized := NormalizeUsageForDisplay(*usage, "deepseek-anthropic")
	if normalized.PromptTokens != 73 {
		t.Fatalf("PromptTokens = %d, want prompt plus cache read", normalized.PromptTokens)
	}
	if normalized.TotalTokens != 81 {
		t.Fatalf("TotalTokens = %d, want Anthropic total unchanged", normalized.TotalTokens)
	}
}

func TestUsageSnapshotFromMapAcceptsAnthropicThinkingTokenDetails(t *testing.T) {
	usage := UsageSnapshotFromMap(map[string]any{
		"input_tokens":  10,
		"output_tokens": 7,
		"output_tokens_details": map[string]any{
			"thinking_tokens": 3,
		},
	})

	if usage == nil {
		t.Fatal("UsageSnapshotFromMap() = nil, want usage")
	}
	if usage.ReasoningTokens != 3 {
		t.Fatalf("ReasoningTokens = %d, want Anthropic thinking token details", usage.ReasoningTokens)
	}
	if usage.TotalTokens != 17 {
		t.Fatalf("TotalTokens = %d, want input plus output", usage.TotalTokens)
	}
}
