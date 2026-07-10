package presets

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	_ "github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestAutoReviewModeAllowsWorkspaceWrites(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), writeCtx(filepath.Join(testWorkspaceRoot(), "notes.md")))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), writeCtx(filepath.Join(testWorkspaceRoot(), "main.go")))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}
}

func TestNewRegistryResolvesLegacyPolicyModeAliases(t *testing.T) {
	t.Parallel()

	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	for _, name := range []string{ModeWorkspaceWrite, ModeAutoReview, ModeManual} {
		mode, ok, err := registry.Lookup(context.Background(), name)
		if err != nil {
			t.Fatalf("Lookup(%q) error = %v", name, err)
		}
		if !ok || mode == nil {
			t.Fatalf("Lookup(%q) = nil/%v, want workspace-write compatible mode", name, ok)
		}
		decision, err := mode.DecideTool(context.Background(), writeCtx(filepath.Join(testWorkspaceRoot(), "notes.md")))
		if err != nil {
			t.Fatalf("%s DecideTool() error = %v", name, err)
		}
		if decision.Action != policy.ActionAllow {
			t.Fatalf("%s action = %q, want allow", name, decision.Action)
		}
	}
}

func TestDefaultModeRestrictsWriteRoots(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), writeCtx(filepath.Join(testWorkspaceRoot(), "main.go")))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), writeCtx(testOutsidePath()))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAskApproval {
		t.Fatalf("Action = %q, want ask_approval for outside write", decision.Action)
	}
	if !hasPathRule(decision.Constraints.PathRules, testOutsidePath(), sandbox.PathAccessReadWrite) {
		t.Fatalf("PathRules = %#v, want scoped write grant for outside path", decision.Constraints.PathRules)
	}
	if got := decision.Metadata["risk_class"]; got != "path_escape" {
		t.Fatalf("Metadata[risk_class] = %#v, want path_escape", got)
	}
}

func TestDefaultModeRejectsMalformedToolInput(t *testing.T) {
	t.Parallel()

	input := writeCtx(filepath.Join(testWorkspaceRoot(), "main.go"))
	input.Call.Input = []byte(`{"path":`)

	_, err := AutoReviewMode().DecideTool(context.Background(), input)
	if err == nil {
		t.Fatal("DecideTool() error = nil, want malformed input error")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Fatalf("DecideTool() error = %v, want decode error", err)
	}
}

func TestDefaultModeAllowsExplicitWebAndMCPTools(t *testing.T) {
	t.Parallel()

	tests := []policy.ToolContext{
		{
			Tool: policyToolDefinition("SKILL"),
			Call: policyToolCall("SKILL", map[string]any{"name": "superpowers:brainstorming"}),
		},
		{
			Tool: policyToolSearchDefinition(),
			Call: policyToolCall(tool.ToolSearchToolName, map[string]any{"query": "calendar"}),
		},
		{
			Tool: policyMCPToolDefinition("mcp__plugin__server__read_fixture"),
			Call: policyToolCall("mcp__plugin__server__read_fixture", map[string]any{"name": "fixture"}),
		},
		{
			Tool: policyToolDefinition("WEB_SEARCH"),
			Call: policyToolCall("WEB_SEARCH", map[string]any{"query": "latest release"}),
		},
		{
			Tool: policyToolDefinition("WEB_FETCH"),
			Call: policyToolCall("WEB_FETCH", map[string]any{"url": "https://example.com"}),
		},
	}
	for _, input := range tests {
		decision, err := AutoReviewMode().DecideTool(context.Background(), input)
		if err != nil {
			t.Fatalf("%s DecideTool() error = %v", input.Tool.Name, err)
		}
		if decision.Action != policy.ActionAllow {
			t.Fatalf("%s Action = %q, want allow (reason=%q)", input.Tool.Name, decision.Action, decision.Reason)
		}
	}
}

func TestDefaultModeDeniesUnknownNonMCPTools(t *testing.T) {
	t.Parallel()

	tests := []policy.ToolContext{
		{
			Tool: policyToolDefinition("CUSTOM_TOOL"),
			Call: policyToolCall("CUSTOM_TOOL", map[string]any{"name": "fixture"}),
		},
		{
			Tool: policyToolDefinition("mcp__plugin__server__read_fixture"),
			Call: policyToolCall("mcp__plugin__server__read_fixture", map[string]any{"name": "fixture"}),
		},
	}
	for _, input := range tests {
		decision, err := AutoReviewMode().DecideTool(context.Background(), input)
		if err != nil {
			t.Fatalf("%s DecideTool() error = %v", input.Tool.Name, err)
		}
		if decision.Action != policy.ActionDeny {
			t.Fatalf("%s Action = %q, want deny", input.Tool.Name, decision.Action)
		}
	}
}

