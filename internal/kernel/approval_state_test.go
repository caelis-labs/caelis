package kernel

import (
	"errors"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestApprovalSnapshotExposesOnlyCallerResolvableHead(t *testing.T) {
	handle := newTestTurnHandle()
	request := func(id string) *agent.ApprovalRequest {
		return &agent.ApprovalRequest{
			SessionRef: handle.sessionRef, PauseTokenID: id,
			Tool: tool.Definition{Name: "RunCommand"}, Call: tool.Call{ID: id, Name: "RunCommand"},
			Approval: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{ID: id, Name: "RunCommand"},
				Options:  []session.ProtocolApprovalOption{{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}},
			},
		}
	}
	automatic, err := handle.enqueueApproval(request("automatic"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.releasePendingApproval(automatic, "abandoned")
	manual, err := handle.enqueueApproval(request("manual"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.releasePendingApproval(manual, "abandoned")
	// No Guardian progress has been emitted. A bootstrap or reconnect can read
	// this exact queue now, before any review notification reaches the Surface.
	gateway := &Gateway{runtime: mockRuntime{}, active: map[string]*turnHandle{handle.sessionRef.SessionID: handle}, approvals: map[string]*approvalCoordinator{handle.sessionRef.SessionID: handle.approvals}}
	for _, foreground := range []bool{true, false} {
		if !foreground {
			delete(gateway.active, handle.sessionRef.SessionID)
		}
		state, err := gateway.ControlClientRuntimeState(t.Context(), handle.sessionRef)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Run.WaitingApproval || state.Approval.QueuedCount != 1 {
			t.Fatalf("automatic wait or queued manual request lost: %+v", state)
		}
		if state.Approval.Active != nil {
			t.Errorf("automatic review exposed as caller-resolvable head before review event: %+v", state.Approval.Active)
		}
	}
	decision := ApprovalDecision{RequestID: automatic.id, OptionID: "allow_once", Outcome: "selected", Approved: true}
	var rejected *Error
	if err := handle.submitApproval(t.Context(), decision); !errors.As(err, &rejected) || rejected.Code != CodeApprovalNotPending {
		t.Fatal("automatic approval did not preserve caller rejection", err)
	}
	if err := handle.resolvePendingApproval(automatic, decision); err != nil {
		t.Fatal(err)
	}
	// The same FIFO now exposes the real manual request with its original
	// identity and native choice. Filtering must not hide all native approvals.
	state := handle.controlClientRuntimeState("running", false)
	if state.Approval.Active == nil || state.Approval.Active.RequestID != manual.id || state.Approval.Active.Permission.Options[0].ID != "allow_once" || state.Approval.QueuedCount != 0 {
		t.Fatalf("manual head not exposed after automatic settlement: %+v", state)
	}
	decision.RequestID = manual.id
	if err := handle.submitApproval(t.Context(), decision); err != nil {
		t.Fatal(err)
	}
	state = handle.controlClientRuntimeState("running", false)
	if state.Approval.Active != nil || state.Run.WaitingApproval || state.Approval.QueuedCount != 0 {
		t.Fatalf("settled approval resurrected in snapshot: %+v", state)
	}
}
