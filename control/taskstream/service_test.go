package taskstream

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

var taskStreamTestSecret = []byte("0123456789abcdef0123456789abcdef")

func TestServiceListsOnlyOwningSessionAndRejectsCrossSessionTask(t *testing.T) {
	store := newTaskStreamTestStore(
		taskStreamTestEntry("session-1", "task-1", task.KindSubagent),
		taskStreamTestEntry("session-2", "task-2", task.KindCommand),
	)
	streams := &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{
		"task-1": taskStreamTestSnapshot("session-1", "task-1", "turn-1", "one"),
		"task-2": taskStreamTestSnapshot("session-2", "task-2", "terminal-2", "two"),
	}}
	service := newTaskStreamTestService(t, store, streams, "generation-1")

	listed, err := service.List(context.Background(), Principal{ID: "owner"}, ListRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tasks) != 1 || listed.Tasks[0].TaskID != "task-1" || listed.Tasks[0].Handle != "handle-task-1" || listed.Tasks[0].AgentHandle != "orbit" || listed.Tasks[0].SessionID != "session-1" {
		t.Fatalf("List() = %#v, want only session-1/task-1", listed)
	}
	_, err = service.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-2"})
	if !errorcode.Is(err, errorcode.PermissionDenied) {
		t.Fatalf("cross-session Events() error = %v, want permission_denied", err)
	}
}

func TestTaskCursorIsBoundToSessionAndTask(t *testing.T) {
	store := newTaskStreamTestStore(
		taskStreamTestEntry("session-1", "task-1", task.KindCommand),
		taskStreamTestEntry("session-1", "task-2", task.KindCommand),
	)
	streams := &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{
		"task-1": taskStreamTestSnapshot("session-1", "task-1", "terminal-shared", "same"),
		"task-2": taskStreamTestSnapshot("session-1", "task-2", "terminal-shared", "same"),
	}}
	service := newTaskStreamTestService(t, store, streams, "generation-1")

	first, err := service.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Records) == 0 || first.Records[0].Task.TaskID != "task-1" {
		t.Fatalf("task-1 records = %#v", first.Records)
	}
	_, err = service.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-2", Cursor: first.BoundaryCursor,
	})
	if !errorcode.Is(err, errorcode.InvalidArgument) {
		t.Fatalf("cross-task cursor error = %v, want invalid_argument", err)
	}
}

func TestEventsReportsEvictedPrefixAndProjectsRetainedSuffix(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	store := newTaskStreamTestStore(entry)
	snapshot := taskStreamTestSnapshot("session-1", "task-1", "terminal-1", "retained")
	snapshot.EventsTruncatedBefore = 5
	snapshot.TruncatedBefore = 9
	snapshot.Cursor = stream.Cursor{Events: 7, Output: 17}
	snapshot.Frames[0].Cursor = stream.Cursor{Events: 6, Output: 17}
	snapshot.Frames[0].EventsTruncatedBefore = 5
	snapshot.Frames[0].TruncatedBefore = 9
	snapshot.Frames[1].Cursor = snapshot.Cursor
	service := newTaskStreamTestService(t, store, &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{"task-1": snapshot}}, "generation-1")

	batch, err := service.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if batch.ResumeMode != ResumeModeCurrentState || !batch.TransientGap || len(batch.Records) != 3 {
		t.Fatalf("evicted read = %#v, want gap plus two retained frames", batch)
	}
	if batch.Records[0].Gap == nil || batch.Records[0].Gap.TaskID != "task-1" {
		t.Fatalf("gap record = %#v", batch.Records[0])
	}
}

func TestEventsProjectsOversizedFrameMarkerAsGapWithoutBody(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateRunning
	entry.Running = true
	snapshot := stream.Snapshot{
		Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
		Cursor: stream.Cursor{Events: 1, Output: 5 * 1024 * 1024}, State: string(task.StateRunning), Running: true,
		Frames: []stream.Frame{{
			Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
			Cursor: stream.Cursor{Events: 1, Output: 5 * 1024 * 1024}, EventsTruncatedBefore: 1, Running: true,
		}},
	}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), &taskStreamTestRuntime{
		snapshots: map[string]stream.Snapshot{"task-1": snapshot},
	}, "generation-1")

	batch, err := service.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if batch.ResumeMode != ResumeModeCurrentState || !batch.TransientGap || len(batch.Records) != 2 || batch.Records[0].Gap == nil {
		t.Fatalf("oversized-frame batch = %#v, want gap plus cursor marker", batch)
	}
	if batch.Records[1].Frame == nil || batch.Records[1].Frame.Text != "" || batch.Records[1].Frame.Event != nil {
		t.Fatalf("oversized-frame marker retained a body: %#v", batch.Records[1])
	}
}

