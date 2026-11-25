package session_test

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

type approvalRecoveryContractService interface {
	session.Lifecycle
	session.EventAppender
	session.PagedReader
	session.ApprovalRecoveryService
	session.SessionFenceService
}

func TestApprovalRecoverySettlementRevisionCASContract(t *testing.T) {
	factories := map[string]func(*testing.T) approvalRecoveryContractService{
		"file": func(t *testing.T) approvalRecoveryContractService {
			return file.NewStore(file.Config{RootDir: t.TempDir()})
		},
		"memory": func(*testing.T) approvalRecoveryContractService {
			return inmemory.NewStore(inmemory.Config{})
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			t.Run("pending_revision_changed", func(t *testing.T) {
				testPendingApprovalRevisionCAS(t, factory(t))
			})
			t.Run("already_resolved", func(t *testing.T) {
				testPendingApprovalAlreadyResolved(t, factory(t))
			})
		})
	}
}

func testPendingApprovalRevisionCAS(t *testing.T, store approvalRecoveryContractService) {
	ctx := t.Context()
	active := startApprovalRecoverySession(t, store)
	request := pendingApprovalSettlement(t, store, active.SessionRef, session.MutationGuard{})
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef,
		Event: &session.Event{
			Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror,
			Lifecycle: &session.EventLifecycle{Status: "running", Reason: "unrelated_revision"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var conflict *session.RevisionConflictError
	if err := store.SettlePendingApproval(ctx, request); !errors.As(err, &conflict) {
		t.Fatalf("stale revision error = %v, want RevisionConflict", err)
	}
	request.ExpectedRevision = &conflict.Actual
	if err := store.SettlePendingApproval(ctx, request); err != nil {
		t.Fatalf("matching CAS: %v", err)
	}
	assertApprovalSettlement(t, store, request, 3, "startup_recovery")
}

func testPendingApprovalAlreadyResolved(t *testing.T, store approvalRecoveryContractService) {
	ctx := t.Context()
	active := startApprovalRecoverySession(t, store)
	fence, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "runtime-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeGuard := session.RuntimeMutationGuard(session.ContextWithRuntimeFence(ctx, fence))
	request := pendingApprovalSettlement(t, store, active.SessionRef, runtimeGuard)
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval),
		Event: &session.Event{
			Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror,
			ApprovalRequestID: request.ApprovalRequestID,
			Lifecycle:         &session.EventLifecycle{Status: "completed", Reason: "selected"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseSessionFence(ctx, session.SessionFenceReleaseRequest(fence)); err != nil {
		t.Fatal(err)
	}
	if err := store.SettlePendingApproval(ctx, request); err != nil {
		t.Fatal(err)
	}
	assertApprovalSettlement(t, store, request, 2, "selected")
}

func startApprovalRecoverySession(t *testing.T, store approvalRecoveryContractService) session.Session {
	t.Helper()
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func pendingApprovalSettlement(t *testing.T, store approvalRecoveryContractService, ref session.SessionRef, guard session.MutationGuard) session.SettlePendingApprovalRequest {
	t.Helper()
	const requestID = "approval-contract"
	ctx := t.Context()
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref, MutationGuard: guard,
		Event: &session.Event{
			Type: session.EventTypeCustom, Visibility: session.VisibilityMirror,
			ApprovalRequestID: requestID,
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodRequestPermission,
				Permission: &session.ProtocolApproval{
					ToolCall: session.ProtocolToolCall{ID: "call-contract", Name: "Write"},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingApprovals(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingApprovals() = %#v, %v, want one candidate", pending, err)
	}
	candidate := pending[0]
	if candidate.Revision == 0 || candidate.Request.ID == "" || candidate.Request.Seq == 0 {
		t.Fatalf("candidate identity = %#v, want revision/event/seq CAS", candidate)
	}
	return session.SettlePendingApprovalRequest{
		SessionRef:             candidate.SessionRef,
		ExpectedRevision:       &candidate.Revision,
		MutationGuard:          session.ControlMutationGuard(session.ControlMutationPurposeLifecycle),
		ApprovalRequestID:      candidate.Request.ApprovalRequestID,
		ExpectedRequestEventID: candidate.Request.ID,
		ExpectedRequestSeq:     candidate.Request.Seq,
		Settlement: &session.Event{
			Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror,
			ApprovalRequestID: candidate.Request.ApprovalRequestID,
			Lifecycle:         &session.EventLifecycle{Status: "interrupted", Reason: "startup_recovery"},
		},
	}
}

func assertApprovalSettlement(t *testing.T, store approvalRecoveryContractService, request session.SettlePendingApprovalRequest, count int, reason string) {
	t.Helper()
	ctx := t.Context()
	page, err := store.EventsPage(ctx, session.EventPageRequest{
		SessionRef: request.SessionRef, Visibility: session.EventPageAllDurable,
	})
	if err != nil || len(page.Events) != count {
		t.Fatalf("events after settlement = %#v, %v, want %d events", page.Events, err, count)
	}
	settlement := page.Events[count-1]
	if settlement.ApprovalRequestID != request.ApprovalRequestID || settlement.Seq <= request.ExpectedRequestSeq ||
		settlement.Lifecycle == nil || settlement.Lifecycle.Reason != reason {
		t.Fatalf("durable settlement = %#v, want %q", settlement, reason)
	}
	pending, err := store.PendingApprovals(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("PendingApprovals(after settlement) = %#v, %v, want none", pending, err)
	}
}
