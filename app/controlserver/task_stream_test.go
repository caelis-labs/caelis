package controlserver

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestTaskHTTPRoutesBindPrincipalAndPreserveEnvelopeWire(t *testing.T) {
	envelope := eventstream.Envelope{
		Kind: eventstream.KindNotice, Cursor: "task-cursor-1",
		SessionID: "session-1", ActivityID: "activity-2", Notice: "异步输出",
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
			ActivityID:     "activity-2",
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
		apiPrefix+"/sessions/session-1/tasks/task-1/events?after=task-cursor-0&activity_id=activity-2",
		nil,
	)
	authorizeTestRequest(eventsRequest)
	eventsRecorder := httptest.NewRecorder()
	server.ServeHTTP(eventsRecorder, eventsRequest)
	if eventsRecorder.Code != http.StatusOK ||
		eventsRecorder.Header().Get(resumeModeHeader) != string(taskstream.ResumeModeExact) ||
		eventsRecorder.Header().Get(boundaryCursorHeader) != "task-boundary-1" ||
		tasks.read.ExpectedActivityID != "activity-2" ||
		!strings.Contains(eventsRecorder.Body.String(), `"activity_id":"activity-2"`) ||
		!strings.Contains(eventsRecorder.Body.String(), `"sequence":"18446744073709551615"`) {
		t.Fatalf("Task events headers=%#v body=%s", eventsRecorder.Header(), eventsRecorder.Body.String())
	}
}

func TestTaskHTTPSubscribeParsesFollowQuery(t *testing.T) {
	subscription := newTaskTestSubscription(nil, eventstream.Envelope{
		Kind: eventstream.KindNotice, Cursor: "task-cursor-1", SessionID: "session-1", Notice: "live",
	})
	tasks := &fakeTaskService{subscribe: taskstream.SubscribeResult{
		Subscription: subscription, ResumeMode: taskstream.ResumeModeCurrentState,
	}}
	server := newTaskTestServer(t, tasks)
	request := httptest.NewRequest(
		http.MethodGet,
		apiPrefix+"/sessions/session-1/tasks/task-1/subscribe?follow=true",
		nil,
	)
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !tasks.request.Follow || tasks.request.SessionID != "session-1" || tasks.request.TaskID != "task-1" {
		t.Fatalf("Subscribe request = %#v, want Follow=true for exact Task target", tasks.request)
	}
}

func TestTaskHTTPDirectoryWatchStreamsReplaceableStatusSnapshots(t *testing.T) {
	subscription := newTaskTestDirectorySubscription(taskstream.DirectorySnapshot{
		Revision: math.MaxUint64,
		Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "child-1",
			Kind: task.KindSubagent, State: task.StateRunning, Running: true,
			ActivityID: "activity-2", CurrentTurnID: "task-1:2",
		}},
	})
	tasks := &fakeTaskDirectoryService{
		fakeTaskService: &fakeTaskService{},
		watch:           taskstream.DirectoryWatchResult{Subscription: subscription},
	}
	server := newTaskTestServer(t, tasks)
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/tasks/watch", nil)
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK ||
		!strings.Contains(body, "event: "+wirev1.TaskDirectorySnapshotEventName) ||
		!strings.Contains(body, `"revision":"18446744073709551615"`) ||
		!strings.Contains(body, `"activity_id":"activity-2"`) ||
		!strings.Contains(body, "event: "+wirev1.TaskStreamDoneEventName) {
		t.Fatalf("Task directory SSE status=%d body=%s", recorder.Code, body)
	}
	if tasks.principal.ID != "trusted-owner" || tasks.request.SessionID != "session-1" {
		t.Fatalf("Task directory principal/request = %#v/%#v", tasks.principal, tasks.request)
	}
}

func TestTaskHTTPSSEMarksCleanAndFailedSubscriptionEnds(t *testing.T) {
	for _, test := range []struct {
		name      string
		streamErr error
		wantEvent string
	}{
		{name: "clean", wantEvent: wirev1.TaskStreamDoneEventName},
		{name: "slow consumer", streamErr: taskstream.ErrSlowConsumer, wantEvent: wirev1.TaskStreamErrorEventName},
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
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.Tasks = tasks
	server, err := New(HandlerConfig{
		Services:      services,
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

type fakeTaskDirectoryService struct {
	*fakeTaskService
	watch   taskstream.DirectoryWatchResult
	request taskstream.DirectoryWatchRequest
}

func (s *fakeTaskDirectoryService) WatchDirectory(
	_ context.Context,
	principal taskstream.Principal,
	request taskstream.DirectoryWatchRequest,
) (taskstream.DirectoryWatchResult, error) {
	s.principal = principal
	s.request = request
	return s.watch, s.err
}

type taskTestDirectorySubscription struct {
	snapshots chan taskstream.DirectorySnapshot
}

func newTaskTestDirectorySubscription(snapshots ...taskstream.DirectorySnapshot) *taskTestDirectorySubscription {
	channel := make(chan taskstream.DirectorySnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		channel <- snapshot
	}
	close(channel)
	return &taskTestDirectorySubscription{snapshots: channel}
}

func (s *taskTestDirectorySubscription) Snapshots() <-chan taskstream.DirectorySnapshot {
	return s.snapshots
}
func (*taskTestDirectorySubscription) Close() error { return nil }
func (*taskTestDirectorySubscription) Err() error   { return nil }

var _ taskstream.DirectoryService = (*fakeTaskDirectoryService)(nil)
var _ taskstream.DirectorySubscription = (*taskTestDirectorySubscription)(nil)

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
