package providers

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/sdk/model"
)

func newDeepSeek(cfg Config, token string) model.LLM {
	llm := newOpenAICompat(cfg, token)
	llm.options.IncludeReasoningContent = true
	llm.options.EmitEmptyReasoningForToolCall = true
	llm.options.EmitEmptyReasoningForAssistant = true
	llm.options.ApplyReasoning = applyThinkingReasoning
	return llm
}

// thinkingModeMinTokens is the minimum max_tokens value required for DeepSeek
// thinking mode. The API defaults to 32K; sending a lower limit truncates the
// reasoning chain.
const thinkingModeMinTokens = 32768

const (
	deepSeekDefaultMaxTokens = 32768
	deepSeekMaxTokens        = 393216
)

func applyThinkingReasoning(payload *openAICompatRequest, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	if !deepSeekModelSupportsThinking(payload.Model) {
		clearDeepSeekReasoningFields(payload)
		payload.MaxTokens = clampDeepSeekMaxTokens(payload.MaxTokens)
		return
	}
	effort := normalizeDeepSeekReasoningEffort(cfg.Effort)
	switch effort {
	case "none":
		payload.Thinking = &openAIThinking{Type: "disabled"}
		clearDeepSeekReasoningFields(payload)
		payload.MaxTokens = clampDeepSeekMaxTokens(payload.MaxTokens)
	default:
		payload.Thinking = &openAIThinking{Type: "enabled"}
		payload.Reasoning = nil
		payload.ReasoningEffort = effort
		// Thinking mode needs a larger token budget. If the current limit is
		// absent or below the API's default (32K), bump it up so the reasoning
		// chain is not prematurely truncated.
		payload.MaxTokens = clampDeepSeekReasonerMaxTokens(payload.MaxTokens)
	}
}

func normalizeDeepSeekReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none":
		return "none"
	case "max", "xhigh", "very_high", "veryhigh":
		return "max"
	case "", "minimal", "low", "medium", "high":
		return "high"
	default:
		return "high"
	}
}

func clearDeepSeekReasoningFields(payload *openAICompatRequest) {
	if payload == nil {
		return
	}
	payload.Reasoning = nil
	payload.ReasoningEffort = ""
	for i := range payload.Messages {
		payload.Messages[i].ReasoningContent = nil
	}
}

func deepSeekModelSupportsThinking(modelName string) bool {
	switch strings.ToLower(strings.TrimSpace(modelName)) {
	case "deepseek-v4-flash", "deepseek-v4-pro":
		return true
	default:
		return false
	}
}

func clampDeepSeekMaxTokens(current int) int {
	switch {
	case current <= 0:
		return deepSeekDefaultMaxTokens
	case current > deepSeekMaxTokens:
		return deepSeekMaxTokens
	default:
		return current
	}
}

func clampDeepSeekReasonerMaxTokens(current int) int {
	switch {
	case current <= 0:
		return thinkingModeMinTokens
	case current < thinkingModeMinTokens:
		return thinkingModeMinTokens
	case current > deepSeekMaxTokens:
		return deepSeekMaxTokens
	default:
		return current
	}
}
