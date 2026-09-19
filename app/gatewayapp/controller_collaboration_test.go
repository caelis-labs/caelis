package gatewayapp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/placement"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/internal/controlassembly"
)

func TestControllerCollaborationPublishesConfiguredDelegationCatalog(t *testing.T) {
	host := newStackForToolTestWithoutProfiles(t, controlassembly.ResolvedAssembly{})
	native, err := host.connectTestModel(ModelConfig{Provider: "ollama", API: "ollama", Model: "native"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := host.composition.authorities.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	external, profiles := disconnectTestCatalog(agents.Connection{ID: "codex", Name: "Codex", Launcher: agents.Launcher{Kind: agents.LaunchKindHostedAdapter, AdapterID: "codex"}}, "codex", "model")
	doc.ExternalAgents = external
	remote := profiles.Profiles[0]
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, remote)
	if err != nil {
		t.Fatal(err)
	}
	doc.AgentBindings = agentbinding.Configuration{
		Roles: []agentbinding.Role{{Handle: "research", Description: "Investigate unfamiliar systems."}},
		Bindings: []agentbinding.Binding{
			{Handle: agentbinding.HandleBreeze, ProfileID: native.ID, Effort: "none"},
			{Handle: "research", ProfileID: remote.ID, Effort: "none"},
			{Handle: agentbinding.HandleGuardian, ProfileID: native.ID, Effort: "none"},
		},
	}
	if err := host.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	host.composition.invalidateOwnPlacementSnapshot()
	host.SetBuiltInChildControl("http://127.0.0.1:1", "unused")
	frozen, err := host.composition.resolveModelProfilePlacement(t.Context(), remote.ID, "none")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := host.composition.controllerCollaboration(t.Context(), session.SessionRef{SessionID: "work"}, session.ControllerBinding{EpochID: "epoch", Placement: frozen}, subagent.AgentConfig{Name: "codex", HostedAdapterID: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.MCPGrant.Close()
	var definitions []tool.Definition
	for _, variable := range cfg.MCPServers[0].Stdio.Env {
		if variable.Name == "CAELIS_COLLABORATION_TOOLS" {
			if err := json.Unmarshal([]byte(variable.Value), &definitions); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(definitions) != 6 {
		t.Fatalf("controller definitions = %d, want 6", len(definitions))
	}
	configured, targets, err := host.composition.delegationSpawnConfiguration(placement.SessionContext{ProfileID: remote.ID, Effort: "none"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(spawn.NewWithTargets(configured, targets).Definition())
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if definition.Name != spawn.ToolName {
			continue
		}
		got, err := json.Marshal(definition)
		if err != nil || string(got) != string(want) {
			t.Fatalf("MCP schema differs from canonical Spawn: %s, want %s (%v)", got, want, err)
		}
		properties := definition.InputSchema["properties"].(map[string]any)
		agents := properties["agent"].(map[string]any)
		choices := agents["enum"].([]any)
		if len(choices) != 3 || !strings.Contains(agents["description"].(string), "research: Investigate unfamiliar systems.") {
			t.Fatalf("missing configured roles or leaked unbound/system role: %#v", agents)
		}
		for _, name := range []string{"self", "breeze", "research"} {
			if _, ok := targets[name]; !ok {
				t.Fatalf("advertised role has no execution target: %s", name)
			}
		}
		return
	}
	t.Fatal("controller omitted StartThread")
}
