package controladapter

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func newCommandExecDriver(t *testing.T, modelCfg gatewayapp.ModelConfig) (*assembler, *gatewayapp.Stack) {
	t.Helper()
	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "cmd-exec-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: "cmd-exec-workspace",
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Sandbox:      gatewayapp.SandboxConfig{RequestedType: "host"},
		Model:        modelCfg,
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAssemblerFromGatewayAppSession(ctx, stack, "cmd-exec-session", "surface", modelCfg.Provider+"/"+modelCfg.Model)
	if err != nil {
		t.Fatalf("newAssemblerFromGatewayAppSession() error = %v", err)
	}
	return driver, stack
}

func defaultOllamaModelCfg() gatewayapp.ModelConfig {
	return gatewayapp.ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "llama3"}
}

func TestRegressionCommandExecStatus(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	status, err := driver.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Session.ID == "" || status.ModelStatus.Display == "" || status.SandboxStatus.Type == "" || status.Session.SessionMode == "" {
		t.Fatalf("Status() incomplete: %#v", status)
	}
}

func TestRegressionCommandExecAgentListEmpty(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	if _, err := driver.ListAgents(context.Background(), 20); err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
}

func TestRegressionCommandExecAgentStatus(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	if _, err := driver.AgentStatus(context.Background()); err != nil {
		t.Fatalf("AgentStatus() error = %v", err)
	}
}

func TestRegressionCommandExecModelReasoningCompletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "reasoning-completion",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Sandbox:      gatewayapp.SandboxConfig{RequestedType: "host"},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama", API: providers.APIOllama, Model: "llama3",
			ReasoningLevels: []string{"low", "medium", "high"},
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAssemblerFromGatewayAppSession(ctx, stack, "reasoning-completion-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAssemblerFromGatewayAppSession() error = %v", err)
	}
	if _, err := driver.CompleteSlashArg(ctx, "model use", "ollama/llama3 ", 10); err != nil {
		t.Fatalf("CompleteSlashArg(model use + reasoning) error = %v", err)
	}
}
