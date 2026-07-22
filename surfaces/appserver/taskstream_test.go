package appserver

import (
	"context"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	sdkstream "github.com/caelis-labs/caelis/agent-sdk/task/stream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

func TestTaskHTTPDirectoryAndFiniteEvents(t *testing.T) {
	envelope := taskHTTPEnvelope("task-cursor-1")
	tasks := &fakeTaskStreamService{
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", AgentHandle: "orbit", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true, CurrentTurnID: "task-1:1",
		}}},
		batch: taskstream.Batch{Events: []eventstream.Envelope{envelope}, ResumeMode: taskstream.ResumeModeExact, BoundaryCursor: "task-boundary-1"},
	}
	server := newTaskHTTPTestServer(t, tasks)

	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/tasks", nil)
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"task_id":"task-1"`) || !strings.Contains(recorder.Body.String(), `"handle":"zuri"`) {
		t.Fatalf("task list = %d %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/tasks/task-1/events?after=task-cursor-0", nil)
	authorizeTestRequest(request)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || tasks.read.SessionID != "session-1" || tasks.read.TaskID != "task-1" || tasks.read.Cursor != "task-cursor-0" {
		t.Fatalf("task events request = %#v response=%d %s", tasks.read, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"resume_mode":"exact"`) || !strings.Contains(recorder.Body.String(), `"cursor":"task-cursor-1"`) {
		t.Fatalf("task events response = %s", recorder.Body.String())
	}
}

func TestTaskSSEUsesIndependentResumeBoundaryAndEnvelopeCursor(t *testing.T) {
	envelope := taskHTTPEnvelope("task-cursor-1")
	subscription := newTaskHTTPSubscription(envelope)
	tasks := &fakeTaskStreamService{subscribe: taskstream.SubscribeResult{
		Subscription: subscription, ResumeMode: taskstream.ResumeModeCurrentState,
		TransientGap: true, BoundaryCursor: "task-boundary-1",
	}}
	server := newTaskHTTPTestServer(t, tasks)
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/tasks/task-1/stream", nil)
	request.Header.Set("Last-Event-ID", "task-cursor-0")
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get(resumeModeHeader) != string(taskstream.ResumeModeCurrentState) ||
		response.Header.Get(transientGapHeader) != "true" || response.Header.Get(boundaryCursorHeader) != "task-boundary-1" {
		t.Fatalf("Task SSE headers = %#v", response.Header)
	}
	text := string(body)
	if !strings.Contains(text, `"resume_mode":"current_state"`) || !strings.Contains(text, "id: task-cursor-1\n") || !strings.Contains(text, `"scope_id":"task-1"`) {
		t.Fatalf("Task SSE body = %q", text)
	}
	if tasks.subscribed.Cursor != "task-cursor-0" || subscription.closed != 1 {
		t.Fatalf("Task SSE request=%#v subscription closes=%d", tasks.subscribed, subscription.closed)
	}
}

func TestTaskSSESlowConsumerReturnsRetryGapWithoutCancelRoute(t *testing.T) {
	subscription := newTaskHTTPSubscription()
	subscription.err = taskstream.ErrSlowConsumer
	subscription.lastCursor = "retry-task-cursor"
	tasks := &fakeTaskStreamService{subscribe: taskstream.SubscribeResult{
		Subscription: subscription, ResumeMode: taskstream.ResumeModeExact, BoundaryCursor: "task-boundary-1",
	}}
	server := newTaskHTTPTestServer(t, tasks)
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/tasks/task-1/stream", nil)
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if body := recorder.Body.String(); strings.Count(body, "event: "+resumeEventName) != 2 ||
		!strings.Contains(body, `"resume_mode":"current_state"`) || !strings.Contains(body, `"boundary_cursor":"retry-task-cursor"`) {
		t.Fatalf("slow Task SSE body = %q", body)
	}

	cancelRequest := httptest.NewRequest(http.MethodPost, apiPrefix+"/sessions/session-1/tasks/task-1/cancel", nil)
	authorizeTestRequest(cancelRequest)
	cancelRecorder := httptest.NewRecorder()
	server.ServeHTTP(cancelRecorder, cancelRequest)
	if cancelRecorder.Code != http.StatusNotFound {
		t.Fatalf("Task cancel route status = %d, want 404", cancelRecorder.Code)
	}
}

