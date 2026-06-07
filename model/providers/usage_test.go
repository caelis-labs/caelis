package providers

import "testing"

func TestOpenAICompatUsageMapsCachedAndReasoningTokenVariants(t *testing.T) {
	tests := []struct {
		name            string
		usage           openAICompatUsage
		wantCached      int
		wantReasoning   int
		wantTotalTokens int
	}{
		{
			name: "prompt cache hit and root reasoning",
			usage: openAICompatUsage{
				PromptTokens:         10,
				CompletionTokens:     4,
				PromptCacheHitTokens: 7,
				ReasoningTokens:      3,
			},
			wantCached:      7,
			wantReasoning:   3,
			wantTotalTokens: 14,
		},
		{
			name: "legacy cached tokens and output details reasoning",
			usage: openAICompatUsage{
				PromptTokens:        20,
				CompletionTokens:    8,
				CachedTokens:        5,
				OutputTokensDetails: openAIOutputTokenDetails{ReasoningTokens: 2},
				TotalTokens:         30,
			},
			wantCached:      5,
			wantReasoning:   2,
			wantTotalTokens: 30,
		},
		{
			name: "nested input and completion details",
			usage: openAICompatUsage{
				PromptTokens:            12,
				CompletionTokens:        6,
				InputTokensDetails:      openAIInputTokenDetails{CachedTokens: 4},
				CompletionTokensDetails: openAIOutputTokenDetails{ReasoningTokens: 1},
			},
			wantCached:      4,
			wantReasoning:   1,
			wantTotalTokens: 18,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.usage.toModelUsage()
			if got.CachedInputTokens != tt.wantCached || got.ReasoningTokens != tt.wantReasoning || got.TotalTokens != tt.wantTotalTokens {
				t.Fatalf("usage = %+v, want cached=%d reasoning=%d total=%d", got, tt.wantCached, tt.wantReasoning, tt.wantTotalTokens)
			}
		})
	}
}
