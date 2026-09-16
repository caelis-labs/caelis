package gatewayapp

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func TestSelfApprovalInheritsEffectiveParentSessionAndHostPosture(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	profile, err := stack.connectTestModel(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "self-mode"})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := stack.composition.delegationSpawnTargets(controlplacement.SessionContext{ProfileID: profile.ID, Effort: "none"})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"auto-review", "manual"} {
		for _, fullAccess := range []bool{false, true} {
			cfg := stack.composition.activeRuntime
			cfg.ApprovalMode = "manual" // deliberately older than the Spawn context
			cfg.DangerouslySkipPermissions = fullAccess
			resolved, err := stack.composition.delegationPlacementResolver(cfg)(t.Context(), subagent.SpawnContext{ApprovalMode: mode}, delegation.TargetRequest{Target: targets["self"]})
			if err != nil {
				t.Fatal(err)
			}
			got, present := resolved.SessionOptions.ConfigValues[acpConfigModeID]
			if fullAccess && present || !fullAccess && got != mode {
				t.Fatalf("parent=%q fullAccess=%v child=%q present=%v", mode, fullAccess, got, present)
			}
			if !resolved.BuiltinRuntime {
				t.Fatal("self lost its Host-owned runtime")
			}
		}
	}
}
