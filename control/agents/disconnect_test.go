package agents

import (
	"reflect"
	"testing"
)

func TestListDisconnectCandidatesOnlyIncludesConnectionScopedExternalAgents(t *testing.T) {
	t.Parallel()

	roster := Configuration{
		Connections: []Connection{{ID: "claude", Launcher: Launcher{Command: "claude-agent-acp"}}},
		Agents:      []Agent{{ID: "claude", Name: "Claude", ConnectionID: "claude"}},
	}
	want := []DisconnectCandidate{
		{AgentID: "claude", Name: "Claude", ConnectionID: "claude", LastOnConnection: true},
	}
	if got := ListDisconnectCandidates(roster); !reflect.DeepEqual(got, want) {
		t.Fatalf("ListDisconnectCandidates() = %#v, want %#v", got, want)
	}
}

func TestDisconnectExternalAgentReleasesConnectionAndSiblingModelDiscoveries(t *testing.T) {
	t.Parallel()

	roster := Configuration{
		Connections: []Connection{
			{ID: "claude", Launcher: Launcher{Command: "claude-agent-acp"}},
			{ID: "codex", Launcher: Launcher{Command: "codex-acp"}},
		},
		Agents: []Agent{
			{ID: "claude", ConnectionID: "claude"},
			{ID: "codex", ConnectionID: "codex"},
		},
		Discoveries: []DiscoverySnapshot{
			{ConnectionID: "claude", SelectedModelID: "opus"},
			{ConnectionID: "claude", SelectedModelID: "sonnet"},
			{ConnectionID: "codex", SelectedModelID: "sol"},
		},
	}

	next, result, err := DisconnectExternalAgent(roster, "CLAUDE")
	if err != nil {
		t.Fatalf("DisconnectExternalAgent(claude) error = %v", err)
	}
	if result.Agent.ID != "claude" || result.ConnectionID != "claude" || !result.ConnectionRemoved {
		t.Fatalf("result = %#v, want connection removed", result)
	}
	if _, ok := LookupConnection(next, "claude"); ok {
		t.Fatalf("roster retained disconnected connection: %#v", next)
	}
	if got, want := len(next.Discoveries), 1; got != want || next.Discoveries[0].ConnectionID != "codex" {
		t.Fatalf("discoveries = %#v, want only unrelated codex snapshot", next.Discoveries)
	}
	if _, ok := LookupAgent(next, "codex"); !ok {
		t.Fatalf("final roster removed unrelated Agent: %#v", next.Agents)
	}
}

func TestDisconnectExternalAgentRejectsMultipleAgentIdentitiesForConnection(t *testing.T) {
	t.Parallel()

	roster := Configuration{
		Connections: []Connection{{ID: "claude", Launcher: Launcher{Command: "claude-agent-acp"}}},
		Agents: []Agent{
			{ID: "opus", ConnectionID: "claude"},
			{ID: "sonnet", ConnectionID: "claude"},
		},
	}
	if _, _, err := DisconnectExternalAgent(roster, "opus"); err == nil {
		t.Fatal("DisconnectExternalAgent(multiple identities) error = nil")
	}
}

func TestDisconnectExternalAgentRejectsInvalidOrMissingAgent(t *testing.T) {
	t.Parallel()

	roster := Configuration{Agents: []Agent{{ID: "deepseek"}}}
	if _, _, err := DisconnectExternalAgent(roster, "deepseek"); err == nil {
		t.Fatal("DisconnectExternalAgent(invalid Agent) error = nil")
	}
	if _, _, err := DisconnectExternalAgent(roster, "missing"); err == nil {
		t.Fatal("DisconnectExternalAgent(missing) error = nil")
	}
}
