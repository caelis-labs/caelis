package controlplane

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func allowPriorHostFence(context.Context) (func(), bool) { return func() {}, true }

func TestFencedRuntimeUsesHostScopedFenceWithoutRenewal(t *testing.T) {
	t.Parallel()

	service, priorHostFences := inmemory.NewStoreWithPriorHostFences(inmemory.Config{}, allowPriorHostFence)
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "host-fence",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newFenceTestRunner("run-host-a")
	wrapper, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: fenceTestRuntime{runner: runner}, Fences: service, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	durable, err := service.SessionFence(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if durable.OwnerID != "host-a" || durable.FencingToken == 0 {
		t.Fatalf("durable execution fence = %#v", durable)
	}
	if _, err := service.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-a",
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("same Host competing acquire = %v, want ErrFenceConflict", err)
	}
	if _, err := service.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b",
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("untrusted Host takeover = %v, want ErrFenceConflict", err)
	}

	replacement, err := priorHostFences.ReplacePriorHostSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b",
	})
	if err != nil {
		t.Fatalf("authorized Host takeover = %v", err)
	}
	if replacement.FencingToken <= durable.FencingToken {
		t.Fatalf("replacement fence = %#v, want token after %#v", replacement, durable)
	}
	recovered, err := wrapper.RecoverLostRun(context.Background(), active.SessionRef)
	if err != nil || !recovered {
		t.Fatalf("RecoverLostRun() = %v, %v, want replaced producer quiesced", recovered, err)
	}
	runner.mu.Lock()
	cancelCalls := runner.cancel
	runner.mu.Unlock()
	if cancelCalls != 1 {
		t.Fatalf("runner cancel calls = %d, want 1", cancelCalls)
	}
	if err := service.ReleaseSessionFence(context.Background(), session.SessionFenceReleaseRequest(replacement)); err != nil {
		t.Fatal(err)
	}
	_ = run.Handle.Close()
}

func TestFencedRuntimeSleepDoesNotRenewOrExpireTurnFence(t *testing.T) {
	t.Parallel()

	store := inmemory.NewStore(inmemory.Config{})
	active, err := store.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "sleep-with-fence",
	})
	if err != nil {
		t.Fatal(err)
	}
	fences := &countingSessionFenceService{SessionFenceService: store}
	runner := newFenceTestRunner("run-sleep")
	wrapper, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: fenceTestRuntime{runner: runner}, Fences: fences, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	acquires, releases := fences.calls()
	if acquires != 1 || releases != 0 {
		t.Fatalf("fence calls after admission = acquire %d / release %d, want 1/0", acquires, releases)
	}

	// This pause is deliberately longer than the focused test's scheduling
	// granularity. A one-shot fence has no timer or storage activity to wake.
	time.Sleep(150 * time.Millisecond)
	acquires, releases = fences.calls()
	if acquires != 1 || releases != 0 {
		t.Fatalf("fence calls while producer slept = acquire %d / release %d, want 1/0", acquires, releases)
	}
	if durable, err := store.SessionFence(context.Background(), active.SessionRef); err != nil || durable.FenceID == "" {
		t.Fatalf("durable fence after sleep = %#v, %v; want active", durable, err)
	}

	runner.finish()
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, releases = fences.calls()
	if releases != 1 {
		t.Fatalf("release calls after producer completion = %d, want 1", releases)
	}
}

func TestNewFencedRuntimeRequiresCompletionCapability(t *testing.T) {
	service := inmemory.NewStore(inmemory.Config{})
	if _, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: unverifiedRuntime{}, Fences: service, OwnerID: "host-a",
	}); err == nil {
		t.Fatal("NewFencedRuntime() error = nil, want completion capability preflight")
	}
}

func TestExecutePlacedCarriesFenceAndReleases(t *testing.T) {
	t.Parallel()

	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "placed-fence",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = executeWithSessionFence(context.Background(), service, "host-a", context.Background(), nil, active.SessionRef, func(ctx context.Context) error {
		message := model.NewTextMessage(model.RoleAssistant, "fenced")
		_, appendErr := service.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: active.SessionRef, MutationGuard: session.RuntimeMutationGuard(ctx),
			Event: &session.Event{Type: session.EventTypeAssistant, Message: &message},
		})
		return appendErr
	})
	if err != nil {
		t.Fatal(err)
	}
	durable, err := service.SessionFence(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if durable.FenceID != "" {
		t.Fatalf("durable fence after callback = %#v, want released", durable)
	}
}