func TestGenerationChangeFallsBackToCurrentTaskState(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	store := newTaskStreamTestStore(entry)
	oldRuntime := &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{
		"task-1": taskStreamTestSnapshot("session-1", "task-1", "terminal-1", "old output"),
	}}
	oldService := newTaskStreamTestService(t, store, oldRuntime, "old-generation")
	oldBatch, err := oldService.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}

	current := taskStreamTestSnapshot("session-1", "task-1", "terminal-1", "must not replay")
	newService := newTaskStreamTestService(t, store, &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{"task-1": current}}, "new-generation")
	resumed, err := newService.Events(context.Background(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1", Cursor: oldBatch.BoundaryCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ResumeMode != ResumeModeCurrentState || !resumed.TransientGap || len(resumed.Records) != 2 {
		t.Fatalf("generation fallback = %#v, want gap plus current terminal", resumed)
	}
	for _, record := range resumed.Records {
		if record.Frame != nil && strings.Contains(record.Frame.Text, "must not replay") {
			t.Fatalf("generation fallback replayed transient body: %#v", resumed.Records)
		}
	}
}

func TestCloseSubscriptionCancelsOnlyDelivery(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateRunning
	entry.Running = true
	runtime := &taskStreamTestRuntime{
		snapshots: map[string]stream.Snapshot{"task-1": {
			Ref:   stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
			State: string(task.StateRunning), Running: true,
		}},
		subscribeStarted: make(chan struct{}), subscribeStopped: make(chan struct{}),
	}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), runtime, "generation-1")
	result, err := service.Subscribe(context.Background(), Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	waitTaskStreamSignal(t, runtime.subscribeStarted, "runtime subscription start")
	if err := result.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	waitTaskStreamSignal(t, runtime.subscribeStopped, "runtime subscription stop")
	if entry.State != task.StateRunning || !entry.Running {
		t.Fatalf("closing delivery changed Task state: %#v", entry)
	}
}

func TestSubscribeRefreshesTaskDescriptorAcrossContinue(t *testing.T) {
	t.Parallel()

	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State = task.StateCompleted
	entry.Running = false
	entry.Metadata["turn_id"] = "turn-1"
	initial := stream.Snapshot{
		Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
		Cursor: stream.Cursor{Events: 1}, State: string(task.StateCompleted), SupportsInput: true, TerminalFramed: true,
		Frames: []stream.Frame{{
			Ref:   stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
			State: string(task.StateCompleted), Cursor: stream.Cursor{Events: 1}, Closed: true,
		}},
	}
	runtime := &taskStreamTestRuntime{
		snapshots: map[string]stream.Snapshot{"task-1": initial},
		subscribeFrames: []stream.Frame{
			{
				Ref:  stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-2"},
				Text: "continued", State: string(task.StateRunning), Running: true,
				Cursor: stream.Cursor{Events: 2, Output: int64(len("continued"))}, UpdatedAt: time.Unix(200, 0),
			},
			{
				Ref:   stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-2"},
				State: string(task.StateCompleted), Cursor: stream.Cursor{Events: 3, Output: int64(len("continued"))},
				Closed: true, UpdatedAt: time.Unix(300, 0),
			},
		},
	}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), runtime, "generation-1")
	result, err := service.Subscribe(context.Background(), Principal{ID: "owner"}, SubscribeRequest{
		SessionID: "session-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()

	var continued []Record
	for record := range result.Subscription.Records() {
		if record.Frame != nil && record.Frame.Ref.TerminalID == "turn-2" {
			continued = append(continued, record)
		}
	}
	if len(continued) != 2 {
		t.Fatalf("continued records = %#v, want running and terminal turn-2 frames", continued)
	}
	if got := continued[0].Task; got.CurrentTurnID != "turn-2" || got.State != task.StateRunning || !got.Running || got.SupportsInput {
		t.Fatalf("running descriptor = %#v, want live turn-2 state", got)
	}
	if got := continued[1].Task; got.CurrentTurnID != "turn-2" || got.State != task.StateCompleted || got.Running || !got.SupportsInput {
		t.Fatalf("terminal descriptor = %#v, want continuable completed turn-2 state", got)
	}
}

func TestSlowSubscriberDisconnectsItself(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	frames := make([]stream.Frame, 0, subscriberEventCap+2)
	for index := 1; index <= subscriberEventCap+2; index++ {
		frames = append(frames, stream.Frame{
			Ref:  stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "terminal-1"},
			Text: "chunk", Running: true, Cursor: stream.Cursor{Events: int64(index), Output: int64(index * 5)},
		})
	}
	runtime := &taskStreamTestRuntime{snapshots: map[string]stream.Snapshot{"task-1": {
		Ref:    stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "terminal-1"},
		Cursor: stream.Cursor{Events: int64(len(frames)), Output: int64(len(frames) * 5)},
		State:  string(task.StateRunning), Running: true, Frames: frames,
	}}}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), runtime, "generation-1")
	result, err := service.Subscribe(context.Background(), Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	deadline := time.Now().Add(time.Second)
	for !errors.Is(result.Subscription.Err(), ErrSlowConsumer) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(result.Subscription.Err(), ErrSlowConsumer) {
		t.Fatalf("subscription error = %v, want ErrSlowConsumer", result.Subscription.Err())
	}
}

