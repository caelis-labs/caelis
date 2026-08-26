package controladapter

import (
	"context"
	"strings"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func TestCompleteConnectACPIncludesOnlyNativeAgentCatalogAndCustomCommand(t *testing.T) {
	candidates := completeConnectACPAgents("", 20)
	nativeAgents := []string{
		"grok", "kimi", "opencode", "copilot", "qoder", "gemini", "qwen-code",
		"auggie", "cline", "factory-droid", "goose", "kilo",
	}
	for _, want := range append(append([]string(nil), nativeAgents...), "custom") {
		if !slashCandidatesHaveValue(candidates, want) {
			t.Fatalf("ACP Agent candidates = %#v, want %q", candidates, want)
		}
	}
	for _, removed := range []string{"codex", "claude", "deepagents", "glm-acp-agent", "pi-acp"} {
		if slashCandidatesHaveValue(candidates, removed) {
			t.Fatalf("ACP Agent candidates = %#v, removed Registry entry %q remains", candidates, removed)
		}
	}
	for _, native := range nativeAgents {
		launchers := completeConnectACPLaunchers(native, "", 10)
		if len(launchers) != 1 || launchers[0].Value != "installed" || !strings.Contains(launchers[0].Display, "Recommended") {
			t.Fatalf("%s launchers = %#v, want installed command only", native, launchers)
		}
	}
	qoder := completeConnectACPLaunchers("qoder", "", 10)
	if len(qoder) != 1 || !strings.Contains(qoder[0].Detail, `"qoder" or "qodercli"`) {
		t.Fatalf("qoder launchers = %#v, want both official PATH command names", qoder)
	}
	custom := completeConnectACPLaunchers("custom", "", 10)
	if len(custom) != 1 || custom[0].Value != "command" {
		t.Fatalf("custom launchers = %#v, want custom command only", custom)
	}
}

func TestAdapterListsControlOwnedDisconnectCandidates(t *testing.T) {
	t.Parallel()

	driver := &assembler{deps: &runtimeDeps{Agent: AgentRuntimeDeps{
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
