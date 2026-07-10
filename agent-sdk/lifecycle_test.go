package agentsdk_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
)

func TestExecuteLifecycleOrdersInterceptorsAndObserverRecords(t *testing.T) {
	t.Parallel()

	var order []string
	sink := &recordingTraceSink{}
	now := time.Unix(100, 0)
	clock := func() time.Time {
		now = now.Add(time.Millisecond)
		return now
	}
	err := agent.ExecuteLifecycle(context.Background(), agent.LifecycleEvent{
		Operation: agent.LifecycleHandoff,
		Name:      "codex",
	}, agent.LifecycleOptions{
		Interceptors: []agent.LifecycleInterceptor{
			lifecycleInterceptorFunc(func(ctx context.Context, event agent.LifecycleEvent, next agent.LifecycleNext) error {
				order = append(order, "first-before")
				err := next(ctx)
				order = append(order, "first-after")
				return err
			}),
			lifecycleInterceptorFunc(func(ctx context.Context, event agent.LifecycleEvent, next agent.LifecycleNext) error {
				order = append(order, "second-before")
				err := next(ctx)
				order = append(order, "second-after")
				return err
			}),
		},
		TraceSink: sink,
		Clock:     clock,
	}, func(context.Context) error {
		order = append(order, "operation")
		return errors.New("handoff failed")
	})
	if err == nil {
		t.Fatal("ExecuteLifecycle() error = nil")
	}
	wantOrder := []string{"first-before", "second-before", "operation", "second-after", "first-after"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
	if got, want := sink.records, []agent.TraceRecord{
		{Event: agent.LifecycleEvent{Operation: agent.LifecycleHandoff, Name: "codex"}, Status: agent.TraceStarted, At: time.Unix(100, int64(time.Millisecond))},
		{Event: agent.LifecycleEvent{Operation: agent.LifecycleHandoff, Name: "codex"}, Status: agent.TraceFailed, At: time.Unix(100, int64(2*time.Millisecond)), Duration: time.Millisecond, Error: "handoff failed"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
}

func TestExecuteLifecycleIsolatesTraceSinkPanic(t *testing.T) {
	t.Parallel()

	called := false
	err := agent.ExecuteLifecycle(context.Background(), agent.LifecycleEvent{Operation: agent.LifecycleRun}, agent.LifecycleOptions{
		TraceSink: panicTraceSink{},
	}, func(context.Context) error {
		called = true
		return nil
	})
	if err != nil || !called {
		t.Fatalf("ExecuteLifecycle() = called %v, err %v", called, err)
	}
}

type lifecycleInterceptorFunc func(context.Context, agent.LifecycleEvent, agent.LifecycleNext) error

func (f lifecycleInterceptorFunc) InterceptLifecycle(ctx context.Context, event agent.LifecycleEvent, next agent.LifecycleNext) error {
	return f(ctx, event, next)
}

type recordingTraceSink struct{ records []agent.TraceRecord }

func (s *recordingTraceSink) RecordTrace(record agent.TraceRecord) {
	s.records = append(s.records, record)
}

type panicTraceSink struct{}

func (panicTraceSink) RecordTrace(agent.TraceRecord) { panic("observer panic") }