func TestDefaultModeAllowsUserConfigReadsButRequiresWriteGrant(t *testing.T) {
	home := t.TempDir()
	setHomeForPresetsTest(t, home)
	tempRoot := filepath.Join(filepath.Dir(home), "caelis-temp-root")
	configPath := filepath.Join(home, ".config", "ghostty", "config")

	readInput := readCtx(configPath)
	readInput.Options.TempRoot = tempRoot
	decision, err := AutoReviewMode().DecideTool(context.Background(), readInput)
	if err != nil {
		t.Fatalf("READ DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("READ action = %q, want allow (reason=%q)", decision.Action, decision.Reason)
	}

	writeInput := writeCtx(configPath)
	writeInput.Options.TempRoot = tempRoot
	decision, err = AutoReviewMode().DecideTool(context.Background(), writeInput)
	if err != nil {
		t.Fatalf("WRITE DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAskApproval {
		t.Fatalf("WRITE action = %q, want ask_approval without explicit grant", decision.Action)
	}
	if !hasPathRule(decision.Constraints.PathRules, configPath, sandbox.PathAccessReadWrite) {
		t.Fatalf("PathRules = %#v, want scoped write grant for config path", decision.Constraints.PathRules)
	}
}

func TestDefaultModeReadToolsDoNotRequireExplicitReadableRootsForOrdinaryReads(t *testing.T) {
	home := t.TempDir()
	setHomeForPresetsTest(t, home)
	configPath := filepath.Join(home, ".config", "ghostty", "config")
	extraReadRoot := testExtraReadRoot()

	cases := []policy.ToolContext{
		readCtx(configPath),
		listCtx(filepath.Dir(configPath)),
		searchCtx(filepath.Dir(configPath), "theme"),
		globCtx(filepath.Join(filepath.Dir(configPath), "*")),
	}
	for _, input := range cases {
		input.Options.ExtraReadRoots = []string{extraReadRoot}
		decision, err := AutoReviewMode().DecideTool(context.Background(), input)
		if err != nil {
			t.Fatalf("%s DecideTool() error = %v", input.Tool.Name, err)
		}
		if decision.Action != policy.ActionAllow {
			t.Fatalf("%s action = %q, want allow (reason=%q)", input.Tool.Name, decision.Action, decision.Reason)
		}
		if hasPathRule(decision.Constraints.PathRules, extraReadRoot, sandbox.PathAccessReadOnly) {
			t.Fatalf("%s PathRules = %#v, want no explicit read root for filesystem read tool", input.Tool.Name, decision.Constraints.PathRules)
		}
		if hasPathRule(decision.Constraints.PathRules, filepath.Join(home, ".config", "gh"), sandbox.PathAccessHidden) {
			t.Fatalf("%s PathRules = %#v, want no sensitive user config hidden root", input.Tool.Name, decision.Constraints.PathRules)
		}
	}
}

func TestDefaultModeCommandConstraintsKeepExtraReadRoots(t *testing.T) {
	t.Parallel()

	extraReadRoot := testExtraReadRoot()
	input := commandCtx("go test ./...", false)
	input.Options.ExtraReadRoots = []string{extraReadRoot}

	decision, err := AutoReviewMode().DecideTool(context.Background(), input)
	if err != nil {
		t.Fatalf("RUN_COMMAND DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("RUN_COMMAND action = %q, want allow (reason=%q)", decision.Action, decision.Reason)
	}
	if !hasPathRule(decision.Constraints.PathRules, extraReadRoot, sandbox.PathAccessReadOnly) {
		t.Fatalf("PathRules = %#v, want command extra read root", decision.Constraints.PathRules)
	}
}

func TestDefaultModeDeniesSensitiveUserConfigReadsByDefault(t *testing.T) {
	home := t.TempDir()
	setHomeForPresetsTest(t, home)

	for _, secretPath := range []string{
		filepath.Join(home, ".ssh", "id_rsa"),
		filepath.Join(home, ".config", "gh", "hosts.yml"),
	} {
		decision, err := AutoReviewMode().DecideTool(context.Background(), readCtx(secretPath))
		if err != nil {
			t.Fatalf("READ DecideTool(%q) error = %v", secretPath, err)
		}
		if decision.Action != policy.ActionDeny {
			t.Fatalf("READ action for %q = %q, want deny", secretPath, decision.Action)
		}
	}
}

func TestDefaultModeAllowsExplicitSensitiveUserConfigReadRoot(t *testing.T) {
	home := t.TempDir()
	setHomeForPresetsTest(t, home)
	ghRoot := filepath.Join(home, ".config", "gh")
	secretPath := filepath.Join(ghRoot, "hosts.yml")
	input := readCtx(secretPath)
	input.Options.ExtraReadRoots = []string{ghRoot}

	decision, err := AutoReviewMode().DecideTool(context.Background(), input)
	if err != nil {
		t.Fatalf("READ DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("READ action = %q, want allow with explicit read root (reason=%q)", decision.Action, decision.Reason)
	}
}

func TestDefaultModeOnlyApprovesCommandEscalation(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx("go test ./...", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), commandCtx("go test ./...", true))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAskApproval {
		t.Fatalf("Action = %q, want ask_approval", decision.Action)
	}
	if decision.Approval == nil {
		t.Fatal("Approval = nil, want protocol approval payload")
	}
}

func TestDefaultModeAddsDeveloperCacheWriteRoots(t *testing.T) {
	home := filepath.Join(testTempRoot(), "caelis-cache-test")
	setHomeForPresetsTest(t, home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".xdg-cache"))
	t.Setenv("GOCACHE", filepath.Join(home, "custom-go-build"))
	t.Setenv("GOMODCACHE", filepath.Join(home, "custom-go-mod"))
	t.Setenv("GOPATH", strings.Join([]string{
		filepath.Join(home, "go-one"),
		filepath.Join(home, "go-two"),
	}, string(filepath.ListSeparator)))
	t.Setenv("CARGO_HOME", filepath.Join(home, ".custom-cargo"))
	t.Setenv("GRADLE_USER_HOME", filepath.Join(home, ".custom-gradle"))

	decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx("go test ./...", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}
	roots := []string{
		filepath.Join(home, ".xdg-cache"),
		filepath.Join(home, "custom-go-build"),
		filepath.Join(home, "custom-go-mod"),
		filepath.Join(home, "go-one", "pkg", "mod"),
		filepath.Join(home, "go-two", "pkg", "mod"),
		filepath.Join(home, ".npm"),
		filepath.Join(home, ".custom-cargo", "registry"),
		filepath.Join(home, ".custom-cargo", "git"),
		filepath.Join(home, ".custom-gradle", "caches"),
		filepath.Join(home, ".m2", "repository"),
	}
	if runtime.GOOS == "windows" {
		for _, root := range roots {
			if hasPathRule(decision.Constraints.PathRules, root, sandbox.PathAccessReadWrite) {
				t.Fatalf("PathRules = %#v, did not expect Windows host developer cache write root %q", decision.Constraints.PathRules, root)
			}
		}
		return
	}
	for _, root := range roots {
		if !hasPathRule(decision.Constraints.PathRules, root, sandbox.PathAccessReadWrite) {
			t.Fatalf("PathRules = %#v, want default developer cache write root %q", decision.Constraints.PathRules, root)
		}
	}
}

func TestDefaultModeSkipsDefaultTempWriteRootOnWindows(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx("go test ./...", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}
	if runtime.GOOS == "windows" {
		if hasPathRule(decision.Constraints.PathRules, testTempRoot(), sandbox.PathAccessReadWrite) {
			t.Fatalf("PathRules = %#v, did not expect Windows host temp write root %q", decision.Constraints.PathRules, testTempRoot())
		}
		return
	}
	if !hasPathRule(decision.Constraints.PathRules, testTempRoot(), sandbox.PathAccessReadWrite) {
		t.Fatalf("PathRules = %#v, want default temp write root %q", decision.Constraints.PathRules, testTempRoot())
	}
}

func TestDefaultModeKeepsCommandNetworkEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command string
	}{
		{command: "go mod download"},
		{command: "GOPROXY=https://proxy.golang.org go mod tidy"},
		{command: "go get ./..."},
		{command: "cargo fetch"},
		{command: "npm ci --ignore-scripts"},
		{command: "pnpm fetch"},
		{command: "pip download -r requirements.txt"},
		{command: "uv pip download flask"},
		{command: "go test ./..."},
		{command: "npm ci"},
		{command: "go mod download && curl https://example.com"},
		{command: "go mod download & curl https://example.com"},
		{command: "go mod download | cat"},
		{command: "go mod download > deps.log"},
		{command: "go mod download $(cat args)"},
		{command: "./go mod download"},
		{command: "/usr/bin/go mod download"},
		{command: "PATH=.:$PATH go mod download"},
		{command: "env PATH=.:$PATH go mod download"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx(tt.command, false))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != policy.ActionAllow {
				t.Fatalf("Action = %q, want allow", decision.Action)
			}
			if decision.Constraints.Network != sandbox.NetworkEnabled {
				t.Fatalf("Network = %q, want enabled", decision.Constraints.Network)
			}
		})
	}
}

