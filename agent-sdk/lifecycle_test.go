package agentsdk_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
)

func TestTraceLifecyclePreservesOperationErrorAndRecords(t *testing.T) {
	t.Parallel()
	sink := &recordingTraceSink{}
	cause := errors.New("handoff failed")
	err := agent.TraceLifecycle(t.Context(), agent.LifecycleEvent{Operation: agent.LifecycleHandoff, Name: "codex"}, agent.TraceOptions{TraceSink: sink}, func(context.Context) error { return cause })
	if !errors.Is(err, cause) {
		t.Fatal("observer changed owner error")
	}
	records := waitForTraceRecords(t, sink, 2)
	if records[0].Status != agent.TraceStarted || records[1].Status != agent.TraceFailed || records[1].Error != cause.Error() {
		t.Fatalf("records=%v", records)
	}
}

func TestTraceLifecycleIsolatesTraceSinkPanic(t *testing.T) {
	t.Parallel()

	called := false
	err := agent.TraceLifecycle(context.Background(), agent.LifecycleEvent{Operation: agent.LifecycleRun}, agent.TraceOptions{
		TraceSink: panicTraceSink{},
	}, func(context.Context) error {
		called = true
		return nil
	})
	if err != nil || !called {
		t.Fatalf("TraceLifecycle() = called %v, err %v", called, err)
	}
}

func TestTraceLifecycleDoesNotBlockOrSpawnUnboundedCallsForStuckTraceSink(t *testing.T) {
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
			_ = agent.TraceLifecycle(context.Background(), agent.LifecycleEvent{Operation: agent.LifecycleRun}, agent.TraceOptions{
				TraceSink: sink,
			}, func(context.Context) error { return nil })
		}
	}()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("TraceLifecycle blocked on stuck TraceSink")
	}
	deadline := time.Now().Add(time.Second)
	for entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := entered.Load(); got == 0 || got > 32 {
		t.Fatalf("stuck TraceSink calls = %d, want bounded range 1..32", got)
	}
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
