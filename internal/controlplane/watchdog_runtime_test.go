package controlplane

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestWatchdogHighConfidenceToolLoopInterrupts(t *testing.T) {
	t.Parallel()

	service, active := newWatchdogTestSession(t, "watchdog-tool-loop")
	runner := newControlledWatchdogRunner("watchdog-run", repeatedWatchdogToolCalls(3), true)
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime: watchdogTestRuntime{runner: runner}, Sessions: service,
		Thresholds: WatchdogThresholds{ToolLoopStreak: 3, TextLoopStreak: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := watchdog.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	done := consumeWatchdogEvents(run.Handle)
	waitForWatchdogCondition(t, func() bool { return runner.cancelCalls.Load() == 1 }, "high-confidence interrupt")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Events() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("interrupted event stream did not finish")
	}
}

func TestWatchdogCapacitySaturationDropsEvidenceWithoutCancelling(t *testing.T) {
	t.Parallel()

	service, active := newWatchdogTestSession(t, "watchdog-capacity")
	release := make(chan struct{})
	var started atomic.Int32
	owner, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime: watchdogTestRuntime{runner: newControlledWatchdogRunner("unused", nil, false)}, Sessions: service,
		Thresholds: WatchdogThresholds{ToolLoopStreak: 1, TextLoopStreak: 50},
		Reviewer: WatchdogReviewFunc(func(context.Context, WatchdogObservation) (WatchdogDecision, error) {
			started.Add(1)
			<-release
			return WatchdogDecision{Action: WatchdogActionContinue}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	runners := make([]*watchdogRunner, 0, maxOutstandingWatchdogPipelines)
	for index := 0; index < maxOutstandingWatchdogPipelines; index++ {
		inner := newControlledWatchdogRunner(fmt.Sprintf("capacity-%d", index), nil, false)
		runner := newWatchdogRunner(inner, active.SessionRef, owner, nil).(*watchdogRunner)
		runners = append(runners, runner)
		runner.observe(sameWatchdogToolCall(fmt.Sprintf("call-%d", index)))
	}
	waitForWatchdogCondition(t, func() bool {
		return started.Load() == maxOutstandingWatchdogPipelines
	}, "all watchdog review slots to fill")

	overflowInner := newControlledWatchdogRunner("capacity-overflow", nil, false)
	overflow := newWatchdogRunner(overflowInner, active.SessionRef, owner, nil).(*watchdogRunner)
	overflow.observe(sameWatchdogToolCall("overflow-call"))
	time.Sleep(25 * time.Millisecond)
	if got := overflowInner.cancelCalls.Load(); got != 0 {
		t.Fatalf("capacity overflow Cancel calls = %d, want 0", got)
	}
	overflow.mu.Lock()
	inFlight := overflow.reviewInFlight
	overflow.mu.Unlock()
	if inFlight {
		t.Fatal("capacity overflow unexpectedly queued or started a review")
	}

	overflow.finish()
	for _, runner := range runners {
		runner.finish()
	}
	close(release)
}

func TestWatchdogReviewerFailureDoesNotAffectTurn(t *testing.T) {
	t.Parallel()

	service, active := newWatchdogTestSession(t, "watchdog-review-failure")
	runner := newControlledWatchdogRunner("review-failure", []*session.Event{sameWatchdogToolCall("call")}, true)
	reviewed := make(chan struct{})
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime: watchdogTestRuntime{runner: runner}, Sessions: service,
		Thresholds: WatchdogThresholds{ToolLoopStreak: 1, TextLoopStreak: 50},
		Reviewer: WatchdogReviewFunc(func(context.Context, WatchdogObservation) (WatchdogDecision, error) {
			close(reviewed)
			return WatchdogDecision{}, errors.New("forced review failure")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := watchdog.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	done := consumeWatchdogEvents(run.Handle)
	select {
	case <-reviewed:
	case <-time.After(time.Second):
		t.Fatal("reviewer did not run")
	}
	runner.finish()
	if err := <-done; err != nil {
		t.Fatalf("review failure reached Turn: %v", err)
	}
	if got := runner.cancelCalls.Load(); got != 0 {
		t.Fatalf("review failure Cancel calls = %d, want 0", got)
	}
}

func TestWatchdogContextIgnoringReviewerDoesNotDelayNormalCompletion(t *testing.T) {
	t.Parallel()

	service, active := newWatchdogTestSession(t, "watchdog-noncooperative-review")
	runner := newControlledWatchdogRunner("noncooperative-review", []*session.Event{sameWatchdogToolCall("call")}, true)
	started := make(chan struct{})
	release := make(chan struct{})
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime: watchdogTestRuntime{runner: runner}, Sessions: service,
		Thresholds: WatchdogThresholds{ToolLoopStreak: 1, TextLoopStreak: 50}, ReviewTimeout: 20 * time.Millisecond,
		Reviewer: WatchdogReviewFunc(func(context.Context, WatchdogObservation) (WatchdogDecision, error) {
			close(started)
			<-release
			return WatchdogDecision{Action: WatchdogActionInterrupt}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := watchdog.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	done := consumeWatchdogEvents(run.Handle)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reviewer did not start")
	}
	completedAt := time.Now()
	runner.finish()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Events() error = %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("normal Turn waited for context-ignoring watchdog reviewer")
	}
	if elapsed := time.Since(completedAt); elapsed > 200*time.Millisecond {
		t.Fatalf("normal completion delayed by %s", elapsed)
	}
	if got := runner.cancelCalls.Load(); got != 0 {
		t.Fatalf("normal completion Cancel calls = %d, want 0", got)
	}
	close(release)
}

func TestWatchdogLateInterruptAfterNormalCompletionIsIgnored(t *testing.T) {
	t.Parallel()

	service, active := newWatchdogTestSession(t, "watchdog-late-interrupt")
	runner := newControlledWatchdogRunner("late-interrupt", []*session.Event{sameWatchdogToolCall("call")}, true)
	started := make(chan struct{})
	release := make(chan struct{})
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime: watchdogTestRuntime{runner: runner}, Sessions: service,
		Thresholds: WatchdogThresholds{ToolLoopStreak: 1, TextLoopStreak: 50},
		Reviewer: WatchdogReviewFunc(func(context.Context, WatchdogObservation) (WatchdogDecision, error) {
			close(started)
			<-release
			return WatchdogDecision{Action: WatchdogActionInterrupt}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := watchdog.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	done := consumeWatchdogEvents(run.Handle)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reviewer did not start")
	}
	runner.finish()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(release)
	time.Sleep(25 * time.Millisecond)
	if got := runner.cancelCalls.Load(); got != 0 {
		t.Fatalf("late Interrupt Cancel calls = %d, want 0", got)
	}
}

func TestWatchdogCheckpointFailureDoesNotBlockInterruptOrFailTurn(t *testing.T) {
	t.Parallel()

	base, active := newWatchdogTestSession(t, "watchdog-checkpoint-failure")
	service := &failingWatchdogAppendService{Service: base}
	runner := newControlledWatchdogRunner("checkpoint-failure", []*session.Event{sameWatchdogToolCall("call")}, true)
	runner.cancelUnblocks = false
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime: watchdogTestRuntime{runner: runner}, Sessions: service,
		Thresholds: WatchdogThresholds{ToolLoopStreak: 1, TextLoopStreak: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := watchdog.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	done := consumeWatchdogEvents(run.Handle)
	waitForWatchdogCondition(t, func() bool { return runner.cancelCalls.Load() == 1 }, "interrupt before checkpoint")
	waitForWatchdogCondition(t, func() bool { return service.calls.Load() == 1 }, "best-effort checkpoint attempt")
	runner.finish()
	if err := <-done; err != nil {
		t.Fatalf("checkpoint failure reached Turn: %v", err)
	}
}

func TestWatchdogReviewerPanicDoesNotAffectTurn(t *testing.T) {
	t.Parallel()

	service, active := newWatchdogTestSession(t, "watchdog-review-panic")
	runner := newControlledWatchdogRunner("review-panic", []*session.Event{sameWatchdogToolCall("call")}, true)
	started := make(chan struct{})
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime: watchdogTestRuntime{runner: runner}, Sessions: service,
		Thresholds: WatchdogThresholds{ToolLoopStreak: 1, TextLoopStreak: 50},
		Reviewer: WatchdogReviewFunc(func(context.Context, WatchdogObservation) (WatchdogDecision, error) {
			close(started)
			panic("forced reviewer panic")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := watchdog.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	done := consumeWatchdogEvents(run.Handle)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reviewer did not start")
	}
	runner.finish()
	if err := <-done; err != nil {
		t.Fatalf("reviewer panic reached Turn: %v", err)
	}
	if got := runner.cancelCalls.Load(); got != 0 {
		t.Fatalf("reviewer panic Cancel calls = %d, want 0", got)
	}
}

func TestWatchdogCloseCancelsCooperativeReviewerWithoutWaiting(t *testing.T) {
	t.Parallel()

	service, active := newWatchdogTestSession(t, "watchdog-close-reviewer")
	runner := newControlledWatchdogRunner("close-reviewer", []*session.Event{sameWatchdogToolCall("call")}, true)
	started := make(chan struct{})
	exited := make(chan struct{})
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime: watchdogTestRuntime{runner: runner}, Sessions: service,
		Thresholds: WatchdogThresholds{ToolLoopStreak: 1, TextLoopStreak: 50},
		Reviewer: WatchdogReviewFunc(func(ctx context.Context, _ WatchdogObservation) (WatchdogDecision, error) {
			close(started)
			<-ctx.Done()
			close(exited)
			return WatchdogDecision{}, ctx.Err()
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := watchdog.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	done := consumeWatchdogEvents(run.Handle)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reviewer did not start")
	}
	startedAt := time.Now()
	if err := run.Handle.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedAt); elapsed > 200*time.Millisecond {
		t.Fatalf("Close() waited %s for reviewer", elapsed)
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel cooperative reviewer")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close() did not finish event stream")
	}
}

func TestWatchdogPublicCancelAndInterruptShareOneInnerEffect(t *testing.T) {
	t.Parallel()

	service, active := newWatchdogTestSession(t, "watchdog-public-cancel")
	runner := newControlledWatchdogRunner("public-cancel", []*session.Event{sameWatchdogToolCall("call")}, true)
	started := make(chan struct{})
	release := make(chan struct{})
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime: watchdogTestRuntime{runner: runner}, Sessions: service,
		Thresholds: WatchdogThresholds{ToolLoopStreak: 1, TextLoopStreak: 50},
		Reviewer: WatchdogReviewFunc(func(context.Context, WatchdogObservation) (WatchdogDecision, error) {
			close(started)
			<-release
			return WatchdogDecision{Action: WatchdogActionInterrupt}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := watchdog.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	done := consumeWatchdogEvents(run.Handle)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reviewer did not start")
	}
	if result := run.Handle.Cancel(); result.Err != nil {
		t.Fatal(result.Err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("public Cancel did not finish stream")
	}
	time.Sleep(25 * time.Millisecond)
	if got := runner.cancelCalls.Load(); got != 1 {
		t.Fatalf("inner Cancel calls = %d, want exactly 1", got)
	}
}

func TestWatchdogPreservesSourceEvents(t *testing.T) {
	t.Parallel()

	service, active := newWatchdogTestSession(t, "watchdog-source")
	inner := newControlledWatchdogRunner("source", []*session.Event{sameWatchdogToolCall("call")}, false)
	source := &watchdogSourceTestRunner{controlledWatchdogRunner: inner}
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime: watchdogTestRuntime{runner: source}, Sessions: service,
		Thresholds: WatchdogThresholds{ToolLoopStreak: 50, TextLoopStreak: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := watchdog.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	handle, ok := run.Handle.(agent.SourceHandle)
	if !ok {
		t.Fatalf("handle = %T, want SourceHandle", run.Handle)
	}
	var canonical, native int
	for event, eventErr := range handle.SourceEvents() {
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		if event.Canonical != nil {
			canonical++
		}
		if event.Native == "native" {
			native++
		}
	}
	if canonical != 1 || native != 1 || inner.cancelCalls.Load() != 0 {
		t.Fatalf("canonical/native/cancel = %d/%d/%d, want 1/1/0", canonical, native, inner.cancelCalls.Load())
	}
}

func TestWatchdogProductionDefaults(t *testing.T) {
	t.Parallel()

	service, _ := newWatchdogTestSession(t, "watchdog-defaults")
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime: watchdogTestRuntime{runner: newControlledWatchdogRunner("defaults", nil, false)}, Sessions: service,
	})
	if err != nil {
		t.Fatal(err)
	}
	if watchdog.thresholds.TextLoopStreak != defaultTextLoopStreak || watchdog.thresholds.ToolLoopStreak != defaultToolLoopStreak {
		t.Fatalf("defaults = %+v", watchdog.thresholds)
	}
}

func newWatchdogTestSession(t *testing.T, sessionID string) (session.Service, session.Session) {
	t.Helper()
	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, active
}

func repeatedWatchdogToolCalls(count int) []*session.Event {
	events := make([]*session.Event, 0, count)
	for index := 0; index < count; index++ {
		events = append(events, sameWatchdogToolCall(fmt.Sprintf("call-%d", index)))
	}
	return events
}

func sameWatchdogToolCall(id string) *session.Event {
	return watchdogToolCall(id, "READ", map[string]any{"path": "same.txt"})
}

func watchdogToolCall(id, name string, input map[string]any) *session.Event {
	return &session.Event{Type: session.EventTypeToolCall, Tool: &session.EventTool{
		ID: id, Name: name, Input: input,
	}}
}

func consumeWatchdogEvents(handle agent.Runner) <-chan error {
	done := make(chan error, 1)
	go func() {
		var out error
		for _, err := range handle.Events() {
			out = errors.Join(out, err)
		}
		done <- out
	}()
	return done
}

func waitForWatchdogCondition(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !condition() {
		t.Fatalf("timed out waiting for %s", description)
	}
}

type watchdogTestRuntime struct{ runner agent.Runner }

func (r watchdogTestRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{Session: session.Session{SessionRef: req.SessionRef}, Handle: r.runner}, nil
}

func (watchdogTestRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type controlledWatchdogRunner struct {
	id             string
	events         []*session.Event
	waitForFinish  bool
	cancelUnblocks bool
	done           chan struct{}
	doneOnce       sync.Once
	cancelCalls    atomic.Int32
	closeCalls     atomic.Int32
}

func newControlledWatchdogRunner(id string, events []*session.Event, waitForFinish bool) *controlledWatchdogRunner {
	return &controlledWatchdogRunner{
		id: id, events: session.CloneEvents(events), waitForFinish: waitForFinish,
		cancelUnblocks: true, done: make(chan struct{}),
	}
}

func (r *controlledWatchdogRunner) RunID() string { return r.id }

func (r *controlledWatchdogRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, event := range r.events {
			if !yield(session.CloneEvent(event), nil) {
				return
			}
		}
		if r.waitForFinish {
			<-r.done
		}
	}
}

func (*controlledWatchdogRunner) Submit(agent.Submission) error { return nil }

func (r *controlledWatchdogRunner) Cancel() agent.CancelResult {
	r.cancelCalls.Add(1)
	if r.cancelUnblocks {
		r.finish()
	}
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (r *controlledWatchdogRunner) Close() error {
	r.closeCalls.Add(1)
	r.finish()
	return nil
}

func (r *controlledWatchdogRunner) finish() {
	r.doneOnce.Do(func() { close(r.done) })
}

type watchdogSourceTestRunner struct{ *controlledWatchdogRunner }

func (r *watchdogSourceTestRunner) SourceEvents() iter.Seq2[agent.SourceEvent, error] {
	return func(yield func(agent.SourceEvent, error) bool) {
		for _, event := range r.events {
			if !yield(agent.SourceEvent{Canonical: session.CloneEvent(event), Native: "native"}, nil) {
				return
			}
		}
		if r.waitForFinish {
			<-r.done
		}
	}
}

type failingWatchdogAppendService struct {
	session.Service
	calls atomic.Int32
}

func (s *failingWatchdogAppendService) AppendEvent(context.Context, session.AppendEventRequest) (*session.Event, error) {
	s.calls.Add(1)
	return nil, errors.New("forced watchdog checkpoint failure")
}
