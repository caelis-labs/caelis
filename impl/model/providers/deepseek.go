package providers

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

const deepSeekDefaultAnthropicBaseURL = "https://api.deepseek.com/anthropic"
const deepSeekDefaultMaxTokens = 32768
const deepSeekMaxTokens = 393216
const thinkingModeMinTokens = 32768

var deepSeekCompatProfile = openAICompatProfile{
	IncludeReasoningContent:        true,
	EmitEmptyReasoningForToolCall:  true,
	EmitEmptyReasoningForAssistant: true,
	ApplyReasoning:                 applyDeepSeekCompatThinkingReasoning,
	StructuredOutput:               openAICompatStructuredOutputJSONOutput,
}

func newDeepSeek(cfg Config, token string) model.LLM {
	if !deepSeekUsesAnthropicBaseURL(cfg.BaseURL) {
		return newOpenAICompatWithProfile(cfg, token, deepSeekCompatProfile)
	}
	cfg.BaseURL = deepSeekAnthropicBaseURL(cfg.BaseURL)
	if cfg.Auth.HeaderKey == "" && cfg.Auth.Type == AuthAPIKey {
		cfg.Auth.Type = AuthBearerToken
	}
	return newAnthropicWithDefaults(cfg, token, anthropicProviderDefaults{
		provider:     "deepseek",
		baseURL:      deepSeekDefaultAnthropicBaseURL,
		maxOutputTok: deepSeekDefaultMaxTokens,
	})
}

func deepSeekUsesAnthropicBaseURL(raw string) bool {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	switch strings.ToLower(base) {
	case "", "https://api.deepseek.com", "https://api.deepseek.com/v1", "https://api.deepseek.com/anthropic", "https://api.deepseek.com/anthropic/v1":
		return true
	default:
		lower := strings.ToLower(base)
		return strings.HasSuffix(lower, "/anthropic") || strings.HasSuffix(lower, "/anthropic/v1")
	}
}

func deepSeekAnthropicBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	switch strings.ToLower(base) {
	case "", "https://api.deepseek.com", "https://api.deepseek.com/v1", "https://api.deepseek.com/anthropic", "https://api.deepseek.com/anthropic/v1":
		return deepSeekDefaultAnthropicBaseURL
	default:
		return base
	}
}

func applyDeepSeekCompatThinkingReasoning(payload *openAICompatRequest, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	if !deepSeekModelSupportsThinking(payload.Model) {
		clearDeepSeekCompatReasoningFields(payload)
		payload.MaxTokens = clampDeepSeekMaxTokens(payload.MaxTokens)
		return
	}
	effort := normalizeDeepSeekReasoningEffort(cfg.Effort)
	switch effort {
	case "none":
		payload.Thinking = &openAIThinking{Type: "disabled"}
		clearDeepSeekCompatReasoningFields(payload)
		payload.MaxTokens = clampDeepSeekMaxTokens(payload.MaxTokens)
	default:
		payload.Thinking = &openAIThinking{Type: "enabled"}
		payload.Reasoning = nil
		payload.ReasoningEffort = effort
		payload.MaxTokens = clampDeepSeekReasonerMaxTokens(payload.MaxTokens)
	}
}

func normalizeDeepSeekReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none":
		return "none"
	case "max", "xhigh", "very_high", "very-high", "veryhigh":
		return "max"
	case "", "minimal", "low", "medium", "high":
		return "high"
	default:
		return "high"
	}
}

func clearDeepSeekCompatReasoningFields(payload *openAICompatRequest) {
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
