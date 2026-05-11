package presets

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	sdkpolicy "github.com/OnslaughtSnail/caelis/sdk/policy"
	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func TestAutoReviewModeAllowsWorkspaceWrites(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), writeCtx("/workspace/notes.md"))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), writeCtx("/workspace/main.go"))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}
}

func TestDefaultModeRestrictsWriteRoots(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), writeCtx("/workspace/main.go"))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), writeCtx("/etc/passwd"))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionDeny {
		t.Fatalf("Action = %q, want deny", decision.Action)
	}
}

func TestDefaultModeRejectsMalformedToolInput(t *testing.T) {
	t.Parallel()

	input := writeCtx("/workspace/main.go")
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
	home := "/home/caelis-policy-test"
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, ".config", "ghostty", "config")

	decision, err := AutoReviewMode().DecideTool(context.Background(), readCtx(configPath))
	if err != nil {
		t.Fatalf("READ DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAllow {
		t.Fatalf("READ action = %q, want allow (reason=%q)", decision.Action, decision.Reason)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), writeCtx(configPath))
	if err != nil {
		t.Fatalf("WRITE DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionDeny {
		t.Fatalf("WRITE action = %q, want deny without explicit grant", decision.Action)
	}
}

func TestDefaultModeReadConstraintsIncludeDefaultUserRootsWithExtraReadRoot(t *testing.T) {
	home := "/home/caelis-policy-test"
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, ".config", "ghostty", "config")
	input := readCtx(configPath)
	input.Options.ExtraReadRoots = []string{"/var/log"}

	decision, err := AutoReviewMode().DecideTool(context.Background(), input)
	if err != nil {
		t.Fatalf("READ DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAllow {
		t.Fatalf("READ action = %q, want allow (reason=%q)", decision.Action, decision.Reason)
	}
	if !hasPathRule(decision.Constraints.PathRules, filepath.Join(home, ".config"), sdksandbox.PathAccessReadOnly) {
		t.Fatalf("PathRules = %#v, want default user config read root", decision.Constraints.PathRules)
	}
	if !hasPathRule(decision.Constraints.PathRules, filepath.Join(home, ".config", "gh"), sdksandbox.PathAccessHidden) {
		t.Fatalf("PathRules = %#v, want sensitive user config hidden root", decision.Constraints.PathRules)
	}
}

func TestDefaultModeDeniesSensitiveUserConfigReadsWithoutExplicitGrant(t *testing.T) {
	home := "/home/caelis-policy-test"
	t.Setenv("HOME", home)
	secretPath := filepath.Join(home, ".config", "gh", "hosts.yml")

	decision, err := AutoReviewMode().DecideTool(context.Background(), readCtx(secretPath))
	if err != nil {
		t.Fatalf("READ DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionDeny {
		t.Fatalf("READ action = %q, want deny for hidden path", decision.Action)
	}

	input := readCtx(secretPath)
	input.Options.ExtraReadRoots = []string{filepath.Join(home, ".config", "gh")}
	decision, err = AutoReviewMode().DecideTool(context.Background(), input)
	if err != nil {
		t.Fatalf("READ with grant DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAllow {
		t.Fatalf("READ with grant action = %q, want allow (reason=%q)", decision.Action, decision.Reason)
	}
	if hasPathRule(decision.Constraints.PathRules, filepath.Join(home, ".config", "gh"), sdksandbox.PathAccessHidden) {
		t.Fatalf("PathRules = %#v, did not expect hidden rule for explicitly granted root", decision.Constraints.PathRules)
	}
}

func TestDefaultModeOnlyApprovesBashEscalation(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), bashCtx("go test ./...", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), bashCtx("go test ./...", true))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAskApproval {
		t.Fatalf("Action = %q, want ask_approval", decision.Action)
	}
	if decision.Approval == nil {
		t.Fatal("Approval = nil, want protocol approval payload")
	}
}

func TestDefaultModeExplicitEscalationRequiresJustification(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), bashCtxWithArgs(map[string]any{
		"command":             "go test ./...",
		"sandbox_permissions": "require_escalated",
	}))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionDeny {
		t.Fatalf("Action = %q, want deny", decision.Action)
	}
	if !strings.Contains(decision.Reason, "justification") {
		t.Fatalf("Reason = %q, want justification denial", decision.Reason)
	}
}

func TestDefaultModeEscalationApprovalCarriesPromptMetadata(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), bashCtxWithArgs(map[string]any{
		"command":             "go test ./...",
		"sandbox_permissions": "require_escalated",
		"justification":       "Do you want to run tests outside the sandbox?",
	}))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAskApproval {
		t.Fatalf("Action = %q, want ask_approval", decision.Action)
	}
	if decision.Constraints.Route != sdksandbox.RouteHost || decision.Constraints.Permission != sdksandbox.PermissionFullAccess {
		t.Fatalf("Constraints = %#v, want host full access", decision.Constraints)
	}
	if got := decision.Metadata["justification"]; got != "Do you want to run tests outside the sandbox?" {
		t.Fatalf("Metadata[justification] = %#v", got)
	}
	if got := decision.Metadata["sandbox_permissions"]; got != "require_escalated" {
		t.Fatalf("Metadata[sandbox_permissions] = %#v", got)
	}
}

