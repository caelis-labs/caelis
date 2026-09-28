package kernel

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
)

func TestCallerCannotResolveAutoReviewButGuardianCan(t *testing.T) {
	for _, detached := range []bool{false, true} {
		name := "main"
		if detached {
			name = "detached_child"
		}
		t.Run(name, func(t *testing.T) {
			handle := newTestTurnHandle()
			request := &agent.ApprovalRequest{SessionRef: handle.sessionRef, PauseTokenID: "auto-main"}
			gateway := &Gateway{
				active:    map[string]*turnHandle{handle.sessionRef.SessionID: handle},
				approvals: map[string]*approvalCoordinator{handle.sessionRef.SessionID: handle.approvals},
			}
			if detached {
				handle.finish()
				delete(gateway.active, handle.sessionRef.SessionID)
				request = testChildApprovalRequest("task-detached", "detached.txt")
				request.SessionRef = handle.sessionRef
				request.PauseTokenID = "auto-detached"
			}
			pending, err := handle.enqueueApproval(request, false)
			if err != nil {
				t.Fatalf("enqueue auto-review: %v", err)
			}
			owner, found := gateway.ApprovalTarget(handle.sessionRef.SessionID, pending.id)
			if !found {
				t.Fatal("active auto-review target missing")
			}
			decision := &ApprovalDecision{RequestID: pending.id, Outcome: "selected", OptionID: "allow_once", Approved: true}
			err = gateway.SubmitActiveTurn(context.Background(), SubmitActiveTurnRequest{
				SessionRef: handle.sessionRef, HandleID: owner.HandleID, RunID: owner.RunID, TurnID: owner.TurnID,
				Kind: SubmissionKindApproval, Approval: decision,
			})
			assertApprovalNotPending(t, err)
			select {
			case got := <-pending.decisions:
				t.Fatalf("caller resolved Guardian review: %+v", got)
			default:
			}
			if current, _ := handle.approvals.snapshot(); current != pending {
				t.Fatal("rejected caller decision removed the active Guardian review")
			}
			// The private resolver still settles this same review after the
			// rejected public attempt, including when the parent has ended.
			if err := handle.resolvePendingApproval(pending, *decision); err != nil {
				t.Fatalf("private Guardian resolve: %v", err)
			}
			if got := <-pending.decisions; got.RequestID != pending.id || !got.Approved {
				t.Fatalf("Guardian decision = %+v", got)
			}
		})
	}
}

func TestCallerCanStillResolveManualApproval(t *testing.T) {
	handle := newTestTurnHandle()
	request := testChildApprovalRequest("task-manual", "manual.txt")
	request.SessionRef = handle.sessionRef
	request.PauseTokenID = "manual-request"
	pending, err := handle.enqueueApproval(request, true)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{active: map[string]*turnHandle{handle.sessionRef.SessionID: handle}}
	decision := &ApprovalDecision{RequestID: pending.id, Outcome: "selected", OptionID: "allow_once", Approved: true}
	if err := gateway.SubmitActiveTurn(context.Background(), SubmitActiveTurnRequest{
		SessionRef: handle.sessionRef, Kind: SubmissionKindApproval, Approval: decision,
	}); err != nil {
		t.Fatalf("submit manual approval: %v", err)
	}
	if got := <-pending.decisions; got.RequestID != pending.id || !got.Approved {
		t.Fatalf("manual decision = %+v", got)
	}
}
