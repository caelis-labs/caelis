package providers

import "github.com/caelis-labs/caelis/agent-sdk/model"

var volcengineCompatProfile = openAICompatProfile{
	IncludeReasoningContent:       true,
	EmitEmptyReasoningForToolCall: true,
	ApplyReasoning:                applyVolcengineThinkingReasoning,
	StructuredOutput:              openAICompatStructuredOutputJSONOutput,
}

func newVolcengine(cfg Config, token string) model.LLM {
	return newOpenAICompatWithProfile(cfg, token, volcengineCompatProfile)
}
