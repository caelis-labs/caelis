package viewmodel

import "testing"

func TestNormalizeTokenUsage(t *testing.T) {
	usage := NormalizeTokenUsage(TokenUsage{
		InputTokens:         10,
		CachedInputTokens:   -2,
		OutputTokens:        3,
		ReasoningTokens:     -1,
		TotalTokens:         0,
		ContextWindowTokens: -100,
	})

	if usage.InputTokens != 10 || usage.CachedInputTokens != 0 || usage.OutputTokens != 3 || usage.ReasoningTokens != 0 || usage.TotalTokens != 13 || usage.ContextWindowTokens != 0 {
		t.Fatalf("NormalizeTokenUsage() = %+v, want clamped usage with derived total", usage)
	}
}

func TestAddTokenUsage(t *testing.T) {
	total := TokenUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3, ContextWindowTokens: 8000}
	AddTokenUsage(&total, TokenUsage{InputTokens: 4, CachedInputTokens: 2, OutputTokens: 5, ReasoningTokens: 1, TotalTokens: 9, ContextWindowTokens: 16000})

	if total.InputTokens != 6 || total.CachedInputTokens != 2 || total.OutputTokens != 6 || total.ReasoningTokens != 1 || total.TotalTokens != 12 || total.ContextWindowTokens != 16000 {
		t.Fatalf("AddTokenUsage() total = %+v, want accumulated usage and max context window", total)
	}
}
