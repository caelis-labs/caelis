package gatewayapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	sdkdelegation "github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func TestSelfDelegationPlacementTracksEffectiveSessionModelAndEffort(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	firstID, err := stack.Connect(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "session-model-a",
		ContextWindowTokens: 111111,
		ReasoningEffort:     "medium", DefaultReasoningEffort: "medium", ReasoningLevels: []string{"low", "medium", "high"},
	})
	if err != nil {
		t.Fatalf("Connect(first) error = %v", err)
	}
	secondID, err := stack.Connect(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "session-model-b",
		ContextWindowTokens: 222222,
		ReasoningEffort:     "low", DefaultReasoningEffort: "low", ReasoningLevels: []string{"low", "high"},
	})
	if err != nil {
		t.Fatalf("Connect(second) error = %v", err)
	}

	first, err := stack.delegationSpawnTargets(firstID, "high")
	if err != nil {
		t.Fatalf("delegationSpawnTargets(first) error = %v", err)
	}
	second, err := stack.delegationSpawnTargets(secondID, "low")
	if err != nil {
		t.Fatalf("delegationSpawnTargets(second) error = %v", err)
	}
	firstSelf := first["self"]
	secondSelf := second["self"]
	if firstSelf.Placement.Model != firstID || firstSelf.Placement.ReasoningEffort != "high" {
		t.Fatalf("first Session placement = %#v", firstSelf)
	}
	if secondSelf.Placement.Model != secondID || secondSelf.Placement.ReasoningEffort != "low" {
		t.Fatalf("second Session placement = %#v", secondSelf)
	}
	assertDelegationPlacementArgs(t, stack, firstSelf, "session-model-a", "high", "111111")
	assertDelegationPlacementArgs(t, stack, secondSelf, "session-model-b", "low", "222222")
	for _, profile := range []string{"breeze", "orbit", "zenith"} {
		if _, ok := first[profile]; ok {
			t.Fatalf("unbound %s unexpectedly has a Spawn target: %#v", profile, first[profile])
		}
	}
}

func TestDelegationPlacementFingerprintsTrackCompleteConfiguration(t *testing.T) {
	baseModel := modelconfig.Config{
		Provider: "openai-compatible", ProfileID: "shared-profile", EndpointID: "shared-endpoint",
		API: providers.APIOpenAICompatible, Model: "gpt-test", BaseURL: "https://one.example/v1",
		CredentialRef: "credential-a", ContextWindowTokens: 131072,
	}
	changedModel := baseModel
	changedModel.BaseURL = "https://two.example/v1"
	if delegationModelConfigurationFingerprint(baseModel) == delegationModelConfigurationFingerprint(changedModel) {
		t.Fatal("model configuration fingerprint did not change with endpoint configuration")
	}
	withRawToken := baseModel
	withRawToken.Token = "secret-token"
	if delegationModelConfigurationFingerprint(baseModel) != delegationModelConfigurationFingerprint(withRawToken) {
		t.Fatal("model configuration fingerprint includes raw credential material")
	}

	agent := controlagents.Agent{
		ID: "external", Backing: controlagents.AgentBacking{ConnectionID: "external"},
		Defaults: controlagents.SessionOptions{ModelID: "remote-a"},
	}
	connection := controlagents.Connection{
		ID: "external", Launcher: controlagents.Launcher{Command: "external-acp", Args: []string{"--mode", "one"}},
	}
	baseExternal := delegationExternalConfigurationFingerprint(agent, connection)
	changedConnection := connection
	changedConnection.Launcher.Args = []string{"--mode", "two"}
	if baseExternal == delegationExternalConfigurationFingerprint(agent, changedConnection) {
		t.Fatal("external configuration fingerprint did not change with launcher configuration")
	}
	changedAgent := agent
	changedAgent.Defaults.ModelID = "remote-b"
	if baseExternal == delegationExternalConfigurationFingerprint(changedAgent, connection) {
		t.Fatal("external configuration fingerprint did not change with Agent defaults")
	}
}

func TestDelegationPlacementRejectsConfigurationDrift(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	modelID, err := stack.Connect(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "drift-model", BaseURL: "http://one.example",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	targets, err := stack.delegationSpawnTargets(modelID, "")
	if err != nil {
		t.Fatalf("delegationSpawnTargets() error = %v", err)
	}
	configured, err := stack.lookup.ResolveConfig(modelID)
	if err != nil {
		t.Fatalf("ResolveConfig() error = %v", err)
	}
	configured.ReasoningLevels = []string{"low", "high"}
	if _, err := stack.lookup.Upsert(configured); err != nil {
		t.Fatalf("Upsert(changed) error = %v", err)
	}
	_, err = stack.resolveDelegationPlacement(sdkdelegation.TargetRequest{Target: targets["self"]}, stack.runtime)
	if err == nil || !strings.Contains(err.Error(), "changed after Spawn was prepared") {
		t.Fatalf("resolveDelegationPlacement() error = %v, want configuration drift rejection", err)
	}
}

func assertDelegationPlacementArgs(t *testing.T, stack *Stack, target sdkdelegation.Target, model string, effort string, contextWindow string) {
	t.Helper()
	agent, err := stack.resolveDelegationPlacement(sdkdelegation.TargetRequest{Target: target}, stack.runtime)
	if err != nil {
		t.Fatalf("resolveDelegationPlacement() error = %v", err)
	}
	if got, _ := argValue(agent.Args, "-model"); got != model {
		t.Fatalf("placement model = %q, want %q", got, model)
	}
	if got, _ := argValue(agent.Args, "-reasoning-effort"); got != effort {
		t.Fatalf("placement effort = %q, want %q", got, effort)
	}
	if got, _ := argValue(agent.Args, "-context-window"); got != contextWindow {
		t.Fatalf("placement context window = %q, want %q", got, contextWindow)
	}
}