func TestFencedRuntimeContinuesAfterCommittedAcquire(t *testing.T) {
	t.Parallel()

	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "committed-acquire",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newFenceTestRunner("run-committed-acquire")
	fences := &committedAcquireFenceService{SessionFenceService: service}
	wrapper, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: fenceTestRuntime{runner: runner}, Fences: fences, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("Run() error = %v, want committed acquire confirmation", err)
	}
	runner.finish()
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestFencedRuntimeFailsClosedWhenCommittedAcquireOmitsClaim(t *testing.T) {
	t.Parallel()

	service, priorHostFences := inmemory.NewStoreWithPriorHostFences(inmemory.Config{}, allowPriorHostFence)
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "committed-acquire-without-claim",
	})
	if err != nil {
		t.Fatal(err)
	}
	fences := &committedAcquireFenceService{SessionFenceService: service, dropResult: true}
	wrapper, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: fenceTestRuntime{runner: newFenceTestRunner("must-not-run")}, Fences: fences, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef}); !session.IsCommitted(err) {
		t.Fatalf("Run() error = %v, want committed acquire failure without bearer claim", err)
	}
	durable, err := service.SessionFence(context.Background(), active.SessionRef)
	if err != nil || durable.FenceID == "" || durable.OwnerID != "host-a" {
		t.Fatalf("durable fence after dropped result = %#v, %v", durable, err)
	}
	recovered, err := priorHostFences.ReplacePriorHostSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b",
	})
	if err != nil {
		t.Fatalf("next Host recovery = %v", err)
	}
	if err := service.ReleaseSessionFence(context.Background(), session.SessionFenceReleaseRequest(recovered)); err != nil {
		t.Fatalf("release recovered fence = %v", err)
	}
}

func TestFencedRuntimeReleasesAfterProducerCompletesWithoutObserver(t *testing.T) {
	t.Parallel()

	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "unobserved-completion",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newFenceTestRunner("run-unobserved")
	wrapper, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: fenceTestRuntime{runner: runner}, Fences: service, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef}); err != nil {
		t.Fatal(err)
	}
	runner.finish()
	waitForSessionFence(t, service, active.SessionRef, func(fence session.SessionFence) bool {
		return fence.FenceID == ""
	}, "producer completion did not release the fence without an observer")
}

func TestFencedRunnerCloseRetainsFenceUntilProducerQuiescent(t *testing.T) {
	t.Parallel()

	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "close-barrier",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newFenceCompletionRunner("run-close-barrier")
	wrapper, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: singleEventRuntime{runner: runner}, Fences: service, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
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
	if _, err := service.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b",
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("competing acquire before producer completion = %v, want ErrFenceConflict", err)
	}
	close(runner.producerDone)
	if err := <-closeDone; err != nil {
		t.Fatalf("Close() after producer completion = %v", err)
	}
	if _, err := service.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b",
	}); err != nil {
		t.Fatalf("acquire after producer completion = %v", err)
	}
}

func TestFencedRuntimeReleasesFenceAfterProducerCompletesWithTerminalError(t *testing.T) {
	t.Parallel()

	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "failed-completion-barrier",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newFenceTerminalErrorRunner("run-failed-completion")
	wrapper, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: singleEventRuntime{runner: runner}, Fences: service, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	close(runner.producerDone)
	if err := run.Handle.WaitCompletion(t.Context()); err == nil || err.Error() != "producer failed" {
		t.Fatalf("WaitCompletion() error = %v, want terminal producer error", err)
	}
	if _, err := service.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-b",
	}); err != nil {
		t.Fatalf("competing acquire after terminal producer error = %v", err)
	}
}

