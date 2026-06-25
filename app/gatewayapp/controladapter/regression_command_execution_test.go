package controladapter

import (
	"context"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
)

func newCommandExecDriver(t *testing.T, modelCfg gatewayapp.ModelConfig) (*Adapter, *gatewayapp.Stack) {
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
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "cmd-exec-session", "surface", modelCfg.Provider+"/"+modelCfg.Model)
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	return driver, stack
}

func defaultOllamaModelCfg() gatewayapp.ModelConfig {
	return gatewayapp.ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "llama3",
	}
}

// --- /status ---

func TestRegressionCommandExecStatus(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Session.ID == "" {
		t.Fatal("Status().SessionID empty")
	}
	if status.ModelStatus.Display == "" {
		t.Fatal("Status().Model empty")
	}
	if status.SandboxStatus.Type == "" {
		t.Fatal("Status().SandboxType empty")
	}
	if status.Session.SessionMode == "" {
		t.Fatal("Status().SessionMode empty")
	}
}

// --- /model use ---

func TestRegressionCommandExecModelUse(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	_, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "alt-model",
	})
	if err != nil {
		t.Fatalf("Connect(alt-model) error = %v", err)
	}

	status, err := driver.UseModel(ctx, "ollama/alt-model")
	if err != nil {
		t.Fatalf("UseModel(ollama/alt-model) error = %v", err)
	}
	if status.ModelStatus.Display != "ollama/alt-model" {
		t.Fatalf("UseModel() status.ModelStatus.Display = %q, want ollama/alt-model", status.ModelStatus.Display)
	}

	current, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if current.ModelStatus.Display != "ollama/alt-model" {
		t.Fatalf("Status().Model = %q, want ollama/alt-model after UseModel", current.ModelStatus.Display)
	}
}

func TestRegressionCommandExecModelUseWithReasoning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "reasoning-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Sandbox:      gatewayapp.SandboxConfig{RequestedType: "host"},
		Model: gatewayapp.ModelConfig{
			Provider:        "ollama",
			API:             providers.APIOllama,
			Model:           "llama3",
			ReasoningLevels: []string{"low", "medium", "high"},
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "reasoning-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	status, err := driver.UseModel(ctx, "ollama/llama3", "high")
	if err != nil {
		t.Fatalf("UseModel(ollama/llama3, high) error = %v", err)
	}
	if !strings.HasPrefix(status.ModelStatus.Display, "ollama/llama3") {
		t.Fatalf("status.ModelStatus.Display = %q, want prefix ollama/llama3", status.ModelStatus.Display)
	}
}

func TestRegressionCommandExecModelUseCaseInsensitive(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	_, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "TestModel",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	status, err := driver.UseModel(ctx, "ollama/testmodel")
	if err != nil {
		t.Fatalf("UseModel(case insensitive) error = %v", err)
	}
	if !strings.EqualFold(status.ModelStatus.Display, "ollama/TestModel") {
		t.Fatalf("UseModel case insensitive: status.ModelStatus.Display = %q", status.ModelStatus.Display)
	}
}

// --- /model del ---

func TestRegressionCommandExecModelDelete(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	_, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "alt-model",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	if err := driver.DeleteModel(ctx, "ollama/alt-model"); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "model del", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model del) error = %v", err)
	}
	for _, c := range candidates {
		if c.Value == "ollama/alt-model" {
			t.Fatalf("deleted alias still in candidates: %v", candidates)
		}
	}
}

func TestRegressionCommandExecDeleteCurrentModelClearsStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "delete-current-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "delete-current-session", "surface", "")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	_, err = driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "llama3",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	if err := driver.DeleteModel(ctx, "ollama/llama3"); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("model use candidates = %v, want empty after deleting only model", candidates)
	}
}

func TestRegressionCommandExecDeleteUnknownAlias(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	err := driver.DeleteModel(ctx, "nonexistent/model")
	if err == nil {
		t.Fatal("DeleteModel(nonexistent) should return error")
	}
}

