package providers

import "github.com/OnslaughtSnail/caelis/sdk/model"

func newVolcengine(cfg Config, token string) model.LLM {
	llm := newOpenAICompat(cfg, token)
	llm.options.IncludeReasoningContent = true
	llm.options.EmitEmptyReasoningForToolCall = true
	llm.options.ApplyReasoning = applyVolcengineThinkingReasoning
	llm.options.StructuredOutput = openAICompatStructuredOutputJSONOutput
	return llm
}
