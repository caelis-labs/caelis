package runtime

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestMCPRevocationDuringPerCallApprovalRejectsCachedSourceGrant(t *testing.T) {
	grants, err := NewMCPGrantStore(filepath.Join(t.TempDir(), "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	active := session.Session{SessionRef: session.SessionRef{SessionID: "pending-custom"}, CWD: t.TempDir(), CreatedAt: time.Now()}
	def := fixtureMCPDefinition("source")
	grant, err := mcpGrantFor(def, active)
	if err != nil {
		t.Fatal(err)
	}
	if err := grants.Grant(grant, "session", active.SessionID); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	wrapped := policyWrappedTool{session: active, sessionRef: active.SessionRef, mcpGrants: grants, mode: "custom", tool: tool.NamedTool{Def: def, Invoke: func(context.Context, tool.Call) (tool.Result, error) { calls.Add(1); return tool.Result{}, nil }}, policy: policy.NamedMode{ID: "custom", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "allow", Name: "Allow", Kind: "allow_once"}}}}, nil
	}}}
	wrapped.approval = approvalContext{requester: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		if err := grants.Revoke(grant, "session", active.SessionID); err != nil {
			t.Fatal(err)
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow", Approved: true}, nil
	})}
	if _, err := wrapped.Call(t.Context(), tool.Call{ID: "pending", Name: def.Name, Input: []byte(`{}`)}); err == nil || calls.Load() != 0 {
		t.Fatalf("cached source gate survived revoke during custom review: err=%v calls=%d", err, calls.Load())
	}
}
