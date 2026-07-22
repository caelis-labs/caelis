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
			ID:           "Claude",
			Name:         "Claude",
			ConnectionID: "Claude",
		}},
	}
	if err := ValidateConfiguration(cfg); err != nil {
		t.Fatalf("ValidateConfiguration() error = %v", err)
	}
	cfg = NormalizeConfiguration(cfg)

	agent, connection, err := ResolveAgent(cfg, "CLAUDE")
	if err != nil {
		t.Fatalf("ResolveAgent() error = %v", err)
	}
	if agent.ID != "claude" || connection.ID != "claude" {
		t.Fatalf("ResolveAgent() = %#v %#v", agent, connection)
	}

	listed := ListAgents(cfg)
	listed[0].Name = "changed"
	again, _, err := ResolveAgent(cfg, "claude")
	if err != nil {
		t.Fatalf("ResolveAgent(second) error = %v", err)
	}
	if again.Name != "Claude" {
		t.Fatalf("ListAgents() leaked mutation: %#v", again)
	}
}

func TestConfigurationRejectsUnknownAgentConnection(t *testing.T) {
	t.Parallel()

	err := ValidateConfiguration(Configuration{Agents: []Agent{{ID: "opus", ConnectionID: "missing"}}})
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
			{ID: "reviewer", ConnectionID: "claude"},
			{ID: "REVIEWER", ConnectionID: "codex"},
		},
	})
	if err == nil {
		t.Fatal("ValidateConfiguration() error = nil, want duplicate Agent identity")
	}
}

func TestConfigurationRejectsMissingConnection(t *testing.T) {
	t.Parallel()

	agent := Agent{ID: "missing"}
	err := ValidateConfiguration(Configuration{Agents: []Agent{agent}})
	if err == nil {
		t.Fatalf("ValidateConfiguration(%#v) error = nil, want connection rejection", agent)
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
