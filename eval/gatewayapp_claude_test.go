//go:build e2e

package eval

import (
	"context"
	"os"
	osexec "os/exec"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/gatewayapptest"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestLocalStackClaudeCustomAdapterE2E(t *testing.T) {
	requireClaudeACPE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	workdir := t.TempDir()
	storeDir := privateEvalTempDir(t)
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "claude-e2e-test",
		StoreDir:     storeDir,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
	})
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack() error = %v", err)
	}
	activeSession := startEvalSession(t, ctx, stack, "")
	connectClaudeAgentForE2E(ctx, t, stack, activeSession, storeDir)

	const want = "caelis claude acp e2e ok"
	driver := newEvalAppServerAdapter(t, stack, activeSession, "claude_e2e")
	turn, err := driver.StartAgentRun(ctx, string(agentbinding.HandleZenith), "Reply with exactly: "+want, nil)
	if err != nil {
		t.Fatalf("StartAgentRun(zenith) error = %v", err)
	}
	defer turn.Close()
	var assistant schema.FinalAssistantAccumulator
	terminalState := ""
	for envelope := range turn.Events() {
		if envelope.Update != nil {
			assistant.ObserveUpdate(envelope.Update)
		}
		if eventstream.IsTurnTerminalLifecycle(envelope) {
			terminalState = envelope.Lifecycle.State
		}
	}
	if terminalState != eventstream.LifecycleStateCompleted {
		t.Fatalf("Claude participant terminal state = %q, want completed", terminalState)
	}
	result := strings.TrimSpace(assistant.FinalText())
	if !strings.Contains(result, want) {
		t.Fatalf("claude result = %q, want %q", result, want)
	}
}

func TestLocalStackClaudeACPMainResumeOrNewE2E(t *testing.T) {
	requireClaudeACPE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	workdir := t.TempDir()
	storeDir := privateEvalTempDir(t)
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "claude-resume-e2e-test",
		StoreDir:     storeDir,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
	})
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack() error = %v", err)
	}
	activeSession := startEvalSession(t, ctx, stack, "")
	claudeAgent := connectClaudeAgentForE2E(ctx, t, stack, activeSession, storeDir)
	updated := handoffEvalController(t, ctx, stack, activeSession, claudeAgent, "claude-acp-e2e")
	if updated.Controller.Kind != session.ControllerKindACP || strings.TrimSpace(updated.Controller.RemoteSessionID) == "" {
		t.Fatalf("controller binding = %#v, want Claude ACP remote session", updated.Controller)
	}
	firstRemoteSessionID := strings.TrimSpace(updated.Controller.RemoteSessionID)

	const marker = "caelis claude acp resume e2e"
	const wantFirst = marker + " first ok"
	prompt := "Reply with exactly this text and no markdown: " + wantFirst
	result, err := runEvalHeadlessOnce(t, ctx, stack, activeSession, prompt, headless.Options{})
	if err != nil {
		t.Fatalf("RunSessionOnce(claude) error = %v", err)
	}
	if !strings.Contains(strings.TrimSpace(result.Output), marker) {
		t.Fatalf("RunSessionOnce(first Claude turn) output = %q, want marker %q", result.Output, marker)
	}

	resumed := handoffEvalController(t, ctx, stack, activeSession, claudeAgent, "claude-acp-resume-e2e")
	resumedRemoteSessionID := strings.TrimSpace(resumed.Controller.RemoteSessionID)
	if resumedRemoteSessionID == "" {
		t.Fatalf("resumed controller binding = %#v, want non-empty Claude ACP remote session", resumed.Controller)
	}
	if resumedRemoteSessionID != firstRemoteSessionID {
		t.Logf("Claude ACP returned a new remote session on second handoff: old=%s new=%s", firstRemoteSessionID, resumedRemoteSessionID)
	}

	const wantSecond = marker + " second ok"
	result, err = runEvalHeadlessOnce(t, ctx, stack, activeSession, "Reply with exactly this text and no markdown: "+wantSecond, headless.Options{})
	if err != nil {
		t.Fatalf("RunSessionOnce(second Claude turn) error = %v", err)
	}
	if output := strings.TrimSpace(result.Output); output == "" {
		t.Log("Claude ACP second turn completed without assistant text after resume/new handoff")
	} else if !strings.Contains(output, marker) {
		t.Logf("Claude ACP second turn output after resume/new handoff = %q", result.Output)
	}
}

func requireClaudeACPE2E(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("CAELIS_RUN_CLAUDE_ACP_E2E")) != "1" {
		t.Skip("set CAELIS_RUN_CLAUDE_ACP_E2E=1 to run the local Claude Code ACP e2e")
	}
	if _, err := osexec.LookPath("claude-agent-acp"); err != nil {
		t.Skip("claude-agent-acp is not available on PATH")
	}
}

func connectClaudeAgentForE2E(
	ctx context.Context,
	t *testing.T,
	stack *gatewayapp.Stack,
	active session.Session,
	storeDir string,
) string {
	t.Helper()
	driver := newEvalAppServerAdapter(t, stack, active, "claude-acp-e2e")
	launcher := controlagents.LauncherChoiceCommand
	req := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: launcher, CommandLine: "claude-agent-acp", CWD: stack.Workspace().CWD,
	}
	discovered, err := driver.DiscoverACPConnection(ctx, req)
	if err != nil {
		t.Fatalf("DiscoverACPConnection(claude, %s) error = %v", launcher, err)
	}
	if len(discovered.Models) == 0 {
		t.Fatal("Claude ACP discovery returned no selectable models")
	}
	req.ModelID = discovered.Models[0].ID
	connected, err := driver.ConnectACP(ctx, req)
	if err != nil {
		t.Fatalf("ConnectACP(claude, %s) error = %v", launcher, err)
	}
	if len(connected.Profiles) != 1 {
		t.Fatalf("ConnectACP(claude) profiles = %#v, want one", connected.Profiles)
	}
	doc, err := gatewayapp.LoadAppConfig(storeDir)
	if err != nil {
		t.Fatalf("LoadAppConfig(claude) error = %v", err)
	}
	profile, ok := modelprofile.Lookup(doc.ModelProfiles, connected.Profiles[0].ID)
	if !ok {
		t.Fatalf("ConnectACP(claude) profile %q was not persisted", connected.Profiles[0].ID)
	}
	if profile.Backend.ACP == nil ||
		strings.TrimSpace(profile.Backend.ACP.AgentID) == "" ||
		profile.Backend.ACP.RemoteModelID != req.ModelID {
		t.Fatalf("ConnectACP(claude) profile = %#v, want ACP backend for model %q", profile, req.ModelID)
	}
	if profile.Effort.DefaultEffort == "" || !profile.SupportsEffort(profile.Effort.DefaultEffort) {
		t.Fatalf("ConnectACP(claude) effort defaults = %#v, want supported default", profile.Effort)
	}
	if _, err := gatewayapptest.BindAgentBinding(ctx, stack, agentbinding.Binding{
		Handle: agentbinding.HandleZenith, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort,
	}); err != nil {
		t.Fatalf("BindAgentBinding(zenith, Claude) error = %v", err)
	}
	return profile.Backend.ACP.AgentID
}
