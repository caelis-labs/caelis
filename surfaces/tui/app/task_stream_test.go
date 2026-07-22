package tuiapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	sdkstream "github.com/caelis-labs/caelis/agent-sdk/task/stream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

func TestTUITaskPanelSubscribesIndependentStreamAndCloseDoesNotCancelTask(t *testing.T) {
	t.Parallel()

	subscription := newTUITestTaskSubscription()
	controlService := &tuiTestTaskStreamService{
		subscription: subscription,
		requests:     make(chan controltaskstream.SubscribeRequest, 1),
		list: controltaskstream.ListResult{Tasks: []controltaskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true,
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "SPAWN"},
		}}},
	}
	service := taskstream.New(controlService)
	messages := make(chan tea.Msg, 8)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	model := NewModel(Config{
		Context:             context.Background(),
		NoColor:             true,
		NoAnimation:         true,
		TaskStreams:         service,
		TaskStreamPrincipal: taskstream.Principal{ID: "user-1"},
		ProgramSender:       sender,
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "SPAWN",
	})
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "SPAWN helper",
			Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "self", "prompt": "inspect"}, Meta: meta,
		},
	})
	running := schema.ToolStatusInProgress
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "zuri", "state": "running"}, Meta: meta,
		},
	})
	resolved := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	if next, _ := model.Update(resolved); next != nil {
		model = next.(*Model)
	}

	select {
	case request := <-controlService.requests:
		if request.SessionID != "session-1" || request.TaskID != "task-1" {
			t.Fatalf("Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("expanded Task panel did not subscribe")
	}
	opened := receiveTUITaskStreamMessage[taskStreamOpenedMsg](t, messages)
	if next, _ := model.Update(opened); next != nil {
		model = next.(*Model)
	}

	subscription.records <- controltaskstream.Record{
		Cursor: "cursor-1", Generation: "generation-1", Sequence: 1,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true, CurrentTurnID: "child-turn-1",
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "SPAWN"},
		},
		Frame: &sdkstream.Frame{
			Ref:     sdkstream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "child-turn-1"},
			Running: true, Cursor: sdkstream.Cursor{Events: 1},
			Event: &session.Event{
				ID: "child-event-1", Type: session.EventTypeAssistant,
				Scope: &session.EventScope{Participant: session.ParticipantRef{Kind: session.ParticipantKindSubagent}},
				Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), MessageID: "child-message-1",
					Content: session.ProtocolTextContent("isolated child output"),
				}},
			},
		},
	}
	batch := receiveTUITaskStreamMessage[taskStreamBatchMsg](t, messages)
	if next, _ := model.Update(batch); next != nil {
		model = next.(*Model)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	found := false
	for _, event := range block.Events {
		if event.TaskHandle == "zuri" && event.Output == "isolated child output" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Task panel events = %#v, want isolated child output", block.Events)
	}

	model.wantTaskStreamForPanel("spawn-1", "zuri", false)
	if got := subscription.closeCalls.Load(); got != 1 {
		t.Fatalf("subscription Close calls = %d, want one delivery-only close", got)
	}
	if controlService.cancelCalls.Load() != 0 {
		t.Fatalf("Task cancel calls = %d, closing panel must not cancel Task", controlService.cancelCalls.Load())
	}
}

func TestTUITaskPanelRetriesDirectoryAndSubscriptionFailures(t *testing.T) {
	t.Parallel()

	subscription := newTUIProtocolTaskSubscription()
	service := &tuiRetryTaskStreamService{subscription: subscription}
	messages := make(chan tea.Msg, 16)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	model := NewModel(Config{
		Context: context.Background(), NoColor: true, NoAnimation: true,
		TaskStreams: service, TaskStreamPrincipal: taskstream.Principal{ID: "user-1"}, ProgramSender: sender,
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "SPAWN",
	})
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "SPAWN helper",
			Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "self", "prompt": "inspect"}, Meta: meta,
		},
	})
	running := schema.ToolStatusInProgress
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "zuri", "state": "running"}, Meta: meta,
		},
	})

	missing := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	if !errors.Is(missing.err, errTaskStreamNotDiscoverable) {
		t.Fatalf("first directory result error = %v, want retryable discovery miss", missing.err)
	}
	next, retryResolve := model.Update(missing)
	model = next.(*Model)
	if retryResolve == nil {
		t.Fatal("directory miss did not schedule a retry")
	}
	next, _ = model.Update(retryResolve())
	model = next.(*Model)

	resolved := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	if resolved.err != nil || resolved.taskID != "task-1" {
		t.Fatalf("retried directory result = %#v", resolved)
	}
	next, _ = model.Update(resolved)
	model = next.(*Model)

	closed := receiveTUITaskStreamMessage[taskStreamClosedMsg](t, messages)
	if !errorcode.Is(closed.err, errorcode.Unavailable) {
		t.Fatalf("first Subscribe error = %v, want unavailable", closed.err)
	}
	next, retrySubscribe := model.Update(closed)
	model = next.(*Model)
	if retrySubscribe == nil {
		t.Fatal("recoverable Subscribe failure did not schedule a retry")
	}
	next, _ = model.Update(retrySubscribe())
	model = next.(*Model)

	opened := receiveTUITaskStreamMessage[taskStreamOpenedMsg](t, messages)
	next, _ = model.Update(opened)
	model = next.(*Model)
	if calls := service.subscribeCalls.Load(); calls != 2 {
		t.Fatalf("Subscribe calls = %d, want failure plus retry", calls)
	}
	model.closeTaskStreamSubscriptions()
}

