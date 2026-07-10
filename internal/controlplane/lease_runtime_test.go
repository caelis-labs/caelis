package controlplane

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestLeasedRuntimeHeartbeatsUntilRunnerCompletesThenReleases(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "leased-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newLeaseTestRunner("run-1")
	wrapped, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: leaseTestRuntime{runner: runner}, Leases: service,
		OwnerID: "host-a", TTL: 300 * time.Millisecond, HeartbeatInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapped.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := wrapped.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("same-owner second Run() error = %v, want lease conflict without releasing the first run", err)
	}
	if _, err := service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Second,
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("competing acquire error = %v, want live lease conflict", err)
	}
	deadline := time.Now().Add(time.Second)
	var heartbeat session.SessionLease
	for time.Now().Before(deadline) {
		heartbeat, err = service.SessionLease(context.Background(), active.SessionRef)
		if err == nil && heartbeat.Revision > 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if heartbeat.Revision <= 1 {
		t.Fatalf("lease = %#v, want heartbeat revision > 1", heartbeat)
	}
	runner.finish()
	for _, eventErr := range run.Handle.Events() {
		if eventErr != nil {
			t.Fatalf("Events() error = %v", eventErr)
		}
	}
	if _, err := service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Second,
	}); err != nil {
		t.Fatalf("acquire after completion error = %v, want released lease", err)
	}
}

func TestLeasedRuntimeCancelsRunWhenHeartbeatFails(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "heartbeat-failure",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newLeaseTestRunner("run-heartbeat-failure")
	leasing := &heartbeatFailLeaseService{SessionLeaseService: service, err: errors.New("heartbeat unavailable")}
	wrapped, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: leaseTestRuntime{runner: runner}, Leases: leasing,
		OwnerID: "host-a", TTL: 100 * time.Millisecond, HeartbeatInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapped.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	var eventErr error
	for _, err := range run.Handle.Events() {
		if err != nil {
			eventErr = err
		}
	}
	if eventErr == nil || !strings.Contains(eventErr.Error(), "heartbeat unavailable") {
		t.Fatalf("Events() error = %v, want heartbeat failure", eventErr)
	}
	runner.mu.Lock()
	cancelCalls := runner.cancel
	runner.mu.Unlock()
	if cancelCalls != 1 {
		t.Fatalf("runner cancel calls = %d, want 1", cancelCalls)
	}
}

func TestLeasedRuntimeContinuesAfterCommittedAcquireReturnsDurableLease(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "committed-acquire",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newLeaseTestRunner("run-committed-acquire")
	leasing := &committedOutcomeLeaseService{SessionLeaseService: service, commitAcquire: true}
	wrapper, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: leaseTestRuntime{runner: runner}, Leases: leasing,
		OwnerID: "host-a", TTL: time.Second, HeartbeatInterval: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("Run() error = %v, want committed acquire recovery", err)
	}
	runner.finish()
	for _, eventErr := range run.Handle.Events() {
		if eventErr != nil {
			t.Fatal(eventErr)
		}
	}
	if _, err := service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Second,
	}); err != nil {
		t.Fatalf("acquire after recovered run error = %v, want released lease", err)
	}
}

func TestLeasedRuntimeKeepsHealthyRunAfterCommittedHeartbeatReturnsNewRevision(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "committed-heartbeat",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newLeaseTestRunner("run-committed-heartbeat")
	leasing := &committedOutcomeLeaseService{SessionLeaseService: service, commitHeartbeat: true}
	wrapper, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: leaseTestRuntime{runner: runner}, Leases: leasing,
		OwnerID: "host-a", TTL: 300 * time.Millisecond, HeartbeatInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		lease, readErr := service.SessionLease(context.Background(), active.SessionRef)
		if readErr == nil && lease.Revision > 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	runner.mu.Lock()
	cancelCalls := runner.cancel
	runner.mu.Unlock()
	if cancelCalls != 0 {
		t.Fatalf("runner cancel calls = %d, want healthy run retained", cancelCalls)
	}
	runner.finish()
	for _, eventErr := range run.Handle.Events() {
		if eventErr != nil {
			t.Fatal(eventErr)
		}
	}
}

type committedOutcomeLeaseService struct {
	session.SessionLeaseService
	commitAcquire   bool
	commitHeartbeat bool
}

func (s *committedOutcomeLeaseService) AcquireSessionLease(ctx context.Context, req session.AcquireSessionLeaseRequest) (session.SessionLease, error) {
	lease, err := s.SessionLeaseService.AcquireSessionLease(ctx, req)
	if err == nil && s.commitAcquire {
		s.commitAcquire = false
		return lease, &session.CommittedError{Err: errors.New("acquire report failed after commit")}
	}
	return lease, err
}

func (s *committedOutcomeLeaseService) HeartbeatSessionLease(ctx context.Context, req session.HeartbeatSessionLeaseRequest) (session.SessionLease, error) {
	lease, err := s.SessionLeaseService.HeartbeatSessionLease(ctx, req)
	if err == nil && s.commitHeartbeat {
		return lease, &session.CommittedError{Err: errors.New("heartbeat report failed after commit")}
	}
	return lease, err
}

type heartbeatFailLeaseService struct {
	session.SessionLeaseService
	err error
}

func (s *heartbeatFailLeaseService) HeartbeatSessionLease(context.Context, session.HeartbeatSessionLeaseRequest) (session.SessionLease, error) {
	return session.SessionLease{}, s.err
}

type leaseTestRuntime struct{ runner *leaseTestRunner }

func (r leaseTestRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{Handle: r.runner}, nil
}

func (leaseTestRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type leaseTestRunner struct {
	id           string
	complete     chan struct{}
	completeOnce sync.Once
	mu           sync.Mutex
	cancel       int
}

func newLeaseTestRunner(id string) *leaseTestRunner {
	return &leaseTestRunner{id: id, complete: make(chan struct{})}
}

func (r *leaseTestRunner) RunID() string { return r.id }

func (r *leaseTestRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		<-r.complete
	}
}

func (*leaseTestRunner) Submit(agent.Submission) error { return nil }

func (r *leaseTestRunner) Cancel() agent.CancelResult {
	r.mu.Lock()
	r.cancel++
	r.mu.Unlock()
	r.finish()
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (r *leaseTestRunner) Close() error { return nil }

func (r *leaseTestRunner) finish() {
	r.completeOnce.Do(func() { close(r.complete) })
}
