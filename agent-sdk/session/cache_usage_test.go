package session

import (
	"encoding/json"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"testing"
)

func TestCacheUsageRawAndCanonicalPresenceDoNotDoubleCount(t *testing.T) {
	cases := []struct {
		name                       string
		payload                    map[string]any
		prompt, read, write, total int
	}{
		{"raw", map[string]any{"input_tokens": 100, "cache_read_input_tokens": 8000, "cache_creation_input_tokens": 2000, "output_tokens": 200, "cache_creation": map[string]any{"ephemeral_5m_input_tokens": 1500, "ephemeral_1h_input_tokens": 500}}, 2100, 8000, 2000, 10300},
		{"canonical", map[string]any{"prompt_tokens": 2100, "cached_input_tokens": 8000, "cache_write_tokens": 2000, "completion_tokens": 200, "total_tokens": 10300}, 2100, 8000, 2000, 10300},
		{"mixed_zero", map[string]any{"prompt_tokens": 0, "input_tokens": 100, "cached_input_tokens": 0, "cached_input_tokens_reported": true, "cache_read_input_tokens": 8000, "cache_write_tokens": 0, "cache_creation_input_tokens": 2000, "completion_tokens": 0, "output_tokens": 200}, 0, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := usageSnapshotFromPayload(tc.payload, "anthropic")
			if u == nil || u.PromptTokens != tc.prompt || u.CachedInputTokens != tc.read || u.CacheWriteTokens != tc.write || u.TotalTokens != tc.total || !u.CacheWriteTokensReported {
				t.Fatalf("usage=%+v", u)
			}
			display := NormalizeUsageForDisplay(*u, "anthropic")
			if display.PromptTokens != tc.prompt+tc.read || display.TotalTokens != tc.total {
				t.Fatalf("display double-counted cache: %+v", display)
			}
		})
	}
	zero := usageSnapshotFromPayload(map[string]any{"cache_creation": map[string]any{"ephemeral_5m_input_tokens": 0}}, "anthropic")
	if zero == nil || !zero.CacheWrite5mTokensReported || zero.CacheWriteTokensReported {
		t.Fatalf("TTL-only zero=%+v", zero)
	}
	if usageSnapshotFromPayload(map[string]any{"cache_creation_input_tokens": nil}, "anthropic") != nil {
		t.Fatal("null reported as observed zero")
	}
}

func TestInvocationMetadataCachePresenceSurvivesJSON(t *testing.T) {
	for _, tc := range []struct {
		name        string
		usage       model.Usage
		read, write bool
	}{
		{"missing", model.Usage{PromptTokens: 1, TotalTokens: 1}, false, false},
		{"reported_zero", model.Usage{PromptTokens: 1, TotalTokens: 1, CachedInputTokensReported: true, CacheWriteTokensReported: true}, true, true},
		{"legacy_positive", model.Usage{PromptTokens: 10, TotalTokens: 10, CachedInputTokens: 5, CacheWriteTokens: 2}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(ModelUsageMetadata("anthropic", tc.usage))
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err = json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			usage := UsageSnapshotFromMapForProvider(payload, "anthropic")
			if usage == nil || usage.CachedInputTokensReported != tc.read || usage.CacheWriteTokensReported != tc.write {
				t.Fatalf("usage=%+v metadata=%s", usage, raw)
			}
		})
	}
}