// --- approval mode ---

func TestRegressionCommandExecApprovalMode(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	initialMode := status.Session.SessionMode
	if initialMode == "" {
		initialMode = "auto-review"
	}

	newMode := "manual"
	if initialMode == "manual" {
		newMode = "auto-review"
	}

	updated, err := driver.SetSessionMode(ctx, newMode)
	if err != nil {
		t.Fatalf("SetSessionMode(%s) error = %v", newMode, err)
	}
	if updated.Session.SessionMode != newMode {
		t.Fatalf("SetSessionMode(%s): session_mode = %q", newMode, updated.Session.SessionMode)
	}

	current, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if current.Session.SessionMode != newMode {
		t.Fatalf("Status().SessionMode = %q, want %q", current.Session.SessionMode, newMode)
	}
}

// --- /connect ---

func TestRegressionCommandExecConnectNewProvider(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	status, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "gpt-4",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if status.ModelStatus.Display == "" {
		t.Fatal("Connect() status.ModelStatus.Display empty")
	}

	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	found := false
	for _, c := range candidates {
		if strings.Contains(strings.ToLower(c.Value), "gpt-4") || strings.Contains(strings.ToLower(c.Detail), "gpt-4") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("connected model not in candidates: %v", candidates)
	}
}

func TestRegressionCommandExecConnectUpdatesStatus(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	status, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "new-model",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if status.ModelStatus.Display == "" {
		t.Fatal("Connect() status.ModelStatus.Display empty after connect")
	}
}

func TestRegressionCommandExecConnectInvalidArgs(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	_, err := driver.Connect(ctx, ConnectConfig{
		Provider: "",
		Model:    "",
	})
	if err == nil {
		t.Fatal("Connect(empty) should return error")
	}
}

func TestRegressionCommandExecConnectMultipleModels(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	_, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "model-a",
	})
	if err != nil {
		t.Fatalf("Connect(model-a) error = %v", err)
	}

	_, err = driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "model-b",
	})
	if err != nil {
		t.Fatalf("Connect(model-b) error = %v", err)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	foundA, foundB := false, false
	for _, c := range candidates {
		if strings.Contains(strings.ToLower(c.Value), "model-a") {
			foundA = true
		}
		if strings.Contains(strings.ToLower(c.Value), "model-b") {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Fatalf("both models should be in candidates; foundA=%v foundB=%v candidates=%v", foundA, foundB, candidates)
	}
}

// --- /new ---

func TestRegressionCommandExecNewSession(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	before, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	newSess, err := driver.NewSession(ctx)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if newSess.SessionID == "" {
		t.Fatal("NewSession().SessionID empty")
	}
	if newSess.SessionID == before.Session.ID {
		t.Fatalf("NewSession() returned same ID %q", newSess.SessionID)
	}

	after, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if after.Session.ID != newSess.SessionID {
		t.Fatalf("Status().SessionID = %q, want %q after NewSession", after.Session.ID, newSess.SessionID)
	}
}

// --- /resume ---

func TestRegressionCommandExecResumeSession(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	original, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	_, err = driver.NewSession(ctx)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	_, err = driver.ResumeSession(ctx, original.Session.ID)
	if err != nil {
		t.Fatalf("ResumeSession(%s) error = %v", original.Session.ID, err)
	}

	current, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if current.Session.ID != original.Session.ID {
		t.Fatalf("Status().SessionID = %q, want %q after resume", current.Session.ID, original.Session.ID)
	}
}

func TestRegressionCommandExecResumeNoArgs(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	sessions, err := driver.ListSessions(ctx, 10)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	_ = sessions
}

func TestRegressionCommandExecResumeNonexistent(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	_, err := driver.ResumeSession(ctx, "nonexistent-session-id")
	if err == nil {
		t.Fatal("ResumeSession(nonexistent) should return error")
	}
}

// --- /compact ---

func TestRegressionCommandExecCompact(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	err := driver.Compact(ctx)
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
}

// --- Model alias lifecycle ---

func TestRegressionCommandExecModelAliasLifecycle(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	_, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "model-alpha",
	})
	if err != nil {
		t.Fatalf("Connect(alpha) error = %v", err)
	}

	_, err = driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "model-beta",
	})
	if err != nil {
		t.Fatalf("Connect(beta) error = %v", err)
	}

	status, err := driver.UseModel(ctx, "ollama/model-alpha")
	if err != nil {
		t.Fatalf("UseModel(alpha) error = %v", err)
	}
	if !strings.EqualFold(status.ModelStatus.Display, "ollama/model-alpha") {
		t.Fatalf("after UseModel(alpha): model = %q", status.ModelStatus.Display)
	}

	if err := driver.DeleteModel(ctx, "ollama/model-alpha"); err != nil {
		t.Fatalf("DeleteModel(alpha) error = %v", err)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	for _, c := range candidates {
		if strings.Contains(strings.ToLower(c.Value), "model-alpha") {
			t.Fatalf("deleted alpha still in candidates: %v", candidates)
		}
	}
}