func newTaskStreamTestService(t *testing.T, store task.Store, runtime stream.Service, generation string) Service {
	t.Helper()
	service, err := New(Config{
		Tasks: store, Streams: func() stream.Service { return runtime }, Authorizer: taskStreamTestAuthorizer{},
		Secret: taskStreamTestSecret, Generation: generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func taskStreamTestEntry(sessionID, taskID string, kind task.Kind) *task.Entry {
	return &task.Entry{
		TaskID: taskID, Handle: "handle-" + taskID, Session: session.SessionRef{SessionID: sessionID}, Kind: kind,
		Title: "Task " + taskID, State: task.StateCompleted, SupportsInput: kind == task.KindSubagent,
		SupportsCancel: true, Metadata: map[string]any{"parent_call": "parent-" + taskID, "turn_id": "turn-1", "agent": "orbit"},
	}
}

func taskStreamTestSnapshot(sessionID, taskID, terminalID, text string) stream.Snapshot {
	ref := stream.Ref{SessionID: sessionID, TaskID: taskID, TerminalID: terminalID}
	return stream.Snapshot{
		Ref: ref, Cursor: stream.Cursor{Events: 2, Output: int64(len(text))}, State: string(task.StateCompleted),
		TerminalFramed: true,
		Frames: []stream.Frame{
			{Ref: ref, Text: text, Cursor: stream.Cursor{Events: 1, Output: int64(len(text))}},
			{Ref: ref, State: string(task.StateCompleted), Cursor: stream.Cursor{Events: 2, Output: int64(len(text))}, Closed: true},
		},
	}
}

func waitTaskStreamSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

type taskStreamTestAuthorizer struct{}

func (taskStreamTestAuthorizer) AuthorizeTaskStream(_ context.Context, principal Principal, _ string) error {
	if strings.TrimSpace(principal.ID) == "" {
		return errorcode.New(errorcode.Unauthenticated, "missing principal")
	}
	return nil
}

type taskStreamTestStore struct {
	entries map[string]*task.Entry
}

func newTaskStreamTestStore(entries ...*task.Entry) *taskStreamTestStore {
	store := &taskStreamTestStore{entries: map[string]*task.Entry{}}
	for _, entry := range entries {
		store.entries[entry.TaskID] = task.CloneEntry(entry)
	}
	return store
}

func (s *taskStreamTestStore) Upsert(_ context.Context, entry *task.Entry) error {
	s.entries[entry.TaskID] = task.CloneEntry(entry)
	return nil
}

func (s *taskStreamTestStore) Get(_ context.Context, taskID string) (*task.Entry, error) {
	entry := s.entries[taskID]
	if entry == nil {
		return nil, errors.New("task not found")
	}
	return task.CloneEntry(entry), nil
}

func (s *taskStreamTestStore) ListSession(_ context.Context, ref session.SessionRef) ([]*task.Entry, error) {
	var entries []*task.Entry
	for _, entry := range s.entries {
		if entry.Session.SessionID == ref.SessionID {
			entries = append(entries, task.CloneEntry(entry))
		}
	}
	return entries, nil
}

func (s *taskStreamTestStore) GetSessionTaskByHandle(_ context.Context, ref session.SessionRef, handle string) (*task.Entry, error) {
	for _, entry := range s.entries {
		if entry.Session.SessionID == ref.SessionID && task.NormalizeHandle(firstString(entry.Handle, mapString(entry.Metadata, "handle"))) == task.NormalizeHandle(handle) {
			return task.CloneEntry(entry), nil
		}
	}
	return nil, errors.New("task not found")
}

type taskStreamTestRuntime struct {
	mu               sync.Mutex
	snapshots        map[string]stream.Snapshot
	subscribeFrames  []stream.Frame
	subscribeStarted chan struct{}
	subscribeStopped chan struct{}
}

func (r *taskStreamTestRuntime) Read(_ context.Context, req stream.ReadRequest) (stream.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, ok := r.snapshots[req.Ref.TaskID]
	if !ok {
		return stream.Snapshot{}, errors.New("stream not found")
	}
	return stream.CloneSnapshot(snapshot), nil
}

func (r *taskStreamTestRuntime) Subscribe(ctx context.Context, _ stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(yield func(*stream.Frame, error) bool) {
		r.mu.Lock()
		frames := append([]stream.Frame(nil), r.subscribeFrames...)
		r.mu.Unlock()
		for _, frame := range frames {
			cloned := stream.CloneFrame(frame)
			if !yield(&cloned, nil) {
				return
			}
		}
		if len(frames) > 0 {
			return
		}
		if r.subscribeStarted != nil {
			close(r.subscribeStarted)
		}
		<-ctx.Done()
		if r.subscribeStopped != nil {
			close(r.subscribeStopped)
		}
		yield(nil, ctx.Err())
	}
}

func (r *taskStreamTestRuntime) Wait(ctx context.Context, ref stream.Ref) (stream.Snapshot, error) {
	return r.Read(ctx, stream.ReadRequest{Ref: ref})
}

var _ task.Store = (*taskStreamTestStore)(nil)
var _ stream.Service = (*taskStreamTestRuntime)(nil)
