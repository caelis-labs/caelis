package taskstream

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/control/streamspool"
)

type spoolReplayFixture struct {
	loads atomic.Int32
	err   error
}

func (*spoolReplayFixture) LoadSession(context.Context, session.LoadSessionRequest) (session.LoadedSession, error) {
	return session.LoadedSession{Session: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}}, nil
}
func (f *spoolReplayFixture) LoadHistory(ctx context.Context, req subagent.HistoryRequest) (session.LoadedSession, error) {
	f.loads.Add(1)
	if f.err != nil {
		return session.LoadedSession{}, f.err
	}
	events := []*session.Event{
		{ID: "input-1", Type: session.EventTypeUser, Text: "first prompt", Actor: session.ActorRef{Kind: session.ActorKindUser}, Scope: &session.EventScope{TurnID: "child:1"}},
		{ID: "tool-1", Type: session.EventTypeToolCall, Text: "first tool", Scope: &session.EventScope{TurnID: "child:1"}},
		{ID: "answer-1", Type: session.EventTypeAssistant, Text: "first answer", Scope: &session.EventScope{TurnID: "child:1"}},
		{ID: "answer-2", Type: session.EventTypeAssistant, Text: "second answer", Scope: &session.EventScope{TurnID: "child:2"}},
	}
	for _, event := range events {
		message := model.NewTextMessage(model.RoleAssistant, event.Text)
		event.Message = &message
	}
	observer, ok := req.Reconnect.Spawn.Output.(output.HistoryObserver)
	if !ok {
		return session.LoadedSession{}, errors.New("missing replay observer")
	}
	if err := observer.ReplaceTaskHistory(ctx, events); err != nil {
		return session.LoadedSession{}, err
	}
	return session.LoadedSession{Events: events}, nil
}

func restoredTaskFixture() *task.Entry {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.Metadata["child_activity_id"] = "a"
	entry.Metadata["session_id"] = "child"
	entry.Spec = map[string]any{"target": delegation.Target{Selector: "helper", Placement: delegation.Placement{Kind: delegation.PlacementAgent, Agent: "helper"}}}
	return entry
}

func nextTaskDelivery(t *testing.T, ctx context.Context, sub Subscription) Delivery {
	t.Helper()
	select {
	case delivery, ok := <-sub.Deliveries():
		if !ok {
			t.Fatalf("subscription closed: %v", sub.Err())
		}
		return delivery
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return Delivery{}
	}
}

