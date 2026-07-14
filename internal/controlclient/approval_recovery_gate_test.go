package controlclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

type blockingApprovalRecoveryStore struct {
	started chan struct{}
	release chan struct{}
	err     error
}

func (s *blockingApprovalRecoveryStore) ListSessions(context.Context, session.ListSessionsRequest) (session.SessionList, error) {
	close(s.started)
	<-s.release
	return session.SessionList{}, s.err
}

func (*blockingApprovalRecoveryStore) EventsPage(context.Context, session.EventPageRequest) (session.EventPage, error) {
	return session.EventPage{}, nil
}

func (*blockingApprovalRecoveryStore) Session(context.Context, session.SessionRef) (session.Session, error) {
	return session.Session{}, nil
}

func (*blockingApprovalRecoveryStore) SettlePendingApproval(
	context.Context,
	session.SettlePendingApprovalRequest,
) (session.SettlePendingApprovalResult, error) {
	return session.SettlePendingApprovalResult{}, nil
}

func TestApprovalRecoveryGateBlocksTurnsWithoutBlockingStartup(t *testing.T) {
	store := &blockingApprovalRecoveryStore{started: make(chan struct{}), release: make(chan struct{})}
	gate := NewApprovalRecoveryGate(store)
	gate.Start(context.Background())
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("approval recovery did not start")
	}

	waited := make(chan error, 1)
	go func() { waited <- gate.Wait(context.Background()) }()
	select {
	case err := <-waited:
		t.Fatalf("Wait() returned before recovery completed: %v", err)
	default:
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait(canceled) error = %v", err)
	}

	close(store.release)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait() did not return after recovery completed")
	}
}

func TestApprovalRecoveryGateRetainsSweepFailure(t *testing.T) {
	want := errors.New("recovery failed")
	store := &blockingApprovalRecoveryStore{started: make(chan struct{}), release: make(chan struct{}), err: want}
	gate := NewApprovalRecoveryGate(store)
	gate.Start(context.Background())
	<-store.started
	close(store.release)
	if err := gate.Wait(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Wait() error = %v, want %v", err, want)
	}
	if err := gate.Wait(context.Background()); !errors.Is(err, want) {
		t.Fatalf("second Wait() error = %v, want retained %v", err, want)
	}
}

func TestApprovalRecoveryGateDefersForeignLeaseAndSettlesAfterExpiry(t *testing.T) {
	store := inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return "deferred-recovery-session" }})
	service := inmemory.NewService(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := service.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "foreign-runtime", TTL: 80 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef,
		MutationGuard: session.MutationGuard{
			Authority: session.MutationAuthorityRuntime, LeaseID: lease.LeaseID,
			OwnerID: lease.OwnerID, FencingToken: lease.FencingToken,
		},
		Event: &session.Event{
			Type: session.EventTypeCustom, Visibility: session.VisibilityMirror, ApprovalRequestID: "approval-deferred",
			Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{ID: "call-deferred", Name: "WRITE"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	gate := NewApprovalRecoveryGate(service)
	gate.Start(ctx)
	if err := gate.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := service.EventsPage(ctx, session.EventPageRequest{
		SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events while foreign lease is live = %#v, want pending request only", page.Events)
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(page.Events) != 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		page, err = service.EventsPage(ctx, session.EventPageRequest{
			SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(page.Events) != 2 || page.Events[1].Lifecycle == nil || page.Events[1].Lifecycle.Reason != "startup_recovery" {
		t.Fatalf("events after foreign lease expiry = %#v, want one deferred settlement", page.Events)
	}
}
