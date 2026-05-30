package agents

import "testing"

func TestBuiltinACPAgentsExposeRegisterableDescriptors(t *testing.T) {
	agents := BuiltinACPAgents()
	if len(agents) == 0 {
		t.Fatal("BuiltinACPAgents() = nil, want registerable agents")
	}
	seen := map[string]struct{}{}
	for _, agent := range agents {
		if agent.Name == "" || agent.Command == "" {
			t.Fatalf("agent = %#v, want name and command", agent)
		}
		if _, ok := seen[agent.Name]; ok {
			t.Fatalf("duplicate builtin agent %q", agent.Name)
		}
		seen[agent.Name] = struct{}{}
	}
	if _, ok := seen["codex"]; !ok {
		t.Fatalf("builtins = %#v, want codex", agents)
	}
	claude, ok := LookupBuiltinACPAgent("claude")
	if !ok || claude.Command != "npx" || len(claude.Args) == 0 {
		t.Fatalf("LookupBuiltinACPAgent(claude) = %#v ok=%v, want npx adapter", claude, ok)
	}
}