func TestDefaultModeAllowsConfigToDisableSandboxNetwork(t *testing.T) {
	t.Parallel()

	disabled := false
	input := commandCtx("go test ./...", false)
	input.Options.NetworkEnabled = &disabled
	decision, err := AutoReviewMode().DecideTool(context.Background(), input)
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}
	if decision.Constraints.Network != sandbox.NetworkDisabled {
		t.Fatalf("Network = %q, want disabled", decision.Constraints.Network)
	}
}

func TestDefaultModeAllowsReadOnlyGitCommands(t *testing.T) {
	t.Parallel()

	tests := []string{
		"git status --short",
		"git diff -- README.md",
		"git log --oneline -3",
		"git show --stat HEAD",
		"git branch --show-current",
		"git branch --list",
		"git tag --list",
		"git remote -v",
		"git stash list",
		"git add --help",
		"git push --help",
		"git unknown-subcommand --maybe-writes",
		"git clean -n -fd",
		"git clean -nfd",
		"git -C repo status",
	}
	for _, command := range tests {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx(command, false))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != policy.ActionAllow {
				t.Fatalf("Action = %q, want allow (reason=%q)", decision.Action, decision.Reason)
			}
		})
	}
}

func TestDefaultModeRequiresApprovalForGitMetadataCommandsInSandbox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command string
		denied  string
	}{
		{command: "git add .", denied: "git add ."},
		{command: "git -C repo add .", denied: "git -C repo add ."},
		{command: "git commit -m update", denied: "git commit -m update"},
		{command: "git tag v1.2.3", denied: "git tag v1.2.3"},
		{command: "git tag -d v1.2.3", denied: "git tag -d v1.2.3"},
		{command: "git branch feature", denied: "git branch feature"},
		{command: "git branch -d feature", denied: "git branch -d feature"},
		{command: "git remote add origin https://example.com/repo.git", denied: "git remote add origin https://example.com/repo.git"},
		{command: "git config user.name test", denied: "git config user.name test"},
		{command: "git merge feature", denied: "git merge feature"},
		{command: "git rebase main", denied: "git rebase main"},
		{command: "git cherry-pick abc123", denied: "git cherry-pick abc123"},
		{command: "git revert abc123", denied: "git revert abc123"},
		{command: "git stash", denied: "git stash"},
		{command: "git stash -p", denied: "git stash -p"},
		{command: "git stash push", denied: "git stash push"},
		{command: "git reset HEAD README.md", denied: "git reset HEAD README.md"},
		{command: "git restore --staged README.md", denied: "git restore --staged README.md"},
		{command: "git checkout main", denied: "git checkout main"},
		{command: "git switch main", denied: "git switch main"},
		{command: "git fetch origin", denied: "git fetch origin"},
		{command: "git pull --rebase", denied: "git pull --rebase"},
		{command: "git push origin main", denied: "git push origin main"},
		{command: "git submodule update --init", denied: "git submodule update --init"},
		{command: "git worktree add ../wt main", denied: "git worktree add ../wt main"},
		{command: "git add . && git commit -m update", denied: "git add ."},
		{command: "git add . && git push origin main", denied: "git add ."},
		{command: "git add . & touch .git/index.lock", denied: "git add ."},
		{command: "git add . | cat", denied: "git add ."},
		{command: "git add . > staged.log", denied: "git add . > staged.log"},
		{command: "git add $(cat files)", denied: "git add $(cat files)"},
		{command: "./git add .", denied: "./git add ."},
		{command: "/usr/bin/git add .", denied: "/usr/bin/git add ."},
		{command: "PATH=.:$PATH git add .", denied: "git add ."},
		{command: "env PATH=.:$PATH git add .", denied: "git add ."},
		{command: "git add . && ./git commit -m update", denied: "git add ."},
		{command: "sh -c 'git add .'", denied: "git add ."},
		{command: "bash -lc 'git commit -m update'", denied: "git commit -m update"},
		{command: "cmd /c git add .", denied: "git add ."},
		{command: "powershell -Command git add .", denied: "git add ."},
		{command: "sudo -E git add .", denied: "git add ."},
		{command: "sudo -u root git add .", denied: "git add ."},
		{command: "sudo --user root git add .", denied: "git add ."},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx(tt.command, false))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != policy.ActionAskApproval {
				t.Fatalf("Action = %q, want ask_approval (reason=%q)", decision.Action, decision.Reason)
			}
			if decision.Constraints.Route != sandbox.RouteHost {
				t.Fatalf("Constraints.Route = %q, want host", decision.Constraints.Route)
			}
			if got := decision.Metadata["risk_class"]; got != riskClassVCSSandbox {
				t.Fatalf("Metadata[risk_class] = %#v, want %q", got, riskClassVCSSandbox)
			}
			for _, want := range []string{tt.denied, "requires approval", "Git metadata"} {
				if !strings.Contains(decision.Reason, want) {
					t.Fatalf("Reason = %q, want substring %q", decision.Reason, want)
				}
			}
		})
	}
}

