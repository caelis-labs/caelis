package providers

import (
	"net/url"
	"strings"

	"github.com/caelis-labs/caelis/ports/model"
)

var mimoCompatProfile = openAICompatProfile{
	IncludeReasoningContent:       true,
	EmitEmptyReasoningForToolCall: true,
	ApplyReasoning:                applyMimoThinkingReasoning,
	StructuredOutput:              openAICompatStructuredOutputJSONOutput,
	ProviderTools:                 mimoProviderTools,
	UsesProviderExecutedTools:     mimoUsesProviderExecutedTools,
}

func newMimo(cfg Config, token string) model.LLM {
	profile := mimoCompatProfile
	if mimoTokenPlanBaseURL(cfg.BaseURL) {
		profile.ProviderTools = nil
		profile.UsesProviderExecutedTools = nil
		return &mimoTokenPlanLLM{openAICompatLLM: newOpenAICompatWithProfile(cfg, token, profile)}
	}
	return &mimoLLM{openAICompatLLM: newOpenAICompatWithProfile(cfg, token, profile)}
}

type mimoLLM struct {
	*openAICompatLLM
}

type mimoTokenPlanLLM struct {
	*openAICompatLLM
}

func (l *mimoTokenPlanLLM) WebSearchUnavailableReason() string {
	return "Xiaomi Token Plan endpoints do not support provider-native web_search. Use a native Xiaomi MiMo API key with https://api.xiaomimimo.com/v1, or use web_fetch with a known URL."
}

func mimoTokenPlanBaseURL(baseURL string) bool {
	raw := strings.ToLower(strings.TrimSpace(baseURL))
	if raw == "" {
		return false
	}
	host := raw
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	if idx := strings.IndexByte(host, '/'); idx >= 0 {
		host = host[:idx]
	}
	host = strings.TrimPrefix(strings.TrimSpace(host), "www.")
	return strings.HasPrefix(host, "token-plan-") && strings.HasSuffix(host, ".xiaomimimo.com")
}

func applyMimoThinkingReasoning(payload *openAICompatRequest, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	effort := strings.ToLower(strings.TrimSpace(cfg.Effort))
	switch effort {
	case "":
		return
	case "none":
		payload.Thinking = &openAIThinking{Type: "disabled"}
	default:
		payload.Thinking = &openAIThinking{Type: "enabled"}
	}
	payload.Reasoning = nil
	payload.ReasoningEffort = ""
}
