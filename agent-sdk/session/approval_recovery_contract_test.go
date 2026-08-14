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
	session.Reader
	session.EventAppender
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
							ToolCall: session.ProtocolToolCall{ID: "call-contract", Name: "WRITE"},
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
			result, err := service.SettlePendingApproval(ctx, request)
			var conflict *session.RevisionConflictError
			if !errors.As(err, &conflict) || result.Settled {
				t.Fatalf("stale revision result = %#v, error = %v, want RevisionConflict", result, err)
			}
			expectedRevision = conflict.Actual
			result, err = service.SettlePendingApproval(ctx, request)
			if err != nil || !result.Settled || result.Event == nil || result.Event.Lifecycle == nil ||
				result.Event.Lifecycle.Reason != "startup_recovery" {
				t.Fatalf("matching CAS result = %#v, error = %v", result, err)
			}
			pending, err = service.PendingApprovals(ctx)
			if err != nil || len(pending) != 0 {
				t.Fatalf("PendingApprovals(after settlement) = %#v, %v, want none", pending, err)
			}
		})
	}
}
