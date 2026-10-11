package presets

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
)

func TestNativeApprovalSubjectsUsePolicyTargetsAndScopes(t *testing.T) {
	command := commandCtx("git fetch", true)
	command.Session.CWD = "/workspace"
	file := writeCtx("../outside.txt")
	mcp := policy.ToolContext{Tool: tool.Definition{Name: "docs__read", EffectClass: tool.EffectReadOnly, Metadata: map[string]any{tool.MetadataPluginID: "docs-plugin", tool.MetadataMCPServer: "docs", tool.MetadataMCPTool: "read"}}, Session: session.Session{CWD: "/workspace"}}
	for _, tc := range []struct {
		name                string
		input               policy.ToolContext
		mcp                 bool
		kind, target, scope string
	}{
		{"command", command, false, "command", "git fetch", "route: host; permission: danger_full_access"},
		{"file", file, false, "file", "outside.txt", "exact target paths for this invocation"},
		{"app access", mcp, true, "app_access", "docs-plugin/docs/read", "ready MCP tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var decision policy.Decision
			var err error
			if tc.mcp {
				decision, err = MCPApprovalDecision(tc.input, sandbox.Constraints{})
			} else {
				decision, err = WorkspaceWriteMode().DecideTool(t.Context(), tc.input)
			}
			if err != nil || decision.Approval == nil {
				t.Fatalf("approval = %+v, %v", decision, err)
			}
			subject := session.ApprovalSubjectOf(decision.Approval)
			if subject.Kind != tc.kind || len(subject.Targets) == 0 || !strings.Contains(subject.Targets[0].Text, tc.target) || !strings.Contains(subject.Scope.Text, tc.scope) || len(subject.Grants) != len(decision.Approval.Options) {
				t.Fatalf("policy facts = %+v", subject)
			}
			if tc.mcp && (subject.Grants[1].Scope.Text != "this session" || subject.Grants[2].Scope.Text != "this workspace and configuration (persistent)") {
				t.Fatalf("MCP scope = %+v", subject.Grants)
			}
		})
	}
	// There is currently no native network approval gate; its owning fetch
	// policy can provide the same facts when an embedding asks for approval.
	network := policy.ToolContext{Tool: tool.Definition{Name: web.FetchToolName}}
	subject := approvalSubject(network, workspaceWriteConstraints(policy.ModeOptions{}), map[string]any{"url": "https://example.test/docs"})
	if subject.Kind != "network" || subject.Targets[0].Text != "https://example.test/docs" {
		t.Fatalf("network facts = %+v", subject)
	}
}

func TestNativeApprovalSubjectHonorsExplicitSensitiveField(t *testing.T) {
	input := commandCtx("secret-command", true)
	input.Tool.InputSchema = map[string]any{"properties": map[string]any{"command": map[string]any{"writeOnly": true}}}
	decision, err := WorkspaceWriteMode().DecideTool(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	subject := session.ApprovalSubjectOf(decision.Approval)
	if !subject.Targets[0].Sensitive || subject.Targets[0].Text != "" || subject.Targets[0].State != session.ApprovalFieldOpaque || !strings.Contains(subject.Scope.Text, "host") {
		t.Fatalf("sensitive input lost safe scope or copied value: %+v", subject)
	}
}

func TestNativeApprovalSubjectHonorsSensitivePathArrayItems(t *testing.T) {
	input := policy.ToolContext{Tool: tool.Definition{Name: "Write", InputSchema: map[string]any{"properties": map[string]any{"path": map[string]any{"type": "array", "items": map[string]any{"writeOnly": true}}}}}, Call: tool.Call{Input: json.RawMessage(`{"path":["/PRIVATE-PATH"]}`)}, Options: policy.ModeOptions{WorkspaceRoot: "/workspace"}}
	subject := approvalSubject(input, sandbox.Constraints{}, map[string]any{"path": []any{"/PRIVATE-PATH"}})
	if len(subject.Targets) != 1 || !subject.Targets[0].Sensitive || subject.Targets[0].Text != "" || !subject.SensitiveInput {
		t.Fatalf("array annotation leaked derived path: %+v", subject)
	}
}
