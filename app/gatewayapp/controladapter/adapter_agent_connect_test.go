package controladapter

import (
	"context"
	"strings"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func TestCompleteConnectACPLaunchersDistinguishesGlobalAndManaged(t *testing.T) {
	candidates := completeConnectACPLaunchers("claude", "", 10)
	if len(candidates) == 0 || candidates[0].Value != "managed" || !strings.Contains(candidates[0].Display, "Recommended") || !strings.Contains(candidates[0].Detail, "safe to cancel or retry") {
		t.Fatalf("launcher candidates = %#v, want productized managed recommendation first", candidates)
	}
	for _, want := range []string{"npx", "global", "managed"} {
		if !slashCandidatesHaveValue(candidates, want) {
			t.Fatalf("launcher candidates = %#v, want %q", candidates, want)
		}
	}
	if slashCandidatesHaveValue(candidates, "path") {
		t.Fatalf("launcher candidates = %#v, global install must not be mislabeled as PATH", candidates)
	}
}

func TestCompleteConnectACPIncludesRegistryAndNativeAgentCatalog(t *testing.T) {
	candidates := completeConnectACPAgents("", 20)
	for _, want := range []string{"codex", "claude", "copilot", "gemini", "grok", "opencode", "factory-droid", "custom"} {
		if !slashCandidatesHaveValue(candidates, want) {
			t.Fatalf("ACP Agent candidates = %#v, want %q", candidates, want)
		}
	}
	qwen := completeConnectACPAgents("qwen", 10)
	if len(qwen) != 1 || qwen[0].Value != "qwen-code" || !strings.Contains(qwen[0].Detail, "ACP Registry v0.21.0") {
		t.Fatalf("qwen registry candidate = %#v, want searchable Registry metadata", qwen)
	}
	launchers := completeConnectACPLaunchers("opencode", "", 10)
	if len(launchers) != 1 || launchers[0].Value != "installed" {
		t.Fatalf("opencode launchers = %#v, want installed only", launchers)
	}
	grok := completeConnectACPLaunchers("grok", "", 10)
	if len(grok) != 2 || grok[0].Value != "npx" || grok[1].Value != "installed" {
		t.Fatalf("grok launchers = %#v, want Registry npx and installed command", grok)
	}
	factory := completeConnectACPLaunchers("factory-droid", "", 10)
	if len(factory) != 1 || factory[0].Value != "npx" || !strings.Contains(factory[0].Display, "Recommended") {
		t.Fatalf("factory-droid launchers = %#v, want Registry npx", factory)
	}
}

func TestAdapterListsControlOwnedDisconnectCandidates(t *testing.T) {
	t.Parallel()

	driver := &assembler{stack: &RuntimeStack{Agent: AgentRuntimeDeps{
		DisconnectCandidatesFn: func(context.Context) ([]controlagents.DisconnectCandidate, error) {
			return []controlagents.DisconnectCandidate{{AgentID: "codex", Name: "Codex", ConnectionID: "codex", LastOnConnection: true}}, nil
		},
	}}}

	candidates, err := driver.DisconnectCandidates(context.Background())
	if err != nil {
		t.Fatalf("DisconnectCandidates() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].AgentID != "codex" {
		t.Fatalf("DisconnectCandidates() = %#v", candidates)
	}
	sources, err := driver.CompleteSlashArg(context.Background(), "connect", "", 10)
	if err != nil || !slashCandidatesHaveValue(sources, "disconnect") {
		t.Fatalf("CompleteSlashArg(connect) = %#v, err=%v, want disconnect entry", sources, err)
	}
	agents, err := driver.CompleteSlashArg(context.Background(), "connect-disconnect-agent", "", 10)
	if err != nil || len(agents) != 1 || agents[0].Value != "codex" || agents[0].Display != "/codex" {
		t.Fatalf("CompleteSlashArg(disconnect Agent) = %#v, err=%v", agents, err)
	}
	confirm, err := driver.CompleteSlashArg(context.Background(), "connect-disconnect-confirm:codex", "", 10)
	if err != nil || len(confirm) != 1 || confirm[0].Value != "confirm" || !strings.Contains(confirm[0].Detail, "installed adapter") {
		t.Fatalf("CompleteSlashArg(disconnect confirm) = %#v, err=%v", confirm, err)
	}
}