func TestTaskSSEStackProjectsControlGapAndRetainedCommandFrames(t *testing.T) {
	t.Parallel()

	entry := &task.Entry{
		TaskID: "task-1", Kind: task.KindCommand, Session: session.SessionRef{SessionID: "session-1"},
		State: task.StateCompleted, UpdatedAt: time.Unix(200, 0),
		Metadata: map[string]any{"parent_call": "command-1", "parent_tool": "RUN_COMMAND"},
	}
	streams := &taskHTTPStackStream{snapshot: sdkstream.Snapshot{
		Ref:    sdkstream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "terminal-1"},
		Cursor: sdkstream.Cursor{Events: 6, Output: 9}, State: string(task.StateCompleted),
		EventsTruncatedBefore: 4, TerminalFramed: true,
		Frames: []sdkstream.Frame{
			{Ref: sdkstream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "terminal-1"}, Text: "retained\n", Cursor: sdkstream.Cursor{Events: 5, Output: 9}},
			{Ref: sdkstream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "terminal-1"}, State: string(task.StateCompleted), Closed: true, Cursor: sdkstream.Cursor{Events: 6, Output: 9}},
		},
	}}
	controlService, err := controltaskstream.New(controltaskstream.Config{
		Tasks: &taskHTTPStackStore{entry: entry}, Streams: func() sdkstream.Service { return streams },
		Authorizer: taskHTTPStackAuthorizer{}, Secret: []byte("0123456789abcdef0123456789abcdef"), Generation: "generation-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := newTaskHTTPTestServer(t, taskstream.New(controlService))
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/tasks/task-1/stream", nil)
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || recorder.Header().Get(resumeModeHeader) != string(taskstream.ResumeModeCurrentState) ||
		recorder.Header().Get(transientGapHeader) != "true" {
		t.Fatalf("stack Task SSE response = %d headers=%#v body=%q", recorder.Code, recorder.Header(), body)
	}
	if !strings.Contains(body, "transient Task output before this boundary is no longer available") ||
		!strings.Contains(body, `"data":"retained\n"`) ||
		!strings.Contains(body, `"terminal_exit"`) || strings.Count(body, "id: ") != 3 {
		t.Fatalf("stack Task SSE body = %q, want gap, retained output, and close envelopes", body)
	}
	if streams.subscribeCursor != (sdkstream.Cursor{Events: 6, Output: 9}) {
		t.Fatalf("SDK Subscribe cursor = %#v, want finite-read boundary", streams.subscribeCursor)
	}
}

func newTaskHTTPTestServer(t *testing.T, tasks taskstream.Service) *Server {
	t.Helper()
	server, err := New(Config{
		Service: &fakeService{}, TaskStreams: tasks, Authenticator: testAuthenticator(),
		AllowedHosts: []string{"example.test", "127.0.0.1"}, Heartbeat: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func taskHTTPEnvelope(cursor string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindNotice, Cursor: cursor, SessionID: "session-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-1", Notice: "child update",
		Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Generation: "task-generation", Sequence: 1,
		}},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
	}
}

type fakeTaskStreamService struct {
	list       taskstream.ListResult
	batch      taskstream.Batch
	subscribe  taskstream.SubscribeResult
	read       taskstream.ReadRequest
	subscribed taskstream.SubscribeRequest
}

func (s *fakeTaskStreamService) List(context.Context, taskstream.Principal, taskstream.ListRequest) (taskstream.ListResult, error) {
	return s.list, nil
}
func (s *fakeTaskStreamService) Events(_ context.Context, _ taskstream.Principal, req taskstream.ReadRequest) (taskstream.Batch, error) {
	s.read = req
	return s.batch, nil
}
func (s *fakeTaskStreamService) Subscribe(_ context.Context, _ taskstream.Principal, req taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	s.subscribed = req
	return s.subscribe, nil
}

type taskHTTPSubscription struct {
	events     chan eventstream.Envelope
	err        error
	lastCursor string
	closed     int
}

func newTaskHTTPSubscription(events ...eventstream.Envelope) *taskHTTPSubscription {
	channel := make(chan eventstream.Envelope, len(events))
	for _, event := range events {
		channel <- event
	}
	close(channel)
	return &taskHTTPSubscription{events: channel}
}

func (s *taskHTTPSubscription) Events() <-chan eventstream.Envelope { return s.events }
func (s *taskHTTPSubscription) Close() error {
	s.closed++
	return nil
}
func (s *taskHTTPSubscription) Err() error         { return s.err }
func (s *taskHTTPSubscription) LastCursor() string { return s.lastCursor }

var _ taskstream.Service = (*fakeTaskStreamService)(nil)
var _ taskstream.Subscription = (*taskHTTPSubscription)(nil)

type taskHTTPStackStore struct{ entry *task.Entry }

func (s *taskHTTPStackStore) Upsert(_ context.Context, entry *task.Entry) error {
	s.entry = task.CloneEntry(entry)
	return nil
}
func (s *taskHTTPStackStore) Get(_ context.Context, taskID string) (*task.Entry, error) {
	if s.entry == nil || s.entry.TaskID != taskID {
		return nil, nil
	}
	return task.CloneEntry(s.entry), nil
}
func (s *taskHTTPStackStore) ListSession(_ context.Context, ref session.SessionRef) ([]*task.Entry, error) {
	if s.entry == nil || s.entry.Session.SessionID != ref.SessionID {
		return nil, nil
	}
	return []*task.Entry{task.CloneEntry(s.entry)}, nil
}
func (s *taskHTTPStackStore) GetSessionTaskByHandle(_ context.Context, ref session.SessionRef, _ string) (*task.Entry, error) {
	if s.entry == nil || s.entry.Session.SessionID != ref.SessionID {
		return nil, nil
	}
	return task.CloneEntry(s.entry), nil
}

type taskHTTPStackStream struct {
	snapshot        sdkstream.Snapshot
	subscribeCursor sdkstream.Cursor
}

func (s *taskHTTPStackStream) Read(_ context.Context, request sdkstream.ReadRequest) (sdkstream.Snapshot, error) {
	return sdkstream.CloneSnapshot(s.snapshot), nil
}
func (s *taskHTTPStackStream) Subscribe(_ context.Context, request sdkstream.SubscribeRequest) iter.Seq2[*sdkstream.Frame, error] {
	s.subscribeCursor = request.Cursor
	return func(func(*sdkstream.Frame, error) bool) {}
}

type taskHTTPStackAuthorizer struct{}

func (taskHTTPStackAuthorizer) AuthorizeTaskStream(context.Context, controltaskstream.Principal, string) error {
	return nil
}

func TestTaskWireTypesConformToOpenAPI(t *testing.T) {
	descriptor := taskstream.TaskDescriptor{
		SessionID: "session-1", TaskID: "task-1", Handle: "zuri", AgentHandle: "orbit", Kind: task.KindSubagent, Title: "helper",
		State: task.StateWaitingApproval, Running: true, SupportsCancel: true,
		ParentTool: taskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "SPAWN"}, CurrentTurnID: "task-1:2",
	}
	validateWireValue(t, "TaskDescriptor", descriptor)
	validateWireValue(t, "TaskList", taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{descriptor}})
	validateWireValue(t, "TaskResumeBoundary", map[string]any{"resume_mode": taskstream.ResumeModeCurrentState, "transient_gap": true, "boundary_cursor": "cursor-1"})
	validateWireValue(t, "TaskEventBatch", taskstream.Batch{ResumeMode: taskstream.ResumeModeExact, Events: []eventstream.Envelope{taskHTTPEnvelope("cursor-1")}})
}