func TestDefaultModeRequiresApprovalForExplicitGitEscalation(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"git add .", "git push origin main", "sh -c 'git add .'", "sudo -E git add ."} {
		command := command
		t.Run(command+" escalated", func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx(command, true))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != policy.ActionAskApproval {
				t.Fatalf("Action = %q, want ask_approval", decision.Action)
			}
			if decision.Constraints.Route != sandbox.RouteHost {
				t.Fatalf("Constraints.Route = %q, want host", decision.Constraints.Route)
			}
			if got := decision.Metadata["sandbox_permissions"]; got != "require_escalated" {
				t.Fatalf("Metadata[sandbox_permissions] = %#v", got)
			}
		})
	}
}

func TestDefaultModeRequiresApprovalWhenDefaultCommandWouldRunOnHost(t *testing.T) {
	t.Parallel()

	input := commandCtx("git log --oneline -3", false)
	input.Sandbox = sandbox.Descriptor{
		Backend: sandbox.BackendHost,
		DefaultConstraints: sandbox.Constraints{
			Route:      sandbox.RouteHost,
			Backend:    sandbox.BackendHost,
			Permission: sandbox.PermissionFullAccess,
		},
	}
	decision, err := AutoReviewMode().DecideTool(context.Background(), input)
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAskApproval {
		t.Fatalf("Action = %q, want ask_approval", decision.Action)
	}
	if decision.Constraints.Route != sandbox.RouteHost || decision.Constraints.Permission != sandbox.PermissionFullAccess {
		t.Fatalf("Constraints = %#v, want host full access", decision.Constraints)
	}
	if got := decision.Metadata["sandbox_permissions"]; got != "require_escalated" {
		t.Fatalf("Metadata[sandbox_permissions] = %#v, want require_escalated", got)
	}
}

