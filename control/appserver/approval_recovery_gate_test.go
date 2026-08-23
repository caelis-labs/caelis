package appserver

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

type testPriorHostFenceReplacer struct {
	fences session.PriorHostFenceService
}

func allowPriorHostFence(context.Context) (func(), bool) { return func() {}, true }

func (r testPriorHostFenceReplacer) ReplacePriorHostFence(
	ctx context.Context,
	req session.AcquireSessionFenceRequest,
) (session.SessionFence, error) {
	return r.fences.ReplacePriorHostSessionFence(ctx, req)
}

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

type transientApprovalRecoveryReleaseStore struct {
	*inmemory.Store
	mu           sync.Mutex
	releaseCalls int
}

func (s *transientApprovalRecoveryReleaseStore) ReleaseSessionFence(
	ctx context.Context,
	req session.ReleaseSessionFenceRequest,
) error {
	s.mu.Lock()
	s.releaseCalls++
	call := s.releaseCalls
	s.mu.Unlock()
	if call == 1 {
		return errors.New("transient approval recovery release failure")
	}
	return s.Store.ReleaseSessionFence(ctx, req)
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
	return session.SettlePendingApprovalResult{}, session.ErrFenceConflict
}

func (s *deferredApprovalRecoveryStore) SessionFence(_ context.Context, ref session.SessionRef) (session.SessionFence, error) {
	return session.SessionFence{SessionRef: ref, FenceID: "foreign-runtime"}, nil
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
	gate := NewApprovalRecoveryGate(ApprovalRecoveryGateConfig{Store: store})
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
	gate := NewApprovalRecoveryGate(ApprovalRecoveryGateConfig{Store: store})
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
	gate := NewApprovalRecoveryGate(ApprovalRecoveryGateConfig{Store: store})
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
	gate := NewApprovalRecoveryGate(ApprovalRecoveryGateConfig{Store: store})
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

func TestApprovalRecoveryGateDefersForeignFenceAndSettlesAfterRelease(t *testing.T) {
	store := inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "deferred-recovery-session" },
	})
	service := store
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := service.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "foreign-runtime",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.RuntimeMutationGuard(session.ContextWithRuntimeFence(ctx, fence)),
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

	gate := NewApprovalRecoveryGate(ApprovalRecoveryGateConfig{Store: service})
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
		t.Fatalf("events while foreign fence is held = %#v, want pending request only", page.Events)
	}

	if err := service.ReleaseSessionFence(ctx, session.SessionFenceReleaseRequest(fence)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
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
		t.Fatalf("events after foreign fence release = %#v, want one deferred settlement", page.Events)
	}
}

func TestApprovalRecoveryGateReplacesPriorHostFenceAtStartup(t *testing.T) {
	store, priorHostFences := inmemory.NewStoreWithPriorHostFences(inmemory.Config{
		SessionIDGenerator: func() string { return "prior-host-recovery-session" },
	}, allowPriorHostFence)
	ctx := context.Background()
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	prior, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "prior-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.RuntimeMutationGuard(session.ContextWithRuntimeFence(ctx, prior)),
		Event: &session.Event{
			Type: session.EventTypeCustom, Visibility: session.VisibilityMirror,
			ApprovalRequestID: "approval-prior-host",
			Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{ID: "call-prior-host", Name: "Write"},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	gate := NewApprovalRecoveryGate(ApprovalRecoveryGateConfig{
		Store: store, FenceOwnerID: "current-host", PriorHostFences: testPriorHostFenceReplacer{fences: priorHostFences},
	})
	t.Cleanup(gate.Close)
	if err := gate.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := store.EventsPage(ctx, session.EventPageRequest{
		SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[1].Lifecycle == nil || page.Events[1].Lifecycle.Reason != "startup_recovery" {
		t.Fatalf("events after Host takeover recovery = %#v", page.Events)
	}
	durable, err := store.SessionFence(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if durable.FenceID != "" {
		t.Fatalf("startup recovery fence = %#v, want released", durable)
	}
}

func TestApprovalRecoveryGateRetriesTransientFenceRelease(t *testing.T) {
	store, priorHostFences := inmemory.NewStoreWithPriorHostFences(inmemory.Config{
		SessionIDGenerator: func() string { return "recovery-release-retry-session" },
	}, allowPriorHostFence)
	ctx := context.Background()
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	prior, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "prior-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.RuntimeMutationGuard(session.ContextWithRuntimeFence(ctx, prior)),
		Event: &session.Event{
			Type: session.EventTypeCustom, Visibility: session.VisibilityMirror,
			ApprovalRequestID: "approval-release-retry",
			Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{ID: "call-release-retry", Name: "Write"},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	retrying := &transientApprovalRecoveryReleaseStore{Store: store}
	var diagnostics bytes.Buffer
	gate := NewApprovalRecoveryGate(ApprovalRecoveryGateConfig{
		Store: retrying, FenceOwnerID: "current-host", PriorHostFences: testPriorHostFenceReplacer{fences: priorHostFences},
		Diagnostics: slog.New(slog.NewJSONHandler(&diagnostics, nil)),
	})
	t.Cleanup(gate.Close)
	if err := gate.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	retrying.mu.Lock()
	releaseCalls := retrying.releaseCalls
	retrying.mu.Unlock()
	if releaseCalls < 2 {
		t.Fatalf("approval recovery release calls = %d, want retry", releaseCalls)
	}
	logged := diagnostics.String()
	for _, want := range []string{`"phase":"startup_release"`, `"outcome":"error"`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("approval recovery diagnostics = %q, want %s", logged, want)
		}
	}
	for _, secret := range []string{active.SessionID, "approval-release-retry", prior.FenceID} {
		if strings.Contains(logged, secret) {
			t.Fatalf("approval recovery diagnostics leaked %q: %s", secret, logged)
		}
	}
	durable, err := store.SessionFence(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if durable.FenceID != "" {
		t.Fatalf("approval recovery fence after retry = %#v, want released", durable)
	}
}
