package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

type approvalRecoveryContractService interface {
	session.Lifecycle
	session.EventAppender
	session.PagedReader
	session.ApprovalRecoveryService
}

func TestApprovalRecoverySettlementRevisionCASContract(t *testing.T) {
	factories := map[string]func(*testing.T) approvalRecoveryContractService{
		"file": func(t *testing.T) approvalRecoveryContractService {
			return sessionfile.NewStore(sessionfile.Config{
				RootDir: t.TempDir(), SessionIDGenerator: func() string { return "approval-contract-file" },
			})
		},
		"memory": func(*testing.T) approvalRecoveryContractService {
			return inmemory.NewStore(inmemory.Config{
				SessionIDGenerator: func() string { return "approval-contract-memory" },
			})
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			service := factory(t)
			active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
				SessionRef: active.SessionRef,
				Event: &session.Event{
					Type: session.EventTypeCustom, Visibility: session.VisibilityMirror,
					ApprovalRequestID: "approval-contract",
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
			pending, err := service.PendingApprovals(ctx)
			if err != nil || len(pending) != 1 {
				t.Fatalf("PendingApprovals() = %#v, %v, want one", pending, err)
			}
			candidate := pending[0]
			if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
				SessionRef: active.SessionRef,
				Event: &session.Event{
					Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror,
					Lifecycle: &session.EventLifecycle{Status: "running", Reason: "unrelated_revision"},
				},
			}); err != nil {
				t.Fatal(err)
			}

			expectedRevision := candidate.Revision
			request := session.SettlePendingApprovalRequest{
				SessionRef:             candidate.SessionRef,
				ExpectedRevision:       &expectedRevision,
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
			err = service.SettlePendingApproval(ctx, request)
			var conflict *session.RevisionConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("stale revision error = %v, want RevisionConflict", err)
			}
			expectedRevision = conflict.Actual
			if err := service.SettlePendingApproval(ctx, request); err != nil {
				t.Fatalf("matching CAS: %v", err)
			}
			page, err := service.EventsPage(ctx, session.EventPageRequest{
				SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable,
			})
			if err != nil || len(page.Events) != 3 {
				t.Fatalf("events after settlement = %#v, %v, want request, unrelated update, and settlement", page.Events, err)
			}
			settlement := page.Events[2]
			if settlement.ApprovalRequestID != request.ApprovalRequestID || settlement.Seq <= candidate.Request.Seq ||
				settlement.Lifecycle == nil || settlement.Lifecycle.Reason != "startup_recovery" {
				t.Fatalf("unexpected durable settlement: %#v", settlement)
			}
			pending, err = service.PendingApprovals(ctx)
			if err != nil || len(pending) != 0 {
				t.Fatalf("PendingApprovals(after settlement) = %#v, %v, want none", pending, err)
			}
		})
	}
}
