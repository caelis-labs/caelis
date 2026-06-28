package gatewayapp

import (
	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/promptassembly"
	"github.com/OnslaughtSnail/caelis/ports/skill"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

type promptConfig = promptassembly.Config
type SkillMeta = promptassembly.SkillMeta

func buildSystemPrompt(cfg promptConfig) (string, error) {
	return promptassembly.BuildSystemPrompt(cfg)
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
