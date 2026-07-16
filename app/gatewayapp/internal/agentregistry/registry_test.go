package agentregistry

import "testing"

func TestCuratedACPAdaptersMatchZedHandshakeVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pkg     string
		version string
	}{
		{name: "codex", pkg: "@agentclientprotocol/codex-acp", version: "1.1.2"},
		{name: "claude", pkg: "@agentclientprotocol/claude-agent-acp", version: "0.59.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := BuiltinAdapterPackageFor(test.name)
			if !ok {
				t.Fatalf("BuiltinAdapterPackageFor(%q) not found", test.name)
			}
			if got.Package != test.pkg || got.Version != test.version {
				t.Fatalf("BuiltinAdapterPackageFor(%q) = %#v, want %s@%s", test.name, got, test.pkg, test.version)
			}
		})
	}
}

func TestConnectableBuiltInAgentsRestoresNativeACPAgents(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, agent := range ConnectableBuiltInAgents() {
		seen[agent.Name] = true
	}
	for _, name := range []string{"codex", "claude", "opencode", "codefree-o", "grok"} {
		if !seen[name] {
			t.Fatalf("ConnectableBuiltInAgents() = %#v, want %q", seen, name)
		}
	}
	if seen["copilot"] {
		t.Fatalf("ConnectableBuiltInAgents() = %#v, copilot is not in guided onboarding", seen)
	}
}
