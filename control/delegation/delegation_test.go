package delegation

import (
	"reflect"
	"strings"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

func TestDefinitionsUseFixedCaelisProfiles(t *testing.T) {
	t.Parallel()

	definitions := Definitions()
	got := make([]Profile, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.Profile)
	}
	want := []Profile{ProfileSelf, ProfileBreeze, ProfileOrbit, ProfileZenith}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Definitions() profiles = %#v, want %#v", got, want)
	}
	if definitions[1].Name != "Caelis Breeze" || definitions[2].Name != "Caelis Orbit" || definitions[3].Name != "Caelis Zenith" {
		t.Fatalf("Definitions() names = %#v", definitions)
	}
	definitions[1].Name = "changed"
	if Definitions()[1].Name != "Caelis Breeze" {
		t.Fatal("Definitions() leaked package state")
	}
}

func TestListBindingsDefaultsEveryProfileToSelf(t *testing.T) {
	t.Parallel()

	bindings := ListBindings(Configuration{})
	if len(bindings) != 4 {
		t.Fatalf("ListBindings() = %#v, want four fixed profiles", bindings)
	}
	for _, binding := range bindings {
		if binding.Target != TargetSelf || binding.AgentID != "" || binding.ReasoningEffort != "" {
			t.Fatalf("default binding = %#v, want self", binding)
		}
	}
}

func TestBindResolveAndResetModelBackedAgent(t *testing.T) {
	t.Parallel()

	model := testModel()
	roster := controlagents.Configuration{Agents: []controlagents.Agent{{
		ID:      "codex-worker",
		Backing: controlagents.AgentBacking{ModelAlias: model.ID},
	}}}
	configuration, err := BindAgent(Configuration{}, ProfileOrbit, " CODEX-WORKER ", "very-high", roster, []modelconfig.Config{model})
	if err != nil {
		t.Fatalf("BindAgent() error = %v", err)
	}
	if len(configuration.Bindings) != 1 {
		t.Fatalf("BindAgent() configuration = %#v", configuration)
	}
	binding := configuration.Bindings[0]
	if binding.Profile != ProfileOrbit || binding.Target != TargetAgent || binding.AgentID != "codex-worker" || binding.ReasoningEffort != "xhigh" {
		t.Fatalf("normalized binding = %#v", binding)
	}

	resolved, err := Resolve(configuration, ProfileOrbit, roster, []modelconfig.Config{model})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Agent.ID != "codex-worker" || resolved.Binding.ReasoningEffort != "xhigh" {
		t.Fatalf("Resolve() = %#v", resolved)
	}

	configuration, err = Reset(configuration, ProfileOrbit)
	if err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if len(configuration.Bindings) != 0 {
		t.Fatalf("Reset() configuration = %#v, want implicit self", configuration)
	}
	resolved, err = Resolve(configuration, ProfileOrbit, roster, []modelconfig.Config{model})
	if err != nil {
		t.Fatalf("Resolve(reset) error = %v", err)
	}
	if resolved.Binding.Target != TargetSelf || resolved.Agent.ID != "" {
		t.Fatalf("Resolve(reset) = %#v, want self", resolved)
	}
}

