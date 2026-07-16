package gatewayapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	sdkdelegation "github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	"github.com/caelis-labs/caelis/control/modelconfig"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

type delegationResolutionSnapshot struct {
	configuration controldelegation.Configuration
	roster        controlagents.Configuration
	models        []modelconfig.Config
}

func newDelegationResolutionSnapshot(doc AppConfig) *delegationResolutionSnapshot {
	return &delegationResolutionSnapshot{
		configuration: controldelegation.NormalizeConfiguration(doc.Delegation),
		roster:        controlagents.NormalizeConfiguration(doc.AgentRoster),
		models:        append([]modelconfig.Config(nil), doc.Models.Configs...),
	}
}

func (s *Stack) invalidateDelegationResolutionSnapshot() {
	if s == nil {
		return
	}
	s.delegationCacheMu.Lock()
	s.delegationCache = nil
	s.delegationCacheGeneration++
	s.delegationCacheMu.Unlock()
}

func (s *Stack) loadDelegationResolutionSnapshot() (*delegationResolutionSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("gatewayapp: delegation placement is unavailable")
	}
	for {
		s.delegationCacheMu.RLock()
		cached := s.delegationCache
		generation := s.delegationCacheGeneration
		s.delegationCacheMu.RUnlock()
		if cached != nil {
			return cached, nil
		}
		doc, err := s.store.Load()
		if err != nil {
			return nil, err
		}
		loaded := newDelegationResolutionSnapshot(doc)
		s.delegationCacheMu.Lock()
		if s.delegationCacheGeneration != generation {
			s.delegationCacheMu.Unlock()
			continue
		}
		if s.delegationCache == nil {
			s.delegationCache = loaded
		}
		cached = s.delegationCache
		s.delegationCacheMu.Unlock()
		return cached, nil
	}
}

func fixedDelegationAgents() []sdkdelegation.Agent {
	definitions := controldelegation.Definitions()
	out := make([]sdkdelegation.Agent, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, sdkdelegation.Agent{
			Name:        string(definition.Profile),
			Description: definition.Description,
		})
	}
	return out
}

func (s *Stack) delegationSpawnTargets(modelRef string, reasoningEffort string) (map[string]spawn.Target, error) {
	if s == nil || s.store == nil || s.lookup == nil {
		return nil, fmt.Errorf("gatewayapp: delegation placement is unavailable")
	}
	snapshot, err := s.loadDelegationResolutionSnapshot()
	if err != nil {
		return nil, err
	}
	self, err := s.selfDelegationTarget(modelRef, reasoningEffort)
	if err != nil {
		return nil, err
	}
	targets := make(map[string]spawn.Target, len(controldelegation.Definitions()))
	for _, definition := range controldelegation.Definitions() {
		profile := definition.Profile
		resolution, err := controldelegation.Resolve(snapshot.configuration, profile, snapshot.roster, snapshot.models)
		if err != nil {
			return nil, err
		}
		if resolution.Binding.Target == controldelegation.TargetSelf {
			target := self
			target.Selector = string(profile)
			target.Placement.Fingerprint = delegationPlacementFingerprint(target)
			targets[string(profile)] = target
			continue
		}
		target, err := s.rosterDelegationTarget(profile, resolution, snapshot.roster, snapshot.models)
		if err != nil {
			return nil, err
		}
		targets[string(profile)] = target
	}
	return targets, nil
}

func (s *Stack) selfDelegationTarget(modelRef string, reasoningEffort string) (spawn.Target, error) {
	if strings.TrimSpace(modelRef) == "" {
		modelRef = s.lookup.DefaultID()
	}
	model, err := s.lookup.ResolveConfig(strings.TrimSpace(modelRef))
	if err != nil {
		return spawn.Target{}, fmt.Errorf("gatewayapp: resolve Session default delegation model: %w", err)
	}
	return delegationModelTarget("self", model, reasoningEffort), nil
}

func (s *Stack) rosterDelegationTarget(
	profile controldelegation.Profile,
	resolution controldelegation.Resolution,
	roster controlagents.Configuration,
	models []modelconfig.Config,
) (spawn.Target, error) {
	agent := resolution.Agent
	if strings.TrimSpace(agent.Backing.ModelAlias) == "" {
		resolvedAgent, connection, err := controlagents.ResolveAgent(roster, agent.ID)
		if err != nil {
			return spawn.Target{}, err
		}
		target := spawn.Target{
			Selector: string(profile),
			Placement: sdkdelegation.Placement{
				Kind:              sdkdelegation.PlacementAgent,
				Agent:             agent.ID,
				ConfigFingerprint: delegationExternalConfigurationFingerprint(resolvedAgent, connection),
			},
		}
		target.Placement.Fingerprint = delegationPlacementFingerprint(target)
		return target, nil
	}
	configured, ok := configuredDelegationModel(models, agent.Backing.ModelAlias)
	if !ok {
		return spawn.Target{}, fmt.Errorf("gatewayapp: delegation Agent %q references unknown model %q", agent.ID, agent.Backing.ModelAlias)
	}
	effort := firstNonEmpty(
		strings.TrimSpace(resolution.Binding.ReasoningEffort),
		strings.TrimSpace(configured.ReasoningEffort),
		strings.TrimSpace(configured.DefaultReasoningEffort),
	)
	return delegationModelTarget(string(profile), configured, effort), nil
}

