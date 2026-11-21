package providers

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestProviderUsagePresenceDistinguishesMissingAndZero(t *testing.T) {
	for _, tc := range []struct {
		raw             string
		usage, measured bool
		cached          int
	}{
		{raw: `{}`},
		{raw: `{"usage":null}`},
		{raw: `{"usage":{}}`},
		{raw: `{"usage":{"total_tokens":0}}`, usage: true},
		{raw: `{"usage":{"input_tokens_details":{"cached_tokens":null}}}`},
		{raw: `{"usage":{"input_tokens_details":{"cached_tokens":0}}}`, usage: true, measured: true},
		{raw: `{"usage":{"input_tokens_details":{"cached_tokens":124}}}`, usage: true, measured: true, cached: 124},
	} {
		var compat struct {
			Usage openAICompatUsage `json:"usage"`
		}
		var codex struct {
			Usage openAICodexUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(tc.raw), &compat); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(tc.raw), &codex); err != nil {
			t.Fatal(err)
		}
		for _, usage := range []model.Usage{compat.Usage.toKernelUsage(), codex.Usage.toKernelUsage()} {
			if usage.IsReported() != tc.usage || usage.CachedInputTokensReported != tc.measured || usage.CachedInputTokens != tc.cached {
				t.Fatalf("presence mismatch for %s: %+v", tc.raw, usage)
			}
		}
	}
}

func TestOpenAICompatCachedZeroDoesNotFallThroughToOtherAlias(t *testing.T) {
	var wire openAICompatUsage
	if err := json.Unmarshal([]byte(`{"prompt_cache_hit_tokens":0,"prompt_tokens_details":{"cached_tokens":128}}`), &wire); err != nil {
		t.Fatal(err)
	}
	if usage := wire.toKernelUsage(); !usage.CachedInputTokensReported || usage.CachedInputTokens != 0 {
		t.Fatalf("explicit cache miss was replaced: %+v", usage)
	}
}
