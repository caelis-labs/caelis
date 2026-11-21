package gatewayapp

import (
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/promptassembly"
)

type promptConfig = promptassembly.Config
type promptResult = promptassembly.Result

func buildSystemPrompt(cfg promptConfig) (string, error) {
	return promptassembly.BuildSystemPrompt(cfg)
}

func buildSystemPromptResult(cfg promptConfig) (promptResult, error) {
	return promptassembly.BuildSystemPromptResult(cfg)
}

func systemPromptWithCollaborationGuidance(prompt string) string {
	return promptassembly.WithCollaborationGuidance(prompt)
}

func systemPromptWithoutCollaborationGuidance(prompt string) string {
	return promptassembly.WithoutCollaborationGuidance(prompt)
}

func systemPromptWithCollaboratorIdentity(prompt, handle, role string) string {
	return promptassembly.WithCollaboratorIdentity(prompt, handle, role)
}

func resolvePromptPath(path string) (string, error) {
	return promptassembly.ResolvePromptPath(path)
}

func estimatePromptTextTokens(text string) int {
	return promptassembly.EstimatePromptTextTokens(text)
}

func estimateModelPromptPrefixTokens(metadata map[string]any, tools []tool.Tool) int {
	return promptassembly.EstimateModelPromptPrefixTokens(metadata, tools)
}

func estimateToolPromptTokens(tools []tool.Tool) int {
	return promptassembly.EstimateToolPromptTokens(tools)
}