func delegationModelTarget(selector string, model modelconfig.Config, reasoningEffort string) spawn.Target {
	model = modelconfig.NormalizeConfig(model)
	target := spawn.Target{
		Selector: strings.TrimSpace(selector),
		Placement: sdkdelegation.Placement{
			Kind:              sdkdelegation.PlacementModel,
			Model:             model.ID,
			ReasoningEffort:   strings.ToLower(strings.TrimSpace(reasoningEffort)),
			ConfigFingerprint: delegationModelConfigurationFingerprint(model),
		},
	}
	target.Placement.Fingerprint = delegationPlacementFingerprint(target)
	return target
}

func configuredDelegationModel(models []modelconfig.Config, ref string) (modelconfig.Config, bool) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	for _, raw := range models {
		configured := modelconfig.NormalizeConfig(raw)
		if configured.ID == ref || strings.EqualFold(configured.Alias, ref) {
			return configured, true
		}
	}
	return modelconfig.Config{}, false
}

func delegationPlacementFingerprint(raw sdkdelegation.Target) string {
	target := sdkdelegation.NormalizeTarget(raw)
	canonical := strings.Join([]string{
		target.Selector,
		string(target.Placement.Kind),
		target.Placement.Agent,
		target.Placement.Model,
		target.Placement.ReasoningEffort,
		target.Placement.ConfigFingerprint,
	}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func delegationModelConfigurationFingerprint(raw modelconfig.Config) string {
	configured := modelconfig.NormalizeConfig(raw)
	configured.HTTPClient = nil
	configured.Token = ""
	payload, _ := json.Marshal(configured)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func delegationExternalConfigurationFingerprint(agent controlagents.Agent, connection controlagents.Connection) string {
	payload, _ := json.Marshal(struct {
		Agent             controlagents.Agent `json:"agent"`
		LaunchFingerprint string              `json:"launch_fingerprint"`
	}{
		Agent:             controlagents.NormalizeAgent(agent),
		LaunchFingerprint: controlagents.LaunchFingerprint(connection.Launcher),
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (s *Stack) resolveDelegationPlacement(req sdkdelegation.TargetRequest, runtimeCfg stackRuntimeConfig) (assembly.AgentConfig, error) {
	if s == nil || s.store == nil || s.lookup == nil {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: delegation placement is unavailable")
	}
	target := sdkdelegation.NormalizeTarget(req.Target)
	placement := target.Placement
	if target.Selector == "" || placement.ConfigFingerprint == "" || placement.Fingerprint == "" {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: delegation placement is incomplete")
	}
	if got := delegationPlacementFingerprint(target); !strings.EqualFold(got, placement.Fingerprint) {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: delegation placement fingerprint is invalid")
	}
	switch placement.Kind {
	case sdkdelegation.PlacementModel:
		configured, err := s.lookup.ResolveConfig(placement.Model)
		if err != nil {
			return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: resolve delegated model %q: %w", placement.Model, err)
		}
		if got := delegationModelConfigurationFingerprint(configured); !strings.EqualFold(got, placement.ConfigFingerprint) {
			return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: delegated model %q changed after Spawn was prepared", placement.Model)
		}
		configured.ReasoningEffort = placement.ReasoningEffort
		return s.materializeModelRosterAgent(target.Selector, "Caelis delegated model", configured, runtimeCfg)
	case sdkdelegation.PlacementAgent:
		snapshot, err := s.loadDelegationResolutionSnapshot()
		if err != nil {
			return assembly.AgentConfig{}, err
		}
		agent, connection, err := controlagents.ResolveAgent(snapshot.roster, placement.Agent)
		if err != nil {
			return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: resolve delegated Agent %q: %w", placement.Agent, err)
		}
		if got := delegationExternalConfigurationFingerprint(agent, connection); !strings.EqualFold(got, placement.ConfigFingerprint) {
			return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: delegated Agent %q changed after Spawn was prepared", placement.Agent)
		}
		materialized, err := s.materializeRosterAgent(agent, connection, runtimeCfg)
		if err != nil {
			return assembly.AgentConfig{}, err
		}
		materialized.Name = target.Selector
		return materialized, nil
	default:
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: unsupported delegation placement kind %q", placement.Kind)
	}
}

func effectiveDelegationContextWindow(model modelconfig.Config, fallback int) int {
	if model.ContextWindowTokens > 0 {
		return model.ContextWindowTokens
	}
	return fallback
}