func TestFencedRuntimeRetriesTransientReleaseWithoutHostRestart(t *testing.T) {
	t.Parallel()

	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "release-retry",
	})
	if err != nil {
		t.Fatal(err)
	}
	fences := &transientReleaseFenceService{SessionFenceService: service, reader: service, failBeforeCommit: true}
	runner := newFenceTestRunner("run-release-retry")
	wrapper, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: fenceTestRuntime{runner: runner}, Fences: fences, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef}); err != nil {
		t.Fatal(err)
	}
	runner.finish()
	waitForSessionFence(t, service, active.SessionRef, func(fence session.SessionFence) bool {
		return fence.FenceID == ""
	}, "transient release failure permanently retained the fence")

	nextRunner := newFenceTestRunner("run-after-release-retry")
	next, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: fenceTestRuntime{runner: nextRunner}, Fences: fences, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := next.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("same Host Run after release retry = %v", err)
	}
	nextRunner.finish()
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestFencedRuntimeStopsReleaseRetryAfterHostLifecycleEnds(t *testing.T) {
	t.Parallel()

	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "release-lifecycle",
	})
	if err != nil {
		t.Fatal(err)
	}
	fences := &transientReleaseFenceService{SessionFenceService: service, reader: service, failAlways: true}
	runner := newFenceTestRunner("run-release-lifecycle")
	hostCtx, cancelHost := context.WithCancel(context.Background())
	wrapper, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: fenceTestRuntime{runner: runner}, Fences: fences, OwnerID: "host-a", LifecycleContext: hostCtx,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapper.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef}); err != nil {
		t.Fatal(err)
	}
	runner.finish()
	waitForReleaseCalls(t, fences, 1)
	cancelHost()
	fences.mu.Lock()
	callsAfterCancel := fences.releaseCalls
	fences.mu.Unlock()
	time.Sleep(250 * time.Millisecond)
	fences.mu.Lock()
	finalCalls := fences.releaseCalls
	fences.mu.Unlock()
	if finalCalls != callsAfterCancel {
		t.Fatalf("release retries after Host lifecycle ended = %d -> %d", callsAfterCancel, finalCalls)
	}
}

func TestReleaseReconcilesUnreportedCommitWithFreshReadContext(t *testing.T) {
	t.Parallel()

	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "release-readback",
	})
	if err != nil {
		t.Fatal(err)
	}
	fences := &transientReleaseFenceService{SessionFenceService: service, reader: service, failAfterCommit: true}
	fence, err := fences.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseSessionFence(fences, fence); err != nil {
		t.Fatalf("releaseSessionFence() error = %v, want committed outcome reconciled", err)
	}
	fences.mu.Lock()
	readUsedLiveContext := fences.readUsedLiveContext
	releaseCalls := fences.releaseCalls
	fences.mu.Unlock()
	if !readUsedLiveContext || releaseCalls != 1 {
		t.Fatalf("release reconciliation live read/calls = %v/%d, want true/1", readUsedLiveContext, releaseCalls)
	}
}

