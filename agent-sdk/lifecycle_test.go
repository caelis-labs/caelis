package agentsdk_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
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
	wantRecords := []agent.TraceRecord{
		{Event: agent.LifecycleEvent{Operation: agent.LifecycleHandoff, Name: "codex"}, Status: agent.TraceStarted, At: time.Unix(100, int64(time.Millisecond))},
		{Event: agent.LifecycleEvent{Operation: agent.LifecycleHandoff, Name: "codex"}, Status: agent.TraceFailed, At: time.Unix(100, int64(2*time.Millisecond)), Duration: time.Millisecond, Error: "handoff failed"},
	}
	gotRecords := waitForTraceRecords(t, sink, len(wantRecords))
	if !reflect.DeepEqual(gotRecords, wantRecords) {
		t.Fatalf("records = %#v, want %#v", gotRecords, wantRecords)
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

func TestExecuteLifecycleDoesNotBlockOrSpawnUnboundedCallsForStuckTraceSink(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var entered atomic.Int64
	sink := traceSinkFunc(func(agent.TraceRecord) {
		entered.Add(1)
		<-release
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 128 {
			_ = agent.ExecuteLifecycle(context.Background(), agent.LifecycleEvent{Operation: agent.LifecycleRun}, agent.LifecycleOptions{
				TraceSink: sink,
			}, func(context.Context) error { return nil })
		}
	}()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("ExecuteLifecycle blocked on stuck TraceSink")
	}
	deadline := time.Now().Add(time.Second)
	for entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := entered.Load(); got == 0 || got > 32 {
		t.Fatalf("stuck TraceSink calls = %d, want bounded range 1..32", got)
	}
}

type lifecycleInterceptorFunc func(context.Context, agent.LifecycleEvent, agent.LifecycleNext) error

func (f lifecycleInterceptorFunc) InterceptLifecycle(ctx context.Context, event agent.LifecycleEvent, next agent.LifecycleNext) error {
	return f(ctx, event, next)
}

type recordingTraceSink struct {
	mu      sync.Mutex
	records []agent.TraceRecord
}

func (s *recordingTraceSink) RecordTrace(record agent.TraceRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
}

func (s *recordingTraceSink) snapshot() []agent.TraceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agent.TraceRecord(nil), s.records...)
}

func waitForTraceRecords(t *testing.T, sink *recordingTraceSink, count int) []agent.TraceRecord {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		records := sink.snapshot()
		if len(records) >= count || time.Now().After(deadline) {
			return records
		}
		time.Sleep(time.Millisecond)
	}
}

type panicTraceSink struct{}

func (panicTraceSink) RecordTrace(agent.TraceRecord) { panic("observer panic") }

type traceSinkFunc func(agent.TraceRecord)

func (f traceSinkFunc) RecordTrace(record agent.TraceRecord) { f(record) }
