package gatewayapp

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/promptassembly"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const windowsSandboxTLSNoteLine = "  <sandbox_tls>Windows restricted-token sandbox: SChannel/.NET TLS may fail; prefer Python/Node HTTPS or git -c http.sslBackend=openssl.</sandbox_tls>"

type promptConfig = promptassembly.Config
type SkillMeta = promptassembly.SkillMeta

func buildSystemPrompt(cfg promptConfig) (string, error) {
	return promptassembly.BuildSystemPrompt(cfg)
}

func systemPromptWithWindowsSandboxTLSNote(systemPrompt string, enabled bool) string {
	if strings.TrimSpace(systemPrompt) == "" || !enabled {
		return systemPrompt
	}
	if strings.Contains(systemPrompt, "<sandbox_tls>") {
		return systemPrompt
	}
	if strings.Contains(systemPrompt, "\n</environment_context>") {
		return strings.Replace(systemPrompt, "\n</environment_context>", "\n"+windowsSandboxTLSNoteLine+"\n</environment_context>", 1)
	}
	if strings.Contains(systemPrompt, "</environment_context>") {
		return strings.Replace(systemPrompt, "</environment_context>", windowsSandboxTLSNoteLine+"\n</environment_context>", 1)
	}
	return systemPrompt
}

func DefaultSkillDiscoveryDirs(workspaceDir string) []string {
	return promptassembly.DefaultSkillDiscoveryDirs(workspaceDir)
}

func DiscoverSkillMeta(dirs []string, workspaceDir string) ([]SkillMeta, error) {
	return promptassembly.DiscoverSkillMeta(dirs, workspaceDir)
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