func waitForSessionFence(
	t *testing.T,
	reader session.SessionFenceReader,
	ref session.SessionRef,
	done func(session.SessionFence) bool,
	detail string,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		fence, err := reader.SessionFence(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if done(fence) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(detail)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type countingSessionFenceService struct {
	session.SessionFenceService
	mu       sync.Mutex
	acquires int
	releases int
}

func (s *countingSessionFenceService) AcquireSessionFence(ctx context.Context, req session.AcquireSessionFenceRequest) (session.SessionFence, error) {
	s.mu.Lock()
	s.acquires++
	s.mu.Unlock()
	return s.SessionFenceService.AcquireSessionFence(ctx, req)
}

func (s *countingSessionFenceService) ReleaseSessionFence(ctx context.Context, req session.ReleaseSessionFenceRequest) error {
	s.mu.Lock()
	s.releases++
	s.mu.Unlock()
	return s.SessionFenceService.ReleaseSessionFence(ctx, req)
}

func (s *countingSessionFenceService) calls() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquires, s.releases
}

func waitForReleaseCalls(t *testing.T, fences *transientReleaseFenceService, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		fences.mu.Lock()
		calls := fences.releaseCalls
		fences.mu.Unlock()
		if calls >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("release calls = %d, want at least %d", calls, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type committedAcquireFenceService struct {
	session.SessionFenceService
	once       sync.Once
	dropResult bool
}

type transientReleaseFenceService struct {
	session.SessionFenceService
	reader           session.SessionFenceReader
	failBeforeCommit bool
	failAfterCommit  bool
	failAlways       bool

	mu                  sync.Mutex
	releaseCalls        int
	readUsedLiveContext bool
}

func (s *transientReleaseFenceService) ReleaseSessionFence(ctx context.Context, req session.ReleaseSessionFenceRequest) error {
	s.mu.Lock()
	s.releaseCalls++
	call := s.releaseCalls
	failBefore := s.failBeforeCommit && call == 1
	failAfter := s.failAfterCommit && call == 1
	s.mu.Unlock()
	if failBefore || s.failAlways {
		return errors.New("transient release failure before commit")
	}
	err := s.SessionFenceService.ReleaseSessionFence(ctx, req)
	if err == nil && failAfter {
		return context.DeadlineExceeded
	}
	return err
}

func (s *transientReleaseFenceService) SessionFence(ctx context.Context, ref session.SessionRef) (session.SessionFence, error) {
	s.mu.Lock()
	s.readUsedLiveContext = s.readUsedLiveContext || ctx.Err() == nil
	s.mu.Unlock()
	return s.reader.SessionFence(ctx, ref)
}

func (s *committedAcquireFenceService) AcquireSessionFence(ctx context.Context, req session.AcquireSessionFenceRequest) (session.SessionFence, error) {
	fence, err := s.SessionFenceService.AcquireSessionFence(ctx, req)
	committed := false
	s.once.Do(func() { committed = true })
	if err == nil && committed {
		if s.dropResult {
			fence = session.SessionFence{}
		}
		return fence, &session.CommittedError{Err: errors.New("acquire report failed after commit")}
	}
	return fence, err
}

type singleEventRuntime struct{ runner agent.Runner }

func (singleEventRuntime) RunnerCompletionWaiterGuaranteed() {}

func (r singleEventRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{Handle: r.runner}, nil
}

func (singleEventRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type singleEventRunner struct{ id string }

func (r *singleEventRunner) RunID() string               { return r.id }
func (*singleEventRunner) Submit(agent.Submission) error { return nil }
func (*singleEventRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (*singleEventRunner) Close() error                         { return nil }
func (*singleEventRunner) WaitCompletion(context.Context) error { return nil }

type fenceTestRuntime struct{ runner *fenceTestRunner }

func (fenceTestRuntime) RunnerCompletionWaiterGuaranteed() {}

type unverifiedRuntime struct{}

func (unverifiedRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{}, nil
}

func (unverifiedRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (r fenceTestRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{Handle: r.runner}, nil
}

func (fenceTestRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type fenceTestRunner struct {
	id           string
	complete     chan struct{}
	completeOnce sync.Once
	mu           sync.Mutex
	cancel       int
}

type fenceCompletionRunner struct {
	id           string
	closed       chan struct{}
	closeOnce    sync.Once
	producerDone chan struct{}
}

type fenceTerminalErrorRunner struct{ *fenceCompletionRunner }

func newFenceTerminalErrorRunner(id string) *fenceTerminalErrorRunner {
	return &fenceTerminalErrorRunner{fenceCompletionRunner: newFenceCompletionRunner(id)}
}

func (r *fenceTerminalErrorRunner) WaitCompletion(ctx context.Context) error {
	err := r.fenceCompletionRunner.WaitCompletion(ctx)
	if err != nil {
		return err
	}
	return errors.New("producer failed")
}

func newFenceCompletionRunner(id string) *fenceCompletionRunner {
	return &fenceCompletionRunner{id: id, closed: make(chan struct{}), producerDone: make(chan struct{})}
}

func (r *fenceCompletionRunner) RunID() string               { return r.id }
func (*fenceCompletionRunner) Submit(agent.Submission) error { return nil }
func (*fenceCompletionRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (r *fenceCompletionRunner) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}
func (r *fenceCompletionRunner) WaitCompletion(ctx context.Context) error {
	select {
	case <-r.producerDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newFenceTestRunner(id string) *fenceTestRunner {
	return &fenceTestRunner{id: id, complete: make(chan struct{})}
}

func (r *fenceTestRunner) RunID() string { return r.id }

func (*fenceTestRunner) Submit(agent.Submission) error { return nil }

func (r *fenceTestRunner) Cancel() agent.CancelResult {
	r.mu.Lock()
	r.cancel++
	r.mu.Unlock()
	r.finish()
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (*fenceTestRunner) Close() error { return nil }

func (r *fenceTestRunner) WaitCompletion(ctx context.Context) error {
	select {
	case <-r.complete:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *fenceTestRunner) finish() {
	r.completeOnce.Do(func() { close(r.complete) })
}
