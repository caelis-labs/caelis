package kernel

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestSteeringInvalidatesAutomaticApprovalBeforeSettlement(t *testing.T) {
	ref := session.SessionRef{SessionID: "parent"}
	c := newApprovalCoordinator(ref)
	owner := newTurnHandle(turnHandleConfig{handleID: "turn", sessionRef: ref, approvals: c})
	var approvals []*pendingApproval
	var reviews []context.Context
	steps := agent.NewConcurrentModelStepRefs("step", 2)
	for i, call := range []tool.Call{{ID: "push", Name: "RunCommand"}, {ID: "update-pr", Name: "github_update_pull_request"}} {
		request := &agent.ApprovalRequest{SessionRef: ref, RunID: "run", TurnID: "turn", ModelStep: steps[i], Origin: &agent.ApprovalOrigin{Role: agent.ApprovalRoleMain}, Call: call}
		pending, err := c.enqueue(owner, request, false)
		if err != nil {
			t.Fatal(err)
		}
		request.Origin.Role = agent.ApprovalRoleSubagent
		if pending.request.Origin.Role != agent.ApprovalRoleMain {
			t.Fatal("queue origin changed after admission")
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		pending.reviewCancel = cancel
		approvals = append(approvals, pending)
		reviews = append(reviews, ctx)
	}
	for _, pending := range approvals {
		select {
		case <-pending.activated:
		default:
			t.Fatalf("parallel approval %s was not active before steering", pending.request.Call.ID)
		}
	}
	c.invalidateAutoReviews()
	for i, pending := range approvals {
		if reviews[i].Err() == nil {
			t.Fatalf("review %s was not cancelled", pending.request.Call.ID)
		}
		if err := c.resolve(pending, ApprovalDecision{Approved: true, OptionID: "allow_once"}); err == nil {
			t.Fatalf("stale review authorized %s after steering", pending.request.Call.ID)
		}
		select {
		case <-pending.decisions:
			t.Fatalf("stale result delivered for %s", pending.request.Call.ID)
		default:
		}
	}
}