func TestTUITaskMailboxBoundsOneUpdateBatch(t *testing.T) {
	t.Parallel()

	events := make(chan eventstream.Envelope, taskStreamMailboxBatchSize+8)
	for i := 0; i < cap(events); i++ {
		events <- eventstream.Envelope{EventID: "event"}
	}
	batch, open := readTaskStreamMailbox(context.Background(), events)
	if !open || len(batch) != taskStreamMailboxBatchSize {
		t.Fatalf("mailbox batch = %d open=%v, want %d/open", len(batch), open, taskStreamMailboxBatchSize)
	}

	one := make(chan eventstream.Envelope, 1)
	one <- eventstream.Envelope{EventID: "one"}
	started := time.Now()
	batch, open = readTaskStreamMailbox(context.Background(), one)
	if !open || len(batch) != 1 || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("time-bounded mailbox batch = %d open=%v elapsed=%v", len(batch), open, time.Since(started))
	}
}

func TestTUITaskPanelSurfacesPermanentSubscriptionFailure(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamHandlesByID["task-1"] = "zuri"

	next, _ := model.handleTaskStreamClosed(taskStreamClosedMsg{
		sessionID: "session-1", taskID: "task-1", token: 7,
		err: errorcode.New(errorcode.PermissionDenied, "task stream access denied"),
	})
	model = next.(*Model)
	if !strings.Contains(model.hint, "Task zuri live output is unavailable") || !strings.Contains(model.hint, "access denied") {
		t.Fatalf("permanent Task stream failure hint = %q", model.hint)
	}
}

func receiveTUITaskStreamMessage[T any](t *testing.T, messages <-chan tea.Msg) T {
	t.Helper()
	select {
	case raw := <-messages:
		message, ok := raw.(T)
		if !ok {
			t.Fatalf("task stream message = %T, want %T", raw, *new(T))
		}
		return message
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %T", *new(T))
		return *new(T)
	}
}

type tuiTestTaskStreamService struct {
	subscription *tuiTestTaskSubscription
	requests     chan controltaskstream.SubscribeRequest
	list         controltaskstream.ListResult
	cancelCalls  atomic.Int32
}

type tuiRetryTaskStreamService struct {
	listCalls      atomic.Int32
	subscribeCalls atomic.Int32
	subscription   *tuiProtocolTaskSubscription
}

type tuiProtocolTaskSubscription struct {
	events    chan eventstream.Envelope
	closeOnce sync.Once
}

func newTUIProtocolTaskSubscription() *tuiProtocolTaskSubscription {
	return &tuiProtocolTaskSubscription{events: make(chan eventstream.Envelope)}
}

func (s *tuiProtocolTaskSubscription) Events() <-chan eventstream.Envelope { return s.events }
func (*tuiProtocolTaskSubscription) Err() error                            { return nil }
func (*tuiProtocolTaskSubscription) LastCursor() string                    { return "" }
func (s *tuiProtocolTaskSubscription) Close() error {
	s.closeOnce.Do(func() { close(s.events) })
	return nil
}

func (s *tuiRetryTaskStreamService) List(context.Context, taskstream.Principal, taskstream.ListRequest) (taskstream.ListResult, error) {
	if s.listCalls.Add(1) == 1 {
		return taskstream.ListResult{}, nil
	}
	return taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
		SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
		State: task.StateRunning, Running: true,
		ParentTool: taskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "SPAWN"},
	}}}, nil
}

func (*tuiRetryTaskStreamService) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.Batch, error) {
	return taskstream.Batch{}, nil
}

func (s *tuiRetryTaskStreamService) Subscribe(context.Context, taskstream.Principal, taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	if s.subscribeCalls.Add(1) == 1 {
		return taskstream.SubscribeResult{}, errorcode.New(errorcode.Unavailable, "task stream temporarily unavailable")
	}
	return taskstream.SubscribeResult{Subscription: s.subscription, ResumeMode: taskstream.ResumeModeExact}, nil
}

func (s *tuiTestTaskStreamService) List(context.Context, controltaskstream.Principal, controltaskstream.ListRequest) (controltaskstream.ListResult, error) {
	return s.list, nil
}

func (s *tuiTestTaskStreamService) Events(context.Context, controltaskstream.Principal, controltaskstream.ReadRequest) (controltaskstream.Batch, error) {
	return controltaskstream.Batch{}, nil
}

func (s *tuiTestTaskStreamService) Subscribe(_ context.Context, _ controltaskstream.Principal, request controltaskstream.SubscribeRequest) (controltaskstream.SubscribeResult, error) {
	s.requests <- request
	return controltaskstream.SubscribeResult{Subscription: s.subscription, ResumeMode: controltaskstream.ResumeModeExact}, nil
}

type tuiTestTaskSubscription struct {
	records    chan controltaskstream.Record
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func newTUITestTaskSubscription() *tuiTestTaskSubscription {
	return &tuiTestTaskSubscription{records: make(chan controltaskstream.Record, 8)}
}

func (s *tuiTestTaskSubscription) Records() <-chan controltaskstream.Record { return s.records }
func (s *tuiTestTaskSubscription) Err() error                               { return nil }
func (s *tuiTestTaskSubscription) LastCursor() string                       { return "" }
func (s *tuiTestTaskSubscription) Close() error {
	s.closeOnce.Do(func() {
		s.closeCalls.Add(1)
		close(s.records)
	})
	return nil
}
