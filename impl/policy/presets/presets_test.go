package presets

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/internal/testenv"
	"github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
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
	if decision.Action != policy.ActionDeny {
		t.Fatalf("Action = %q, want deny", decision.Action)
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

func TestDefaultModeAllowsMCPPluginTools(t *testing.T) {
	t.Parallel()

	input := policy.ToolContext{
		Tool: tool.Definition{
			Name: "mcp__plugin__server__read_fixture",
			Metadata: map[string]any{
				tool.MetadataToolKind:  tool.MetadataToolKindMCP,
				tool.MetadataPluginID:  "plugin",
				tool.MetadataMCPServer: "server",
			},
		},
		Call: policyToolCall("mcp__plugin__server__read_fixture", map[string]any{"name": "fixture"}),
	}
	decision, err := AutoReviewMode().DecideTool(context.Background(), input)
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("Action = %q, want allow (reason=%q)", decision.Action, decision.Reason)
	}
}

func TestDefaultModeStillDeniesUnknownToolsWithoutMCPMetadata(t *testing.T) {
	t.Parallel()

	input := policy.ToolContext{
		Tool: tool.Definition{Name: "mcp__plugin__server__read_fixture"},
		Call: policyToolCall("mcp__plugin__server__read_fixture", map[string]any{"name": "fixture"}),
	}
	decision, err := AutoReviewMode().DecideTool(context.Background(), input)
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionDeny {
		t.Fatalf("Action = %q, want deny", decision.Action)
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
	if decision.Action != policy.ActionDeny {
		t.Fatalf("WRITE action = %q, want deny without explicit grant", decision.Action)
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

func TestDefaultModeRequiresEscalationForGitControlMetadataCommands(t *testing.T) {
	t.Parallel()

	tests := []string{
		"git add .",
		"git commit -m update",
		"git tag v1.2.3",
		"git merge feature",
		"git rebase main",
		"git cherry-pick abc123",
		"git stash push",
		"git reset HEAD README.md",
		"git restore --staged README.md",
		"git checkout main",
		"git add . && git commit -m update",
		"git push origin main",
		"git add . && git push origin main",
		"git add . & touch .git/index.lock",
		"git add . | cat",
		"git add . > staged.log",
		"git add $(cat files)",
		"./git add .",
		"/usr/bin/git add .",
		"PATH=.:$PATH git add .",
		"env PATH=.:$PATH git add .",
		"git add . && ./git commit -m update",
		"sh -c 'git add .'",
		"bash -lc 'git commit -m update'",
		"cmd /c git add .",
		"powershell -Command git add .",
		"sudo -E git add .",
		"sudo -u root git add .",
		"sudo --user root git add .",
	}
	for _, command := range tests {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx(command, false))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != policy.ActionDeny {
				t.Fatalf("Action = %q, want deny", decision.Action)
			}
			if !strings.Contains(decision.Reason, "require_escalated") {
				t.Fatalf("Reason = %q, want escalation guidance", decision.Reason)
			}
			if hasPathRule(decision.Constraints.PathRules, testWorkspaceGitRoot(), sandbox.PathAccessReadWrite) {
				t.Fatalf("PathRules = %#v, did not expect .git write grant for command %q", decision.Constraints.PathRules, command)
			}
		})
	}

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

func TestDefaultModeDeniesDangerousGitCommands(t *testing.T) {
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
		"sh -c 'git reset --hard'",
		"bash -lc 'git clean -fd'",
		"sudo -E git reset --hard",
		"sudo -Eu root git reset --hard",
	}
	for _, command := range tests {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx(command, false))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != policy.ActionDeny {
				t.Fatalf("Action = %q, want deny", decision.Action)
			}
		})
	}

	decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx("git clean -fd", true))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionDeny {
		t.Fatalf("Escalated git clean action = %q, want deny", decision.Action)
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

func TestDefaultModeExplicitEscalationCanOmitJustification(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtxWithArgs(map[string]any{
		"command":             "go test ./...",
		"sandbox_permissions": "require_escalated",
	}))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAskApproval {
		t.Fatalf("Action = %q, want ask_approval", decision.Action)
	}
	if decision.Constraints.Route != sandbox.RouteHost {
		t.Fatalf("Constraints.Route = %q, want host", decision.Constraints.Route)
	}
	if _, ok := decision.Metadata["justification"]; ok {
		t.Fatalf("Metadata[justification] = %#v, want omitted", decision.Metadata["justification"])
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
}

func TestRecursiveDeleteCommandsAreDenied(t *testing.T) {
	t.Parallel()

	for _, mode := range []struct {
		name string
		mode policy.Mode
	}{
		{name: "auto-review", mode: AutoReviewMode()},
		{name: "manual", mode: ManualMode()},
	} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
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
			} {
				command := command
				decision, err := mode.mode.DecideTool(context.Background(), commandCtx(command, false))
				if err != nil {
					t.Fatalf("DecideTool(%q) error = %v", command, err)
				}
				if decision.Action != policy.ActionDeny {
					t.Fatalf("%q action = %q, want deny", command, decision.Action)
				}
			}
		})
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
		Sandbox: sandbox.Descriptor{Backend: sandbox.BackendHost},
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

func searchCtx(path string, query string) policy.ToolContext {
	raw, _ := json.Marshal(map[string]any{"path": path, "query": query})
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
	testenv.SetHome(t, home)
}

func samePolicyPathForTest(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
