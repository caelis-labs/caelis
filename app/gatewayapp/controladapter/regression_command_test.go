package controladapter

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
)

func newRegressionDriver(t *testing.T) (*Adapter, *gatewayapp.Stack) {
	return newRegressionDriverWithOptions(t, adapterTestStackOptions{})
}

func newRegressionDriverWithOptions(t *testing.T, opts adapterTestStackOptions) (*Adapter, *gatewayapp.Stack) {
	t.Helper()
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	stack, err := newAdapterTestStackWithOptions(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "regression-user",
		StoreDir:     storeDir,
		WorkspaceKey: "regression-workspace",
		WorkspaceCWD: workspace,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Sandbox: gatewayapp.SandboxConfig{
			RequestedType: "host",
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	}, opts)
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "regression-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}
	return driver, stack
}

func TestRegressionCommandStatusSnapshot(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Session.ID == "" {
		t.Fatal("Status().SessionID should not be empty after driver creation with session")
	}
	if status.ModelStatus.Display == "" {
		t.Fatal("Status().Model should not be empty")
	}
	if status.SandboxStatus.Type == "" {
		t.Fatal("Status().SandboxType should not be empty")
	}
	if status.Session.SessionMode == "" {
		t.Fatal("Status().SessionMode should not be empty")
	}
}

func TestRegressionCommandStatusAfterLazySession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "lazy-regression",
		StoreDir:     storeDir,
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Session.ID != "" {
		t.Fatalf("Status().SessionID = %q, want empty before first submission", status.Session.ID)
	}

	_, err = driver.ensureSession(ctx)
	if err != nil {
		t.Fatalf("ensureSession() error = %v", err)
	}

	status, err = driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() after ensureSession error = %v", err)
	}
	if status.Session.ID == "" {
		t.Fatal("Status().SessionID should not be empty after ensureSession")
	}
}

func TestRegressionCommandWorkspaceDir(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)

	dir := driver.WorkspaceDir()
	if dir == "" {
		t.Fatal("WorkspaceDir() should not be empty")
	}
}

func TestRegressionCommandListAgents(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	agents, err := driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	_ = agents
}

func TestRegressionCommandAgentStatus(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	status, err := driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus() error = %v", err)
	}
	_ = status
}

func TestRegressionCommandListSessions(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	sessions, err := driver.ListSessions(ctx, 10)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) == 0 {
		t.Log("ListSessions() returned 0 sessions; session may not be persisted yet")
	}
}

func TestRegressionSlashCompletionModelAliases(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("CompleteSlashArg(model use) should return at least one candidate")
	}
}

func TestRegressionSlashCompletionModelDelete(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "model del", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model del) error = %v", err)
	}
	_ = candidates
}

func TestRegressionSlashCompletionResume(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "resume", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(resume) error = %v", err)
	}
	_ = candidates
}

func TestRegressionSlashCompletionConnect(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "connect", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect) error = %v", err)
	}
	_ = candidates
}

func TestRegressionSlashCompletionAgentUse(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "agent use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent use) error = %v", err)
	}
	_ = candidates
}

func TestRegressionSlashCompletionAgentAdd(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "agent add", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent add) error = %v", err)
	}
	_ = candidates
}

func TestRegressionSlashCompletionAgentRemove(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "agent remove", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent remove) error = %v", err)
	}
	_ = candidates
}

func TestRegressionSlashCompletionSubagentRunExcludesGuardian(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriverWithOptions(t, adapterTestStackOptions{BuiltInAgentProfiles: true})
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "subagent run", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(subagent run) error = %v", err)
	}
	if !slashArgCandidateHasValue(candidates, "explorer") || !slashArgCandidateHasValue(candidates, "reviewer") {
		t.Fatalf("CompleteSlashArg(subagent run) = %#v, want explorer and reviewer", candidates)
	}
	if slashArgCandidateHasValue(candidates, "guardian") {
		t.Fatalf("CompleteSlashArg(subagent run) = %#v, want no guardian", candidates)
	}
}

