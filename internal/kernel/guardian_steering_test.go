package kernel

import (
	"context"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"testing"
)

func TestSteeringInvalidatesAutomaticApprovalBeforeSettlement(t *testing.T) {
	ref := session.SessionRef{SessionID: "parent"}
	c := newApprovalCoordinator(ref)
	owner := newTurnHandle(turnHandleConfig{handleID: "turn", sessionRef: ref, approvals: c})
	original := &agent.ApprovalRequest{SessionRef: ref, Origin: &agent.ApprovalOrigin{Role: agent.ApprovalRoleMain}, Call: tool.Call{ID: "call"}}
	pending, err := c.enqueue(owner, original, false)
	if err != nil {
		t.Fatal(err)
	}
	original.Origin.Role = agent.ApprovalRoleSubagent
	if pending.request.Origin.Role != agent.ApprovalRoleMain {
		t.Fatal("queue origin changed after admission")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pending.reviewCancel = cancel
	c.invalidateAutoReviews()
	if ctx.Err() == nil {
		t.Fatal("review not cancelled")
	}
	if err := c.resolve(pending, ApprovalDecision{Approved: true, OptionID: "allow_once"}); err == nil {
		t.Fatal("stale review authorized action after steering")
	}
	select {
	case <-pending.decisions:
		t.Fatal("stale result delivered")
	default:
	}
}
