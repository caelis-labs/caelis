package gatewayapp

import (
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/promptassembly"
)

type promptConfig = promptassembly.Config
type SkillMeta = promptassembly.SkillMeta
type promptResult = promptassembly.Result

func buildSystemPrompt(cfg promptConfig) (string, error) {
	return promptassembly.BuildSystemPrompt(cfg)
}

func buildSystemPromptResult(cfg promptConfig) (promptResult, error) {
	return promptassembly.BuildSystemPromptResult(cfg)
}

func DefaultSkillDiscoveryDirs(workspaceDir string) []string {
	return promptassembly.DefaultSkillDiscoveryDirs(workspaceDir)
}

func DiscoverSkillMeta(dirs []string, workspaceDir string) ([]SkillMeta, error) {
	return promptassembly.DiscoverSkillMeta(dirs, workspaceDir)
}

func DiscoverSkillMetaRequest(req skill.DiscoverRequest) ([]SkillMeta, error) {
	return promptassembly.DiscoverSkillMetaRequest(req)
}

func DiscoverLegacyPluginSkillCopies(req skill.DiscoverRequest) ([]SkillMeta, error) {
	return promptassembly.DiscoverLegacyPluginSkillCopies(req)
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