func TestDefaultModeRequiresApprovalForCompositeHostFallbackDescriptor(t *testing.T) {
	t.Parallel()

	runtime, err := sandbox.New(sandbox.Config{
		CWD:              testWorkspaceRoot(),
		RequestedBackend: "auto",
		BackendCandidates: []sandbox.Backend{
			sandbox.Backend("unavailable-test-sandbox"),
		},
	})
	if err != nil {
		t.Fatalf("sandbox.New() error = %v", err)
	}
	t.Cleanup(func() {
		_ = runtime.Close()
	})
	status := runtime.Status()
	if !status.FallbackToHost {
		t.Fatalf("runtime Status().FallbackToHost = false, want fallback host status: %+v", status)
	}

	input := commandCtx("git log --oneline -3", false)
	input.Sandbox = runtime.Describe()
	decision, err := AutoReviewMode().DecideTool(context.Background(), input)
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAskApproval {
		t.Fatalf("Action = %q, want ask_approval", decision.Action)
	}
}

func TestDefaultModeRequiresApprovalForDestructiveGitCommands(t *testing.T) {
	t.Parallel()

	tests := []string{
		"git clean -fd",
		"git clean -xfd",
		"git reset --hard",
		"git checkout -- .",
		"git checkout .",
		"git restore .",
		"git push --force origin main",
		"git push -f origin main",
		"git push --force-with-lease origin feature/my-branch",
		"sh -c 'git reset --hard'",
		"bash -lc 'git clean -fd'",
		"sudo -E git reset --hard",
		"sudo -Eu root git reset --hard",
		"echo ready\ngit reset --hard",
		"echo ready\r\ngit clean -fd",
	}
	for _, command := range tests {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx(command, false))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != policy.ActionAskApproval {
				t.Fatalf("Action = %q, want ask_approval", decision.Action)
			}
			if decision.Constraints.Route != sandbox.RouteHost {
				t.Fatalf("Constraints.Route = %q, want host", decision.Constraints.Route)
			}
		})
	}

	decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx("git clean -fd", true))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAskApproval {
		t.Fatalf("Escalated git clean action = %q, want ask_approval", decision.Action)
	}
}

