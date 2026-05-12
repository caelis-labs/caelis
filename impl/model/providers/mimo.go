package providers

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

func newMimo(cfg Config, token string) model.LLM {
	llm := newOpenAICompat(cfg, token)
	llm.options.IncludeReasoningContent = true
	llm.options.EmitEmptyReasoningForToolCall = true
	llm.options.ApplyReasoning = applyMimoThinkingReasoning
	llm.options.StructuredOutput = openAICompatStructuredOutputJSONOutput
	return llm
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
