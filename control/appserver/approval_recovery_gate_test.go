package appserver

import (
	"context"
	"errors"
	"sync"
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

type cancelAwareApprovalRecoveryStore struct {
	started chan struct{}
}

type deferredApprovalRecoveryStore struct {
	retryAt     time.Time
	settleCalls int
}

func (s *cancelAwareApprovalRecoveryStore) ListSessions(ctx context.Context, _ session.ListSessionsRequest) (session.SessionList, error) {
	close(s.started)
	<-ctx.Done()
	return session.SessionList{}, ctx.Err()
}

func (*cancelAwareApprovalRecoveryStore) EventsPage(context.Context, session.EventPageRequest) (session.EventPage, error) {
	return session.EventPage{}, nil
}

func (*cancelAwareApprovalRecoveryStore) Session(context.Context, session.SessionRef) (session.Session, error) {
	return session.Session{}, nil
}

func (*cancelAwareApprovalRecoveryStore) SettlePendingApproval(context.Context, session.SettlePendingApprovalRequest) (session.SettlePendingApprovalResult, error) {
	return session.SettlePendingApprovalResult{}, nil
}

func (*deferredApprovalRecoveryStore) ListSessions(context.Context, session.ListSessionsRequest) (session.SessionList, error) {
	return session.SessionList{}, nil
}

func (*deferredApprovalRecoveryStore) EventsPage(context.Context, session.EventPageRequest) (session.EventPage, error) {
	return session.EventPage{}, nil
}

func (*deferredApprovalRecoveryStore) Session(context.Context, session.SessionRef) (session.Session, error) {
	return session.Session{}, nil
}

func (*deferredApprovalRecoveryStore) PendingApprovals(context.Context) ([]session.PendingApproval, error) {
	return []session.PendingApproval{{
		SessionRef: session.SessionRef{SessionID: "deferred-close-session"},
		Revision:   1,
		Request: &session.Event{
			ID:                "deferred-close-event",
			Seq:               1,
			SessionID:         "deferred-close-session",
			Type:              session.EventTypeCustom,
			ApprovalRequestID: "deferred-close-approval",
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodRequestPermission,
				Permission: &session.ProtocolApproval{
					ToolCall: session.ProtocolToolCall{ID: "deferred-close-call", Name: "Write"},
				},
			},
		},
	}}, nil
}

func (s *deferredApprovalRecoveryStore) SettlePendingApproval(context.Context, session.SettlePendingApprovalRequest) (session.SettlePendingApprovalResult, error) {
	s.settleCalls++
	return session.SettlePendingApprovalResult{}, session.ErrLeaseConflict
}

func (s *deferredApprovalRecoveryStore) SessionLease(_ context.Context, ref session.SessionRef) (session.SessionLease, error) {
	return session.SessionLease{SessionRef: ref, LeaseID: "foreign-runtime", ExpiresAt: s.retryAt}, nil
}

type approvalRecoveryClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *approvalRecoveryClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *approvalRecoveryClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
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
	t.Cleanup(gate.Close)
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
	t.Cleanup(gate.Close)
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

func TestApprovalRecoveryGateCloseWaitsForStoreAccessToStop(t *testing.T) {
	store := &cancelAwareApprovalRecoveryStore{started: make(chan struct{})}
	gate := NewApprovalRecoveryGate(store)
	t.Cleanup(gate.Close)
	gate.Start(context.Background())
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("approval recovery did not start")
	}

	gate.Close()
	if err := gate.Wait(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() after Close() error = %v, want context canceled", err)
	}
}

func TestApprovalRecoveryGateCloseInterruptsDeferredRetryWithoutOverwritingSuccess(t *testing.T) {
	store := &deferredApprovalRecoveryStore{retryAt: time.Now().Add(time.Hour)}
	gate := NewApprovalRecoveryGate(store)
	t.Cleanup(gate.Close)
	gate.Start(context.Background())
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatalf("initial Wait() error = %v", err)
	}
	if store.settleCalls != 1 {
		t.Fatalf("initial settlement attempts = %d, want 1", store.settleCalls)
	}

	closed := make(chan struct{})
	go func() {
		gate.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close() did not interrupt deferred retry timer")
	}
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() after deferred retry cancellation = %v, want retained success", err)
	}
	if store.settleCalls != 1 {
		t.Fatalf("settlement attempts after Close() = %d, want 1", store.settleCalls)
	}
}

func TestApprovalRecoveryGateDefersForeignLeaseAndSettlesAfterExpiry(t *testing.T) {
	const leaseTTL = 100 * time.Millisecond
	clock := &approvalRecoveryClock{now: time.Now()}
	store := inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "deferred-recovery-session" },
		Clock:              clock.Now,
	})
	service := store
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := service.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "foreign-runtime", TTL: leaseTTL,
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
				ToolCall: session.ProtocolToolCall{ID: "call-deferred", Name: "Write"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	gate := NewApprovalRecoveryGate(service)
	t.Cleanup(gate.Close)
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

	clock.Advance(leaseTTL + time.Nanosecond)
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
