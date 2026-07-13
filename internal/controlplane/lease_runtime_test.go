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
	"github.com/caelis-labs/caelis/agent-sdk/model"
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

func TestLeasedRuntimeHeartbeatsDuringSynchronousRuntimeStartup(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "leased-startup",
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	releaseStartup := make(chan struct{})
	runner := newLeaseTestRunner("run-startup")
	runtime := &leaseStartupRuntime{
		sessions: service, ref: active.SessionRef, started: started,
		release: releaseStartup, runner: runner,
	}
	wrapped, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: runtime, Leases: service, OwnerID: "host-a",
		TTL: 90 * time.Millisecond, HeartbeatInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	var run agent.RunResult
	go func() {
		var runErr error
		run, runErr = wrapped.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
		result <- runErr
	}()
	<-started
	time.Sleep(140 * time.Millisecond)
	close(releaseStartup)
	if err := <-result; err != nil {
		t.Fatalf("Run() after startup longer than original TTL = %v", err)
	}
	runner.finish()
	for _, eventErr := range run.Handle.Events() {
		if eventErr != nil {
			t.Fatal(eventErr)
		}
	}
}

func TestLeasedRunnerCloseRetainsLeaseUntilProducerQuiescent(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "leased-close-barrier",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newLeaseCompletionRunner("run-close-barrier")
	wrapped, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: singleEventRuntime{runner: runner}, Leases: service,
		OwnerID: "host-a", TTL: 300 * time.Millisecond, HeartbeatInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapped.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- run.Handle.Close() }()
	<-runner.closed

	select {
	case err := <-closeDone:
		t.Fatalf("Close() returned before producer completion: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	if _, err := service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Second,
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("competing acquire before producer completion = %v, want lease conflict", err)
	}

	close(runner.producerDone)
	if err := <-closeDone; err != nil {
		t.Fatalf("Close() after producer completion = %v", err)
	}
	if _, err := service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Second,
	}); err != nil {
		t.Fatalf("acquire after producer completion = %v", err)
	}
}

func TestExecutePlacedCarriesFenceAndReleasesLease(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "placed-operation",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: leaseTestRuntime{}, Leases: service, OwnerID: "host-a", TTL: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := wrapped.ExecutePlaced(context.Background(), active.SessionRef, func(ctx context.Context) error {
		guard := session.RuntimeMutationGuard(ctx)
		if guard.Authority != session.MutationAuthorityRuntime || guard.LeaseID == "" || guard.FencingToken == 0 {
			t.Fatalf("mutation guard = %#v, want live placement fence", guard)
		}
		_, acquireErr := service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
			SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Second,
		})
		if !errors.Is(acquireErr, session.ErrLeaseConflict) {
			t.Fatalf("competing acquire error = %v, want lease conflict", acquireErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("ExecutePlaced() error = %v", err)
	}
	if _, err := service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Second,
	}); err != nil {
		t.Fatalf("acquire after placed operation = %v, want released lease", err)
	}
}

func TestExecutePlacedHeartbeatsDuringLongCallback(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "placed-heartbeat",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: leaseTestRuntime{}, Leases: service,
		OwnerID: "host-a", TTL: 300 * time.Millisecond, HeartbeatInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := wrapped.ExecutePlaced(context.Background(), active.SessionRef, func(ctx context.Context) error {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			lease, leaseErr := service.SessionLease(context.Background(), active.SessionRef)
			if leaseErr == nil && lease.Revision > 1 {
				// Survive longer than the original TTL only because heartbeats run.
				time.Sleep(350 * time.Millisecond)
				if _, acquireErr := service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
					SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Second,
				}); !errors.Is(acquireErr, session.ErrLeaseConflict) {
					t.Fatalf("competing acquire during placed op = %v, want conflict while heartbeating", acquireErr)
				}
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("placed lease never heartbeated above revision 1")
		return nil
	}); err != nil {
		t.Fatalf("ExecutePlaced() error = %v", err)
	}
	if _, err := service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Second,
	}); err != nil {
		t.Fatalf("acquire after heartbeating placed op = %v, want released", err)
	}
}

func TestExecutePlacedCancelsCallbackWhenHeartbeatFails(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "placed-heartbeat-cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	leasing := &heartbeatFailLeaseService{SessionLeaseService: service, err: errors.New("heartbeat unavailable")}
	wrapped, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: leaseTestRuntime{}, Leases: leasing,
		OwnerID: "host-a", TTL: 100 * time.Millisecond, HeartbeatInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	sawCancel := false
	err = wrapped.ExecutePlaced(context.Background(), active.SessionRef, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			sawCancel = true
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return errors.New("placed callback was not cancelled after heartbeat failure")
		}
	})
	if err == nil {
		t.Fatal("ExecutePlaced() error = nil, want heartbeat failure")
	}
	if !sawCancel {
		t.Fatal("placed callback context was not cancelled on lease loss")
	}
	if !strings.Contains(err.Error(), "heartbeat unavailable") && !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecutePlaced() error = %v, want heartbeat or cancel signal", err)
	}
}

