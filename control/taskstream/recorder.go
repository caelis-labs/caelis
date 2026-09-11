package taskstream

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/control/streamspool"
)

const (
	taskOutputRecordType     uint16 = 1
	taskOutputReleaseTimeout        = 5 * time.Second
	taskOutputFlushInterval         = 40 * time.Millisecond
	taskOutputBatchBytes            = 64 << 10
	taskOutputQueueBytes            = 32 << 20
	taskOutputGlobalBytes           = 64 << 20
	taskOutputQueueRecords          = 8192
)

type recordedTaskOutput struct {
	TerminalID string       `json:"terminal_id,omitempty"`
	ActivityID string       `json:"activity_id,omitempty"`
	Event      output.Event `json:"event"`
}

// Recorder binds trusted Task identity to raw SDK output and appends it to the
// Control-owned transient spool. It owns no Task lifecycle authority.
type Recorder struct {
	store       streamspool.Store
	diagnostics *slog.Logger

	mu          sync.Mutex
	writers     map[streamspool.LogicalKey]*recordingPartition
	queuedBytes atomic.Int64
	closed      bool
}

type recordingPartition struct {
	writer            streamspool.Writer // worker-owned after registration
	unpublished       bool
	sessionID, taskID string
	mu                sync.Mutex // never held during store or file I/O
	queue             []outputWrite
	bytes, records    int
	failure           error
	released          bool
	wake              chan struct{}
	done              chan struct{}
}

type outputWrite struct {
	records []streamspool.Record
	bytes   int
	replace bool
	seal    bool
	barrier chan error
}

type boundRecorder struct {
	recorder          *Recorder
	partition         *recordingPartition
	binding           output.Binding
	partitionTerminal bool
}

// NewRecorder creates one process-local Task recorder. A nil Store produces
// no-op observers so authoritative Task results remain available.
func NewRecorder(store streamspool.Store, diagnostics *slog.Logger) *Recorder {
	return &Recorder{store: store, diagnostics: diagnostics, writers: map[streamspool.LogicalKey]*recordingPartition{}}
}

// BindTaskOutput installs or reuses the single writer for a stable Task. A
// later child activity shares the partition and receives only a new binding.
func (r *Recorder) BindTaskOutput(ctx context.Context, binding output.Binding) output.Observer {
	binding.SessionID = strings.TrimSpace(binding.SessionID)
	binding.TaskID = strings.TrimSpace(binding.TaskID)
	binding.TerminalID = strings.TrimSpace(binding.TerminalID)
	binding.ActivityID = strings.TrimSpace(binding.ActivityID)
	if r == nil || r.store == nil || binding.SessionID == "" || binding.TaskID == "" {
		return output.Nop()
	}
	logical := streamspool.LogicalKey{
		Namespace: streamspool.NamespaceTask,
		Digest:    streamspool.DigestStrings(binding.SessionID, binding.TaskID),
	}
	r.mu.Lock()
	partition := r.writers[logical]
	unpublished := false
	if r.closed {
		r.mu.Unlock()
		return output.Nop()
	}
	if partition != nil {
		partition.mu.Lock()
		failed := partition.failure != nil
		partition.mu.Unlock()
		if failed {
			select {
			case <-partition.done:
				// A new binding may recover a failed cache through ordered
				// provider replay. Old observers remain failed, never redirected.
				partition = nil
				unpublished = true
			default:
			}
		}
	}
	if partition == nil {
		writer, err := r.store.Register(ctx, logical, streamspool.WriterOptions{OriginComplete: binding.StartsAtTaskOrigin, Unpublished: unpublished})
		if errors.Is(err, streamspool.ErrInUse) && binding.Kind == output.TaskKindSubagent {
			// A retired connection can leave a sealed incarnation behind. Its
			// replacement stays private until session/load supplies the origin.
			unpublished = true
			writer, err = r.store.Register(ctx, logical, streamspool.WriterOptions{Unpublished: true})
		}
		if err != nil {
			r.mu.Unlock()
			r.logFailure("register", binding, err)
			return output.Nop()
		}
		partition = &recordingPartition{writer: writer, unpublished: unpublished, sessionID: binding.SessionID, taskID: binding.TaskID, wake: make(chan struct{}, 1), done: make(chan struct{})}
		r.writers[logical] = partition
		go r.writeLoop(logical, partition)
	}
	r.mu.Unlock()
	return &boundRecorder{
		recorder: r, partition: partition, binding: binding,
		partitionTerminal: binding.Kind == output.TaskKindCommand,
	}
}