func TestDefaultModeAdditionalSandboxPermissionsStaySandboxed(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), bashCtxWithArgs(map[string]any{
		"command":             "make generate",
		"workdir":             "subdir",
		"sandbox_permissions": "with_additional_permissions",
		"additional_permissions": map[string]any{
			"network": map[string]any{"enabled": true},
			"file_system": map[string]any{
				"read":  []string{"/var/log"},
				"write": []string{"./generated"},
			},
		},
	}))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAskApproval {
		t.Fatalf("Action = %q, want ask_approval", decision.Action)
	}
	if decision.Constraints.Route != sdksandbox.RouteSandbox || decision.Constraints.Permission != sdksandbox.PermissionWorkspaceWrite {
		t.Fatalf("Constraints = %#v, want sandbox workspace_write", decision.Constraints)
	}
	if decision.Constraints.Network != sdksandbox.NetworkEnabled {
		t.Fatalf("Network = %q, want enabled", decision.Constraints.Network)
	}
	if !hasPathRule(decision.Constraints.PathRules, "/var/log", sdksandbox.PathAccessReadOnly) {
		t.Fatalf("PathRules = %#v, want read-only /var/log", decision.Constraints.PathRules)
	}
	if !hasPathRule(decision.Constraints.PathRules, "/workspace/subdir", sdksandbox.PathAccessReadWrite) {
		t.Fatalf("PathRules = %#v, want read-write /workspace/subdir shell write root", decision.Constraints.PathRules)
	}
	if decision.Approval == nil {
		t.Fatal("Approval = nil, want protocol approval payload")
	}
	if got := decision.Metadata["sandbox_permissions"]; got != "with_additional_permissions" {
		t.Fatalf("Metadata[sandbox_permissions] = %#v", got)
	}
	additional, ok := decision.Metadata["additional_permissions"].(map[string]any)
	if !ok {
		t.Fatalf("Metadata[additional_permissions] = %#v, want normalized map", decision.Metadata["additional_permissions"])
	}
	fileSystem, ok := additional["file_system"].(map[string]any)
	if !ok {
		t.Fatalf("additional_permissions.file_system = %#v, want map", additional["file_system"])
	}
	writePaths, ok := fileSystem["write"].([]string)
	if !ok || len(writePaths) != 1 || writePaths[0] != "/workspace/subdir" {
		t.Fatalf("additional_permissions.file_system.write = %#v, want shell write root", fileSystem["write"])
	}
}

func TestDefaultModeRejectsMisScopedSandboxPermissionFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "additional without mode",
			args: map[string]any{
				"command": "go test ./...",
				"additional_permissions": map[string]any{
					"network": map[string]any{"enabled": true},
				},
			},
			want: "additional_permissions requires",
		},
		{
			name: "additional mode without grant",
			args: map[string]any{
				"command":             "go test ./...",
				"sandbox_permissions": "with_additional_permissions",
			},
			want: "requires non-empty additional_permissions",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision, err := AutoReviewMode().DecideTool(context.Background(), bashCtxWithArgs(tt.args))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != sdkpolicy.ActionDeny {
				t.Fatalf("Action = %q, want deny", decision.Action)
			}
			if !strings.Contains(decision.Reason, tt.want) {
				t.Fatalf("Reason = %q, want substring %q", decision.Reason, tt.want)
			}
		})
	}
}

func TestDefaultModeAllowsRelativeFilesystemPathsWithinWorkspace(t *testing.T) {
	t.Parallel()

	cases := []sdkpolicy.ToolContext{
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
		if decision.Action != sdkpolicy.ActionAllow {
			t.Fatalf("%s action = %q, want allow (reason=%q)", input.Tool.Name, decision.Action, decision.Reason)
		}
	}
}

func TestDefaultModeDeniesRelativeFilesystemPathsOutsideWorkspace(t *testing.T) {
	t.Parallel()

	cases := []sdkpolicy.ToolContext{
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
		if decision.Action != sdkpolicy.ActionDeny {
			t.Fatalf("%s action = %q, want deny", input.Tool.Name, decision.Action)
		}
	}
}