func TestExecutePlacedRetainsHeartbeatFailureThatArrivesDuringFinish(t *testing.T) {
	t.Parallel()
	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "late-heartbeat-error",
	})
	if err != nil {
		t.Fatal(err)
	}
	leasing := &lateHeartbeatLeaseService{
		SessionLeaseService: service, started: make(chan struct{}), release: make(chan struct{}),
	}
	wrapper, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: leaseTestRuntime{}, Leases: leasing, OwnerID: "host-a",
		TTL: 100 * time.Millisecond, HeartbeatInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = wrapper.ExecutePlaced(context.Background(), active.SessionRef, func(context.Context) error {
		<-leasing.started
		go func() {
			time.Sleep(10 * time.Millisecond)
			close(leasing.release)
		}()
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "late heartbeat failure") {
		t.Fatalf("ExecutePlaced error = %v, want late heartbeat failure", err)
	}
}

func TestLeasedRunnerCloseRetainsHeartbeatFailure(t *testing.T) {
	t.Parallel()
	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "close-heartbeat-error",
	})
	if err != nil {
		t.Fatal(err)
	}
	leasing := &lateHeartbeatLeaseService{
		SessionLeaseService: service, started: make(chan struct{}), release: make(chan struct{}),
	}
	wrapper, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: leaseTestRuntime{runner: newLeaseTestRunner("close-heartbeat-run")}, Leases: leasing,
		OwnerID: "host-a", TTL: 100 * time.Millisecond, HeartbeatInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	<-leasing.started
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(leasing.release)
	}()
	if err := run.Handle.Close(); err == nil || !strings.Contains(err.Error(), "late heartbeat failure") {
		t.Fatalf("Close error = %v, want late heartbeat failure", err)
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

func TestLeasedRuntimeEarlyConsumerStopDoesNotYieldCleanupError(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "early-stop",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &singleEventRunner{id: "early-stop-run"}
	leasing := &releaseErrorLeaseService{SessionLeaseService: service}
	wrapper, err := NewLeasedRuntime(LeasedRuntimeConfig{
		Runtime: singleEventRuntime{runner: runner}, Leases: leasing, OwnerID: "host-a", TTL: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for range run.Handle.Events() {
		seen++
		break
	}
	if seen != 1 {
		t.Fatalf("events seen = %d, want 1", seen)
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

type releaseErrorLeaseService struct{ session.SessionLeaseService }

func (s *releaseErrorLeaseService) ReleaseSessionLease(ctx context.Context, req session.ReleaseSessionLeaseRequest) error {
	_ = s.SessionLeaseService.ReleaseSessionLease(ctx, req)
	return errors.New("release failed after early consumer stop")
}

type lateHeartbeatLeaseService struct {
	session.SessionLeaseService
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *lateHeartbeatLeaseService) HeartbeatSessionLease(context.Context, session.HeartbeatSessionLeaseRequest) (session.SessionLease, error) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return session.SessionLease{}, errors.New("late heartbeat failure")
}

type singleEventRuntime struct{ runner agent.Runner }

func (r singleEventRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{Handle: r.runner}, nil
}

func (singleEventRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type singleEventRunner struct{ id string }

func (r *singleEventRunner) RunID() string { return r.id }
func (*singleEventRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		yield(&session.Event{Type: session.EventTypeNotice, Text: "one"}, nil)
	}
}
func (*singleEventRunner) Submit(agent.Submission) error { return nil }
func (*singleEventRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (*singleEventRunner) Close() error { return nil }

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

type leaseStartupRuntime struct {
	sessions session.Service
	ref      session.SessionRef
	started  chan struct{}
	release  chan struct{}
	runner   agent.Runner
}

func (r *leaseStartupRuntime) Run(ctx context.Context, _ agent.RunRequest) (agent.RunResult, error) {
	close(r.started)
	select {
	case <-r.release:
	case <-ctx.Done():
		return agent.RunResult{}, ctx.Err()
	}
	message := model.NewTextMessage(model.RoleAssistant, "startup survived")
	if _, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: r.ref, MutationGuard: session.RuntimeMutationGuard(ctx),
		Event: &session.Event{Type: session.EventTypeAssistant, Message: &message},
	}); err != nil {
		return agent.RunResult{}, err
	}
	return agent.RunResult{Handle: r.runner}, nil
}

func (*leaseStartupRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type leaseCompletionRunner struct {
	id           string
	closed       chan struct{}
	closeOnce    sync.Once
	producerDone chan struct{}
}

func newLeaseCompletionRunner(id string) *leaseCompletionRunner {
	return &leaseCompletionRunner{id: id, closed: make(chan struct{}), producerDone: make(chan struct{})}
}

func (r *leaseCompletionRunner) RunID() string { return r.id }
func (*leaseCompletionRunner) Events() iter.Seq2[*session.Event, error] {
	return func(func(*session.Event, error) bool) {}
}
func (*leaseCompletionRunner) Submit(agent.Submission) error { return nil }
func (*leaseCompletionRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (r *leaseCompletionRunner) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}
func (r *leaseCompletionRunner) WaitCompletion(ctx context.Context) error {
	select {
	case <-r.producerDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
