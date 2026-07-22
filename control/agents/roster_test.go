package agents

import "testing"

func TestNormalizeDiscoverySnapshotClassifiesReasoningEffortOnce(t *testing.T) {
	snapshot := NormalizeDiscoverySnapshot(DiscoverySnapshot{ConfigOptions: []ConfigOption{
		{ID: "thought_level", Name: "Thought depth", Category: "reasoning"},
		{ID: "theme", Name: "Theme", Category: "display"},
	}})
	if len(snapshot.ConfigOptions) != 2 {
		t.Fatalf("ConfigOptions = %#v", snapshot.ConfigOptions)
	}
	if snapshot.ConfigOptions[0].Purpose != ConfigOptionPurposeReasoningEffort || snapshot.ConfigOptions[1].Purpose != "" {
		t.Fatalf("classified options = %#v", snapshot.ConfigOptions)
	}
}

func TestUpsertExternalConnectionKeepsOneAgentAcrossModelDiscoveries(t *testing.T) {
	connection := Connection{ID: "claude", Name: "Claude", Launcher: Launcher{Command: "/usr/bin/claude-acp"}}
	current, first, err := UpsertExternalConnection(Configuration{}, connection, DiscoverySnapshot{
		CWD: "/workspace", SelectedModelID: "opus",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	next, second, err := UpsertExternalConnection(current, connection, DiscoverySnapshot{
		CWD: "/workspace", SelectedModelID: "sonnet",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(next.Agents) != 1 || len(next.Discoveries) != 2 {
		t.Fatalf("external connection = first:%#v second:%#v config:%#v", first, second, next)
	}
	if next.Agents[0].ConnectionID != "claude" {
		t.Fatalf("external Agent connection = %#v", next.Agents[0])
	}
}

func TestUpsertExternalConnectionHonorsProductNamePolicy(t *testing.T) {
	_, agent, err := UpsertExternalConnection(Configuration{}, Connection{
		ID: "review", Name: "review", Launcher: Launcher{Command: "/usr/bin/review-acp"},
	}, DiscoverySnapshot{}, func(name string) bool { return name != "review" })
	if err != nil {
		t.Fatal(err)
	}
	if agent.ID == "review" {
		t.Fatalf("Agent ID = %q, must honor reserved-name policy", agent.ID)
	}
}

func TestCustomConnectionIDUsesCompleteLauncher(t *testing.T) {
	one := Launcher{Command: "/one/bin/acp", Args: []string{"--profile", "one"}}
	two := Launcher{Command: "/two/bin/acp", Args: []string{"--profile", "two"}}
	if got, want := CustomConnectionID(one.Command, one), CustomConnectionID(two.Command, two); got == want {
		t.Fatalf("CustomConnectionID() collision = %q for distinct launchers", got)
	}
	if got := CustomConnectionID(one.Command, one); got != CustomConnectionID(one.Command, one) {
		t.Fatalf("CustomConnectionID() is unstable: %q", got)
	}
}

func TestResolveDiscoverySelectionRequiresModelScopedSnapshot(t *testing.T) {
	snapshot := DiscoverySnapshot{
		SelectedModelID: "opus",
		Models:          []RemoteModel{{ID: "opus", Name: "Opus"}, {ID: "sonnet", Name: "Sonnet"}},
		ModelControl:    ModelControl{Kind: ModelControlConfigOption, ConfigID: "model"},
		ConfigOptions: []ConfigOption{
			{ID: "effort", Options: []ConfigChoice{{Value: "max"}}},
			{ID: "model", Options: []ConfigChoice{{Value: "opus"}, {Value: "sonnet"}}},
		},
	}
	model, defaults, err := ResolveDiscoverySelection(snapshot, "opus", map[string]string{"effort": "max"})
	if err != nil {
		t.Fatal(err)
	}
	if model.ID != "opus" || defaults.ModelID != "opus" || defaults.ConfigValues["effort"] != "max" {
		t.Fatalf("selection = model:%#v defaults:%#v", model, defaults)
	}
	if _, _, err := ResolveDiscoverySelection(snapshot, "sonnet", nil); err == nil {
		t.Fatal("ResolveDiscoverySelection(sonnet) error = nil for opus-scoped snapshot")
	}
	if _, _, err := ResolveDiscoverySelection(snapshot, "opus", map[string]string{"model": "sonnet"}); err == nil {
		t.Fatal("ResolveDiscoverySelection(model default) error = nil for duplicated model selection")
	}
}
