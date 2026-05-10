//go:build e2e

package gatewayapp

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalStackClaudeBuiltInACPE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv("CAELIS_RUN_CLAUDE_ACP_E2E")) != "1" {
		t.Skip("set CAELIS_RUN_CLAUDE_ACP_E2E=1 to run the local Claude Code ACP e2e")
	}
	if _, err := osexec.LookPath("claude-agent-acp"); err != nil {
		if strings.TrimSpace(os.Getenv("CAELIS_CLAUDE_ACP_E2E_INSTALL")) != "1" {
			t.Skip("claude-agent-acp is not on PATH; set CAELIS_CLAUDE_ACP_E2E_INSTALL=1 to allow npm install during e2e")
		}
		if _, err := osexec.LookPath("npm"); err != nil {
			t.Skip("npm is not available")
		}
	}
	t.Setenv("npm_config_cache", filepath.Join(t.TempDir(), "npm-cache"))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	workdir := t.TempDir()
	stack, err := NewLocalStack(Config{
		AppName:        "caelis",
		UserID:         "claude-e2e-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Model: ModelConfig{
			Provider: "ollama",
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	session, err := stack.StartSession(ctx, "", "claude-e2e")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if err := stack.RegisterBuiltinACPAgentWithOptions(ctx, "claude", RegisterBuiltinACPAgentOptions{Install: true}); err != nil {
		t.Fatalf("RegisterBuiltinACPAgentWithOptions(claude, install) error = %v", err)
	}

	const want = "caelis claude acp e2e ok"
	snapshot, err := stack.StartSubagent(ctx, session.SessionRef, "claude", "Reply with exactly: "+want, "claude_e2e")
	if err != nil {
		t.Fatalf("StartSubagent(claude) error = %v", err)
	}
	for snapshot.Running {
		snapshot, err = stack.WaitSubagentTask(ctx, session.SessionRef, snapshot.Ref.TaskID, 5*time.Second)
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