func TestChildReplayAndLiveShareOneSpoolAcrossIdleAndReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entry, spool := restoredTaskFixture(), newTaskStreamTestSpool(t)
	recorder, history := NewRecorder(spool, nil), &spoolReplayFixture{}
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	service, err := New(Config{Tasks: newTaskStreamTestStore(entry), Spool: spool, Recorder: recorder, Sessions: history, SubagentHistory: history, Authorizer: taskStreamTestAuthorizer{}, Secret: taskStreamTestSecret})
	if err != nil {
		t.Fatal(err)
	}
	var subscriptions []Subscription
	for range 2 {
		result, err := service.Subscribe(ctx, Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1", TaskID: "task-1", Follow: true})
		if err != nil {
			t.Fatal(err)
		}
		subscriptions = append(subscriptions, result.Subscription)
		defer result.Subscription.Close()
	}
	var cursors []string
	for _, sub := range subscriptions {
		var texts []string
		for {
			delivery := nextTaskDelivery(t, ctx, sub)
			for _, record := range delivery.Records {
				if record.Frame != nil && record.Frame.Event != nil {
					texts = append(texts, session.EventText(record.Frame.Event))
				}
			}
			if delivery.Kind == DeliveryAppendPage && delivery.NextCursor != "" {
				cursors = append(cursors, delivery.NextCursor)
				break
			}
		}
		if strings.Join(texts, "|") != "first prompt|first tool|first answer|second answer" {
			t.Fatalf("replay lost history: %q", texts)
		}
	}
	if history.loads.Load() != 1 {
		t.Fatalf("provider loads=%d, want shared one", history.loads.Load())
	}
	// The durable directory can still say completed. Actual producer output must
	// arrive without waiting for its next Task commit or for a Closed frame.
	second := recorder.BindTaskOutput(ctx, output.Binding{SessionID: "session-1", TaskID: "task-1", ActivityID: "b", Kind: output.TaskKindSubagent})
	if err := second.ObserveTaskOutput(ctx, output.Event{Running: true, State: "running", Text: "live before completion"}); err != nil {
		t.Fatal(err)
	}
	for _, sub := range subscriptions {
		delivery := nextTaskDelivery(t, ctx, sub)
		if delivery.Kind != DeliveryAppendPage || len(delivery.Records) != 1 || delivery.Records[0].Frame.Text != "live before completion" {
			t.Fatalf("not live: %#v", delivery)
		}
	}
	reopened, err := service.Subscribe(ctx, Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1", TaskID: "task-1", Cursor: cursors[0], Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Subscription.Close()
	delivery := nextTaskDelivery(t, ctx, reopened.Subscription)
	if delivery.Kind != DeliveryAppendPage || len(delivery.Records) != 1 || delivery.Records[0].Frame.Text != "live before completion" {
		t.Fatalf("reopen duplicated replay: %#v", delivery)
	}
	if history.loads.Load() != 1 {
		t.Fatal("reopen reloaded provider instead of retained spool")
	}
}

func TestChildHistoryFailureNeverPublishesTaskFinalAsFullHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	entry, spool := restoredTaskFixture(), newTaskStreamTestSpool(t)
	entry.Result = map[string]any{"result": "only final answer"}
	recorder, history := NewRecorder(spool, nil), &spoolReplayFixture{err: errors.New("provider replay failed")}
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	service, err := New(Config{Tasks: newTaskStreamTestStore(entry), Spool: spool, Recorder: recorder, Sessions: history, SubagentHistory: history, Authorizer: taskStreamTestAuthorizer{}, Secret: taskStreamTestSecret})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Subscribe(ctx, Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1", TaskID: "task-1", Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	select {
	case delivery, ok := <-result.Subscription.Deliveries():
		if ok {
			t.Fatalf("failed replay replaced document: %#v", delivery)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if result.Subscription.Err() == nil {
		t.Fatal("failure was hidden")
	}
}

type blockingReplayStore struct {
	streamspool.Store
	entered, release chan struct{}
}

func (s blockingReplayStore) Register(ctx context.Context, key streamspool.LogicalKey, opts streamspool.WriterOptions) (streamspool.Writer, error) {
	w, err := s.Store.Register(ctx, key, opts)
	if err == nil && opts.Unpublished {
		return &blockingReplayWriter{Writer: w, entered: s.entered, release: s.release}, nil
	}
	return w, err
}

type blockingReplayWriter struct {
	streamspool.Writer
	entered, release chan struct{}
	blocked          bool
}

func (w *blockingReplayWriter) AppendBatch(ctx context.Context, records []streamspool.Record) (streamspool.Offset, error) {
	if !w.blocked {
		w.blocked = true
		close(w.entered)
		<-w.release
	}
	return w.Writer.AppendBatch(ctx, records)
}

func TestReplayPublicationIsAtomicAndDoesNotBlockLiveProducer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	base := newTaskStreamTestSpool(t)
	blocked := blockingReplayStore{Store: base, entered: make(chan struct{}), release: make(chan struct{})}
	recorder := NewRecorder(blocked, nil)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	observer := recorder.BindTaskOutput(ctx, output.Binding{SessionID: "session-1", TaskID: "task-1", Kind: output.TaskKindSubagent})
	logical := streamspool.LogicalKey{Namespace: streamspool.NamespaceTask, Digest: streamspool.DigestStrings("session-1", "task-1")}
	old, _, _ := base.Resolve(ctx, logical)
	if err := observer.(output.HistoryObserver).ReplaceTaskHistory(ctx, []*session.Event{{Type: session.EventTypeAssistant, Text: "old complete history"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer func() {
		select {
		case <-blocked.release:
		default:
			close(blocked.release)
		}
	}()
	produced := make(chan error, 1)
	go func() {
		for range 100 {
			if err := observer.ObserveTaskOutput(ctx, output.Event{Text: "live", Running: true}); err != nil {
				produced <- err
				return
			}
		}
		produced <- nil
	}()
	select {
	case err := <-produced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("disk blocked producer")
	}
	current, bounds, err := base.Resolve(ctx, logical)
	if err != nil || current != old || bounds.OriginComplete {
		t.Fatalf("partial replay published: %#v %v", bounds, err)
	}
	close(blocked.release)
	if err := recorder.Flush(ctx, task.Ref{SessionID: "session-1", TaskID: "task-1"}); err != nil {
		t.Fatal(err)
	}
	current, bounds, err = base.Resolve(ctx, logical)
	if err != nil || current == old || !bounds.OriginComplete || bounds.High != 101 {
		t.Fatalf("replay/live join=%#v %v", bounds, err)
	}
	reader, err := base.Reader(ctx, current, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for index := range 101 {
		record, err := reader.Next(ctx)
		if err != nil || record.Offset != streamspool.Offset(index) {
			t.Fatalf("ordered record %d: %#v %v", index, record, err)
		}
	}
}

func TestFailedChildCacheRebuildsFromProviderOnNewSubscription(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	entry, spool := restoredTaskFixture(), newTaskStreamTestSpool(t)
	recorder, history := NewRecorder(spool, nil), &spoolReplayFixture{}
	defer recorder.Close(context.Background())
	observer := recorder.BindTaskOutput(ctx, output.Binding{SessionID: "session-1", TaskID: "task-1", Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true}).(*boundRecorder)
	if err := observer.ObserveTaskOutput(ctx, output.Event{Text: "incomplete"}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(ctx, task.Ref{SessionID: "session-1", TaskID: "task-1"}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.failQueue(observer.partition, streamspool.ErrLimit); !errors.Is(err, streamspool.ErrLimit) {
		t.Fatal(err)
	}
	select {
	case <-observer.partition.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	service, err := New(Config{Tasks: newTaskStreamTestStore(entry), Spool: spool, Recorder: recorder, Sessions: history, SubagentHistory: history, Authorizer: taskStreamTestAuthorizer{}, Secret: taskStreamTestSecret})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Events(ctx, Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deliveries) != 1 || result.Deliveries[0].Kind != DeliveryAppendPage || len(result.Deliveries[0].Records) != 4 || history.loads.Load() != 1 {
		t.Fatalf("recovery = %#v", result)
	}
	if err := observer.ObserveTaskOutput(ctx, output.Event{Text: "stale writer"}); !errors.Is(err, streamspool.ErrLimit) {
		t.Fatalf("old producer redirected into recovered cache: %v", err)
	}
}
