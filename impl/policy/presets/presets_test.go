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

func TestDefaultModeReadConstraintsDoNotAddDefaultReadableRoots(t *testing.T) {
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

func TestDefaultModeAllowsSensitiveUserConfigReadsByDefault(t *testing.T) {
	home := t.TempDir()
	setHomeForPresetsTest(t, home)
	secretPath := filepath.Join(home, ".config", "gh", "hosts.yml")

	decision, err := AutoReviewMode().DecideTool(context.Background(), readCtx(secretPath))
	if err != nil {
		t.Fatalf("READ DecideTool() error = %v", err)
	}
	if decision.Action != policy.ActionAllow {
		t.Fatalf("READ action = %q, want allow (reason=%q)", decision.Action, decision.Reason)
	}
	if hasPathRule(decision.Constraints.PathRules, filepath.Join(home, ".config", "gh"), sandbox.PathAccessHidden) {
		t.Fatalf("PathRules = %#v, did not expect hidden rule for default read", decision.Constraints.PathRules)
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

func TestDefaultModeDoesNotGrantGitMetadataWriteRules(t *testing.T) {
	t.Parallel()

	tests := []string{
		"git add .",
		"git commit -m update",
		"git tag v1.2.3",
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
	}
	for _, command := range tests {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), commandCtx(command, false))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if hasPathRule(decision.Constraints.PathRules, testWorkspaceGitRoot(), sandbox.PathAccessReadWrite) {
				t.Fatalf("PathRules = %#v, did not expect .git write grant for command %q", decision.Constraints.PathRules, command)
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

func TestScopedRecursiveDeleteRequiresApproval(t *testing.T) {
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

			decision, err := mode.mode.DecideTool(context.Background(), commandCtx("rm -rf /tmp/caelis-gocache", false))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != policy.ActionAskApproval {
				t.Fatalf("Action = %q, want ask_approval", decision.Action)
			}
			if decision.Approval == nil {
				t.Fatal("Approval = nil, want protocol approval payload")
			}
			if !strings.Contains(decision.Reason, "destructive") {
				t.Fatalf("Reason = %q, want destructive approval reason", decision.Reason)
			}
			if got, _ := decision.Metadata["destructive_command"].(bool); !got {
				t.Fatalf("Metadata[destructive_command] = %#v, want true", decision.Metadata["destructive_command"])
			}

			decision, err = mode.mode.DecideTool(context.Background(), commandCtx("rm /tmp/caelis-gocache -rf", false))
			if err != nil {
				t.Fatalf("DecideTool() trailing flags error = %v", err)
			}
			if decision.Action != policy.ActionAskApproval {
				t.Fatalf("Trailing flags action = %q, want ask_approval", decision.Action)
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
