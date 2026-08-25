package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestRuntimeEmitsTypedRunTurnModelAndToolLifecycle(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	llm := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
		if calls.Add(1) == 1 {
			return toolCallResponse("probe-1", "probe")
		}
		return textResponse("done", model.Usage{})
	}}
	probe := tool.NamedTool{Def: tool.Definition{Name: "probe"}, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
		return tool.Result{ID: call.ID, Name: call.Name}, nil
	}}
	sessions, active := newTestSessionService(t, "lifecycle-runtime")
	sink := &concurrentTraceSink{}
	interceptor := &recordingLifecycleInterceptor{}
	runtime, err := New(Config{
		Sessions:              sessions,
		AgentFactory:          chat.Factory{},
		TraceSink:             sink,
		LifecycleInterceptors: []agent.LifecycleInterceptor{interceptor},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	run, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		AgentSpec:  agent.AgentSpec{Name: "chat", Model: llm, Tools: []tool.Tool{probe}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := runnerError(run.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	want := map[agent.LifecycleOperation]bool{
		agent.LifecycleRun:   true,
		agent.LifecycleTurn:  true,
		agent.LifecycleModel: true,
		agent.LifecycleTool:  true,
	}
	for operation := range want {
		if !sink.saw(operation, agent.TraceStarted) || !sink.saw(operation, agent.TraceCompleted) {
			t.Fatalf("trace records = %#v, want start/completion for %q", sink.snapshot(), operation)
		}
		if !interceptor.saw(operation) {
			t.Fatalf("interceptor events = %#v, want %q", interceptor.snapshot(), operation)
		}
	}
}

func TestRuntimeEmitsCompactLifecycle(t *testing.T) {
	t.Parallel()

	sessions, active := newTestSessionService(t, "lifecycle-compact")
	appendTestEvent(t, sessions, active.SessionRef, userTextEvent("preserve manual compact lifecycle"))
	sink := &concurrentTraceSink{}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, TraceSink: sink})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = runtime.Compact(context.Background(), CompactRequest{
		SessionRef: active.SessionRef,
		Model:      staticModel{text: "CONTEXT CHECKPOINT\n\nManual compact lifecycle was preserved."},
		Trigger:    "manual",
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if !sink.saw(agent.LifecycleCompact, agent.TraceStarted) || !sink.saw(agent.LifecycleCompact, agent.TraceCompleted) {
		t.Fatalf("trace records = %#v, want compact lifecycle", sink.snapshot())
	}
	for _, record := range sink.snapshot() {
		if record.Event.Operation == agent.LifecycleCompact && record.Event.SessionRef.SessionID != active.SessionID {
			t.Fatalf("compact lifecycle SessionRef = %#v, want Session %q", record.Event.SessionRef, active.SessionID)
		}
	}
}

func TestRuntimeManualCompactLifecycleFailsWhenCheckpointPersistenceFails(t *testing.T) {
	t.Parallel()

	baseSessions, active := newTestSessionService(t, "lifecycle-compact-persist-failure")
	appendTestEvent(t, baseSessions, active.SessionRef, userTextEvent("preserve failed compact lifecycle ordering"))
	persistErr := errors.New("forced compact persistence failure")
	sessions := &failCompactAppendSessionService{Service: baseSessions, err: persistErr}
	sink := &concurrentTraceSink{}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, TraceSink: sink})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = runtime.Compact(context.Background(), CompactRequest{
		SessionRef: active.SessionRef,
		Model:      staticModel{text: "CONTEXT CHECKPOINT\n\nThis checkpoint must fail to persist."},
		Trigger:    "manual",
	})
	if !errors.Is(err, persistErr) {
		t.Fatalf("Compact() error = %v, want persistence failure", err)
	}
	if !sink.saw(agent.LifecycleCompact, agent.TraceStarted) || !sink.saw(agent.LifecycleCompact, agent.TraceFailed) {
		t.Fatalf("trace records = %#v, want compact started and failed", sink.snapshot())
	}
	if sink.saw(agent.LifecycleCompact, agent.TraceCompleted) {
		t.Fatalf("trace records = %#v, must not complete before checkpoint persistence", sink.snapshot())
	}
	loaded, loadErr := baseSessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if loadErr != nil {
		t.Fatalf("LoadSession() error = %v", loadErr)
	}
	for _, event := range loaded.Events {
		if compact.IsCompactEvent(event) {
			t.Fatalf("failed persistence left compact event %#v", event)
		}
	}
}

type failCompactAppendSessionService struct {
	session.Service
	err error
}

func (s *failCompactAppendSessionService) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if req.Event != nil && compact.IsCompactEvent(req.Event) {
		return nil, s.err
	}
	return s.Service.AppendEvent(ctx, req)
}

type concurrentTraceSink struct {
	mu      sync.Mutex
	records []agent.TraceRecord
}

func (s *concurrentTraceSink) RecordTrace(record agent.TraceRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
}

func (s *concurrentTraceSink) snapshot() []agent.TraceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agent.TraceRecord(nil), s.records...)
}

func (s *concurrentTraceSink) saw(operation agent.LifecycleOperation, status agent.TraceStatus) bool {
	deadline := time.Now().Add(time.Second)
	for {
		for _, record := range s.snapshot() {
			if record.Event.Operation == operation && record.Status == status {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

type recordingLifecycleInterceptor struct {
	mu     sync.Mutex
	events []agent.LifecycleEvent
}

func (i *recordingLifecycleInterceptor) InterceptLifecycle(ctx context.Context, event agent.LifecycleEvent, next agent.LifecycleNext) error {
	i.mu.Lock()
	i.events = append(i.events, event)
	i.mu.Unlock()
	return next(ctx)
}

func (i *recordingLifecycleInterceptor) snapshot() []agent.LifecycleEvent {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]agent.LifecycleEvent(nil), i.events...)
}

func (i *recordingLifecycleInterceptor) saw(operation agent.LifecycleOperation) bool {
	for _, event := range i.snapshot() {
		if event.Operation == operation {
			return true
		}
	}
	return false
}