func TestValidateConfigurationRejectsUnknownOrStaleTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		configuration Configuration
		want          string
	}{
		{
			name: "unknown profile",
			configuration: Configuration{Bindings: []Binding{{
				Profile: "nova", Target: TargetAgent, AgentID: "worker",
			}}},
			want: "unknown profile",
		},
		{
			name: "self is fixed",
			configuration: Configuration{Bindings: []Binding{{
				Profile: ProfileSelf, Target: TargetAgent, AgentID: "worker",
			}}},
			want: "self is fixed",
		},
		{
			name: "duplicate profile",
			configuration: Configuration{Bindings: []Binding{
				{Profile: ProfileOrbit, Target: TargetAgent, AgentID: "worker"},
				{Profile: ProfileOrbit, Target: TargetAgent, AgentID: "worker"},
			}},
			want: "duplicate binding",
		},
		{
			name: "missing target",
			configuration: Configuration{Bindings: []Binding{{
				Profile: ProfileOrbit, AgentID: "worker",
			}}},
			want: "unsupported target",
		},
		{
			name: "unknown Agent",
			configuration: Configuration{Bindings: []Binding{{
				Profile: ProfileOrbit, Target: TargetAgent, AgentID: "missing",
			}}},
			want: "unknown Agent",
		},
	}
	roster := controlagents.Configuration{
		Connections: []controlagents.Connection{{
			ID: "remote", Launcher: controlagents.Launcher{Command: "remote-agent-acp"},
		}},
		Agents: []controlagents.Agent{{
			ID: "worker", Backing: controlagents.AgentBacking{ConnectionID: "remote"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateConfiguration(test.configuration, roster, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateConfiguration() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateConfigurationRestrictsReasoningEffortToModelBackedAgents(t *testing.T) {
	t.Parallel()

	connection := controlagents.Connection{
		ID: "claude", Launcher: controlagents.Launcher{Command: "claude-agent-acp"},
	}
	externalRoster := controlagents.Configuration{
		Connections: []controlagents.Connection{connection},
		Agents: []controlagents.Agent{{
			ID: "claude", Backing: controlagents.AgentBacking{ConnectionID: connection.ID},
		}},
	}
	_, err := BindAgent(Configuration{}, ProfileZenith, "claude", "max", externalRoster, nil)
	if err == nil || !strings.Contains(err.Error(), "external ACP Agent") {
		t.Fatalf("BindAgent(external effort) error = %v", err)
	}

	model := testModel()
	modelRoster := controlagents.Configuration{Agents: []controlagents.Agent{{
		ID: "codex", Backing: controlagents.AgentBacking{ModelAlias: model.ID},
	}}}
	_, err = BindAgent(Configuration{}, ProfileZenith, "codex", "max", modelRoster, []modelconfig.Config{model})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("BindAgent(unsupported effort) error = %v", err)
	}
}

func TestBindAgentCanRepairTheSelectedStaleBinding(t *testing.T) {
	t.Parallel()

	model := testModel()
	roster := controlagents.Configuration{Agents: []controlagents.Agent{{
		ID: "codex", Backing: controlagents.AgentBacking{ModelAlias: model.ID},
	}}}
	stale := Configuration{Bindings: []Binding{{
		Profile: ProfileOrbit, Target: TargetAgent, AgentID: "removed",
	}}}
	repaired, err := BindAgent(stale, ProfileOrbit, "codex", "high", roster, []modelconfig.Config{model})
	if err != nil {
		t.Fatalf("BindAgent(stale) error = %v", err)
	}
	binding, ok := LookupBinding(repaired, ProfileOrbit)
	if !ok || binding.AgentID != "codex" || binding.ReasoningEffort != "high" {
		t.Fatalf("repaired binding = %#v, %v", binding, ok)
	}
}

func TestNormalizeConfigurationOmitsExplicitSelfAndOrdersBindings(t *testing.T) {
	t.Parallel()

	got := NormalizeConfiguration(Configuration{Bindings: []Binding{
		{Profile: ProfileZenith, Target: TargetAgent, AgentID: "deep"},
		{Profile: ProfileBreeze, Target: TargetSelf},
		{Profile: ProfileOrbit, Target: TargetAgent, AgentID: "general"},
	}})
	want := []Binding{
		{Profile: ProfileOrbit, Target: TargetAgent, AgentID: "general"},
		{Profile: ProfileZenith, Target: TargetAgent, AgentID: "deep"},
	}
	if !reflect.DeepEqual(got.Bindings, want) {
		t.Fatalf("NormalizeConfiguration() = %#v, want %#v", got.Bindings, want)
	}
}

func TestResetAgentBindingsRestoresEveryReferenceToSelf(t *testing.T) {
	t.Parallel()

	configuration := Configuration{Bindings: []Binding{
		{Profile: ProfileBreeze, Target: TargetAgent, AgentID: "worker", ReasoningEffort: "low"},
		{Profile: ProfileOrbit, Target: TargetAgent, AgentID: "other"},
		{Profile: ProfileZenith, Target: TargetAgent, AgentID: "WORKER", ReasoningEffort: "xhigh"},
	}}
	next, reset := ResetAgentBindings(configuration, " Worker ")
	if !reflect.DeepEqual(reset, []Profile{ProfileBreeze, ProfileZenith}) {
		t.Fatalf("ResetAgentBindings() reset = %#v", reset)
	}
	if len(next.Bindings) != 1 || next.Bindings[0].Profile != ProfileOrbit || next.Bindings[0].AgentID != "other" {
		t.Fatalf("ResetAgentBindings() configuration = %#v", next)
	}
	for _, profile := range []Profile{ProfileBreeze, ProfileZenith} {
		binding, ok := LookupBinding(next, profile)
		if !ok || binding.Target != TargetSelf {
			t.Fatalf("profile %q binding = %#v, ok=%v, want self", profile, binding, ok)
		}
	}
}

func testModel() modelconfig.Config {
	return modelconfig.NormalizeConfig(modelconfig.Config{
		Provider:            "openai-codex",
		Model:               "gpt-5.6-luna",
		ReasoningMode:       "effort",
		ReasoningLevels:     []string{"low", "medium", "high", "xhigh"},
		ReasoningEffort:     "medium",
		MaxOutputTok:        32768,
		ContextWindowTokens: 258000,
	})
}
