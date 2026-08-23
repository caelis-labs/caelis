package appserver

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

type resolveAfterRecoverySnapshotStore struct {
	*sessionfile.Store
	fence session.SessionFence
	once  sync.Once
	err   error
}

func (s *resolveAfterRecoverySnapshotStore) PendingApprovals(ctx context.Context) ([]session.PendingApproval, error) {
	pending, err := s.Store.PendingApprovals(ctx)
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
		s.err = s.ReleaseSessionFence(ctx, session.SessionFenceReleaseRequest(s.fence))
	})
	return pending, s.err
}

func TestSweepAbandonedApprovalsDoesNotOverwriteResolutionAfterCandidateSnapshot(t *testing.T) {
	ctx := context.Background()
	service := sessionfile.NewStore(sessionfile.Config{
		RootDir: t.TempDir(), SessionIDGenerator: func() string { return "session-recovery-cas" },
	})
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := service.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "runtime-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.RuntimeMutationGuard(session.ContextWithRuntimeFence(ctx, fence)),
		Event: &session.Event{
			Type: session.EventTypeCustom, Visibility: session.VisibilityMirror,
			ApprovalRequestID: "approval-recovery-cas",
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodRequestPermission,
				Permission: &session.ProtocolApproval{
					ToolCall: session.ProtocolToolCall{ID: "call-recovery-cas", Name: "Write"},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	recovery := &resolveAfterRecoverySnapshotStore{Store: service, fence: fence}
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

func TestSweepAbandonedApprovalsDefersLiveForeignFenceThenInterruptsOnceAfterRelease(t *testing.T) {
	ctx := context.Background()
	store := inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "session-1" },
	})
	runtimeService := store
	recoveryService := store
	active, err := runtimeService.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := runtimeService.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "foreign-runtime",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtimeService.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.RuntimeMutationGuard(session.ContextWithRuntimeFence(ctx, fence)),
		Event: &session.Event{
			Type: session.EventTypeCustom, Visibility: session.VisibilityMirror, ApprovalRequestID: "approval-1",
			Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{ID: "call-1", Name: "Write"},
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
		t.Fatalf("live foreign fence recovery events = %#v, want pending request only", page.Events)
	}

	if err := runtimeService.ReleaseSessionFence(ctx, session.SessionFenceReleaseRequest(fence)); err != nil {
		t.Fatal(err)
	}
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
	service := inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string {
		nextID++
		return fmt.Sprintf("session-%03d", nextID)
	}})
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
			ToolCall: session.ProtocolToolCall{ID: "call-last-page", Name: "Write"},
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
