package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

type accountingFailureApprover struct {
	calls           int
	accountingCalls int
}

func (a *accountingFailureApprover) Decide(context.Context, ApprovalReviewRequest) (ApprovalReviewResult, error) {
	a.calls++
	return ApprovalReviewResult{Approved: true, OptionID: "allow_once", DisplayText: "Authorized action.\nGuardian usage accounting could not be persisted."}, nil
}
func (a *accountingFailureApprover) ReviewApproval(ctx context.Context, req ApprovalReviewRequest) (ApprovalReviewResult, error) {
	return a.Decide(ctx, req)
}
func (a *accountingFailureApprover) ApprovalReviewAccounting(context.Context, ApprovalReviewRequest, ApprovalReviewResult) (*UsageSnapshot, *session.EventInvocation, error) {
	a.accountingCalls++
	return nil, nil, errors.New("accounting unavailable")
}

func TestGatewayAccountingFailureDoesNotRepeatApprovedExecution(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared_approver_reviewer", true: "legacy_reviewer_adapter"}[legacy], func(t *testing.T) {
			active := session.Session{SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s", WorkspaceKey: "w"}}
			rt := &approvalRuntime{session: active}
			reviewer := &accountingFailureApprover{}
			config := Config{Sessions: staticSessionService{session: active}, Runtime: rt, Resolver: staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{}}}, ApprovalReviewer: reviewer}
			if !legacy {
				config.ApprovalApprover = reviewer
			}
			gw, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			recorder := newTestTurnEventRecorder()
			result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{SessionRef: active.SessionRef, Input: "inspect", Observer: recorder})
			if err != nil {
				t.Fatal(err)
			}
			_ = collectHandleEvents(t, result.Handle)
			if reviewer.calls != 1 || rt.executionCount() != 1 {
				t.Fatalf("decisions=%d executions=%d", reviewer.calls, rt.executionCount())
			}
			visible := false
			for _, event := range recorder.snapshot() {
				if event.Kind == eventstream.KindApprovalReview && event.ApprovalReview != nil && event.ApprovalReview.Status == string(ApprovalReviewStatusApproved) {
					visible = strings.Contains(event.ApprovalReview.Text, "Authorized action.") && strings.Contains(event.ApprovalReview.Text, "usage accounting could not be persisted")
				}
			}
			if !visible {
				t.Fatal("typed approval output lost accounting warning")
			}
		})
	}
}
