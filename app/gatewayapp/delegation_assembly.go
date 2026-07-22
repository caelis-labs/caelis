package gatewayapp

import (
	"context"
	"fmt"
	"maps"
	"strings"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	sdkdelegation "github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func delegationAgentsForBindings(bindings agentbinding.Configuration, includeSelf bool) []sdkdelegation.Agent {
	visible := map[agentbinding.Handle]struct{}{}
	if includeSelf {
		visible[agentbinding.HandleSelf] = struct{}{}
	}
	for _, binding := range agentbinding.NormalizeConfiguration(bindings).Bindings {
		if agentbinding.IsDelegation(binding.Handle) {
			visible[binding.Handle] = struct{}{}
		}
	}
	out := make([]sdkdelegation.Agent, 0, len(visible))
	for _, definition := range agentbinding.DelegationDefinitions() {
		handle := definition.Handle
		if _, ok := visible[handle]; !ok {
			continue
		}
		out = append(out, sdkdelegation.Agent{
			Name:        string(handle),
			Description: definition.Description,
		})
	}
	return out
}

func (s *Stack) delegationSpawnTargets(session controlplacement.SessionContext) (map[string]spawn.Target, error) {
	_, targets, err := s.delegationSpawnConfiguration(session)
	return targets, err
}

func (s *Stack) delegationSpawnConfiguration(session controlplacement.SessionContext) ([]sdkdelegation.Agent, map[string]spawn.Target, error) {
	if s == nil || s.store == nil {
		return nil, nil, fmt.Errorf("gatewayapp: delegation placement is unavailable")
	}
	snapshot, err := s.placementSnapshot(context.Background())
	if err != nil {
		return nil, nil, err
	}
	hasSessionSelection := strings.TrimSpace(session.ProfileID) != "" || strings.TrimSpace(session.Effort) != ""
	agents := delegationAgentsForBindings(snapshot.placement.Bindings, hasSessionSelection)
	targets := make(map[string]spawn.Target, len(agents))

	if hasSessionSelection {
		self, err := controlplacement.ResolveHandle(snapshot.placement, controlplacement.HandleRequest{
			Handle:  agentbinding.HandleSelf,
			Purpose: controlplacement.PurposeSpawn,
			Session: session,
		})
		if err != nil {
			return nil, nil, err
		}
		targets[string(agentbinding.HandleSelf)] = spawn.Target{Selector: string(agentbinding.HandleSelf), Placement: self}
	}

	for _, definition := range agentbinding.DelegationDefinitions() {
		handle := definition.Handle
		if handle == agentbinding.HandleSelf {
			continue
		}
		if _, ok := agentbinding.Lookup(snapshot.placement.Bindings, handle); !ok {
			continue
		}
		placement, err := controlplacement.ResolveHandle(snapshot.placement, controlplacement.HandleRequest{
			Handle:  handle,
			Purpose: controlplacement.PurposeSpawn,
		})
		if err != nil {
			return nil, nil, err
		}
		targets[string(handle)] = spawn.Target{Selector: string(handle), Placement: placement}
	}
	return agents, targets, nil
}

func (s *Stack) resolveDelegationPlacement(req sdkdelegation.TargetRequest, runtimeCfg stackRuntimeConfig) (assembly.AgentConfig, error) {
	if s == nil || s.store == nil || s.lookup == nil {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: delegation placement is unavailable")
	}
	target := sdkdelegation.NormalizeTarget(req.Target)
	if target.Selector == "" {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: delegation selector is required")
	}
	if err := sdkplacement.ValidateSealed(target.Placement); err != nil {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: invalid delegation placement: %w", err)
	}
	snapshot, err := s.placementSnapshot(context.Background())
	if err != nil {
		return assembly.AgentConfig{}, err
	}
	if err := controlplacement.ValidateFrozen(snapshot.placement, target.Placement); err != nil {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: validate frozen delegation placement: %w", err)
	}

	switch target.Placement.Kind {
	case sdkplacement.KindModel:
		configured, err := s.lookup.ResolveConfig(target.Placement.Model)
		if err != nil {
			return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: resolve delegated model %q: %w", target.Placement.Model, err)
		}
		configured.ReasoningEffort = target.Placement.ReasoningEffort
		return s.materializeDelegatedModel(target.Selector, configured, runtimeCfg)
	case sdkplacement.KindAgent:
		agent, connection, err := controlagents.ResolveAgent(snapshot.placement.Agents, target.Placement.Agent)
		if err != nil {
			return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: resolve delegated Agent %q: %w", target.Placement.Agent, err)
		}
		materialized, err := s.materializeExternalAgent(agent, connection)
		if err != nil {
			return assembly.AgentConfig{}, err
		}
		materialized.Name = target.Selector
		materialized.SessionOptions = controlagents.SessionOptions{
			ModelID:                 target.Placement.Model,
			ConfigValues:            maps.Clone(target.Placement.SessionConfigValues),
			ReasoningEffortConfigID: target.Placement.ReasoningEffortConfigID,
		}
		return materialized, nil
	default:
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: unsupported delegation placement kind %q", target.Placement.Kind)
	}
}

func (s *Stack) materializeDelegatedModel(name string, configured ModelConfig, runtimeCfg stackRuntimeConfig) (assembly.AgentConfig, error) {
	materialized, err := configuredModelSpawnedSelfACPAgent(defaultSpawnedSelfACPAgentConfig{
		Config: Config{
			AppName:                   s.AppName,
			UserID:                    s.UserID,
			StoreDir:                  s.storeDir,
			ControlOperationRetention: s.controlOperationRetention,
			WorkspaceKey:              s.Workspace.Key,
			WorkspaceCWD:              s.Workspace.CWD,
			PolicyProfile:             runtimeCfg.PolicyProfile,
			ContextWindow:             effectiveDelegationContextWindow(configured, runtimeCfg.ContextWindow),
			SystemPrompt:              runtimeCfg.SystemPrompt,
			Model:                     configured,
		},
		AppName:      s.AppName,
		UserID:       s.UserID,
		StoreDir:     s.storeDir,
		WorkspaceKey: s.Workspace.Key,
		WorkspaceCWD: s.Workspace.CWD,
	})
	if err != nil {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: materialize delegated model %q: %w", name, err)
	}
	materialized.Name = strings.TrimSpace(name)
	materialized.Description = "Caelis delegated model"
	return materialized, nil
}

func effectiveDelegationContextWindow(model modelconfig.Config, fallback int) int {
	if model.ContextWindowTokens > 0 {
		return model.ContextWindowTokens
	}
	return fallback
}
