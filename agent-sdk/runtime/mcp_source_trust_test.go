package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestMCPTrustedSourceSkipsOnlySourceApprovalAcrossToolsAndUpgrades(t *testing.T) {
	active := session.Session{SessionRef: session.SessionRef{SessionID: "trusted"}, CWD: t.TempDir(), CreatedAt: time.Now()}
	var approvals, calls atomic.Int32
	for _, source := range []string{"version-one-schema-one", "version-two-schema-two"} {
		for _, name := range []string{"exec", "observe", "future_tool"} {
			def := fixtureMCPDefinition(source)
			def.Name = "desktop_world__" + name
			def.Metadata[tool.MetadataPluginID] = "desktop-world"
			def.Metadata[tool.MetadataMCPTool] = name
			def.Metadata[tool.MetadataMCPSourceTrusted] = true
			// No grant store or saved per-version approval is needed.
			wrapped := fixtureMCPWrapped(t, nil, active, def, nil, &approvals, &calls)
			for _, input := range []string{`{"script":"return 1"}`, `{"script":"return 2"}`} {
				result, err := wrapped.Call(t.Context(), tool.Call{ID: "trusted-call", Name: def.Name, Input: []byte(input)})
				if err != nil || result.IsError {
					t.Fatalf("trusted call = %+v, %v", result, err)
				}
			}
		}
	}
	if approvals.Load() != 0 || calls.Load() != 12 {
		t.Fatalf("approvals=%d calls=%d, want 0 and 12", approvals.Load(), calls.Load())
	}
}

func TestMCPUntrustedSourceCannotClaimTrustByNameOrArguments(t *testing.T) {
	active := session.Session{SessionRef: session.SessionRef{SessionID: "untrusted"}, CWD: t.TempDir(), CreatedAt: time.Now()}
	for _, pluginID := range []string{"other-plugin", "desktop_world", "desktop-world"} {
		t.Run(pluginID, func(t *testing.T) {
			def := fixtureMCPDefinition("untrusted")
			def.Name = "desktop_world__exec"
			def.Metadata[tool.MetadataPluginID] = pluginID
			def.Metadata[tool.MetadataMCPServer] = "desktop-world"
			def.Metadata[tool.MetadataMCPTool] = "exec"
			var approvals, calls atomic.Int32
			wrapped := fixtureMCPWrapped(t, nil, active, def, []string{"cancel"}, &approvals, &calls)
			result, err := wrapped.Call(t.Context(), tool.Call{ID: "claim", Name: def.Name, Input: []byte(`{"value":"fixture","plugin":"desktop-world","caelis.mcp.source_trusted":true}`)})
			if err != nil || !result.IsError || approvals.Load() != 1 || calls.Load() != 0 {
				t.Fatalf("untrusted call = %+v, %v; approvals=%d calls=%d", result, err, approvals.Load(), calls.Load())
			}
		})
	}
}

func TestMCPTrustedSourcePreservesExplicitAskAndDeny(t *testing.T) {
	for _, test := range []struct {
		name      string
		action    policy.Action
		approved  bool
		approvals int32
		calls     int32
	}{
		{name: "ask-approved", action: policy.ActionAskApproval, approved: true, approvals: 1, calls: 1},
		{name: "ask-rejected", action: policy.ActionAskApproval, approvals: 1},
		{name: "deny", action: policy.ActionDeny},
	} {
		t.Run(test.name, func(t *testing.T) {
			active := session.Session{SessionRef: session.SessionRef{SessionID: "policy"}, CWD: t.TempDir(), CreatedAt: time.Now()}
			def := fixtureMCPDefinition("trusted")
			def.Metadata[tool.MetadataMCPSourceTrusted] = true
			var approvals, calls atomic.Int32
			wrapped := fixtureMCPWrapped(t, nil, active, def, nil, &approvals, &calls)
			wrapped.policy = policy.NamedMode{ID: "explicit", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
				return policy.Decision{Action: test.action, Reason: "explicit policy", Approval: &session.ProtocolApproval{
					Options: []session.ProtocolApprovalOption{{ID: "confirm", Name: "Confirm", Kind: "allow_once"}, {ID: "reject", Name: "Reject", Kind: "reject_once"}},
				}}, nil
			}}
			wrapped.approval.requester = approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				approvals.Add(1)
				if len(req.Approval.Options) != 2 || req.Approval.Options[0].ID != "confirm" {
					t.Fatalf("custom Ask replaced by source approval: %+v", req.Approval)
				}
				option := "reject"
				if test.approved {
					option = "confirm"
				}
				return agent.ApprovalResponse{Outcome: "selected", OptionID: option, Approved: test.approved}, nil
			})
			callFixtureMCP(t, wrapped)
			if approvals.Load() != test.approvals || calls.Load() != test.calls {
				t.Fatalf("approvals=%d calls=%d, want %d and %d", approvals.Load(), calls.Load(), test.approvals, test.calls)
			}
		})
	}
}