// ObserveTaskOutput copies and admits an event into bounded memory. Consumer
// speed, file writes and fsync are never on this callback's lock path.
func (o *boundRecorder) ObserveTaskOutput(ctx context.Context, event output.Event) error {
	if o == nil || o.partition == nil {
		return nil
	}
	if event.ProducerClosed {
		return o.recorder.enqueue(o.partition, outputWrite{seal: true})
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	o.partition.mu.Lock()
	payload, err := json.Marshal(recordedTaskOutput{TerminalID: o.binding.TerminalID, ActivityID: o.binding.ActivityID, Event: cloneOutputEvent(event)})
	if err != nil {
		o.partition.mu.Unlock()
		return o.recorder.failQueue(o.partition, err)
	}
	return o.recorder.enqueueLocked(o.partition, outputWrite{
		records: []streamspool.Record{{Type: taskOutputRecordType, OccurredAt: event.OccurredAt, Payload: payload}}, bytes: len(payload),
		seal: event.Closed && o.partitionTerminal,
	})
}

// ReplaceTaskHistory queues an atomic cache replacement ahead of subsequent
// live events. Only the writer publishes it, after every replay record is written.
func (o *boundRecorder) ReplaceTaskHistory(ctx context.Context, events []*session.Event) error {
	if o == nil || o.partition == nil {
		return nil
	}
	item := outputWrite{replace: true}
	for _, event := range events {
		if event == nil {
			continue
		}
		terminalID := ""
		if event.Scope != nil {
			terminalID = event.Scope.TurnID
		}
		payload, err := json.Marshal(recordedTaskOutput{TerminalID: terminalID, ActivityID: o.binding.ActivityID, Event: output.Event{Event: event, OccurredAt: event.Time}})
		if err != nil {
			return err
		}
		item.bytes += len(payload)
		if item.bytes > taskOutputQueueBytes || len(item.records) >= taskOutputQueueRecords {
			return o.recorder.failQueue(o.partition, streamspool.ErrLimit)
		}
		item.records = append(item.records, streamspool.Record{Type: taskOutputRecordType, OccurredAt: event.Time, Payload: payload})
	}
	return o.recorder.enqueue(o.partition, item)
}

func (r *Recorder) enqueue(p *recordingPartition, item outputWrite) error {
	p.mu.Lock()
	return r.enqueueLocked(p, item)
}

// enqueueLocked always unlocks p.mu; neither it nor a producer waits for I/O.
func (r *Recorder) enqueueLocked(p *recordingPartition, item outputWrite) error {
	defer p.mu.Unlock()
	if p.failure != nil {
		return p.failure
	}
	if p.released {
		return streamspool.ErrClosed
	}
	if len(p.queue) >= taskOutputQueueRecords || p.records+len(item.records) > taskOutputQueueRecords || p.bytes+item.bytes > taskOutputQueueBytes {
		p.failure = streamspool.ErrLimit
		signalOutputWriter(p)
		return p.failure
	}
	if total := r.queuedBytes.Add(int64(item.bytes)); total > taskOutputGlobalBytes {
		r.queuedBytes.Add(-int64(item.bytes))
		p.failure = streamspool.ErrLimit
		signalOutputWriter(p)
		return p.failure
	}
	p.bytes += item.bytes
	p.records += len(item.records)
	p.queue = append(p.queue, item)
	if item.seal {
		p.released = true
	}
	signalOutputWriter(p)
	return nil
}

func signalOutputWriter(p *recordingPartition) {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (r *Recorder) failQueue(p *recordingPartition, err error) error {
	p.mu.Lock()
	if p.failure == nil {
		p.failure = err
	}
	signalOutputWriter(p)
	p.mu.Unlock()
	return err
}

// Flush waits for observations already accepted by a Task writer. It is a
// reader/lifecycle barrier, never called by live producer callbacks.
func (r *Recorder) Flush(ctx context.Context, ref taskapi.Ref) error {
	if r == nil {
		return nil
	}
	logical := streamspool.LogicalKey{Namespace: streamspool.NamespaceTask, Digest: streamspool.DigestStrings(ref.SessionID, ref.TaskID)}
	r.mu.Lock()
	p := r.writers[logical]
	r.mu.Unlock()
	if p == nil {
		return nil
	}
	p.mu.Lock()
	released := p.released
	failure := p.failure
	p.mu.Unlock()
	if failure != nil {
		return failure
	}
	if released {
		return waitOutputDone(ctx, p)
	}
	barrier := make(chan error, 1)
	if err := r.enqueue(p, outputWrite{barrier: barrier}); err != nil {
		if errors.Is(err, streamspool.ErrClosed) {
			return waitOutputDone(ctx, p)
		}
		return err
	}
	select {
	case err := <-barrier:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitOutputDone(ctx context.Context, p *recordingPartition) error {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.failure
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReleaseTask permanently closes one Task partition after its product address
// is released. It is a Control lifecycle API; producer-facing SDK interfaces
// remain limited to binding and pushing raw output.
func (r *Recorder) ReleaseTask(ctx context.Context, ref taskapi.Ref) error {
	if r == nil {
		return nil
	}
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.TaskID = strings.TrimSpace(ref.TaskID)
	if ref.SessionID == "" || ref.TaskID == "" {
		return nil
	}
	logical := streamspool.LogicalKey{
		Namespace: streamspool.NamespaceTask,
		Digest:    streamspool.DigestStrings(ref.SessionID, ref.TaskID),
	}
	r.mu.Lock()
	partition := r.writers[logical]
	r.mu.Unlock()
	return r.releasePartition(ctx, logical, partition)
}

// ReleaseSession permanently closes every Task partition owned by a closed
// Session. Retained sealed bytes remain a TTL-bounded, lossy replay trace.
func (r *Recorder) ReleaseSession(ctx context.Context, ref session.SessionRef) error {
	if r == nil {
		return nil
	}
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return nil
	}
	type candidate struct {
		logical   streamspool.LogicalKey
		partition *recordingPartition
	}
	r.mu.Lock()
	partitions := make([]candidate, 0)
	for logical, partition := range r.writers {
		if partition != nil && partition.sessionID == sessionID {
			partitions = append(partitions, candidate{logical: logical, partition: partition})
		}
	}
	r.mu.Unlock()
	var joined error
	for _, item := range partitions {
		joined = errors.Join(joined, r.releasePartition(ctx, item.logical, item.partition))
	}
	return joined
}

// Close seals every remaining writer before the shared Store is closed.
func (r *Recorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	type candidate struct {
		logical   streamspool.LogicalKey
		partition *recordingPartition
	}
	r.mu.Lock()
	r.closed = true
	partitions := make([]candidate, 0, len(r.writers))
	for logical, partition := range r.writers {
		partitions = append(partitions, candidate{logical: logical, partition: partition})
	}
	r.mu.Unlock()
	var joined error
	for _, item := range partitions {
		joined = errors.Join(joined, r.releasePartition(ctx, item.logical, item.partition))
	}
	return joined
}

func (r *Recorder) releasePartition(ctx context.Context, logical streamspool.LogicalKey, p *recordingPartition) error {
	if r == nil || p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	if !p.released && p.failure == nil {
		_ = r.enqueueLocked(p, outputWrite{seal: true})
	} else {
		p.mu.Unlock()
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskOutputReleaseTimeout)
	defer cancel()
	err := waitOutputDone(releaseCtx, p)
	if releaseCtx.Err() == nil {
		r.forgetPartition(logical, p)
	}
	return err
}

func (r *Recorder) forgetPartition(logical streamspool.LogicalKey, partition *recordingPartition) {
	if r == nil || partition == nil {
		return
	}
	r.mu.Lock()
	if r.writers[logical] == partition {
		delete(r.writers, logical)
	}
	r.mu.Unlock()
}

func cloneOutputEvent(in output.Event) output.Event {
	out := in
	if in.ExitCode != nil {
		code := *in.ExitCode
		out.ExitCode = &code
	}
	out.Event = session.CloneEvent(in.Event)
	return out
}

func (r *Recorder) logFailure(operation string, binding output.Binding, err error) {
	if r == nil || r.diagnostics == nil || err == nil {
		return
	}
	r.diagnostics.Warn("Control Task output trace unavailable", "operation", operation, "error", err)
}

var _ output.Binder = (*Recorder)(nil)
var _ output.Observer = (*boundRecorder)(nil)
