package gatewayapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	sdkdelegation "github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelprofile"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func TestDelegationAgentHandleKeepsACPPlacementIdentity(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	profile := modelprofile.ModelProfile{
		ID: "acp:grok:deep", DisplayName: "Grok Deep",
		Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{AgentID: "grok", RemoteModelID: "deep"}},
		Effort: modelprofile.EffortCapability{
			DefaultEffort: "none",
			Choices:       []modelprofile.EffortChoice{{Canonical: "none"}},
		},
	}
	if err := stack.composition.store.Save(AppConfig{
		ExternalAgents: controlagents.Configuration{
			Connections: []controlagents.Connection{{
				ID: "grok", Name: "Grok", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindExecutable, Command: "grok-acp"},
			}},
			Agents: []controlagents.Agent{{ID: "grok", Name: "Grok", ConnectionID: "grok"}},
		},
		ModelProfiles: modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{profile}},
		AgentBindings: agentbinding.Configuration{Bindings: []agentbinding.Binding{{
			Handle: agentbinding.HandleZenith, ProfileID: profile.ID, Effort: "none",
		}}},
	}); err != nil {
		t.Fatal(err)
	}

	targets, err := stack.composition.delegationSpawnTargets(controlplacement.SessionContext{})
	if err != nil {
		t.Fatal(err)
	}
	target := targets[string(agentbinding.HandleZenith)]
	if target.Selector != "zenith" || target.Placement.Agent != "grok" {
		t.Fatalf("resolved target = %#v, want AgentHandle zenith backed by ACP Agent grok", target)
	}
	materialized, err := stack.composition.resolveDelegationPlacement(sdkdelegation.TargetRequest{Target: target}, stack.composition.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Name != "grok" || materialized.Command != "grok-acp" {
		t.Fatalf("materialized Agent = %#v, want execution identity grok", materialized)
	}
}

func TestSelfDelegationPlacementTracksEffectiveSessionModelAndEffort(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	firstProfile, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "session-model-a",
		ContextWindowTokens: 111111,
		ReasoningEffort:     "medium", DefaultReasoningEffort: "medium", ReasoningLevels: []string{"low", "medium", "high"},
	})
	if err != nil {
		t.Fatalf("Connect(first) error = %v", err)
	}
	secondProfile, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "session-model-b",
		ContextWindowTokens: 222222,
		ReasoningEffort:     "low", DefaultReasoningEffort: "low", ReasoningLevels: []string{"low", "high"},
	})
	if err != nil {
		t.Fatalf("Connect(second) error = %v", err)
	}

	firstID := firstProfile.Backend.Provider.ModelConfigID
	secondID := secondProfile.Backend.Provider.ModelConfigID
	first, err := stack.composition.delegationSpawnTargets(controlplacement.SessionContext{ProfileID: firstProfile.ID, Effort: "high"})
	if err != nil {
		t.Fatalf("delegationSpawnTargets(first) error = %v", err)
	}
	second, err := stack.composition.delegationSpawnTargets(controlplacement.SessionContext{ProfileID: secondProfile.ID, Effort: "low"})
	if err != nil {
		t.Fatalf("delegationSpawnTargets(second) error = %v", err)
	}
	firstSelf := first["self"]
	secondSelf := second["self"]
	if firstSelf.Placement.Model != firstID || firstSelf.Placement.ProfileID != firstProfile.ID || firstSelf.Placement.ReasoningEffort != "high" {
		t.Fatalf("first Session placement = %#v", firstSelf)
	}
	if secondSelf.Placement.Model != secondID || secondSelf.Placement.ProfileID != secondProfile.ID || secondSelf.Placement.ReasoningEffort != "low" {
		t.Fatalf("second Session placement = %#v", secondSelf)
	}
	assertDelegationPlacementSessionOptions(t, stack, firstSelf, firstID, "high")
	assertDelegationPlacementSessionOptions(t, stack, secondSelf, secondID, "low")
	for _, profile := range []string{"breeze", "orbit", "zenith"} {
		if _, ok := first[profile]; ok {
			t.Fatalf("unbound %s unexpectedly has a Spawn target: %#v", profile, first[profile])
		}
	}
}

