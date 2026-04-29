package presets

import (
	"context"
	"encoding/json"
	"testing"

	sdkpolicy "github.com/OnslaughtSnail/caelis/sdk/policy"
	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func TestPlanModeAllowsMarkdownWriteOnly(t *testing.T) {
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
	if decision.Action != sdkpolicy.ActionDeny {
		t.Fatalf("Action = %q, want deny", decision.Action)
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
	raw, _ := json.Marshal(map[string]any{"command": command, "with_escalation": withEscalation})
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
