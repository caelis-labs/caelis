package presets

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkpolicy "github.com/OnslaughtSnail/caelis/sdk/policy"
	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func TestLegacyPlanModeMapsToAutoReview(t *testing.T) {
	t.Parallel()

	decision, err := PlanMode().DecideTool(context.Background(), writeCtx("/workspace/notes.md"))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}

	decision, err = PlanMode().DecideTool(context.Background(), writeCtx("/workspace/main.go"))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}
}

func TestDefaultModeRestrictsWriteRoots(t *testing.T) {
	t.Parallel()

	decision, err := DefaultMode().DecideTool(context.Background(), writeCtx("/workspace/main.go"))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}

	decision, err = DefaultMode().DecideTool(context.Background(), writeCtx("/etc/passwd"))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionDeny {
		t.Fatalf("Action = %q, want deny", decision.Action)
	}
}

func TestDefaultModeOnlyApprovesBashEscalation(t *testing.T) {
	t.Parallel()

	decision, err := DefaultMode().DecideTool(context.Background(), bashCtx("go test ./...", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionAllow {
		t.Fatalf("Action = %q, want allow", decision.Action)
	}

	decision, err = DefaultMode().DecideTool(context.Background(), bashCtx("go test ./...", true))
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

	decision, err := DefaultMode().DecideTool(context.Background(), bashCtxWithArgs(map[string]any{
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

	decision, err := DefaultMode().DecideTool(context.Background(), bashCtxWithArgs(map[string]any{
		"command":             "go test ./...",
		"sandbox_permissions": "require_escalated",
		"justification":       "Do you want to run tests outside the sandbox?",
		"prefix_rule":         []string{"go", "test"},
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
	prefix, ok := decision.Metadata["prefix_rule"].([]string)
	if !ok || len(prefix) != 2 || prefix[0] != "go" || prefix[1] != "test" {
		t.Fatalf("Metadata[prefix_rule] = %#v, want [go test]", decision.Metadata["prefix_rule"])
	}
}

func TestDefaultModeAdditionalSandboxPermissionsStaySandboxed(t *testing.T) {
	t.Parallel()

	decision, err := DefaultMode().DecideTool(context.Background(), bashCtxWithArgs(map[string]any{
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
	if !hasPathRule(decision.Constraints.PathRules, "/workspace/subdir/generated", sdksandbox.PathAccessReadWrite) {
		t.Fatalf("PathRules = %#v, want read-write /workspace/subdir/generated", decision.Constraints.PathRules)
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
	if !ok || len(writePaths) != 1 || writePaths[0] != "/workspace/subdir/generated" {
		t.Fatalf("additional_permissions.file_system.write = %#v, want resolved path", fileSystem["write"])
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
			name: "broad prefix",
			args: map[string]any{
				"command":             "python3 script.py",
				"sandbox_permissions": "require_escalated",
				"justification":       "Do you want to run python outside the sandbox?",
				"prefix_rule":         []string{"python3"},
			},
			want: "too broad",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision, err := DefaultMode().DecideTool(context.Background(), bashCtxWithArgs(tt.args))
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
		decision, err := DefaultMode().DecideTool(context.Background(), input)
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
		decision, err := DefaultMode().DecideTool(context.Background(), input)
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

	decision, err := FullAccessMode().DecideTool(context.Background(), bashCtx("rm -rf /", false))
	if err != nil {
		t.Fatalf("DecideTool() error = %v", err)
	}
	if decision.Action != sdkpolicy.ActionDeny {
		t.Fatalf("Action = %q, want deny", decision.Action)
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

func bashCtx(command string, withEscalation bool) sdkpolicy.ToolContext {
	return bashCtxWithArgs(map[string]any{"command": command, "with_escalation": withEscalation})
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
