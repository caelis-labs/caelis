package agentsdk

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// LifecycleOperation identifies one typed execution boundary.
type LifecycleOperation string

const (
	LifecycleRun       LifecycleOperation = "run"
	LifecycleTurn      LifecycleOperation = "turn"
	LifecycleModel     LifecycleOperation = "model"
	LifecycleTool      LifecycleOperation = "tool"
	LifecycleApproval  LifecycleOperation = "approval"
	LifecycleCompact   LifecycleOperation = "compact"
	LifecycleHandoff   LifecycleOperation = "handoff"
	LifecycleGuardrail LifecycleOperation = "guardrail"
)

// LifecycleEvent identifies an operation without exposing mutable request,
// response, session, or metadata objects.
type LifecycleEvent struct {
	Operation  LifecycleOperation `json:"operation"`
	SessionRef session.SessionRef `json:"session_ref,omitempty"`
	RunID      string             `json:"run_id,omitempty"`
	TurnID     string             `json:"turn_id,omitempty"`
	StepID     string             `json:"step_id,omitempty"`
	Name       string             `json:"name,omitempty"`
}

// LifecycleNext is the next immutable operation in an interceptor chain.
type LifecycleNext func(context.Context) error

// LifecycleInterceptor observes or wraps one typed execution boundary. The
// configured slice order is the nesting order: the first interceptor is the
// outermost.
type LifecycleInterceptor interface {
	InterceptLifecycle(context.Context, LifecycleEvent, LifecycleNext) error
}

// TraceStatus identifies one observer-only lifecycle record.
type TraceStatus string

const (
	TraceStarted   TraceStatus = "started"
	TraceCompleted TraceStatus = "completed"
	TraceFailed    TraceStatus = "failed"
)

// TraceRecord is an immutable lifecycle observation. Error is text by design;
// TraceSink cannot mutate or wrap the execution error.
type TraceRecord struct {
	Event    LifecycleEvent `json:"event"`
	Status   TraceStatus    `json:"status"`
	At       time.Time      `json:"at"`
	Duration time.Duration  `json:"duration,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// TraceSink receives read-only lifecycle records asynchronously. Calls may be
// concurrent across lifecycle operations, so implementations must synchronize
// mutable state. Slow, stuck, or panicking sinks cannot block execution;
// observer-only records may be dropped after the bounded dispatcher saturates.
type TraceSink interface {
	RecordTrace(TraceRecord)
}

// LifecycleOptions configures one host-neutral interceptor invocation. OTel or
// another telemetry implementation belongs in a host adapter implementing
// TraceSink or LifecycleInterceptor.
type LifecycleOptions struct {
	Interceptors []LifecycleInterceptor
	TraceSink    TraceSink
	Clock        func() time.Time
}

// ExecuteLifecycle runs one operation through the configured interceptors and
// emits observer-only start/terminal trace records.
func ExecuteLifecycle(ctx context.Context, event LifecycleEvent, options LifecycleOptions, next LifecycleNext) error {
	if next == nil {
		return fmt.Errorf("agent-sdk: lifecycle next is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	event = normalizeLifecycleEvent(event)
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	startedAt := clock()
	dispatcher := newTraceDispatcher(options.TraceSink)
	if dispatcher != nil {
		defer dispatcher.close()
		dispatcher.record(TraceRecord{Event: event, Status: TraceStarted, At: startedAt})
	}

	chain := next
	for i := len(options.Interceptors) - 1; i >= 0; i-- {
		interceptor := options.Interceptors[i]
		if interceptor == nil {
			continue
		}
		following := chain
		chain = func(callCtx context.Context) error {
			return invokeLifecycleInterceptor(callCtx, event, interceptor, following)
		}
	}
	err := chain(ctx)
	finishedAt := clock()
	record := TraceRecord{
		Event:    event,
		Status:   TraceCompleted,
		At:       finishedAt,
		Duration: max(finishedAt.Sub(startedAt), 0),
	}
	if err != nil {
		record.Status = TraceFailed
		record.Error = strings.TrimSpace(err.Error())
	}
	if dispatcher != nil {
		dispatcher.record(record)
	}
	return err
}

func invokeLifecycleInterceptor(ctx context.Context, event LifecycleEvent, interceptor LifecycleInterceptor, next LifecycleNext) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("agent-sdk: %s lifecycle interceptor panic: %v", event.Operation, recovered)
		}
	}()
	return interceptor.InterceptLifecycle(ctx, event, next)
}

func normalizeLifecycleEvent(event LifecycleEvent) LifecycleEvent {
	event.SessionRef = session.NormalizeSessionRef(event.SessionRef)
	event.RunID = strings.TrimSpace(event.RunID)
	event.TurnID = strings.TrimSpace(event.TurnID)
	event.StepID = strings.TrimSpace(event.StepID)
	event.Name = strings.TrimSpace(event.Name)
	return event
}

const maxOutstandingTraceDispatchers = 32

var traceDispatcherSlots = make(chan struct{}, maxOutstandingTraceDispatchers)

type traceDispatcher struct {
	once    sync.Once
	records chan TraceRecord
}

func newTraceDispatcher(sink TraceSink) *traceDispatcher {
	if sink == nil {
		return nil
	}
	select {
	case traceDispatcherSlots <- struct{}{}:
	default:
		return nil
	}
	dispatcher := &traceDispatcher{records: make(chan TraceRecord, 2)}
	go func() {
		defer func() { <-traceDispatcherSlots }()
		for record := range dispatcher.records {
			func() {
				defer func() { _ = recover() }()
				sink.RecordTrace(record)
			}()
		}
	}()
	return dispatcher
}

func (d *traceDispatcher) record(record TraceRecord) {
	if d == nil {
		return
	}
	select {
	case d.records <- record:
	default:
	}
}

func (d *traceDispatcher) close() {
	if d == nil {
		return
	}
	d.once.Do(func() { close(d.records) })
}
