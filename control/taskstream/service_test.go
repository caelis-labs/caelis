package taskstream

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/control/streamspool"
	spoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
)

var taskStreamTestSecret = []byte("0123456789abcdef0123456789abcdef")

func TestEventsReadsExactControlSpool(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	entry.State = task.StateRunning
	entry.Running = true
	store := newTaskStreamTestStore(entry)
	spool := newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	observer := recorder.BindTaskOutput(t.Context(), output.Binding{
		SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1",
		Kind: output.TaskKindCommand, StartsAtTaskOrigin: true,
	})
	if err := observer.ObserveTaskOutput(t.Context(), output.Event{Text: "alpha", Running: true}); err != nil {
		t.Fatal(err)
	}

	service := newTaskStreamTestService(t, store, spool)
	batch, err := service.Events(t.Context(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Deliveries) != 1 || batch.Deliveries[0].Kind != DeliveryAppendPage || batch.Deliveries[0].Source != SourceExact ||
		len(batch.Deliveries[0].Records) != 1 || batch.Deliveries[0].Records[0].Frame == nil || batch.Deliveries[0].Records[0].Frame.Text != "alpha" {
		t.Fatalf("Events() = %#v", batch)
	}
	boundary := batch.Deliveries[0].NextCursor
	if boundary == "" || batch.Deliveries[0].Records[0].Cursor == "" {
		t.Fatalf("exact cursors are empty: %#v", batch)
	}

	if err := observer.ObserveTaskOutput(t.Context(), output.Event{Text: "beta", Running: true}); err != nil {
		t.Fatal(err)
	}
	continued, err := service.Events(t.Context(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1", Cursor: boundary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(continued.Deliveries) != 1 || len(continued.Deliveries[0].Records) != 1 || continued.Deliveries[0].Records[0].Frame.Text != "beta" {
		t.Fatalf("continued Events() = %#v", continued)
	}
}

func TestSubscribeWaitsForFirstProducerRecord(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	entry.State = task.StateRunning
	entry.Running = true
	store := newTaskStreamTestStore(entry)
	spool := newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	observer := recorder.BindTaskOutput(t.Context(), output.Binding{
		SessionID: "session-1", TaskID: "task-1", Kind: output.TaskKindCommand, StartsAtTaskOrigin: true,
	})
	service := newTaskStreamTestService(t, store, spool)
	result, err := service.Subscribe(t.Context(), Principal{ID: "owner"}, SubscribeRequest{
		SessionID: "session-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()

	select {
	case delivery := <-result.Subscription.Deliveries():
		t.Fatalf("pending subscription returned early: %#v", delivery)
	case <-time.After(20 * time.Millisecond):
	}
	if err := observer.ObserveTaskOutput(t.Context(), output.Event{Text: "first", Running: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case delivery := <-result.Subscription.Deliveries():
		if len(delivery.Records) != 1 || delivery.Records[0].Frame == nil || delivery.Records[0].Frame.Text != "first" {
			t.Fatalf("delivery = %#v", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("subscription did not wake for first record")
	}
}

func TestUnreadSubscriptionDoesNotBackpressureProducer(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	entry.State = task.StateRunning
	entry.Running = true
	store := newTaskStreamTestStore(entry)
	spool := newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	observer := recorder.BindTaskOutput(t.Context(), output.Binding{
		SessionID: "session-1", TaskID: "task-1", Kind: output.TaskKindCommand, StartsAtTaskOrigin: true,
	})
	service := newTaskStreamTestService(t, store, spool)
	result, err := service.Subscribe(t.Context(), Principal{ID: "owner"}, SubscribeRequest{
		SessionID: "session-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()

	done := make(chan error, 1)
	go func() {
		for range 128 {
			if err := observer.ObserveTaskOutput(context.Background(), output.Event{Text: "chunk", Running: true}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unread consumer blocked producer appends")
	}
}

func TestTerminalCommandFallsBackToDurableFinalResult(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	entry.State = task.StateCompleted
	entry.Running = false
	entry.Result = map[string]any{"result": "final output", "exit_code": 0}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), nil)
	batch, err := service.Events(t.Context(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Deliveries) < 3 || batch.Deliveries[0].Kind != DeliveryReplaceBegin ||
		batch.Deliveries[len(batch.Deliveries)-1].Kind != DeliveryReplaceEnd {
		t.Fatalf("fallback batch = %#v", batch)
	}
	var records []Record
	for _, delivery := range batch.Deliveries {
		records = append(records, delivery.Records...)
	}
	last := records[len(records)-1]
	if last.Frame == nil || last.Frame.Text != "final output" || !last.Frame.Closed {
		t.Fatalf("terminal fallback = %#v", last)
	}
}

func TestRunningFallbackSubscriptionWaitsForDurableFinalResult(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	entry.State = task.StateRunning
	entry.Running = true
	store := newTaskStreamTestStore(entry)
	service := newTaskStreamTestService(t, store, nil)
	result, err := service.Subscribe(t.Context(), Principal{ID: "owner"}, SubscribeRequest{
		SessionID: "session-1", TaskID: "task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	select {
	case delivery := <-result.Subscription.Deliveries():
		if delivery.Kind != DeliveryStatus || delivery.Source != SourceStatus {
			t.Fatalf("initial fallback delivery = %#v", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("running fallback emitted no status")
	}

	entry.Running = false
	entry.State = task.StateCompleted
	entry.Result = map[string]any{"result": "durable final", "exit_code": 0}
	if err := store.Upsert(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	select {
	case delivery := <-result.Subscription.Deliveries():
		if delivery.Kind != DeliveryReplaceBegin || delivery.Source != SourceReplacement {
			t.Fatalf("terminal fallback begin = %#v", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("running fallback did not observe terminal Task result")
	}
}

func TestValidCursorCacheMissReturnsAtomicFallback(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	entry.State = task.StateCompleted
	entry.Result = map[string]any{"result": "fallback"}
	spool := newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	observer := recorder.BindTaskOutput(t.Context(), output.Binding{
		SessionID: "session-1", TaskID: "task-1", Kind: output.TaskKindCommand, StartsAtTaskOrigin: true,
	})
	if err := observer.ObserveTaskOutput(t.Context(), output.Event{Text: "prefix", Running: true}); err != nil {
		t.Fatal(err)
	}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), spool)
	batch, err := service.Events(t.Context(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := spool.Resolve(t.Context(), streamspool.LogicalKey{
		Namespace: streamspool.NamespaceTask, Digest: streamspool.DigestStrings("session-1", "task-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.ObserveTaskOutput(t.Context(), output.Event{State: string(task.StateCompleted), Closed: true}); err != nil {
		t.Fatal(err)
	}
	if err := spool.Remove(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	reloaded, err := service.Events(t.Context(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1", Cursor: batch.Deliveries[0].NextCursor,
	})
	if err != nil {
		t.Fatalf("cursor cache miss error = %v", err)
	}
	if len(reloaded.Deliveries) < 3 || reloaded.Deliveries[0].Kind != DeliveryReplaceBegin ||
		reloaded.Deliveries[len(reloaded.Deliveries)-1].Kind != DeliveryReplaceEnd {
		t.Fatalf("cursor cache miss fallback = %#v", reloaded)
	}
	var records []Record
	for _, delivery := range reloaded.Deliveries {
		records = append(records, delivery.Records...)
	}
	if len(records) != 1 || records[0].Frame == nil || records[0].Frame.Text != "fallback" {
		t.Fatalf("cursor cache miss records = %#v", records)
	}
}

func TestInvalidCursorStillFailsClosed(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindCommand)
	entry.State = task.StateCompleted
	entry.Result = map[string]any{"result": "fallback"}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), nil)
	_, err := service.Events(t.Context(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1", Cursor: "not-signed",
	})
	if errorcode.CodeOf(err) != errorcode.InvalidArgument {
		t.Fatalf("invalid cursor error = %v", err)
	}
}

func TestExpectedActivityRejectsStaleRead(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.Metadata["child_activity_id"] = "activity-2"
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), nil)
	_, err := service.Events(t.Context(), Principal{ID: "owner"}, ReadRequest{
		SessionID: "session-1", TaskID: "task-1", ExpectedActivityID: "activity-1",
	})
	if errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("stale activity error = %v", err)
	}
}

func newTaskStreamTestService(t *testing.T, store task.Store, spool streamspool.Store) Service {
	t.Helper()
	service, err := New(Config{
		Tasks: store, Spool: spool, Authorizer: taskStreamTestAuthorizer{}, Secret: taskStreamTestSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newTaskStreamTestSpool(t *testing.T) *spoolfile.Store {
	t.Helper()
	store, err := spoolfile.New(t.Context(), spoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func taskStreamTestEntry(sessionID, taskID string, kind task.Kind) *task.Entry {
	return &task.Entry{
		TaskID: taskID, Handle: "handle-" + taskID, Session: session.SessionRef{SessionID: sessionID}, Kind: kind,
		Title: "Task " + taskID, State: task.StateCompleted, SupportsCancel: true,
		Metadata: map[string]any{"parent_call": "parent-" + taskID, "turn_id": "turn-1", "agent": "orbit"},
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
	mu      sync.RWMutex
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[entry.TaskID] = task.CloneEntry(entry)
	return nil
}

func (s *taskStreamTestStore) Get(_ context.Context, taskID string) (*task.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry := s.entries[taskID]
	if entry == nil {
		return nil, errors.New("task not found")
	}
	return task.CloneEntry(entry), nil
}

func (s *taskStreamTestStore) ListSession(_ context.Context, ref session.SessionRef) ([]*task.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var entries []*task.Entry
	for _, entry := range s.entries {
		if entry.Session.SessionID == ref.SessionID {
			entries = append(entries, task.CloneEntry(entry))
		}
	}
	return entries, nil
}

func (s *taskStreamTestStore) GetSessionTaskByHandle(_ context.Context, ref session.SessionRef, handle string) (*task.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.entries {
		if entry.Session.SessionID == ref.SessionID && task.NormalizeHandle(entry.Handle) == task.NormalizeHandle(handle) {
			return task.CloneEntry(entry), nil
		}
	}
	return nil, errors.New("task not found")
}

var _ task.Store = (*taskStreamTestStore)(nil)
