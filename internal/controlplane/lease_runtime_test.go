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
	if _, err := service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Second,
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("competing acquire error = %v, want live lease conflict", err)
	}
	deadline := time.Now().Add(time.Second)
	var heartbeat session.SessionLease
	for time.Now().Before(deadline) {
		heartbeat, err = service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
			SessionRef: active.SessionRef, OwnerID: "host-a", TTL: time.Second,
		})
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
