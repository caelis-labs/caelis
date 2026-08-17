package gatewayapp

import (
	"context"
	"fmt"
	"maps"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func (s *runtimeComposition) materializeReviewerAgent(
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
			StoreDir:     s.authorities.storeDir,
			WorkspaceKey: s.workspace.Key,
			WorkspaceCWD: s.workspace.CWD,
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
