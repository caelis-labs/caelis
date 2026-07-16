package systemagent

import (
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

func TestBindRequiresModelBackedRosterAgentAndResolvesModel(t *testing.T) {
	configuredModel := modelconfig.NormalizeConfig(modelconfig.Config{
		Alias: "openai-codex/gpt-5.6-sol", Provider: "openai-codex", Model: "gpt-5.6-sol",
		ReasoningMode: "effort", ReasoningEffort: "high", ReasoningLevels: []string{"high", "xhigh"},
	})
	models := []modelconfig.Config{configuredModel}
	roster := controlagents.Configuration{Agents: []controlagents.Agent{
		{ID: "sol", Backing: controlagents.AgentBacking{ModelAlias: configuredModel.ID}},
		{ID: "claude", Backing: controlagents.AgentBacking{ConnectionID: "claude"}},
	}}

	configured, err := Bind(Configuration{}, BindRequest{ID: Guardian, AgentID: "sol", ReasoningEffort: "xhigh"}, roster, models)
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	resolved, bound, err := ResolveModel(configured, Guardian, roster, models)
	if err != nil || !bound || resolved.ID != configuredModel.ID || resolved.ReasoningEffort != "xhigh" {
		t.Fatalf("ResolveModel() = (%#v, %v, %v)", resolved, bound, err)
	}
	if _, err := Bind(configured, BindRequest{ID: Reviewer, AgentID: "sol", ReasoningEffort: "max"}, roster, models); err == nil {
		t.Fatal("Bind(unsupported effort) error = nil")
	}
	if _, err := Bind(configured, BindRequest{ID: Reviewer, AgentID: "claude"}, roster, models); err == nil {
		t.Fatal("Bind(external ACP Agent) error = nil")
	}
}

func TestResetAgentBindingsDropsRemovedModelAgent(t *testing.T) {
	configured := Configuration{Bindings: []Binding{{ID: Guardian, AgentID: "sol"}, {ID: Reviewer, AgentID: "terra"}}}
	got := ResetAgentBindings(configured, "sol")
	if len(got.Bindings) != 1 || got.Bindings[0].ID != Reviewer {
		t.Fatalf("ResetAgentBindings() = %#v", got)
	}
}
