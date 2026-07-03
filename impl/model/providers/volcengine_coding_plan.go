package providers

import (
	"strings"

	"github.com/caelis-labs/caelis/ports/model"
)

func newVolcengineCodingPlan(cfg Config, token string) model.LLM {
	return newOpenAICompatWithProfile(cfg, token, volcengineCompatProfile)
}

func applyVolcengineThinkingReasoning(payload *openAICompatRequest, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	effort := strings.ToLower(strings.TrimSpace(cfg.Effort))
	state := "enabled"
	switch effort {
	case "none":
		state = "disabled"
	case "":
		state = "auto"
	}
	payload.Thinking = &openAIThinking{Type: state}
	payload.Reasoning = nil
	payload.ReasoningEffort = ""
}
