package controladapter

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestDeepSeekOpenAICompatCacheHitDoesNotDoubleCount(t *testing.T) {
	usage := session.UsageSnapshotFromMapForProvider(map[string]any{
		"prompt_tokens":           42,
		"prompt_cache_hit_tokens": 31,
		"completion_tokens":       8,
		"total_tokens":            50,
	}, "deepseek")

	if usage == nil {
		t.Fatal("session.UsageSnapshotFromMapForProvider() = nil, want usage")
		return
	}
	normalized := normalizeUsageForDisplay(*usage, "deepseek")
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
	usage := session.UsageSnapshotFromMapForProvider(map[string]any{
		"provider":            "deepseek-anthropic",
		"prompt_tokens":       42,
		"cached_input_tokens": 31,
		"completion_tokens":   8,
		"total_tokens":        81,
	}, "deepseek")

	if usage == nil {
		t.Fatal("session.UsageSnapshotFromMapForProvider() = nil, want usage")
		return
	}
	normalized := normalizeUsageForDisplay(*usage, "deepseek-anthropic")
	if normalized.PromptTokens != 73 {
		t.Fatalf("PromptTokens = %d, want prompt plus cache read", normalized.PromptTokens)
	}
	if normalized.TotalTokens != 81 {
		t.Fatalf("TotalTokens = %d, want Anthropic total unchanged", normalized.TotalTokens)
	}
}

func TestAnthropicUsageDisplayDoesNotDoubleCount(t *testing.T) {
	cases := []struct {
		name          string
		payload       map[string]any
		prompt, total int
	}{
		{"raw", map[string]any{"input_tokens": 100, "cache_read_input_tokens": 8000, "cache_creation_input_tokens": 2000, "output_tokens": 200}, 10100, 10300},
		{"canonical", map[string]any{"prompt_tokens": 2100, "cached_input_tokens": 8000, "cache_write_tokens": 2000, "completion_tokens": 200, "total_tokens": 10300}, 10100, 10300},
		{"mixed_zero", map[string]any{"prompt_tokens": 0, "input_tokens": 100, "cached_input_tokens": 0, "cached_input_tokens_reported": true, "cache_read_input_tokens": 8000, "cache_write_tokens": 0, "cache_creation_input_tokens": 2000, "completion_tokens": 0, "output_tokens": 200}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := session.UsageSnapshotFromMapForProvider(tc.payload, "anthropic")
			if raw == nil {
				t.Fatal("usage is missing")
			}
			display := normalizeUsageForDisplay(*raw, "anthropic")
			if display.PromptTokens != tc.prompt || display.TotalTokens != tc.total {
				t.Fatalf("display double-counted cache: %+v", display)
			}
			if again := normalizeUsageForDisplay(display, "anthropic"); again != display {
				t.Fatalf("second normalization double-counted cache: %+v", again)
			}
		})
	}
}

func TestUsageDisplayPrefersExplicitAPIAccounting(t *testing.T) {
	usage := session.UsageSnapshot{
		CacheReadAccounting: model.CacheReadIncluded,
		PromptTokens:        42,
		CachedInputTokens:   31,
		CompletionTokens:    8,
		TotalTokens:         50,
	}
	if display := normalizeUsageForDisplay(usage, "anthropic"); display != usage {
		t.Fatalf("display = %+v, want %+v", display, usage)
	}
}
