package providers

import (
	"encoding/json"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type openAICompatUsage struct {
	reported                bool
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
	return u.reported || u.PromptTokens != 0 ||
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
	return usageWithPresence(model.Usage{
		PromptTokens:      u.PromptTokens,
		CachedInputTokens: u.cachedInputTokens(),
		CompletionTokens:  u.CompletionTokens,
		ReasoningTokens:   u.reasoningTokens(),
		TotalTokens:       total,
	}, u.reported)
}

func (u openAICompatUsage) toKernelUsageOr(fallback model.Usage) model.Usage {
	out := u.toKernelUsage()
	if !out.Reported && out.PromptTokens == 0 && out.CachedInputTokens == 0 && out.CompletionTokens == 0 && out.ReasoningTokens == 0 && out.TotalTokens == 0 {
		return fallback
	}
	return out
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

func (u *openAICompatUsage) UnmarshalJSON(data []byte) error {
	type wire openAICompatUsage
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*u = openAICompatUsage(decoded)
	u.reported = reportedTokenFields(data)
	return nil
}

// Nonzero counters retain their legacy representation. Presence is needed only
// to distinguish a provider's explicit zero from missing usage.
func usageWithPresence(usage model.Usage, present bool) model.Usage {
	if !usage.IsReported() {
		usage.Reported = present
	}
	return usage
}

func reportedTokenFields(data []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "output_tokens", "cached_tokens", "prompt_cache_hit_tokens", "reasoning_tokens"} {
		if value, ok := fields[key]; ok && string(value) != "null" {
			return true
		}
	}
	return false
}