func TestDelegationSpawnConfigurationOmitsSelfWithoutLocalSessionSelection(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	agents, targets, err := stack.composition.delegationSpawnConfiguration(controlplacement.SessionContext{})
	if err != nil {
		t.Fatalf("delegationSpawnConfiguration() error = %v", err)
	}
	if len(agents) != 0 || len(targets) != 0 {
		t.Fatalf("delegation Spawn configuration = %#v / %#v, want no self target", agents, targets)
	}
}

func TestCaelisModelSessionOptionsPreserveExplicitNoneWhenAdvertised(t *testing.T) {
	options := caelisModelSessionOptions(ModelConfig{
		ID: "reasoning-model", ReasoningLevels: []string{"none", "high"},
	}, "none")
	if options.ModelID != "reasoning-model" ||
		options.ConfigValues[acpConfigReasoningID] != "none" ||
		options.ReasoningEffortConfigID != acpConfigReasoningID {
		t.Fatalf("caelisModelSessionOptions(reasoning) = %#v, want frozen explicit none", options)
	}

	plain := caelisModelSessionOptions(ModelConfig{ID: "plain-model"}, "none")
	if plain.ModelID != "plain-model" || plain.ReasoningEffortConfigID != "" || len(plain.ConfigValues) != 0 {
		t.Fatalf("caelisModelSessionOptions(plain) = %#v, want no unavailable effort option", plain)
	}
}

func TestDelegationAgentsIncludeBoundCustomRoleDescription(t *testing.T) {
	agents := delegationAgentsForBindings(agentbinding.Configuration{
		Roles: []agentbinding.Role{{
			Handle: "research", Description: "Investigate unfamiliar systems.",
		}},
		Bindings: []agentbinding.Binding{{
			Handle: "research", ProfileID: "provider:sol", Effort: "high",
		}},
	}, false)
	if len(agents) != 1 || agents[0].Name != "research" || agents[0].Description != "Investigate unfamiliar systems." {
		t.Fatalf("delegationAgentsForBindings(custom) = %#v", agents)
	}
}

func TestDelegationPlacementRejectsConfigurationDrift(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	profile, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "drift-model", BaseURL: "http://one.example",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	modelID := profile.Backend.Provider.ModelConfigID
	targets, err := stack.composition.delegationSpawnTargets(controlplacement.SessionContext{ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort})
	if err != nil {
		t.Fatalf("delegationSpawnTargets() error = %v", err)
	}
	doc, err := stack.composition.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for index := range doc.Models.Configs {
		if doc.Models.Configs[index].ID == modelID {
			doc.Models.Configs[index].ContextWindowTokens++
		}
	}
	if err := stack.composition.store.Save(doc); err != nil {
		t.Fatalf("Save(changed) error = %v", err)
	}
	_, err = stack.composition.resolveDelegationPlacement(sdkdelegation.TargetRequest{Target: targets["self"]}, stack.composition.runtime)
	if err == nil || !strings.Contains(err.Error(), "changed after placement was frozen") {
		t.Fatalf("resolveDelegationPlacement() error = %v, want configuration drift rejection", err)
	}
}

func assertDelegationPlacementSessionOptions(t *testing.T, stack *Stack, target sdkdelegation.Target, modelID string, effort string) {
	t.Helper()
	agent, err := stack.composition.resolveDelegationPlacement(sdkdelegation.TargetRequest{Target: target}, stack.composition.runtime)
	if err != nil {
		t.Fatalf("resolveDelegationPlacement() error = %v", err)
	}
	joined := strings.Join(agent.Args, " ")
	for _, forbidden := range []string{
		"--embedded", "-approval-mode", "-policy-profile", "-model-profile",
		"-reasoning-effort", "-system-prompt", "--dangerously-skip-permissions",
		"-control-operation-retention", "-context-window", "-sandbox-backend",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("delegated child args = %#v, contain Host-construction option %q", agent.Args, forbidden)
		}
	}
	if got := agent.SessionOptions.ModelID; got != modelID {
		t.Fatalf("placement model session option = %q, want %q", got, modelID)
	}
	if got := agent.SessionOptions.ConfigValues[acpConfigModeID]; got != "manual" {
		t.Fatalf("placement mode session option = %q, want manual", got)
	}
	if got := agent.SessionOptions.ConfigValues[acpConfigReasoningID]; got != effort {
		t.Fatalf("placement effort session option = %q, want %q", got, effort)
	}
	if got := agent.SessionOptions.ReasoningEffortConfigID; got != acpConfigReasoningID {
		t.Fatalf("placement effort config id = %q, want %q", got, acpConfigReasoningID)
	}
}
