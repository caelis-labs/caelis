package gatewayapp

import (
	"context"
	"fmt"
	"maps"
	"strings"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// ReviewerAgentID is the hidden Control-owned Agent used by /review.
const ReviewerAgentID = "reviewer"

const reviewWorkspaceScopePrompt = "Review the current workspace changes (staged, unstaged, and untracked). Lead with concrete findings; consider correctness, regressions, maintainability, architecture, and test coverage."

const reviewerSceneInstructions = `You are Caelis' code reviewer. Stay scoped to the requested workspace change, prioritize high-confidence findings, and do not change code unless explicitly asked.`

// ReviewPrompt returns the model-visible /review prompt and the rune offset
// where user-provided instructions begin after the fixed workspace scope.
func ReviewPrompt(instructions string) (string, int) {
	// The built-in Reviewer now attaches to the existing Host, so its fixed
	// scene must travel with the Control-owned review request instead of a child
	// process system-prompt override.
	base := reviewerSceneInstructions + "\n\n" + reviewWorkspaceScopePrompt
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return base, len([]rune(base))
	}
	prefix := base + "\n\nAdditional review instructions:\n"
	return prefix + instructions, len([]rune(prefix))
}

func (s *Stack) materializeReviewerAgent(
	ctx context.Context,
	placement sdkplacement.Placement,
	runtimeCfg stackRuntimeConfig,
) (assembly.AgentConfig, error) {
	switch placement.Kind {
	case sdkplacement.KindModel:
		if s.lookup == nil {
			return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: resolve Reviewer model: model lookup unavailable")
		}
		configured, err := s.resolveProviderProfileConfig(placement.ProfileID, placement.ReasoningEffort)
		if err != nil {
			return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: resolve Reviewer model profile %q: %w", placement.ProfileID, err)
		}
		return configuredModelSpawnedSelfACPAgent(defaultSpawnedSelfACPAgentConfig{
			AppName:      s.AppName,
			UserID:       s.UserID,
			StoreDir:     s.storeDir,
			WorkspaceKey: s.Workspace.Key,
			WorkspaceCWD: s.Workspace.CWD,
			SessionOptions: caelisModelSessionOptions(
				configured,
				placement.ReasoningEffort,
			),
			PinnedModel:    ptrToModelConfig(configured),
			BridgeApproval: !runtimeCfg.DangerouslySkipPermissions,
			ControlURL:     s.childControlURL, ControlTokenFile: s.childControlTokenFile,
		})
	case sdkplacement.KindAgent:
		snapshot, err := s.placementSnapshot(ctx)
		if err != nil {
			return assembly.AgentConfig{}, err
		}
		agent, connection, err := controlagents.ResolveAgent(snapshot.placement.Agents, placement.Agent)
		if err != nil {
			return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: resolve Reviewer ACP Agent %q: %w", placement.Agent, err)
		}
		materialized, err := s.materializeExternalAgent(agent, connection)
		if err != nil {
			return assembly.AgentConfig{}, err
		}
		materialized.SessionOptions = controlagents.SessionOptions{
			ModelID:                 placement.Model,
			ConfigValues:            maps.Clone(placement.SessionConfigValues),
			ReasoningEffortConfigID: placement.ReasoningEffortConfigID,
		}
		return materialized, nil
	default:
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: unsupported Reviewer placement kind %q", placement.Kind)
	}
}
