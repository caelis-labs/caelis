package agents

import (
	"testing"
)

func TestConfigurationRosterResolvesStableAgentPlacement(t *testing.T) {
	t.Parallel()

	cfg := Configuration{
		Connections: []Connection{{
			ID:   "Claude",
			Name: "Claude ACP",
			Launcher: Launcher{
				Kind:    LaunchKindPackageExec,
				Command: "npx",
				Args:    []string{"-y", "claude-agent-acp"},
			},
		}},
		Agents: []Agent{{
			ID:      "Opus",
			Name:    "Opus",
			Backing: AgentBacking{ConnectionID: "Claude"},
			Defaults: SessionOptions{
				ModelID:      "claude-opus-4-8",
				ConfigValues: map[string]string{"effort": "max"},
			},
		}},
	}
	if err := ValidateConfiguration(cfg); err != nil {
		t.Fatalf("ValidateConfiguration() error = %v", err)
	}
	cfg = NormalizeConfiguration(cfg)

	agent, connection, err := ResolveAgent(cfg, "OPUS")
	if err != nil {
		t.Fatalf("ResolveAgent() error = %v", err)
	}
	if agent.ID != "opus" || connection.ID != "claude" {
		t.Fatalf("ResolveAgent() = %#v %#v", agent, connection)
	}
	if agent.Defaults.ModelID != "claude-opus-4-8" || agent.Defaults.ConfigValues["effort"] != "max" {
		t.Fatalf("ResolveAgent().Defaults = %#v", agent.Defaults)
	}

	listed := ListAgents(cfg)
	listed[0].Defaults.ConfigValues["effort"] = "low"
	again, _, err := ResolveAgent(cfg, "opus")
	if err != nil {
		t.Fatalf("ResolveAgent(second) error = %v", err)
	}
	if again.Defaults.ConfigValues["effort"] != "max" {
		t.Fatalf("ListAgents() leaked mutable defaults: %#v", again.Defaults)
	}
}

func TestConfigurationRejectsUnknownAgentConnection(t *testing.T) {
	t.Parallel()

	err := ValidateConfiguration(Configuration{Agents: []Agent{{ID: "opus", Backing: AgentBacking{ConnectionID: "missing"}}}})
	if err == nil {
		t.Fatal("ValidateConfiguration() error = nil, want unknown connection")
	}
}

func TestConfigurationRejectsDuplicateAgentIdentity(t *testing.T) {
	t.Parallel()

	err := ValidateConfiguration(Configuration{
		Connections: []Connection{
			{ID: "claude", Launcher: Launcher{Command: "claude-acp"}},
			{ID: "codex", Launcher: Launcher{Command: "codex-acp"}},
		},
		Agents: []Agent{
			{ID: "reviewer", Backing: AgentBacking{ConnectionID: "claude"}},
			{ID: "REVIEWER", Backing: AgentBacking{ConnectionID: "codex"}},
		},
	})
	if err == nil {
		t.Fatal("ValidateConfiguration() error = nil, want duplicate Agent identity")
	}
}

func TestConfigurationAcceptsConfiguredModelBacking(t *testing.T) {
	t.Parallel()

	cfg := Configuration{Agents: []Agent{{
		ID:      "deepseek",
		Backing: AgentBacking{ModelAlias: "deepseek@default/deepseek/deepseek-v4-pro"},
	}}}
	if err := ValidateConfiguration(cfg); err != nil {
		t.Fatalf("ValidateConfiguration() error = %v", err)
	}
	agent, connection, err := ResolveAgent(cfg, "deepseek")
	if err != nil {
		t.Fatalf("ResolveAgent() error = %v", err)
	}
	if agent.Backing.ModelAlias == "" || connection.ID != "" {
		t.Fatalf("ResolveAgent() = %#v %#v, want configured-model backing", agent, connection)
	}
}

func TestConfigurationRejectsAmbiguousOrMissingBacking(t *testing.T) {
	t.Parallel()

	for _, agent := range []Agent{
		{ID: "missing"},
		{ID: "ambiguous", Backing: AgentBacking{ModelAlias: "model", ConnectionID: "acp"}},
	} {
		err := ValidateConfiguration(Configuration{
			Connections: []Connection{{ID: "acp", Launcher: Launcher{Command: "acp"}}},
			Agents:      []Agent{agent},
		})
		if err == nil {
			t.Fatalf("ValidateConfiguration(%#v) error = nil, want exactly-one rejection", agent)
		}
	}
}

func TestLaunchFingerprintIsStableAndChangesWithLauncher(t *testing.T) {
	t.Parallel()

	base := Launcher{Kind: LaunchKindPackageExec, Command: "npx", Args: []string{"-y", "codex-acp"}, Env: map[string]string{"B": "2", "A": "1"}}
	reordered := Launcher{Kind: LaunchKindPackageExec, Command: "npx", Args: []string{"-y", "codex-acp"}, Env: map[string]string{"A": "1", "B": "2"}}
	if LaunchFingerprint(base) != LaunchFingerprint(reordered) {
		t.Fatal("LaunchFingerprint() changed with map iteration order")
	}
	changed := base
	changed.Args = []string{"-y", "claude-agent-acp"}
	if LaunchFingerprint(base) == LaunchFingerprint(changed) {
		t.Fatal("LaunchFingerprint() did not change with args")
	}
}
