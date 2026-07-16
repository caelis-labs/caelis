//go:build e2e

package eval

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestLocalStackClaudeBuiltInACPE2E(t *testing.T) {
	requireClaudeACPE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	workdir := t.TempDir()
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "claude-e2e-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "", "claude-e2e")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	claudeAgent := connectClaudeAgentForE2E(ctx, t, stack)

	const want = "caelis claude acp e2e ok"
	snapshot, err := stack.StartSubagent(ctx, activeSession.SessionRef, claudeAgent, "Reply with exactly: "+want, "claude_e2e")
	if err != nil {
		t.Fatalf("StartSubagent(claude) error = %v", err)
	}
	for snapshot.Running {
		snapshot, err = stack.WaitSubagentTask(ctx, activeSession.SessionRef, snapshot.Ref.TaskID, 5*time.Second)
		if err != nil {
			t.Fatalf("WaitSubagentTask(claude) error = %v", err)
		}
	}
	if strings.TrimSpace(string(snapshot.State)) != "completed" {
		t.Fatalf("claude snapshot state = %s result = %#v", snapshot.State, snapshot.Result)
	}
	result := strings.TrimSpace(fmt.Sprint(snapshot.Result["result"]))
	if !strings.Contains(result, want) {
		t.Fatalf("claude result = %q, want %q", result, want)
	}
}

func TestLocalStackClaudeACPMainResumeOrNewE2E(t *testing.T) {
	requireClaudeACPE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	workdir := t.TempDir()
	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "claude-resume-e2e-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "", "claude-resume-e2e")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	claudeAgent := connectClaudeAgentForE2E(ctx, t, stack)
	updated, err := stack.KernelControlPlane().HandoffController(ctx, gateway.HandoffControllerRequest{
		SessionRef: activeSession.SessionRef,
		Kind:       session.ControllerKindACP,
		Agent:      claudeAgent,
		Source:     "test",
		Reason:     "exercise real Claude ACP session/resume",
	})
	if err != nil {
		t.Fatalf("HandoffController(claude) error = %v", err)
	}
	if updated.Controller.Kind != session.ControllerKindACP || strings.TrimSpace(updated.Controller.RemoteSessionID) == "" {
		t.Fatalf("controller binding = %#v, want Claude ACP remote session", updated.Controller)
	}
	firstRemoteSessionID := strings.TrimSpace(updated.Controller.RemoteSessionID)

	const marker = "caelis claude acp resume e2e"
	const wantFirst = marker + " first ok"
	prompt := "Reply with exactly this text and no markdown: " + wantFirst
	driver, err := local.NewLocalAdapterForSession(ctx, stack, updated, "headless-claude-acp-resume-e2e", "")
	if err != nil {
		t.Fatalf("NewLocalAdapterForSession(first) error = %v", err)
	}
	result, err := headless.RunOnce(ctx, driver, control.Submission{Text: prompt}, headless.Options{})
	if err != nil {
		t.Fatalf("RunOnce(claude) error = %v", err)
	}
	if !strings.Contains(strings.TrimSpace(result.Output), marker) {
		t.Fatalf("RunOnce(first Claude turn) output = %q, want marker %q", result.Output, marker)
	}

	resumed, err := stack.KernelControlPlane().HandoffController(ctx, gateway.HandoffControllerRequest{
		SessionRef: activeSession.SessionRef,
		Kind:       session.ControllerKindACP,
		Agent:      claudeAgent,
		Source:     "test-resume",
		Reason:     "resume existing Claude ACP remote session",
	})
	if err != nil {
		t.Fatalf("HandoffController(claude resume) error = %v", err)
	}
	resumedRemoteSessionID := strings.TrimSpace(resumed.Controller.RemoteSessionID)
	if resumedRemoteSessionID == "" {
		t.Fatalf("resumed controller binding = %#v, want non-empty Claude ACP remote session", resumed.Controller)
	}
	if resumedRemoteSessionID != firstRemoteSessionID {
		t.Logf("Claude ACP returned a new remote session on second handoff: old=%s new=%s", firstRemoteSessionID, resumedRemoteSessionID)
	}

	const wantSecond = marker + " second ok"
	driver, err = local.NewLocalAdapterForSession(ctx, stack, resumed, "headless-claude-acp-resume-e2e", "")
	if err != nil {
		t.Fatalf("NewLocalAdapterForSession(second) error = %v", err)
	}
	result, err = headless.RunOnce(ctx, driver, control.Submission{Text: "Reply with exactly this text and no markdown: " + wantSecond}, headless.Options{})
	if err != nil {
		t.Fatalf("RunOnce(second Claude turn) error = %v", err)
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
	if _, err := osexec.LookPath("npx"); err != nil {
		t.Skip("npx is not available")
	}
	if strings.TrimSpace(os.Getenv("CAELIS_CLAUDE_ACP_E2E_INSTALL")) == "1" {
		if _, err := osexec.LookPath("npm"); err != nil {
			t.Skip("npm is not available")
		}
	}
	if strings.TrimSpace(os.Getenv("npm_config_cache")) == "" {
		t.Setenv("npm_config_cache", filepath.Join(os.TempDir(), "caelis-npm-cache"))
	}
}

func connectClaudeAgentForE2E(ctx context.Context, t *testing.T, stack *gatewayapp.Stack) string {
	t.Helper()
	launcher := controlagents.LauncherChoiceNPX
	if strings.TrimSpace(os.Getenv("CAELIS_CLAUDE_ACP_E2E_INSTALL")) == "1" {
		launcher = controlagents.LauncherChoiceManaged
	}
	req := controlagents.ConnectRequest{AdapterID: "claude", Launcher: launcher, CWD: stack.Workspace.CWD}
	discovered, err := stack.DiscoverACPConnection(ctx, req)
	if err != nil {
		t.Fatalf("DiscoverACPConnection(claude, %s) error = %v", launcher, err)
	}
	if len(discovered.Models) == 0 {
		t.Fatal("Claude ACP discovery returned no selectable models")
	}
	req.ModelID = discovered.Models[0].ID
	req.Discovery = &discovered
	connected, err := stack.ConnectACP(ctx, req)
	if err != nil {
		t.Fatalf("ConnectACP(claude, %s) error = %v", launcher, err)
	}
	if len(connected.Agents) != 1 {
		t.Fatalf("ConnectACP(claude) Agents = %#v, want one", connected.Agents)
	}
	return connected.Agents[0].ID
}
