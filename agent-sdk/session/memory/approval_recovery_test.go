package inmemory

import (
	"context"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestSettlePendingApprovalRejectsStaleSnapshotAfterLiveResolutionAndLeaseRelease(t *testing.T) {
	service := NewService(NewStore(Config{SessionIDGenerator: func() string { return "session-settlement-cas" }}))
	ctx := context.Background()
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := service.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "runtime-1", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeGuard := session.MutationGuard{
		Authority: session.MutationAuthorityRuntime, LeaseID: lease.LeaseID,
		OwnerID: lease.OwnerID, FencingToken: lease.FencingToken,
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef, MutationGuard: runtimeGuard,
		Event: pendingApprovalEvent("approval-settlement-cas"),
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := service.PendingApprovals(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingApprovals() = %#v, %v, want one candidate", pending, err)
	}
	stale := pending[0]
	if stale.Revision == 0 || stale.Request.ID == "" || stale.Request.Seq == 0 {
		t.Fatalf("candidate identity = %#v, want revision/event/seq CAS", stale)
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval),
		Event: &session.Event{
			Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror,
			ApprovalRequestID: "approval-settlement-cas",
			Lifecycle:         &session.EventLifecycle{Status: "completed", Reason: "selected"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReleaseSessionLease(ctx, session.ReleaseSessionLeaseRequest{
		SessionRef: active.SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID,
		ExpectedLeaseRevision: lease.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	expectedRevision := stale.Revision
	result, err := service.SettlePendingApproval(ctx, session.SettlePendingApprovalRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &expectedRevision,
		MutationGuard:          session.ControlMutationGuard(session.ControlMutationPurposeLifecycle),
		ApprovalRequestID:      stale.Request.ApprovalRequestID,
		ExpectedRequestEventID: stale.Request.ID,
		ExpectedRequestSeq:     stale.Request.Seq,
		Settlement: &session.Event{
			Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror,
			ApprovalRequestID: stale.Request.ApprovalRequestID,
			Lifecycle:         &session.EventLifecycle{Status: "interrupted", Reason: "startup_recovery"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Settled || result.Event != nil {
		t.Fatalf("SettlePendingApproval(stale) = %#v, want no-op", result)
	}
	page, err := service.EventsPage(ctx, session.EventPageRequest{
		SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[1].Lifecycle == nil || page.Events[1].Lifecycle.Reason != "selected" {
		t.Fatalf("events after stale recovery = %#v, want request plus real settlement only", page.Events)
	}
}

func pendingApprovalEvent(requestID string) *session.Event {
	return &session.Event{
		Type: session.EventTypeCustom, Visibility: session.VisibilityMirror, ApprovalRequestID: requestID,
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodRequestPermission,
			Permission: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{ID: "call-" + requestID, Name: "WRITE"},
			},
		},
	}
}
