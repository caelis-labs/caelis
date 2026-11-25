package kernel

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestApprovalProjectionReadsRuntimeFactDuringFence(t *testing.T) {
	t.Parallel()

	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "approval-leased",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "runtime-a",
	}); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{sessions: sessions}
	request := &approval.Request{
		SessionRef: active.SessionRef, RunID: "run-1", TurnID: "turn-1",
		Tool: tool.Definition{Name: "Write"},
		Call: tool.Call{ID: "write-1", Name: "Write", Input: json.RawMessage(`{"path":"child.txt"}`)},
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{ID: "write-1", Name: "Write", RawInput: map[string]any{"path": "child.txt"}},
			Options:  []session.ProtocolApprovalOption{{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}},
		},
	}
	requestID := eventstream.ApprovalRequestID("approval-1")
	appendRuntimeApprovalFixture(t, sessions, request, string(requestID))
	persisted, err := gateway.projectApprovalRequest(request, active.SessionRef, "turn-1", requestID)
	if err != nil {
		t.Fatalf("projectApprovalRequest() with active runtime lease = %v", err)
	}
	if persisted == nil || !session.IsMirror(persisted) {
		t.Fatalf("persisted approval = %#v, want durable mirror", persisted)
	}
	settled, err := gateway.projectApprovalSettlement(request, active.SessionRef, "turn-1", requestID, "resolved")
	if err != nil {
		t.Fatalf("projectApprovalSettlement() with active runtime lease = %v", err)
	}
	if settled == nil || !session.IsUIOnly(settled) {
		t.Fatalf("settled approval = %#v, want live settlement", settled)
	}
}

// The fixture authors the Runtime-domain request once; Control only reads it.
func appendRuntimeApprovalFixture(t *testing.T, service session.Service, req *approval.Request, id string) {
	t.Helper()
	presentation := approval.ProtocolApprovalFromPayload(approval.DisplayPayloadFromRuntimeRequest(*req))
	record := session.ApprovalRecord{Schema: session.ApprovalSchemaVersion, RequestID: id, SessionID: req.SessionRef.SessionID, RunID: req.RunID, TurnID: req.TurnID, ToolCallID: req.Call.ID, ToolName: req.Tool.Name, ItemID: req.Execution.ItemID, Revision: 1, Status: session.ApprovalRecordPending, Input: req.Call.Input, Approval: req.Approval, Presentation: presentation}
	if _, err := service.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: req.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval), Event: &session.Event{ID: "approval:" + id, Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Approval: &record}}); err != nil {
		t.Fatal(err)
	}
}