func TestFullAccessBlocksDangerousCommands(t *testing.T) {
	t.Parallel()

	decision, err := AutoReviewMode().DecideTool(context.Background(), bashCtx("rm -rf /", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionDeny {
		t.Fatalf("Action = %q, want deny", decision.Action)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), bashCtx("rm -rf $HOME", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionDeny {
		t.Fatalf("Action = %q, want deny for home target", decision.Action)
	}

	decision, err = AutoReviewMode().DecideTool(context.Background(), bashCtx("rm $HOME -rf", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionDeny {
		t.Fatalf("Action = %q, want deny for trailing flags on home target", decision.Action)
	}
}

func TestScopedRecursiveDeleteRequiresApproval(t *testing.T) {
	t.Parallel()

	for _, mode := range []struct {
		name string
		mode sdkpolicy.Mode
	}{
		{name: "auto-review", mode: AutoReviewMode()},
		{name: "manual", mode: ManualMode()},
	} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()

			decision, err := mode.mode.DecideTool(context.Background(), bashCtx("rm -rf /tmp/caelis-gocache", false))
			if err != nil {
				t.Fatalf("DecideTool() error = %v", err)
			}
			if decision.Action != sdkpolicy.ActionAskApproval {
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

			decision, err = mode.mode.DecideTool(context.Background(), bashCtx("rm /tmp/caelis-gocache -rf", false))
			if err != nil {
				t.Fatalf("DecideTool() trailing flags error = %v", err)
			}
			if decision.Action != sdkpolicy.ActionAskApproval {
				t.Fatalf("Trailing flags action = %q, want ask_approval", decision.Action)
			}
		})
	}
}

func writeCtx(path string) sdkpolicy.ToolContext {
	raw, _ := json.Marshal(map[string]any{"path": path, "content": "x"})
	return sdkpolicy.ToolContext{
		Tool: sdktool.Definition{Name: "WRITE"},
		Call: sdktool.Call{Name: "WRITE", Input: raw},
		Options: sdkpolicy.ModeOptions{
			WorkspaceRoot: "/workspace",
			TempRoot:      "/tmp",
		},
		Sandbox: sdksandbox.Descriptor{Backend: sdksandbox.BackendHost},
	}
}

func bashCtx(command string, requireEscalated bool) sdkpolicy.ToolContext {
	args := map[string]any{"command": command}
	if requireEscalated {
		args["sandbox_permissions"] = "require_escalated"
		args["justification"] = "Do you want to run this command outside the sandbox?"
	}
	return bashCtxWithArgs(args)
}

func bashCtxWithArgs(args map[string]any) sdkpolicy.ToolContext {
	raw, _ := json.Marshal(args)
	return sdkpolicy.ToolContext{
		Tool: sdktool.Definition{Name: "BASH"},
		Call: sdktool.Call{Name: "BASH", Input: raw},
		Options: sdkpolicy.ModeOptions{
			WorkspaceRoot: "/workspace",
			TempRoot:      "/tmp",
		},
		Sandbox: sdksandbox.Descriptor{Backend: sdksandbox.BackendHost},
	}
}

func hasPathRule(rules []sdksandbox.PathRule, path string, access sdksandbox.PathAccess) bool {
	for _, rule := range rules {
		if rule.Path == path && rule.Access == access {
			return true
		}
	}
	return false
}

func readCtx(path string) sdkpolicy.ToolContext {
	raw, _ := json.Marshal(map[string]any{"path": path})
	return sdkpolicy.ToolContext{
		Tool: sdktool.Definition{Name: "READ"},
		Call: sdktool.Call{Name: "READ", Input: raw},
		Options: sdkpolicy.ModeOptions{
			WorkspaceRoot: "/workspace/project",
			TempRoot:      "/tmp",
		},
		Sandbox: sdksandbox.Descriptor{Backend: sdksandbox.BackendHost},
	}
}

func listCtx(path string) sdkpolicy.ToolContext {
	raw, _ := json.Marshal(map[string]any{"path": path})
	return sdkpolicy.ToolContext{
		Tool: sdktool.Definition{Name: "LIST"},
		Call: sdktool.Call{Name: "LIST", Input: raw},
		Options: sdkpolicy.ModeOptions{
			WorkspaceRoot: "/workspace/project",
			TempRoot:      "/tmp",
		},
		Sandbox: sdksandbox.Descriptor{Backend: sdksandbox.BackendHost},
	}
}

func searchCtx(path string, query string) sdkpolicy.ToolContext {
	raw, _ := json.Marshal(map[string]any{"path": path, "query": query})
	return sdkpolicy.ToolContext{
		Tool: sdktool.Definition{Name: "SEARCH"},
		Call: sdktool.Call{Name: "SEARCH", Input: raw},
		Options: sdkpolicy.ModeOptions{
			WorkspaceRoot: "/workspace/project",
			TempRoot:      "/tmp",
		},
		Sandbox: sdksandbox.Descriptor{Backend: sdksandbox.BackendHost},
	}
}

func globCtx(pattern string) sdkpolicy.ToolContext {
	raw, _ := json.Marshal(map[string]any{"pattern": pattern})
	return sdkpolicy.ToolContext{
		Tool: sdktool.Definition{Name: "GLOB"},
		Call: sdktool.Call{Name: "GLOB", Input: raw},
		Options: sdkpolicy.ModeOptions{
			WorkspaceRoot: "/workspace/project",
			TempRoot:      "/tmp",
		},
		Sandbox: sdksandbox.Descriptor{Backend: sdksandbox.BackendHost},
	}
}