func TestDefaultModeDoesNotClassifyCommandTextAsDangerous(t *testing.T) {
	t.Parallel()

	for _, command := range []string{
		"echo git clean -fd",
		"echo git reset --hard",
		"echo rm -rf /tmp/caelis-gocache",
		"sh -c 'echo git reset --hard'",
	} {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx(command, false))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != policy.ActionAllow {
				t.Fatalf("Action = %q, want allow for command text (reason=%q)", decision.Action, decision.Reason)
			}
		})
	}
}

func TestDefaultModeExplicitEscalationRequiresJustification(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args map[string]any
	}{
		{
			name: "missing",
			args: map[string]any{
				"command":             "go test ./...",
				"sandbox_permissions": "require_escalated",
			},
		},
		{
			name: "blank",
			args: map[string]any{
				"command":             "go test ./...",
				"sandbox_permissions": "require_escalated",
				"justification":       "   ",
			},
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtxWithArgs(tt.args))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != policy.ActionDeny {
				t.Fatalf("Action = %q, want deny (reason=%q)", decision.Action, decision.Reason)
			}
			for _, want := range []string{"require_escalated requires justification", "why sandbox"} {
				if !strings.Contains(decision.Reason, want) {
					t.Fatalf("Reason = %q, want substring %q", decision.Reason, want)
				}
			}
		})
	}
}

func TestDefaultModeEscalationApprovalCarriesPromptMetadata(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtxWithArgs(map[string]any{
		"command":             "go test ./...",
		"sandbox_permissions": "require_escalated",
		"justification":       "Do you want to run tests outside the sandbox?",
	}))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAskApproval {
		t.Fatalf("Action = %q, want ask_approval", decision.Action)
	}
	if decision.Constraints.Route != sandbox.RouteHost || decision.Constraints.Permission != sandbox.PermissionFullAccess {
		t.Fatalf("Constraints = %#v, want host full access", decision.Constraints)
	}
	if got := decision.Metadata["justification"]; got != "Do you want to run tests outside the sandbox?" {
		t.Fatalf("Metadata[justification] = %#v", got)
	}
	if got := decision.Metadata["sandbox_permissions"]; got != "require_escalated" {
		t.Fatalf("Metadata[sandbox_permissions] = %#v", got)
	}
}

func TestDefaultModeIgnoresRemovedSandboxPermissionFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: "additional without mode",
			args: map[string]any{
				"command": "go test ./...",
				"additional_permissions": map[string]any{
					"network": map[string]any{"enabled": true},
				},
			},
		},
		{
			name: "legacy additional mode",
			args: map[string]any{
				"command":             "go test ./...",
				"sandbox_permissions": "with_additional_permissions",
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtxWithArgs(tt.args))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != policy.ActionAllow {
				t.Fatalf("Action = %q, want allow", decision.Action)
			}
			if decision.Constraints.Route == sandbox.RouteHost {
				t.Fatalf("Constraints = %#v, want default sandbox route", decision.Constraints)
			}
		})
	}
}

func TestDefaultModeAllowsRelativeFilesystemPathsWithinWorkspace(t *testing.T) {
	t.Parallel()

	cases := []policy.ToolContext{
		readCtx("README.md"),
		listCtx("."),
		listCtx("sdk"),
		searchCtx(".", "prompt"),
		globCtx("*.md"),
		globCtx("README*"),
	}
	for _, input := range cases {
		decision, err := AutoReviewMode().DecideTool(context.Background(), input)
		if err != nil {
			t.Fatalf("%s DecideTool() error = %v", input.Tool.Name, err)
		}
		if decision.Action != policy.ActionAllow {
			t.Fatalf("%s action = %q, want allow (reason=%q)", input.Tool.Name, decision.Action, decision.Reason)
		}
	}
}

