package agents

import (
	"reflect"
	"testing"
)

func TestListDisconnectCandidatesOnlyIncludesExternalRosterAgents(t *testing.T) {
	t.Parallel()

	roster := Configuration{
		Connections: []Connection{{ID: "claude", Launcher: Launcher{Command: "claude-agent-acp"}}},
		Agents: []Agent{
			{ID: "opus", Name: "Claude Opus", Backing: AgentBacking{ConnectionID: "claude"}, Defaults: SessionOptions{ModelID: "opus"}},
			{ID: "sonnet", Name: "Claude Sonnet", Backing: AgentBacking{ConnectionID: "claude"}, Defaults: SessionOptions{ModelID: "sonnet"}},
			{ID: "deepseek", Name: "DeepSeek", Backing: AgentBacking{ModelAlias: "deepseek"}},
		},
	}
	want := []DisconnectCandidate{
		{AgentID: "opus", Name: "Claude Opus", ConnectionID: "claude", SiblingCount: 1},
		{AgentID: "sonnet", Name: "Claude Sonnet", ConnectionID: "claude", SiblingCount: 1},
	}
	if got := ListDisconnectCandidates(roster); !reflect.DeepEqual(got, want) {
		t.Fatalf("ListDisconnectCandidates() = %#v, want %#v", got, want)
	}
}

func TestDisconnectExternalAgentReleasesSharedConnectionOnlyAfterLastReference(t *testing.T) {
	t.Parallel()

	roster := Configuration{
		Connections: []Connection{
			{ID: "claude", Launcher: Launcher{Command: "claude-agent-acp"}},
			{ID: "codex", Launcher: Launcher{Command: "codex-acp"}},
		},
		Agents: []Agent{
			{ID: "opus", Backing: AgentBacking{ConnectionID: "claude"}, Defaults: SessionOptions{ModelID: "opus"}},
			{ID: "sonnet", Backing: AgentBacking{ConnectionID: "claude"}, Defaults: SessionOptions{ModelID: "sonnet"}},
			{ID: "codex", Backing: AgentBacking{ConnectionID: "codex"}, Defaults: SessionOptions{ModelID: "sol"}},
		},
		Discoveries: []DiscoverySnapshot{
			{ConnectionID: "claude", SelectedModelID: "opus"},
			{ConnectionID: "claude", SelectedModelID: "sonnet"},
			{ConnectionID: "codex", SelectedModelID: "sol"},
		},
	}

	next, first, err := DisconnectExternalAgent(roster, "OPUS")
	if err != nil {
		t.Fatalf("DisconnectExternalAgent(opus) error = %v", err)
	}
	if first.Agent.ID != "opus" || first.ConnectionID != "claude" || first.ConnectionRemoved {
		t.Fatalf("first result = %#v, want shared connection retained", first)
	}
	if _, ok := LookupConnection(next, "claude"); !ok || len(next.Discoveries) != 2 {
		t.Fatalf("shared roster = %#v, want connection retained and removed model discovery released", next)
	}
	for _, snapshot := range next.Discoveries {
		if snapshot.ConnectionID == "claude" && snapshot.SelectedModelID != "sonnet" {
			t.Fatalf("shared discoveries = %#v, want only sonnet on claude", next.Discoveries)
		}
	}

	next, last, err := DisconnectExternalAgent(next, "sonnet")
	if err != nil {
		t.Fatalf("DisconnectExternalAgent(sonnet) error = %v", err)
	}
	if !last.ConnectionRemoved {
		t.Fatalf("last result = %#v, want connection released", last)
	}
	if _, ok := LookupConnection(next, "claude"); ok {
		t.Fatalf("final roster still contains released connection: %#v", next.Connections)
	}
	if got, want := len(next.Discoveries), 1; got != want || next.Discoveries[0].ConnectionID != "codex" {
		t.Fatalf("discoveries = %#v, want only unrelated codex snapshot", next.Discoveries)
	}
	if _, ok := LookupAgent(next, "codex"); !ok {
		t.Fatalf("final roster removed unrelated Agent: %#v", next.Agents)
	}
}

func TestDisconnectExternalAgentRejectsModelBackedAgent(t *testing.T) {
	t.Parallel()

	roster := Configuration{Agents: []Agent{{ID: "deepseek", Backing: AgentBacking{ModelAlias: "deepseek"}}}}
	if _, _, err := DisconnectExternalAgent(roster, "deepseek"); err == nil {
		t.Fatal("DisconnectExternalAgent(model-backed) error = nil")
	}
	if _, _, err := DisconnectExternalAgent(roster, "missing"); err == nil {
		t.Fatal("DisconnectExternalAgent(missing) error = nil")
	}
}
