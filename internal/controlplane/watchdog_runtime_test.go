package controlplane

import (
	"context"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestWatchdogObservesSignalsCheckpointsAndCancelsOnlyAfterConfirmation(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "watchdog-signals",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newWatchdogTestRunner("watchdog-run", []*session.Event{
		watchdogToolCall("call-1", "READ", map[string]any{"path": "same.txt"}),
		watchdogToolCall("call-2", "READ", map[string]any{"path": "same.txt"}),
		{
			Type: session.EventTypeCustom,
			Meta: map[string]any{"usage": map[string]any{
				"prompt_tokens": 40, "completion_tokens": 2, "total_tokens": 42,
			}},
		},
		watchdogToolCall("call-3", "READ", map[string]any{"path": "same.txt"}),
	})
	reviewed := make(chan WatchdogObservation, 1)
	lifecycle := NewWatchdogLifecycleObserver()
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime:  watchdogTestRuntime{runner: runner},
		Sessions: service,
		Thresholds: WatchdogThresholds{
			RepeatedToolCalls: 3,
		},
		ReviewInterval: time.Hour,
		Lifecycle:      lifecycle,
		Reviewer: WatchdogReviewFunc(func(_ context.Context, observation WatchdogObservation) (WatchdogDecision, error) {
			reviewed <- observation
			return WatchdogDecision{Action: WatchdogActionCancel, Confirmed: true, Reason: "user confirmed loop cancellation"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := watchdog.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.RecordTrace(agent.TraceRecord{
		Event:  agent.LifecycleEvent{Operation: agent.LifecycleModel, SessionRef: active.SessionRef, RunID: run.Handle.RunID()},
		Status: agent.TraceStarted,
	})
	for _, eventErr := range run.Handle.Events() {
		if eventErr != nil {
			t.Fatalf("Events() error = %v", eventErr)
		}
	}
	var observation WatchdogObservation
	select {
	case observation = <-reviewed:
	case <-time.After(time.Second):
		t.Fatal("watchdog reviewer was not invoked")
	}
	if observation.RepeatedToolCalls != 3 || observation.RepeatedToolSignature == "" {
		t.Fatalf("repeated tool observation = %+v", observation)
	}
	if observation.Usage.TotalTokens != 42 || observation.LifecycleStatus != "model:started" {
		t.Fatalf("usage/lifecycle observation = %+v", observation)
	}
	if !observation.HasReason(WatchdogReasonRepeatedTool) {
		t.Fatalf("reasons = %v, want repeated tool", observation.Reasons)
	}
	if got := runner.cancelCalls(); got != 1 {
		t.Fatalf("cancel calls = %d, want 1 confirmed cancel", got)
	}
	loaded, err := service.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := watchdogCheckpoint(loaded.Events)
	if checkpoint == nil || checkpoint.Lifecycle == nil || checkpoint.Lifecycle.Status != watchdogCheckpointStatus {
		t.Fatalf("events = %#v, want durable watchdog checkpoint", loaded.Events)
	}
	if !strings.Contains(checkpoint.Lifecycle.Reason, "user confirmed") {
		t.Fatalf("checkpoint reason = %q", checkpoint.Lifecycle.Reason)
	}
}

func TestWatchdogElapsedSoftThresholdRequestsReviewButUnconfirmedCancelContinues(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "watchdog-elapsed",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := newWatchdogTestRunner("elapsed-run", nil)
	reviewed := make(chan WatchdogObservation, 1)
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime:  watchdogTestRuntime{runner: runner},
		Sessions: service,
		Thresholds: WatchdogThresholds{
			Elapsed:    15 * time.Millisecond,
			NoProgress: 15 * time.Millisecond,
		},
		TickInterval:   2 * time.Millisecond,
		ReviewInterval: time.Hour,
		Reviewer: WatchdogReviewFunc(func(_ context.Context, observation WatchdogObservation) (WatchdogDecision, error) {
			reviewed <- observation
			return WatchdogDecision{Action: WatchdogActionCancel, Confirmed: false, Reason: "confirmation declined"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := watchdog.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for _, eventErr := range run.Handle.Events() {
			if eventErr != nil {
				done <- eventErr
				return
			}
		}
		done <- nil
	}()
	var observation WatchdogObservation
	select {
	case observation = <-reviewed:
	case <-time.After(time.Second):
		t.Fatal("elapsed watchdog reviewer was not invoked")
	}
	if !observation.HasReason(WatchdogReasonElapsed) || !observation.HasReason(WatchdogReasonNoProgress) {
		t.Fatalf("reasons = %v, want elapsed and no-progress", observation.Reasons)
	}
	if got := runner.cancelCalls(); got != 0 {
		t.Fatalf("cancel calls = %d, want unconfirmed cancel ignored", got)
	}
	runner.finish()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog runner did not finish")
	}
}

func TestWatchdogPreservesSourceEventsAndObservesCanonicalPayload(t *testing.T) {
	t.Parallel()

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "watchdog-source",
	})
	if err != nil {
		t.Fatal(err)
	}
	inner := newWatchdogTestRunner("source-run", []*session.Event{watchdogToolCall("call-1", "READ", map[string]any{"path": "same.txt"})})
	source := &watchdogSourceTestRunner{watchdogTestRunner: inner}
	watchdog, err := NewWatchdogRuntime(WatchdogRuntimeConfig{
		Runtime:        watchdogSourceTestRuntime{runner: source},
		Sessions:       service,
		Thresholds:     WatchdogThresholds{RepeatedToolCalls: 1},
		ReviewInterval: time.Hour,
		Reviewer: WatchdogReviewFunc(func(context.Context, WatchdogObservation) (WatchdogDecision, error) {
			return WatchdogDecision{Action: WatchdogActionCancel, Confirmed: true, Reason: "confirmed"}, nil
		}),
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
		t.Fatalf("handle = %T, want SourceHandle preservation", run.Handle)
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
	if canonical != 1 || native != 1 || inner.cancelCalls() != 1 {
		t.Fatalf("canonical/native/cancel = %d/%d/%d, want 1/1/1", canonical, native, inner.cancelCalls())
	}
}

func watchdogToolCall(id, name string, input map[string]any) *session.Event {
	return &session.Event{Type: session.EventTypeToolCall, Tool: &session.EventTool{ID: id, Name: name, Input: input}}
}

func watchdogCheckpoint(events []*session.Event) *session.Event {
	for _, event := range events {
		if event != nil && event.Lifecycle != nil && event.Lifecycle.Status == watchdogCheckpointStatus {
			return event
		}
	}
	return nil
}

type watchdogTestRuntime struct{ runner *watchdogTestRunner }

func (r watchdogTestRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{Handle: r.runner}, nil
}

func (watchdogTestRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type watchdogSourceTestRuntime struct{ runner *watchdogSourceTestRunner }

func (r watchdogSourceTestRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{Handle: r.runner}, nil
}

func (watchdogSourceTestRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type watchdogSourceTestRunner struct{ *watchdogTestRunner }

func (r *watchdogSourceTestRunner) SourceEvents() iter.Seq2[agent.SourceEvent, error] {
	return func(yield func(agent.SourceEvent, error) bool) {
		for _, event := range r.events {
			if !yield(agent.SourceEvent{Canonical: session.CloneEvent(event), Native: "native"}, nil) {
				return
			}
		}
		<-r.done
	}
}

type watchdogTestRunner struct {
	id     string
	events []*session.Event
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	cancel int
}

func newWatchdogTestRunner(id string, events []*session.Event) *watchdogTestRunner {
	return &watchdogTestRunner{id: id, events: session.CloneEvents(events), done: make(chan struct{})}
}

func (r *watchdogTestRunner) RunID() string { return r.id }

func (r *watchdogTestRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, event := range r.events {
			if !yield(session.CloneEvent(event), nil) {
				return
			}
		}
		<-r.done
	}
}

func (*watchdogTestRunner) Submit(agent.Submission) error { return nil }

func (r *watchdogTestRunner) Cancel() agent.CancelResult {
	r.mu.Lock()
	r.cancel++
	r.mu.Unlock()
	r.finish()
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (r *watchdogTestRunner) Close() error {
	r.finish()
	return nil
}

func (r *watchdogTestRunner) finish() { r.once.Do(func() { close(r.done) }) }

func (r *watchdogTestRunner) cancelCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancel
}
