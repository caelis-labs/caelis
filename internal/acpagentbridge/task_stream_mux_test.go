package acpagentbridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	sdkstream "github.com/caelis-labs/caelis/agent-sdk/task/stream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

func TestACPTaskStreamMuxProjectsOnlyRunCommandTerminalOutput(t *testing.T) {
	t.Parallel()

	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 4)}
	service := &acpMuxTestService{
		requests: make(chan taskstream.SubscribeRequest, 1), sub: sub,
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RUN_COMMAND"},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "RUN_COMMAND",
	})
	mux.Observe(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": "command", "state": "running"}, Meta: meta,
		},
	})
	select {
	case request := <-service.requests:
		if request.SessionID != "session-1" || request.TaskID != "task-1" {
			t.Fatalf("Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand Task stream was not subscribed")
	}

	terminalMeta := metautil.WithTerminalOutput(nil, "command-1", "line\n")
	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Meta: terminalMeta},
	}
	select {
	case envelope := <-mux.Events():
		output, ok := metautil.TerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "line\n" {
			t.Fatalf("projected terminal output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand terminal output was not projected")
	}

	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		Update: schema.ToolCallUpdate{SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "nested", Meta: terminalMeta},
	}
	select {
	case envelope := <-mux.Events():
		t.Fatalf("subagent stream leaked into standard ACP: %#v", envelope)
	case <-time.After(30 * time.Millisecond):
	}
	exitCode := 7
	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			Meta: metautil.WithTerminalExit(nil, "command-1", &exitCode, nil),
		},
	}
	select {
	case envelope := <-mux.Events():
		exit, ok := metautil.TerminalExit(eventstream.UpdateMeta(envelope.Update))
		if !ok || exit.ExitCode == nil || *exit.ExitCode != exitCode {
			t.Fatalf("projected terminal exit = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand terminal exit without trailing output was not projected")
	}
	mux.Close()
	if !sub.closed() {
		t.Fatal("mux close did not close delivery subscription")
	}
}

func TestACPTaskStreamMuxDetachedDeliveryOutlivesParentPrompt(t *testing.T) {
	t.Parallel()

	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 2)}
	service := &acpMuxTestService{
		requests: make(chan taskstream.SubscribeRequest, 1), sub: sub,
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RUN_COMMAND"},
		}}},
	}
	agent := &RuntimeAgent{
		taskStreams: service, taskStreamPrincipal: taskstream.Principal{ID: "user-1"},
		taskMuxes: map[string]map[*acpTaskStreamMux]struct{}{},
	}
	mux := agent.startACPTaskStreamMux(context.Background(), "session-1")
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "RUN_COMMAND",
	})
	mux.Observe(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": "command", "state": "running"}, Meta: meta,
		},
	})
	select {
	case <-service.requests:
	case <-time.After(time.Second):
		t.Fatal("RunCommand Task stream was not subscribed before parent Prompt completion")
	}

	callbacks := &acpMuxPromptCallbacks{updates: make(chan acp.SessionNotification, 2)}
	agent.detachACPTaskStreamMux(context.Background(), mux, callbacks, "session-1", newACPNarrativeFilter(false))
	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			Meta: metautil.WithTerminalOutput(nil, "command-1", "after parent\n"),
		},
	}
	select {
	case notification := <-callbacks.updates:
		output, ok := metautil.TerminalOutput(eventstream.UpdateMeta(notification.Update))
		if !ok || output.Data != "after parent\n" {
			t.Fatalf("detached Task delivery = %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand output stopped when the parent Prompt completed")
	}
	exitCode := 0
	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			Meta: metautil.WithTerminalExit(nil, "command-1", &exitCode, nil),
		},
	}
	select {
	case notification := <-callbacks.updates:
		exit, ok := metautil.TerminalExit(eventstream.UpdateMeta(notification.Update))
		if !ok || exit.ExitCode == nil || *exit.ExitCode != exitCode {
			t.Fatalf("detached terminal exit = %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand terminal exit stopped when the parent Prompt completed")
	}

	agent.closeACPTaskStreamMuxes("session-1")
	if !sub.closed() {
		t.Fatal("Session close did not release detached Task delivery")
	}
}

