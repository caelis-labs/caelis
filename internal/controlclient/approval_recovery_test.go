package controlclient

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

type resolveAfterRecoverySnapshotStore struct {
	*sessionfile.Service
	lease session.SessionLease
	once  sync.Once
	err   error
}

func (s *resolveAfterRecoverySnapshotStore) PendingApprovals(ctx context.Context) ([]session.PendingApproval, error) {
	pending, err := s.Service.PendingApprovals(ctx)
	if err != nil || len(pending) == 0 {
		return pending, err
	}
	s.once.Do(func() {
		requestID := pending[0].Request.ApprovalRequestID
		_, s.err = s.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef:    pending[0].SessionRef,
			MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval),
			Event: &session.Event{
				Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror,
				ApprovalRequestID: requestID,
				Lifecycle:         &session.EventLifecycle{Status: "completed", Reason: "selected"},
			},
		})
		if s.err != nil {
			return
		}
		s.err = s.ReleaseSessionLease(ctx, session.ReleaseSessionLeaseRequest{
			SessionRef: pending[0].SessionRef, LeaseID: s.lease.LeaseID, OwnerID: s.lease.OwnerID,
			ExpectedLeaseRevision: s.lease.Revision,
		})
	})
	return pending, s.err
}

func TestSweepAbandonedApprovalsDoesNotOverwriteResolutionAfterCandidateSnapshot(t *testing.T) {
	ctx := context.Background()
	service := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir: t.TempDir(), SessionIDGenerator: func() string { return "session-recovery-cas" },
	}))
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
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef,
		MutationGuard: session.MutationGuard{
			Authority: session.MutationAuthorityRuntime, LeaseID: lease.LeaseID,
			OwnerID: lease.OwnerID, FencingToken: lease.FencingToken,
		},
		Event: &session.Event{
			Type: session.EventTypeCustom, Visibility: session.VisibilityMirror,
			ApprovalRequestID: "approval-recovery-cas",
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodRequestPermission,
				Permission: &session.ProtocolApproval{
					ToolCall: session.ProtocolToolCall{ID: "call-recovery-cas", Name: "WRITE"},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	recovery := &resolveAfterRecoverySnapshotStore{Service: service, lease: lease}
	if err := SweepAbandonedApprovals(ctx, recovery); err != nil {
		t.Fatal(err)
	}
	page, err := service.EventsPage(ctx, session.EventPageRequest{
		SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[1].Lifecycle == nil || page.Events[1].Lifecycle.Reason != "selected" {
		t.Fatalf("events after interleaved recovery = %#v, want request plus real settlement only", page.Events)
	}
	for _, event := range page.Events {
		if event.Lifecycle != nil && event.Lifecycle.Reason == "startup_recovery" {
			t.Fatalf("startup recovery appended after real resolution: %#v", page.Events)
		}
	}
}

func TestSweepAbandonedApprovalsDefersLiveForeignLeaseThenInterruptsOnceAfterExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	store := inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "session-1" },
		Clock:              func() time.Time { return now },
	})
	runtimeService := inmemory.NewService(store)
	recoveryService := inmemory.NewService(store)
	active, err := runtimeService.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := runtimeService.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "foreign-runtime", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtimeService.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef,
		MutationGuard: session.MutationGuard{
			Authority: session.MutationAuthorityRuntime, LeaseID: lease.LeaseID,
			OwnerID: lease.OwnerID, FencingToken: lease.FencingToken,
		},
		Event: &session.Event{
			Type: session.EventTypeCustom, Visibility: session.VisibilityMirror, ApprovalRequestID: "approval-1",
			Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{ID: "call-1", Name: "WRITE"},
			}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := SweepAbandonedApprovals(ctx, recoveryService); err != nil {
		t.Fatal(err)
	}
	page, err := recoveryService.EventsPage(ctx, session.EventPageRequest{
		SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].ApprovalRequestID != "approval-1" {
		t.Fatalf("live foreign lease recovery events = %#v, want pending request only", page.Events)
	}

	now = now.Add(time.Minute + time.Nanosecond)
	if err := SweepAbandonedApprovals(ctx, recoveryService); err != nil {
		t.Fatal(err)
	}
	if err := SweepAbandonedApprovals(ctx, recoveryService); err != nil {
		t.Fatal(err)
	}
	page, err = recoveryService.EventsPage(ctx, session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[1].Lifecycle == nil || page.Events[1].Lifecycle.Status != "interrupted" || page.Events[1].ApprovalRequestID != "approval-1" {
		t.Fatalf("recovered approval events = %#v", page.Events)
	}
}

func TestSweepAbandonedApprovalsContinuesPastTwoHundredSessions(t *testing.T) {
	ctx := context.Background()
	nextID := 0
	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string {
		nextID++
		return fmt.Sprintf("session-%03d", nextID)
	}}))
	var target session.Session
	for i := 0; i < 205; i++ {
		active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 204 {
			target = active
		}
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{SessionRef: target.SessionRef, Event: &session.Event{
		Type: session.EventTypeCustom, Visibility: session.VisibilityMirror, ApprovalRequestID: "approval-last-page",
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{ID: "call-last-page", Name: "WRITE"},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := SweepAbandonedApprovals(ctx, service); err != nil {
		t.Fatal(err)
	}
	page, err := service.EventsPage(ctx, session.EventPageRequest{SessionRef: target.SessionRef, Visibility: session.EventPageClientReplay})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[1].Lifecycle == nil || page.Events[1].ApprovalRequestID != "approval-last-page" {
		t.Fatalf("last-page recovered events = %#v", page.Events)
	}
}