func TestRegressionSlashCompletionSubagentBindGuardianOmitsACP(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriverWithOptions(t, adapterTestStackOptions{BuiltInAgentProfiles: true})
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "subagent bind guardian", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(subagent bind guardian) error = %v", err)
	}
	if !slashArgCandidateHasValue(candidates, "default") || !slashArgCandidateHasValue(candidates, "model") {
		t.Fatalf("CompleteSlashArg(subagent bind guardian) = %#v, want default and model", candidates)
	}
	if slashArgCandidateHasValue(candidates, "acp") {
		t.Fatalf("CompleteSlashArg(subagent bind guardian) = %#v, want no acp target", candidates)
	}
}

func TestRegressionSlashCompletionModelUseFiltered(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "model use", "llama", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use llama) error = %v", err)
	}
	for _, c := range candidates {
		_ = c
	}
}

func slashArgCandidateHasValue(candidates []SlashArgCandidate, value string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate.Value), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func TestRegressionCommandWorkspaceStatusDisplay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		status gitWorkspaceStatus
		want   string
	}{
		{
			name:   "clean branch",
			input:  "/tmp/workspace",
			status: gitWorkspaceStatus{Branch: "main", Dirty: false},
			want:   "/tmp/workspace [⎇ main]",
		},
		{
			name:   "dirty branch",
			input:  "/tmp/workspace",
			status: gitWorkspaceStatus{Branch: "feature/test", Dirty: true},
			want:   "/tmp/workspace [⎇ feature/test*]",
		},
		{
			name:   "empty branch",
			input:  "/tmp/workspace",
			status: gitWorkspaceStatus{Branch: "", Dirty: false},
			want:   "/tmp/workspace",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatWorkspaceStatusDisplay(tt.input, tt.status)
			if got != tt.want {
				t.Fatalf("formatWorkspaceStatusDisplay(%q, %v) = %q, want %q", tt.input, tt.status, got, tt.want)
			}
		})
	}
}

func TestRegressionCommandParseGitWorkspaceStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		wantOK bool
		branch string
		dirty  bool
	}{
		{
			name:   "clean main",
			output: "## main\n",
			wantOK: true,
			branch: "main",
			dirty:  false,
		},
		{
			name:   "dirty feature",
			output: "## feature/test\n M file.go\n",
			wantOK: true,
			branch: "feature/test",
			dirty:  true,
		},
		{
			name:   "tracking branch",
			output: "## main...origin/main\n",
			wantOK: true,
			branch: "main",
			dirty:  false,
		},
		{
			name:   "empty output",
			output: "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			status, ok := parseGitWorkspaceStatusOutput(tt.output)
			if ok != tt.wantOK {
				t.Fatalf("parseGitWorkspaceStatusOutput(%q) ok = %v, want %v", tt.output, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if status.Branch != tt.branch {
				t.Fatalf("branch = %q, want %q", status.Branch, tt.branch)
			}
			if status.Dirty != tt.dirty {
				t.Fatalf("dirty = %v, want %v", status.Dirty, tt.dirty)
			}
		})
	}
}

func TestRegressionCommandConnectCatalog(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "connect-baseurl", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-baseurl) error = %v", err)
	}
	_ = candidates
}

func TestRegressionCommandConnectModel(t *testing.T) {
	t.Parallel()
	driver, _ := newRegressionDriver(t)
	ctx := context.Background()

	candidates, err := driver.CompleteSlashArg(ctx, "connect-model", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model) error = %v", err)
	}
	_ = candidates
}

func TestRegressionCommandNewDriverWithSandboxConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	stack, err := newAdapterTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "sandbox-regression",
		StoreDir:     storeDir,
		WorkspaceKey: "sandbox-workspace",
		WorkspaceCWD: workspace,
		ApprovalMode: "default",
		Assembly:     assembly.ResolvedAssembly{},
		Sandbox: gatewayapp.SandboxConfig{
			RequestedType: "host",
			HelperPath:    filepath.Join(t.TempDir(), "missing-helper"),
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newAdapterFromGatewayAppStack(ctx, stack, "sandbox-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newAdapterFromGatewayAppStack() error = %v", err)
	}

	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.SandboxStatus.Type == "" {
		t.Fatal("Status().SandboxType should not be empty")
	}
}