// --- /connect with full config ---

func TestRegressionCommandExecConnectFullConfig(t *testing.T) {
	t.Parallel()
	driver, stack := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	status, err := driver.Connect(ctx, ConnectConfig{
		Provider:                       "ollama",
		Model:                          "custom-model",
		TimeoutSeconds:                 120,
		StreamFirstEventTimeoutSeconds: 300,
		ContextWindowTokens:            128000,
		MaxOutputTokens:                4096,
		ReasoningEffort:                "medium",
		ReasoningLevels:                []string{"low", "medium", "high"},
	})
	if err != nil {
		t.Fatalf("Connect(full config) error = %v", err)
	}
	if status.ModelStatus.Display == "" {
		t.Fatal("Connect(full config) status.ModelStatus.Display empty")
	}
	cfg, ok := stack.ModelConfig("ollama/custom-model")
	if !ok {
		t.Fatal("ModelConfig(ollama/custom-model) not found")
	}
	if got := cfg.StreamFirstEventTimeout.Seconds(); got != 300 {
		t.Fatalf("StreamFirstEventTimeout = %.0fs, want 300s", got)
	}
}

// --- /connect then /model use then /model del ---

func TestRegressionCommandExecConnectUseDeleteCycle(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	_, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "cycle-model",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	status, err := driver.UseModel(ctx, "ollama/cycle-model")
	if err != nil {
		t.Fatalf("UseModel() error = %v", err)
	}
	if !strings.EqualFold(status.ModelStatus.Display, "ollama/cycle-model") {
		t.Fatalf("UseModel() model = %q", status.ModelStatus.Display)
	}

	if err := driver.DeleteModel(ctx, "ollama/cycle-model"); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	for _, c := range candidates {
		if strings.Contains(strings.ToLower(c.Value), "cycle-model") {
			t.Fatalf("cycle-model still in candidates after delete: %v", candidates)
		}
	}
}

// --- Agent commands ---

func TestRegressionCommandExecAgentListEmpty(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	agents, err := driver.ListAgents(ctx, 20)
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	_ = agents
}

func TestRegressionCommandExecAgentStatus(t *testing.T) {
	t.Parallel()
	driver, _ := newCommandExecDriver(t, defaultOllamaModelCfg())
	ctx := context.Background()

	status, err := driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus() error = %v", err)
	}
	_ = status
}

// --- Slash arg completion for model reasoning ---

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
			Provider:        "ollama",
			API:             providers.APIOllama,
			Model:           "llama3",
			ReasoningLevels: []string{"low", "medium", "high"},
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "reasoning-completion-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "model use", "ollama/llama3 ", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use + reasoning) error = %v", err)
	}
	_ = candidates
}
