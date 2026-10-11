package kernel

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestNativeApprovalInvalidSelectionKeepsMainAndChildPending(t *testing.T) {
	for _, child := range []bool{false, true} {
		name := "main"
		if child {
			name = "detached_child"
		}
		t.Run(name, func(t *testing.T) {
			handle := newTestTurnHandle()
			request := &agent.ApprovalRequest{SessionRef: handle.sessionRef, RunID: handle.runID, TurnID: handle.turnID, PauseTokenID: "native-pending"}
			if child {
				handle.finish()
				request = testChildApprovalRequest("native-task", "native.txt")
				request.SessionRef, request.PauseTokenID = handle.sessionRef, "native-pending"
			}
			request.Approval = &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "native-reject", Kind: "reject_once"}, {ID: "native-allow", Kind: "allow_once"}}}
			pending, err := handle.enqueueApproval(request, true)
			if err != nil {
				t.Fatal(err)
			}
			var settlements int
			handle.settleApproval = func(*agent.ApprovalRequest, eventstream.ApprovalRequestID, string) (*session.Event, error) {
				settlements++
				return &session.Event{ID: "original-settlement", SessionID: handle.sessionRef.SessionID, Seq: 2}, nil
			}
			gateway := &Gateway{active: map[string]*turnHandle{handle.sessionRef.SessionID: handle}, approvals: map[string]*approvalCoordinator{handle.sessionRef.SessionID: handle.approvals}}
			target, found := gateway.ApprovalTarget(handle.sessionRef.SessionID, pending.id)
			if !found {
				t.Fatal("original target missing")
			}
			for _, invalid := range []ApprovalDecision{
				{Outcome: "selected", OptionID: "forged-option", Approved: true},
				{Outcome: "selected", OptionID: " native-allow ", Approved: true},
				{Outcome: "selected", Approved: true},
				{Outcome: "cancelled", OptionID: "native-allow", Approved: true},
			} {
				invalid.RequestID = pending.id
				if err := gateway.SubmitActiveTurn(t.Context(), SubmitActiveTurnRequest{SessionRef: handle.sessionRef, HandleID: target.HandleID, RunID: target.RunID, TurnID: target.TurnID, Kind: SubmissionKindApproval, Approval: &invalid}); err == nil {
					t.Fatalf("invalid native decision settled: %+v", invalid)
				}
				if current, _ := handle.approvals.snapshot(); current != pending || pending.settled || settlements != 0 {
					t.Fatal("invalid decision changed pending or durable settlement")
				}
				select {
				case got := <-pending.decisions:
					t.Fatalf("invalid decision woke execution: %+v", got)
				default:
				}
				if got, ok := gateway.ApprovalTarget(handle.sessionRef.SessionID, pending.id); !ok || got != target {
					t.Fatal("invalid selection changed native target")
				}
			}
			// The native option owns its allow meaning even when the optional
			// legacy boolean is omitted by the selecting client.
			if err := handle.submitApproval(t.Context(), ApprovalDecision{RequestID: pending.id, Outcome: "selected", OptionID: "native-allow"}); err != nil {
				t.Fatal(err)
			}
			if got := <-pending.decisions; got.OptionID != "native-allow" || !got.Approved {
				t.Fatalf("original choice lost: %+v", got)
			}
		})
	}
}
