package providers

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

var mimoCompatProfile = openAICompatProfile{
	IncludeReasoningContent:       true,
	EmitEmptyReasoningForToolCall: true,
	ApplyReasoning:                applyMimoThinkingReasoning,
	StructuredOutput:              openAICompatStructuredOutputJSONOutput,
}

func newMimo(cfg Config, token string) model.LLM {
	return newOpenAICompatWithProfile(cfg, token, mimoCompatProfile)
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
