package providers

import "github.com/OnslaughtSnail/caelis/sdk/model"

type openAICompatUsage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	CachedTokens            int                      `json:"cached_tokens"`
	PromptCacheHitTokens    int                      `json:"prompt_cache_hit_tokens"`
	ReasoningTokens         int                      `json:"reasoning_tokens"`
	PromptTokensDetails     openAIInputTokenDetails  `json:"prompt_tokens_details"`
	InputTokensDetails      openAIInputTokenDetails  `json:"input_tokens_details"`
	CompletionTokensDetails openAIOutputTokenDetails `json:"completion_tokens_details"`
	OutputTokensDetails     openAIOutputTokenDetails `json:"output_tokens_details"`
}

type openAIInputTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type openAIOutputTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

func (u openAICompatUsage) hasAny() bool {
	return u.PromptTokens != 0 ||
		u.CompletionTokens != 0 ||
		u.TotalTokens != 0 ||
		u.cachedInputTokens() != 0 ||
		u.reasoningTokens() != 0
}

func (u openAICompatUsage) toKernelUsage() model.Usage {
	total := u.TotalTokens
	if total == 0 && (u.PromptTokens != 0 || u.CompletionTokens != 0) {
		total = u.PromptTokens + u.CompletionTokens
	}
	return model.Usage{
		PromptTokens:      u.PromptTokens,
		CachedInputTokens: u.cachedInputTokens(),
		CompletionTokens:  u.CompletionTokens,
		ReasoningTokens:   u.reasoningTokens(),
		TotalTokens:       total,
	}
}

func (u openAICompatUsage) cachedInputTokens() int {
	if u.PromptCacheHitTokens != 0 {
		return u.PromptCacheHitTokens
	}
	if u.CachedTokens != 0 {
		return u.CachedTokens
	}
	if u.InputTokensDetails.CachedTokens != 0 {
		return u.InputTokensDetails.CachedTokens
	}
	return u.PromptTokensDetails.CachedTokens
}

func (u openAICompatUsage) reasoningTokens() int {
	if u.ReasoningTokens != 0 {
		return u.ReasoningTokens
	}
	if u.OutputTokensDetails.ReasoningTokens != 0 {
		return u.OutputTokensDetails.ReasoningTokens
	}
	return u.CompletionTokensDetails.ReasoningTokens
}
