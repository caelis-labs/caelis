package providers

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func newOpenRouter(cfg Config, token string) *openAICompatLLM {
	llm := newOpenAICompat(cfg, token)
	llm.api = model.APIOpenRouter
	llm.options.StrictFunctionTools = true
	if strings.EqualFold(strings.TrimSpace(cfg.ReasoningMode), "none") &&
		len(cfg.ReasoningLevels) == 0 {
		llm.options.ApplyReasoning = func(payload *openAICompatRequest, reasoning model.ReasoningConfig) {
			// No selector is declared: none preserves the endpoint default
			// instead of sending an unadvertised disable command.
			if !strings.EqualFold(strings.TrimSpace(reasoning.Effort), "none") {
				applyOpenAIReasoning(payload, reasoning)
			}
		}
	}
	return llm
}

func normalizeOpenRouterModelID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	const providerPrefix = "openrouter/"
	if strings.HasPrefix(strings.ToLower(value), providerPrefix) {
		remainder := strings.TrimSpace(value[len(providerPrefix):])
		if strings.Contains(remainder, "/") {
			return remainder
		}
	}
	return value
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if key = strings.TrimSpace(key); key != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
