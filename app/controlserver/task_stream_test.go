package controlserver

import (
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

func TestTaskHTTPRoutesBindPrincipalAndPreserveEnvelopeWire(t *testing.T) {
	envelope := eventstream.Envelope{
		Kind: eventstream.KindNotice, Cursor: "task-cursor-1",
		SessionID: "session-1", Notice: "异步输出",
		Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Anchor:     eventstream.DurableFeedPosition{Seq: math.MaxUint64},
			Generation: "generation-1",
			Sequence:   math.MaxUint64,
		}},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
	}
	tasks := &fakeTaskService{
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "command-1",
			Kind: task.KindCommand, State: task.StateRunning, Running: true,
		}}},
		batch: taskstream.Batch{
			Events:         []eventstream.Envelope{envelope},
			ResumeMode:     taskstream.ResumeModeExact,
			BoundaryCursor: "task-boundary-1",
		},
	}
	server := newTaskTestServer(t, tasks)

	listRequest := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/tasks", nil)
	authorizeTestRequest(listRequest)
	listRecorder := httptest.NewRecorder()
	server.ServeHTTP(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK ||
		!strings.Contains(listRecorder.Body.String(), `"task_id":"task-1"`) ||
		tasks.principal.ID != "trusted-owner" {
		t.Fatalf("Task list status=%d principal=%#v body=%s", listRecorder.Code, tasks.principal, listRecorder.Body.String())
	}

	eventsRequest := httptest.NewRequest(
		http.MethodGet,
		apiPrefix+"/sessions/session-1/tasks/task-1/events?after=task-cursor-0",
		nil,
	)
	authorizeTestRequest(eventsRequest)
	eventsRecorder := httptest.NewRecorder()
	server.ServeHTTP(eventsRecorder, eventsRequest)
	if eventsRecorder.Code != http.StatusOK ||
		eventsRecorder.Header().Get(resumeModeHeader) != string(taskstream.ResumeModeExact) ||
		eventsRecorder.Header().Get(boundaryCursorHeader) != "task-boundary-1" ||
		!strings.Contains(eventsRecorder.Body.String(), `"sequence":"18446744073709551615"`) {
		t.Fatalf("Task events headers=%#v body=%s", eventsRecorder.Header(), eventsRecorder.Body.String())
	}
}

func TestTaskHTTPSSEMarksCleanAndFailedSubscriptionEnds(t *testing.T) {
	for _, test := range []struct {
		name      string
		streamErr error
		wantEvent string
	}{
		{name: "clean", wantEvent: taskstream.StreamDoneEventName},
		{name: "slow consumer", streamErr: taskstream.ErrSlowConsumer, wantEvent: taskstream.StreamErrorEventName},
	} {
		t.Run(test.name, func(t *testing.T) {
			subscription := newTaskTestSubscription(test.streamErr, eventstream.Envelope{
				Kind: eventstream.KindNotice, Cursor: "task-cursor-1",
				SessionID: "session-1", Notice: "live",
			})
			tasks := &fakeTaskService{subscribe: taskstream.SubscribeResult{
				Subscription: subscription,
				ResumeMode:   taskstream.ResumeModeCurrentState,
				TransientGap: true,
			}}
			server := newTaskTestServer(t, tasks)
			request := httptest.NewRequest(
				http.MethodGet,
				apiPrefix+"/sessions/session-1/tasks/task-1/subscribe",
				nil,
			)
			authorizeTestRequest(request)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			response := recorder.Result()
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK ||
				response.Header.Get(resumeModeHeader) != string(taskstream.ResumeModeCurrentState) ||
				!strings.Contains(string(body), "id: task-cursor-1") ||
				!strings.Contains(string(body), "event: "+test.wantEvent) {
				t.Fatalf("Task SSE status=%d headers=%#v body=%s", response.StatusCode, response.Header, body)
			}
			if test.streamErr != nil &&
				(!strings.Contains(string(body), `"code":"slow_consumer"`) ||
					strings.Contains(string(body), test.streamErr.Error())) {
				t.Fatalf("Task SSE error was not safely typed: %s", body)
			}
		})
	}
}

func newTaskTestServer(t *testing.T, tasks taskstream.Service) *Server {
	t.Helper()
	server, err := New(HandlerConfig{
		Service:       &fakeService{},
		TaskStreams:   tasks,
		Authenticator: testAuthenticator(),
		AllowedHosts:  []string{"example.test", "127.0.0.1"},
		Heartbeat:     time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

type taskTestSubscription struct {
	events chan eventstream.Envelope
	err    error
	last   string
}

func newTaskTestSubscription(err error, events ...eventstream.Envelope) *taskTestSubscription {
	channel := make(chan eventstream.Envelope, len(events))
	last := ""
	for _, envelope := range events {
		channel <- envelope
		last = envelope.Cursor
	}
	close(channel)
	return &taskTestSubscription{events: channel, err: err, last: last}
}

func (s *taskTestSubscription) Events() <-chan eventstream.Envelope { return s.events }
func (*taskTestSubscription) Close() error                          { return nil }
func (s *taskTestSubscription) Err() error                          { return s.err }
func (s *taskTestSubscription) LastCursor() string                  { return s.last }

var _ taskstream.Subscription = (*taskTestSubscription)(nil)

func TestTaskHTTPRejectsUnauthorizedSessionBeforeStreaming(t *testing.T) {
	tasks := &fakeTaskService{err: errors.New("must not leak")}
	server := newTaskTestServer(t, tasks)
	request := httptest.NewRequest(
		http.MethodGet,
		apiPrefix+"/sessions/session-1/tasks/task-1/subscribe",
		nil,
	)
	request.Host = "example.test"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if tasks.principal.ID != "" || len(tasks.principal.Roles) != 0 {
		t.Fatalf("Task service received unauthenticated principal %#v", tasks.principal)
	}
}
