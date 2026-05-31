package gatewayapp

import (
	"context"

	"github.com/OnslaughtSnail/caelis/core/plugin"
	appprompt "github.com/OnslaughtSnail/caelis/internal/app/prompt"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const windowsSandboxTLSNoteLine = appprompt.WindowsSandboxTLSNoteLine

type promptConfig struct {
	AppName          string
	WorkspaceDir     string
	BasePrompt       string
	SkillDirs        []string
	DelegationAgents []delegation.Agent
}

func buildSystemPrompt(cfg promptConfig) (string, error) {
	ctx := context.Background()
	workspaceDir, err := appprompt.ResolvePath(cfg.WorkspaceDir)
	if err != nil {
		return "", err
	}
	catalog, err := appresources.Discover(ctx, appresources.Request{
		WorkspaceDir: workspaceDir,
		SkillDirs:    cfg.SkillDirs,
	})
	if err != nil {
		return "", err
	}
	return appprompt.BuildSystemPrompt(ctx, appprompt.Config{
		AppName:      cfg.AppName,
		WorkspaceDir: workspaceDir,
		BasePrompt:   cfg.BasePrompt,
		Catalog:      catalog,
		ACPAgents:    acpAgentDescriptorsFromDelegation(cfg.DelegationAgents),
	})
}

func systemPromptWithWindowsSandboxTLSNote(systemPrompt string, enabled bool) string {
	return appprompt.WithWindowsSandboxTLSNote(systemPrompt, enabled)
}

func resolvePromptPath(path string) (string, error) {
	return appprompt.ResolvePath(path)
}

func estimatePromptTextTokens(text string) int {
	return appprompt.EstimateTextTokens(text)
}

func estimateModelPromptPrefixTokens(metadata map[string]any, tools []tool.Tool) int {
	return appprompt.EstimateModelPromptPrefixTokens(metadata, tools)
}

func estimateToolPromptTokens(tools []tool.Tool) int {
	return appprompt.EstimateToolPromptTokens(tools)
}

func acpAgentDescriptorsFromDelegation(agents []delegation.Agent) []plugin.ACPAgentDescriptor {
	if len(agents) == 0 {
		return nil
	}
	out := make([]plugin.ACPAgentDescriptor, 0, len(agents))
	for _, agent := range agents {
		agent = delegation.NormalizeAgent(agent)
		if agent.Name == "" {
			continue
		}
		out = append(out, plugin.ACPAgentDescriptor{
			Name:        agent.Name,
			Description: agent.Description,
		})
	}
	return out
}
