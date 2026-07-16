package agents

import "testing"

func TestUpsertExternalAgentPreservesSiblingAgentsAndDefaults(t *testing.T) {
	connection := Connection{ID: "claude", Launcher: Launcher{Command: "/usr/bin/claude-acp"}}
	current, opus, err := UpsertExternalAgent(Configuration{}, connection, RemoteModel{ID: "opus", Name: "Opus"}, SessionOptions{
		ModelID: "opus", ConfigValues: map[string]string{"effort": "max"},
	}, DiscoverySnapshot{CWD: "/workspace", SelectedModelID: "opus"}, nil)
	if err != nil {
		t.Fatalf("UpsertExternalAgent(opus) error = %v", err)
	}
	if opus.ID != "claude" || opus.Name != "claude(opus)" {
		t.Fatalf("Opus Agent = %#v, want provider-first identity", opus)
	}
	next, sonnet, err := UpsertExternalAgent(current, connection, RemoteModel{ID: "sonnet", Name: "Sonnet"}, SessionOptions{
		ModelID: "sonnet", ConfigValues: map[string]string{"effort": "high"},
	}, DiscoverySnapshot{CWD: "/workspace", SelectedModelID: "sonnet"}, nil)
	if err != nil {
		t.Fatalf("UpsertExternalAgent(sonnet) error = %v", err)
	}
	if len(next.Agents) != 2 || len(next.Discoveries) != 2 {
		t.Fatalf("upserted roster = %#v, want two Agents and discoveries", next)
	}
	gotOpus, ok := LookupAgent(next, opus.ID)
	if !ok || gotOpus.Defaults.ConfigValues["effort"] != "max" {
		t.Fatalf("preserved Opus = %#v, want max defaults", gotOpus)
	}
	if sonnet.ID == opus.ID || sonnet.Defaults.ConfigValues["effort"] != "high" {
		t.Fatalf("Sonnet = %#v, want distinct high-effort Agent", sonnet)
	}
	if sonnet.ID != "claude-sonnet" || sonnet.Name != "claude(sonnet)" {
		t.Fatalf("Sonnet Agent = %#v, want provider-qualified sibling", sonnet)
	}

	updated, updatedOpus, err := UpsertExternalAgent(next, connection, RemoteModel{ID: "opus", Name: "Opus 4.9"}, SessionOptions{
		ModelID: "opus", ConfigValues: map[string]string{"effort": "xhigh"},
	}, DiscoverySnapshot{CWD: "/workspace", SelectedModelID: "opus"}, nil)
	if err != nil {
		t.Fatalf("UpsertExternalAgent(update opus) error = %v", err)
	}
	if updatedOpus.ID != opus.ID || len(updated.Agents) != 2 || len(updated.Discoveries) != 2 {
		t.Fatalf("updated roster = %#v agent=%#v, want stable additive upsert", updated, updatedOpus)
	}
}

func TestExternalAgentDisplayNameKeepsProviderAsSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		connection Connection
		model      RemoteModel
		want       string
	}{
		{connection: Connection{ID: "claude", Name: "claude"}, model: RemoteModel{ID: "default", Name: "Default (recommended)"}, want: "claude(default-recommended)"},
		{connection: Connection{ID: "codex", Name: "codex"}, model: RemoteModel{ID: "gpt-5.6-sol", Name: "GPT-5.6-Sol"}, want: "codex(gpt-5.6-sol)"},
	}
	for _, test := range tests {
		if got := externalAgentDisplayName(test.connection, test.model); got != test.want {
			t.Fatalf("externalAgentDisplayName(%#v, %#v) = %q, want %q", test.connection, test.model, got, test.want)
		}
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

func TestUpsertModelBackedAgentsUsesDeterministicSlugWithoutFamilyMagic(t *testing.T) {
	next, selected, err := UpsertModelBackedAgents(Configuration{}, []ModelBackingSelection{
		{Alias: "provider/feble-v2", Name: "Feble Experimental", Namespace: "provider"},
	}, nil)
	if err != nil {
		t.Fatalf("UpsertModelBackedAgents() error = %v", err)
	}
	if len(selected) != 1 || selected[0].ID != "feble-experimental" {
		t.Fatalf("selected Agents = %#v, want direct deterministic slug", selected)
	}
	if err := ValidateConfiguration(next); err != nil {
		t.Fatalf("ValidateConfiguration() error = %v", err)
	}
}

func TestUpsertExternalAgentHonorsProductNamePolicy(t *testing.T) {
	_, agent, err := UpsertExternalAgent(Configuration{}, Connection{
		ID: "claude", Launcher: Launcher{Command: "/usr/bin/claude-acp"},
	}, RemoteModel{ID: "review", Name: "review"}, SessionOptions{ModelID: "review"}, DiscoverySnapshot{}, func(name string) bool {
		return name != "review"
	})
	if err != nil {
		t.Fatalf("UpsertExternalAgent() error = %v", err)
	}
	if agent.ID == "review" {
		t.Fatalf("Agent ID = %q, must honor reserved-name policy", agent.ID)
	}
}

func TestResolveDiscoverySelectionRequiresModelScopedSnapshot(t *testing.T) {
	snapshot := DiscoverySnapshot{
		SelectedModelID: "opus",
		Models:          []RemoteModel{{ID: "opus", Name: "Opus"}, {ID: "sonnet", Name: "Sonnet"}},
		ModelControl:    ModelControl{Kind: ModelControlConfigOption, ConfigID: "model"},
		ConfigOptions: []ConfigOption{{
			ID: "effort", Options: []ConfigChoice{{Value: "max"}},
		}, {
			ID: "model", Options: []ConfigChoice{{Value: "opus"}, {Value: "sonnet"}},
		}},
	}
	model, defaults, err := ResolveDiscoverySelection(snapshot, "opus", map[string]string{"effort": "max"})
	if err != nil {
		t.Fatalf("ResolveDiscoverySelection() error = %v", err)
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

func TestUpsertExternalAgentRejectsLauncherChangeWithSiblingAgent(t *testing.T) {
	oldConnection := Connection{ID: "claude", Launcher: Launcher{Command: "/usr/bin/npx", Args: []string{"claude-acp"}}}
	current, _, err := UpsertExternalAgent(Configuration{}, oldConnection, RemoteModel{ID: "opus"}, SessionOptions{ModelID: "opus"}, DiscoverySnapshot{}, nil)
	if err != nil {
		t.Fatalf("UpsertExternalAgent(opus) error = %v", err)
	}
	current, _, err = UpsertExternalAgent(current, oldConnection, RemoteModel{ID: "sonnet"}, SessionOptions{ModelID: "sonnet"}, DiscoverySnapshot{}, nil)
	if err != nil {
		t.Fatalf("UpsertExternalAgent(sonnet) error = %v", err)
	}
	changed := Connection{ID: "claude", Launcher: Launcher{Command: "/usr/local/bin/claude-acp"}}
	if _, _, err := UpsertExternalAgent(current, changed, RemoteModel{ID: "opus"}, SessionOptions{ModelID: "opus"}, DiscoverySnapshot{}, nil); err == nil {
		t.Fatal("UpsertExternalAgent(changed launcher) error = nil with sibling Agent")
	}
}
