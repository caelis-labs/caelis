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
