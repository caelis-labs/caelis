package runtime

import "testing"

func TestCompactionFullCacheReadBaselineCountsReadOnce(t *testing.T) {
	for _, provider := range []string{"anthropic", "deepseek", "minimax"} {
		for _, nested := range []bool{false, true} {
			usage := map[string]any{"prompt_tokens": 0, "cached_input_tokens": 6000, "completion_tokens": 20, "total_tokens": 6020, "cache_write_tokens": 0}
			meta := map[string]any{"provider": provider}
			if nested {
				meta["caelis"] = map[string]any{"sdk": map[string]any{"provider": provider, "usage": usage}}
			} else {
				for k, v := range usage {
					meta[k] = v
				}
			}
			got, input, ok := providerPromptBaselineTokens(meta)
			if !ok || !input || got != 6000 {
				t.Fatalf("provider %s nested %v baseline=%d input=%v ok=%v", provider, nested, got, input, ok)
			}
		}
	}
}
