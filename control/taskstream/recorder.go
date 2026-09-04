package taskstream

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/control/streamspool"
)

const (
	taskOutputRecordType     uint16 = 1
	taskOutputReleaseTimeout        = 5 * time.Second
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

	mu      sync.Mutex
	writers map[streamspool.LogicalKey]*recordingPartition
}

type recordingPartition struct {
	writer    streamspool.Writer
	sessionID string
	taskID    string
	mu        sync.Mutex
	failed    bool
	released  bool
}

type boundRecorder struct {
	recorder          *Recorder
	logical           streamspool.LogicalKey
	partition         *recordingPartition
	binding           output.Binding
	diagnostics       *slog.Logger
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
	if partition == nil {
		writer, err := r.store.Register(ctx, logical, streamspool.WriterOptions{OriginComplete: binding.StartsAtTaskOrigin})
		if err != nil {
			r.mu.Unlock()
			r.logFailure("register", binding, err)
			return output.Nop()
		}
		partition = &recordingPartition{writer: writer, sessionID: binding.SessionID, taskID: binding.TaskID}
		r.writers[logical] = partition
	}
	r.mu.Unlock()
	return &boundRecorder{
		recorder: r, logical: logical, partition: partition, binding: binding, diagnostics: r.diagnostics,
		partitionTerminal: binding.Kind == output.TaskKindCommand,
	}
}

func (o *boundRecorder) ObserveTaskOutput(ctx context.Context, event output.Event) error {
	if o == nil || o.partition == nil || o.partition.writer == nil {
		return nil
	}
	if event.ProducerClosed {
		return o.recorder.releasePartition(ctx, o.logical, o.partition)
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	payload, err := json.Marshal(recordedTaskOutput{
		TerminalID: o.binding.TerminalID,
		ActivityID: o.binding.ActivityID,
		Event:      cloneOutputEvent(event),
	})
	if err != nil {
		return err
	}
	o.partition.mu.Lock()
	if o.partition.failed || o.partition.released {
		o.partition.mu.Unlock()
		return nil
	}
	_, err = o.partition.writer.Append(ctx, taskOutputRecordType, event.OccurredAt, payload)
	if err != nil {
		o.partition.failed = true
		o.partition.mu.Unlock()
		o.forgetPartition()
		o.logFailure("append", err)
		return err
	}
	if event.Closed && o.partitionTerminal {
		o.partition.released = true
		if err := o.partition.writer.Seal(ctx); err != nil && !errors.Is(err, streamspool.ErrEmptyTerminal) {
			o.partition.failed = true
			o.partition.mu.Unlock()
			o.forgetPartition()
			o.logFailure("seal", err)
			return err
		}
		o.partition.mu.Unlock()
		o.forgetPartition()
		return nil
	}
	o.partition.mu.Unlock()
	return nil
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

func (r *Recorder) releasePartition(ctx context.Context, logical streamspool.LogicalKey, partition *recordingPartition) error {
	if r == nil || partition == nil || partition.writer == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	partition.mu.Lock()
	if partition.failed {
		partition.mu.Unlock()
		r.forgetPartition(logical, partition)
		return nil
	}
	partition.released = true
	// A detached client must not turn a permanent product-address release into
	// a process-lifetime registration leak. Preserve values but make physical
	// sealing independent of caller cancellation; failures stay in the map so a
	// later lifecycle close can retry.
	sealCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskOutputReleaseTimeout)
	err := partition.writer.Seal(sealCtx)
	cancel()
	if errors.Is(err, streamspool.ErrEmptyTerminal) || errors.Is(err, streamspool.ErrClosed) {
		err = nil
	}
	partition.mu.Unlock()
	if err == nil {
		r.forgetPartition(logical, partition)
	} else {
		r.logReleaseFailure(partition, err)
	}
	return err
}

func (o *boundRecorder) forgetPartition() {
	if o == nil || o.recorder == nil || o.partition == nil {
		return
	}
	o.recorder.forgetPartition(o.logical, o.partition)
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

func (o *boundRecorder) logFailure(operation string, err error) {
	if o == nil || o.diagnostics == nil || err == nil {
		return
	}
	o.diagnostics.Warn("Control Task output trace unavailable", "operation", operation, "error", err)
}

func (r *Recorder) logReleaseFailure(partition *recordingPartition, err error) {
	if r == nil || r.diagnostics == nil || partition == nil || err == nil {
		return
	}
	r.diagnostics.Warn("Control Task output trace release failed",
		"session_id", partition.sessionID, "task_id", partition.taskID, "error", err)
}

var _ output.Binder = (*Recorder)(nil)
var _ output.Observer = (*boundRecorder)(nil)
