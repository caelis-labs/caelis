package kernel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestApprovalPersistenceUsesApprovalControlAuthorityDuringRuntimeLease(t *testing.T) {
	t.Parallel()

	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "approval-leased",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "runtime-a", TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{sessions: sessions}
	request := &agent.ApprovalRequest{
		SessionRef: active.SessionRef, RunID: "run-1", TurnID: "turn-1",
		Tool: tool.Definition{Name: "WRITE"},
		Call: tool.Call{ID: "write-1", Name: "WRITE", Input: json.RawMessage(`{"path":"child.txt"}`)},
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{ID: "write-1", Name: "WRITE", RawInput: map[string]any{"path": "child.txt"}},
			Options:  []session.ProtocolApprovalOption{{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}},
		},
	}
	requestID := eventstream.ApprovalRequestID("approval-1")
	persisted, err := gateway.persistApprovalRequest(request, active.SessionRef, "turn-1", requestID)
	if err != nil {
		t.Fatalf("persistApprovalRequest() with active runtime lease = %v", err)
	}
	if persisted == nil || !session.IsMirror(persisted) {
		t.Fatalf("persisted approval = %#v, want durable mirror", persisted)
	}
	settled, err := gateway.persistApprovalSettlement(request, active.SessionRef, "turn-1", requestID, "resolved")
	if err != nil {
		t.Fatalf("persistApprovalSettlement() with active runtime lease = %v", err)
	}
	if settled == nil || !session.IsMirror(settled) {
		t.Fatalf("settled approval = %#v, want durable mirror", settled)
	}
}