func TestDefaultModeAllowsRelativeReadPathsOutsideWorkspace(t *testing.T) {
	t.Parallel()

	cases := []policy.ToolContext{
		readCtx("../secret.txt"),
		listCtx("../outside"),
		searchCtx("../outside", "prompt"),
		globCtx("../*.md"),
	}
	for _, input := range cases {
		decision, err := AutoReviewMode().DecideTool(context.Background(), input)
		if err != nil {
			t.Fatalf("%s DecideTool() error = %v", input.Tool.Name, err)
		}
		if decision.Action != policy.ActionAllow {
			t.Fatalf("%s action = %q, want allow (reason=%q)", input.Tool.Name, decision.Action, decision.Reason)
		}
	}
}

func TestFullAccessBlocksDangerousCommands(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx("rm -rf /", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionDeny {
		t.Fatalf("Action = %q, want deny", decision.Action)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), commandCtx("rm -rf $HOME", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionDeny {
		t.Fatalf("Action = %q, want deny for home target", decision.Action)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), commandCtx("rm $HOME -rf", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionDeny {
		t.Fatalf("Action = %q, want deny for trailing flags on home target", decision.Action)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), commandCtx("echo ready\nrm -rf /", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionDeny {
		t.Fatalf("Action = %q, want deny for recursive delete after newline", decision.Action)
	}
}

func TestRecursiveDeleteInsideWritableRootsIsAllowed(t *testing.T) {
	t.Parallel()

	for _, command := range []string{
		"rm -rf /tmp/caelis-gocache",
		"rm /tmp/caelis-gocache -rf",
		"rm -r /tmp/caelis-gocache",
		"sh -c 'rm -rf /tmp/caelis-gocache'",
		"bash -lc 'rm -r /tmp/caelis-gocache'",
		"sudo -u root rm -rf /tmp/caelis-gocache",
		"sudo -Eu root rm -rf /tmp/caelis-gocache",
		"Remove-Item -Recurse -Force .\\tmp",
		"powershell -Command Remove-Item -Recurse -Force .\\tmp",
		"del /s /q tmp",
		"del /s/q tmp",
		"rmdir /s /q tmp",
		"rmdir /S/Q tmp",
		"rd /s/q tmp",
		"cmd /c rmdir /s/q tmp",
		"rm -rf ./build",
	} {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx(command, false))
			if err != nil {
				t.Fatalf("DecideTool(%q) error = %v", command, err)
			}
			if decision.Action != policy.ActionAllow {
				t.Fatalf("%q action = %q, want allow (reason=%q)", command, decision.Action, decision.Reason)
			}
		})
	}
}

func TestRecursiveDeleteOutsideRootsRequiresApproval(t *testing.T) {
	t.Parallel()

	outside := testOutsidePath()
	command := "rm -rf " + outside
	decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx(command, false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAskApproval {
		t.Fatalf("Action = %q, want ask_approval for outside recursive delete", decision.Action)
	}
}

func TestSensitiveWritePathsRemainDenied(t *testing.T) {
	home := t.TempDir()
	setHomeForPresetsTest(t, home)
	secretPath := filepath.Join(home, ".ssh", "id_rsa")

	decision, err := AutoReviewMode().DecideTool(context.Background(), writeCtx(secretPath))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionDeny {
		t.Fatalf("Action = %q, want deny for sensitive write", decision.Action)
	}
}

func writeCtx(path string) policy.ToolContext {
	raw, _ := json.Marshal(map[string]any{"path": path, "content": "x"})
	return policy.ToolContext{
		Tool: tool.Definition{Name: "WRITE"},
		Call: tool.Call{Name: "WRITE", Input: raw},
		Options: policy.ModeOptions{
			WorkspaceRoot: testWorkspaceRoot(),
			TempRoot:      testTempRoot(),
		},
		Sandbox: sandbox.Descriptor{Backend: sandbox.BackendHost},
	}
}

func commandCtx(command string, requireEscalated bool) policy.ToolContext {
	args := map[string]any{"command": command}
	if requireEscalated {
		args["sandbox_permissions"] = "require_escalated"
		args["justification"] = "Do you want to run this command outside the sandbox?"
	}
	return commandCtxWithArgs(args)
}

func commandCtxWithArgs(args map[string]any) policy.ToolContext {
	raw, _ := json.Marshal(args)
	return policy.ToolContext{
		Tool: tool.Definition{Name: "RUN_COMMAND"},
		Call: tool.Call{Name: "RUN_COMMAND", Input: raw},
		Options: policy.ModeOptions{
			WorkspaceRoot: testWorkspaceRoot(),
			TempRoot:      testTempRoot(),
		},
		Sandbox: sandboxCommandDescriptor(),
	}
}

func sandboxCommandDescriptor() sandbox.Descriptor {
	return sandbox.Descriptor{
		Backend: sandbox.BackendSeatbelt,
		DefaultConstraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendSeatbelt,
			Permission: sandbox.PermissionWorkspaceWrite,
			Isolation:  sandbox.IsolationContainer,
		},
	}
}

func hasPathRule(rules []sandbox.PathRule, path string, access sandbox.PathAccess) bool {
	for _, rule := range rules {
		if samePolicyPathForTest(rule.Path, path) && rule.Access == access {
			return true
		}
	}
	return false
}

func readCtx(path string) policy.ToolContext {
	raw, _ := json.Marshal(map[string]any{"path": path})
	return policy.ToolContext{
		Tool: tool.Definition{Name: "READ"},
		Call: tool.Call{Name: "READ", Input: raw},
		Options: policy.ModeOptions{
			WorkspaceRoot: testWorkspaceProjectRoot(),
			TempRoot:      testTempRoot(),
		},
		Sandbox: sandbox.Descriptor{Backend: sandbox.BackendHost},
	}
}

func listCtx(path string) policy.ToolContext {
	raw, _ := json.Marshal(map[string]any{"path": path})
	return policy.ToolContext{
		Tool: tool.Definition{Name: "LIST"},
		Call: tool.Call{Name: "LIST", Input: raw},
		Options: policy.ModeOptions{
			WorkspaceRoot: testWorkspaceProjectRoot(),
			TempRoot:      testTempRoot(),
		},
		Sandbox: sandbox.Descriptor{Backend: sandbox.BackendHost},
	}
}

func searchCtx(path string, pattern string) policy.ToolContext {
	raw, _ := json.Marshal(map[string]any{"path": path, "pattern": pattern})
	return policy.ToolContext{
		Tool: tool.Definition{Name: "SEARCH"},
		Call: tool.Call{Name: "SEARCH", Input: raw},
		Options: policy.ModeOptions{
			WorkspaceRoot: testWorkspaceProjectRoot(),
			TempRoot:      testTempRoot(),
		},
		Sandbox: sandbox.Descriptor{Backend: sandbox.BackendHost},
	}
}

func globCtx(pattern string) policy.ToolContext {
	raw, _ := json.Marshal(map[string]any{"pattern": pattern})
	return policy.ToolContext{
		Tool: tool.Definition{Name: "GLOB"},
		Call: tool.Call{Name: "GLOB", Input: raw},
		Options: policy.ModeOptions{
			WorkspaceRoot: testWorkspaceProjectRoot(),
			TempRoot:      testTempRoot(),
		},
		Sandbox: sandbox.Descriptor{Backend: sandbox.BackendHost},
	}
}

func policyToolCall(name string, input map[string]any) tool.Call {
	raw, _ := json.Marshal(input)
	return tool.Call{Name: name, Input: raw}
}

func policyToolDefinition(name string) tool.Definition {
	return tool.Definition{Name: name}
}

func policyMCPToolDefinition(name string) tool.Definition {
	return tool.Definition{
		Name: name,
		Metadata: map[string]any{
			tool.MetadataToolKind: tool.MetadataToolKindMCP,
		},
	}
}

func policyToolSearchDefinition() tool.Definition {
	return tool.Definition{
		Name: tool.ToolSearchToolName,
		Metadata: map[string]any{
			tool.MetadataToolKind: tool.MetadataToolKindToolSearch,
		},
	}
}

func testWorkspaceRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\workspace`
	}
	return "/workspace"
}

func testWorkspaceProjectRoot() string {
	return filepath.Join(testWorkspaceRoot(), "project")
}

func testWorkspaceGitRoot() string {
	return filepath.Join(testWorkspaceRoot(), ".git")
}

func testTempRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\tmp`
	}
	return "/tmp"
}

func testOutsidePath() string {
	if runtime.GOOS == "windows" {
		return `C:\outside\passwd`
	}
	return "/etc/passwd"
}

func testExtraReadRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\var\log`
	}
	return "/var/log"
}

func setHomeForPresetsTest(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	if runtime.GOOS != "windows" {
		return
	}
	t.Setenv("USERPROFILE", home)
	volume := filepath.VolumeName(home)
	if volume == "" {
		return
	}
	t.Setenv("HOMEDRIVE", volume)
	t.Setenv("HOMEPATH", strings.TrimPrefix(home, volume))
}

func samePolicyPathForTest(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