func TestACPTaskStreamMuxProjectsControlTaskRecordThroughACPAdapter(t *testing.T) {
	t.Parallel()

	sub := &acpMuxControlSubscription{records: make(chan controltaskstream.Record, 2)}
	controlService := &acpMuxControlService{
		requests: make(chan controltaskstream.SubscribeRequest, 1), sub: sub,
		list: controltaskstream.ListResult{Tasks: []controltaskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: controltaskstream.ParentTool{ToolCallID: "command-1", ToolName: "RUN_COMMAND"},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), taskstream.New(controlService), taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "RUN_COMMAND",
	})
	mux.Observe(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": "command", "state": "running"}, Meta: meta,
		},
	})
	select {
	case request := <-controlService.requests:
		if request.SessionID != "session-1" || request.TaskID != "task-1" {
			t.Fatalf("Control Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand did not reach the Control Task stream")
	}
	sub.records <- controltaskstream.Record{
		Cursor: "cursor-1", Generation: "generation-1", Sequence: 1,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: controltaskstream.ParentTool{ToolCallID: "command-1", ToolName: "RUN_COMMAND"},
		},
		Frame: &sdkstream.Frame{
			Ref:  sdkstream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "terminal-1"},
			Text: "from control\n", Running: true, Cursor: sdkstream.Cursor{Events: 1, Output: 13},
		},
	}

	select {
	case envelope := <-mux.Events():
		output, ok := metautil.TerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "from control\n" || envelope.Cursor != "cursor-1" {
			t.Fatalf("Control→ACP→mux terminal output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("Control Task record did not reach the ACP terminal mux")
	}
}

func TestACPTaskStreamMuxMakesSubscribeFailureVisible(t *testing.T) {
	t.Parallel()

	service := &acpMuxTestService{
		err: errors.New("stream backend unavailable"),
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RUN_COMMAND"},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "RUN_COMMAND",
	})
	mux.Observe(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": "command", "state": "running"}, Meta: meta,
		},
	})

	select {
	case envelope := <-mux.Events():
		if envelope.Kind != eventstream.KindNotice || envelope.Delivery == nil || envelope.Delivery.Mode != eventstream.DeliveryTransient ||
			!strings.Contains(envelope.Notice, "stream backend unavailable") || !strings.Contains(envelope.Notice, "command") {
			t.Fatalf("subscribe failure envelope = %#v, want transient visible notice", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("Task stream subscribe failure was silent")
	}
}

func TestACPTaskStreamMuxRetriesAnchorAfterEarlyDirectoryMiss(t *testing.T) {
	t.Parallel()

	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	service := &acpMuxRetryService{
		requests: make(chan taskstream.SubscribeRequest, 1),
		sub:      sub,
		descriptor: taskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RUN_COMMAND"},
		},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "RUN_COMMAND",
	})
	anchor := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": "command", "state": "running"}, Meta: meta,
		},
	}

	mux.Observe(anchor)
	select {
	case envelope := <-mux.Events():
		if envelope.Kind != eventstream.KindNotice || !strings.Contains(envelope.Notice, "not discoverable yet") {
			t.Fatalf("early directory miss = %#v, want visible transient notice", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("early directory miss was not reported")
	}

	// A later canonical update for the same tool call must be allowed to retry.
	mux.Observe(anchor)
	select {
	case request := <-service.requests:
		if request.TaskID != "task-1" {
			t.Fatalf("retry Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("later anchor was permanently suppressed after directory miss")
	}
}

type acpMuxTestService struct {
	requests chan taskstream.SubscribeRequest
	sub      *acpMuxTestSubscription
	list     taskstream.ListResult
	err      error
}

type acpMuxRetryService struct {
	mu         sync.Mutex
	listCalls  int
	requests   chan taskstream.SubscribeRequest
	sub        *acpMuxTestSubscription
	descriptor taskstream.TaskDescriptor
}

type acpMuxPromptCallbacks struct {
	updates chan acp.SessionNotification
}

func (c *acpMuxPromptCallbacks) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	c.updates <- notification
	return nil
}

func (*acpMuxPromptCallbacks) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, nil
}

func (s *acpMuxRetryService) List(context.Context, taskstream.Principal, taskstream.ListRequest) (taskstream.ListResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	if s.listCalls == 1 {
		return taskstream.ListResult{}, nil
	}
	return taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{s.descriptor}}, nil
}

func (*acpMuxRetryService) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.Batch, error) {
	return taskstream.Batch{}, nil
}

func (s *acpMuxRetryService) Subscribe(_ context.Context, _ taskstream.Principal, request taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	s.requests <- request
	return taskstream.SubscribeResult{Subscription: s.sub, ResumeMode: taskstream.ResumeModeExact}, nil
}

func (s *acpMuxTestService) List(context.Context, taskstream.Principal, taskstream.ListRequest) (taskstream.ListResult, error) {
	return s.list, nil
}

func (s *acpMuxTestService) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.Batch, error) {
	return taskstream.Batch{}, nil
}

func (s *acpMuxTestService) Subscribe(_ context.Context, _ taskstream.Principal, request taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	if s.requests != nil {
		s.requests <- request
	}
	if s.err != nil {
		return taskstream.SubscribeResult{}, s.err
	}
	return taskstream.SubscribeResult{Subscription: s.sub, ResumeMode: taskstream.ResumeModeExact}, nil
}

type acpMuxTestSubscription struct {
	events chan eventstream.Envelope
	once   sync.Once
	mu     sync.Mutex
	done   bool
}

type acpMuxControlService struct {
	requests chan controltaskstream.SubscribeRequest
	sub      *acpMuxControlSubscription
	list     controltaskstream.ListResult
}

func (s *acpMuxControlService) List(context.Context, controltaskstream.Principal, controltaskstream.ListRequest) (controltaskstream.ListResult, error) {
	return s.list, nil
}
func (*acpMuxControlService) Events(context.Context, controltaskstream.Principal, controltaskstream.ReadRequest) (controltaskstream.Batch, error) {
	return controltaskstream.Batch{}, nil
}
func (s *acpMuxControlService) Subscribe(_ context.Context, _ controltaskstream.Principal, request controltaskstream.SubscribeRequest) (controltaskstream.SubscribeResult, error) {
	s.requests <- request
	return controltaskstream.SubscribeResult{Subscription: s.sub, ResumeMode: controltaskstream.ResumeModeExact}, nil
}

type acpMuxControlSubscription struct {
	records chan controltaskstream.Record
	once    sync.Once
}

func (s *acpMuxControlSubscription) Records() <-chan controltaskstream.Record { return s.records }
func (*acpMuxControlSubscription) Err() error                                 { return nil }
func (*acpMuxControlSubscription) LastCursor() string                         { return "" }
func (s *acpMuxControlSubscription) Close() error {
	s.once.Do(func() { close(s.records) })
	return nil
}

func (s *acpMuxTestSubscription) Events() <-chan eventstream.Envelope { return s.events }
func (s *acpMuxTestSubscription) Err() error                          { return nil }
func (s *acpMuxTestSubscription) LastCursor() string                  { return "" }
func (s *acpMuxTestSubscription) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.done = true
		s.mu.Unlock()
		close(s.events)
	})
	return nil
}
func (s *acpMuxTestSubscription) closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}
