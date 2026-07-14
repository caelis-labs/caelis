package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
)

func TestRuntimeRequiresControlContextRouterForExternalEndpoints(t *testing.T) {
	t.Parallel()

	sessions, _ := newTestSessionService(t, "context-router-required")
	_, err := New(Config{
		Sessions:                 sessions,
		AgentFactory:             chat.Factory{},
		Controllers:              stubACPController{},
		ControllerEventForwarder: testControllerForwarder{sessions: sessions},
	})
	if err == nil || !strings.Contains(err.Error(), "controller context router is required") {
		t.Fatalf("New() error = %v, want missing Control context router", err)
	}
	_, err = New(Config{
		Sessions:                 sessions,
		AgentFactory:             chat.Factory{},
		Controllers:              stubACPController{},
		ControllerContextRouter:  testContextRouter{sessions: sessions},
		ControllerEventForwarder: testControllerForwarder{sessions: sessions},
	})
	if err == nil || !strings.Contains(err.Error(), "controller recovery coordinator is required") {
		t.Fatalf("New() error = %v, want missing Control recovery coordinator", err)
	}
}

func TestRuntimeRunPersistsMinimalChatTurn(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "sess-1" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/project",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		RunIDGenerator: func() string { return "run-1" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: staticModel{text: "world"},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := result.Handle.RunID(); got != "run-1" {
		t.Fatalf("RunID() = %q, want %q", got, "run-1")
	}

	var count int
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil {
			count++
		}
	}
	if got, want := count, 2; got != want {
		t.Fatalf("runner event count = %d, want %d", got, want)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if got := session.EventText(loaded.Events[1]); got != "world" {
		t.Fatalf("assistant text = %q, want %q", got, "world")
	}

	state, err := runtime.RunState(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("RunState() error = %v", err)
	}
	if state.Status != agent.RunLifecycleStatusCompleted {
		t.Fatalf("state.Status = %q, want %q", state.Status, agent.RunLifecycleStatusCompleted)
	}
}

func TestRuntimeRejectsConcurrentRunForSameSession(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-concurrent-run")
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	blocking := &blockingTestAgent{started: make(chan struct{})}
	first, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "first",
		Agent:      blocking,
	})
	if err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not start")
	}
	_, err = runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "second",
		Agent:      blocking,
	})
	var conflict *agent.RunConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Run(second) error = %v, want *agentsdk.RunConflictError", err)
	}
	if conflict.ActiveRunID != first.Handle.RunID() {
		t.Fatalf("conflict.ActiveRunID = %q, want %q", conflict.ActiveRunID, first.Handle.RunID())
	}
	first.Handle.Cancel()
	for range first.Handle.Events() {
	}
}

type blockingTestAgent struct {
	started chan struct{}
}

func (a *blockingTestAgent) Name() string { return "blocking" }

func (a *blockingTestAgent) Run(ctx agent.Context) iter.Seq2[*session.Event, error] {
	return func(func(*session.Event, error) bool) {
		close(a.started)
		<-ctx.Done()
	}
}

func TestRuntimeRunPersistsDisplayInputSeparateFromModelInput(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "sess-display-input" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-display-input",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	var runNumber int
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		RunIDGenerator: func() string {
			runNumber++
			return fmt.Sprintf("run-display-input-%d", runNumber)
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	modelInput := "Load skill `cmpctl` before taking task actions, then follow its instructions.\n\nUser request:\narchive preflight"
	displayInput := "$cmpctl archive preflight"
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef:   activeSession.SessionRef,
		Input:        modelInput,
		DisplayInput: displayInput,
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: staticModel{text: "done"},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	user := loaded.Events[0]
	if user.Message == nil || user.Message.TextContent() != modelInput {
		t.Fatalf("user message text = %#v, want model input", user.Message)
	}
	if got := user.Text; got != displayInput {
		t.Fatalf("user display text = %q, want %q", got, displayInput)
	}
	if got := user.Meta["display_input"]; got != displayInput {
		t.Fatalf("user display_input metadata = %#v, want %q", got, displayInput)
	}
	summary, err := sessions.Session(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if summary.Title != displayInput {
		t.Fatalf("session title = %q, want display input %q", summary.Title, displayInput)
	}

	replayModel := &historyReplayModel{
		t:         t,
		wantTexts: []string{modelInput, "done", "follow-up"},
		replyText: "roundtrip ok",
	}
	result, err = runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "follow-up",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: replayModel,
		},
	})
	if err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("second runner error = %v", err)
	}
	if replayModel.calls != 1 {
		t.Fatalf("history replay model calls = %d, want 1", replayModel.calls)
	}
}

func TestRuntimePropagatesInvalidModelVisibleAppend(t *testing.T) {
	t.Parallel()

	baseSessions, activeSession := newTestSessionService(t, "sess-recover-invalid-append")
	sessions := &invalidAppendSessionService{
		Service:  baseSessions,
		failType: session.EventTypeAssistant,
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		RunIDGenerator: func() string { return "run-recover-invalid-append" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: staticModel{text: "world"},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events, err := drainRunnerEvents(t, result.Handle)
	if !errors.Is(err, session.ErrInvalidEvent) {
		t.Fatalf("runner error = %v, want ErrInvalidEvent; events=%#v", err, events)
	}
	state, err := runtime.RunState(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("RunState() error = %v", err)
	}
	if state.Status != agent.RunLifecycleStatusFailed {
		t.Fatalf("state.Status = %q, want %q", state.Status, agent.RunLifecycleStatusFailed)
	}
}

func TestRuntimeRecoversInvalidNonModelVisibleAppend(t *testing.T) {
	t.Parallel()

	baseSessions, activeSession := newTestSessionService(t, "sess-recover-invalid-plan-append")
	sessions := &invalidAppendSessionService{
		Service:  baseSessions,
		failType: session.EventTypePlan,
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		RunIDGenerator: func() string { return "run-recover-invalid-plan-append" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	persisted, err := runtime.appendRuntimeEventOrLifecycle(context.Background(), activeSession, activeSession.SessionRef, "turn-1", &session.Event{
		Type:       session.EventTypePlan,
		Visibility: session.VisibilityCanonical,
		PlanPayload: &session.EventPlanPayload{Entries: []session.EventPlanEntry{{
			Content: "keep going",
			Status:  "in_progress",
		}}},
	})
	if err != nil {
		t.Fatalf("appendRuntimeEventOrLifecycle() error = %v, want recovered lifecycle", err)
	}
	if persisted == nil || persisted.Type != session.EventTypeLifecycle || persisted.Lifecycle == nil {
		t.Fatalf("persisted = %#v, want lifecycle recovery event", persisted)
	}
	if persisted.Lifecycle.Status != "recovered" {
		t.Fatalf("lifecycle status = %q, want recovered", persisted.Lifecycle.Status)
	}
	if got, _ := persisted.Lifecycle.Meta["event_type"].(string); got != string(session.EventTypePlan) {
		t.Fatalf("lifecycle meta = %#v, want plan event_type", persisted.Lifecycle.Meta)
	}
}

func drainRunnerEvents(t *testing.T, handle agent.Runner) ([]*session.Event, error) {
	t.Helper()
	if handle == nil {
		return nil, nil
	}
	var events []*session.Event
	for event, seqErr := range handle.Events() {
		if seqErr != nil {
			return events, seqErr
		}
		if event != nil {
			events = append(events, event)
		}
	}
	return events, nil
}

type invalidAppendSessionService struct {
	session.Service
	failType session.EventType
	failed   bool
}

func (s *invalidAppendSessionService) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if s != nil && !s.failed && req.Event != nil && session.EventTypeOf(req.Event) == s.failType {
		s.failed = true
		return nil, &session.EventValidationError{Detail: "forced invalid event"}
	}
	return s.Service.AppendEvent(ctx, req)
}

func lastAssistantText(events []*session.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event != nil && event.Type == session.EventTypeAssistant {
			return strings.TrimSpace(session.EventText(event))
		}
	}
	return ""
}

func boolPtr(v bool) *bool { return &v }

func TestRuntimeRunReturnsLiveRunnerBeforeModelCompletion(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "sess-live" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-live",
			CWD: "/tmp/project",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	testModel := &gatedStreamingModel{
		started:      make(chan struct{}),
		releaseFinal: make(chan struct{}),
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		RunIDGenerator: func() string { return "run-live" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	type runResult struct {
		result agent.RunResult
		err    error
	}
	runDone := make(chan runResult, 1)
	go func() {
		result, err := runtime.Run(context.Background(), agent.RunRequest{
			SessionRef: activeSession.SessionRef,
			Input:      "hello",
			Request: agent.ModelRequestOptions{
				Stream: boolPtr(true),
			},
			AgentSpec: agent.AgentSpec{
				Name:  "chat",
				Model: testModel,
			},
		})
		runDone <- runResult{result: result, err: err}
	}()

	select {
	case <-testModel.started:
	case <-time.After(2 * time.Second):
		t.Fatal("model did not start")
	}

	var result agent.RunResult
	select {
	case got := <-runDone:
		if got.err != nil {
			t.Fatalf("Run() error = %v", got.err)
		}
		result = got.result
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Run() did not return before model completion")
	}

	state, err := runtime.RunState(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("RunState() error = %v", err)
	}
	if state.Status != agent.RunLifecycleStatusRunning {
		t.Fatalf("state.Status = %q, want %q while final response is gated", state.Status, agent.RunLifecycleStatusRunning)
	}

	eventCh := make(chan *session.Event, 8)
	errCh := make(chan error, 1)
	go func() {
		for event, seqErr := range result.Handle.Events() {
			if seqErr != nil {
				errCh <- seqErr
				return
			}
			eventCh <- event
		}
		close(eventCh)
	}()

	var sawUser bool
	var sawChunk bool
	deadline := time.After(2 * time.Second)
	for !sawUser || !sawChunk {
		select {
		case seqErr := <-errCh:
			t.Fatalf("runner error = %v", seqErr)
		case event := <-eventCh:
			if event == nil {
				t.Fatal("runner yielded nil event before final completion")
			}
			switch {
			case session.EventTypeOf(event) == session.EventTypeUser:
				sawUser = true
			case session.ProtocolSessionUpdateType(event) == string(session.ProtocolUpdateTypeAgentMessage) && session.EventText(event) == "hel":
				sawChunk = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for live user + chunk events (sawUser=%v sawChunk=%v)", sawUser, sawChunk)
		}
	}

	close(testModel.releaseFinal)

	var final *session.Event
	for event := range eventCh {
		if event != nil && session.EventTypeOf(event) == session.EventTypeAssistant && strings.TrimSpace(session.EventText(event)) == "hello" {
			final = event
		}
	}
	if final == nil {
		t.Fatal("final assistant event was not emitted")
	}

	state, err = runtime.RunState(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("RunState() after completion error = %v", err)
	}
	if state.Status != agent.RunLifecycleStatusCompleted {
		t.Fatalf("state.Status = %q, want %q after completion", state.Status, agent.RunLifecycleStatusCompleted)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d (chunk events must stay transient)", got, want)
	}
}

func TestRuntimeSubmitQueuesGuidanceForNextModelStep(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "sess-steer" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-steer",
			CWD: "/tmp/project",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	testModel := &steerRuntimeModel{
		started:      make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		RunIDGenerator: func() string { return "run-steer" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "first prompt",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	select {
	case <-testModel.started:
	case <-time.After(2 * time.Second):
		t.Fatal("model did not start")
	}

	if err := result.Handle.Submit(agent.Submission{
		Kind:         agent.SubmissionKindConversation,
		Text:         "steer next step",
		DisplayInput: "$cmpctl steer next step",
	}); err != nil {
		t.Fatalf("Submit() while running error = %v", err)
	}
	close(testModel.releaseFirst)

	events, err := drainRunnerEvents(t, result.Handle)
	if err != nil {
		t.Fatalf("runner error = %v", err)
	}
	gotTexts := make([]string, 0, len(events))
	for _, event := range events {
		if event != nil && (event.Type == session.EventTypeUser || event.Type == session.EventTypeAssistant) {
			gotTexts = append(gotTexts, session.EventText(event))
		}
	}
	wantTexts := []string{"first prompt", "first answer", "steer next step", "steered answer"}
	if !reflect.DeepEqual(gotTexts, wantTexts) {
		t.Fatalf("runner user/assistant texts = %#v, want %#v", gotTexts, wantTexts)
	}

	requests := testModel.Requests()
	if got, want := len(requests), 2; got != want {
		t.Fatalf("model request count = %d, want %d", got, want)
	}
	if got := requests[1].Messages[len(requests[1].Messages)-1].TextContent(); got != "steer next step" {
		t.Fatalf("second request last message = %q, want steer", got)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	gotPersisted := make([]string, 0, len(loaded.Events))
	for _, event := range loaded.Events {
		if event != nil && (event.Type == session.EventTypeUser || event.Type == session.EventTypeAssistant) {
			gotPersisted = append(gotPersisted, session.EventText(event))
		}
	}
	if !reflect.DeepEqual(gotPersisted, wantTexts) {
		t.Fatalf("persisted user/assistant texts = %#v, want %#v", gotPersisted, wantTexts)
	}
	for _, event := range loaded.Events {
		if event == nil || event.Message == nil || event.Message.TextContent() != "steer next step" {
			continue
		}
		if got := event.Text; got != "$cmpctl steer next step" {
			t.Fatalf("pending submission display text = %q, want original display text", got)
		}
	}
	if err := result.Handle.Submit(agent.Submission{Kind: agent.SubmissionKindConversation, Text: "too late"}); err == nil {
		t.Fatal("Submit() after runner completion error = nil, want closed-runner error")
	}
}

func TestRuntimeRunDoesNotPersistInterruptedAssistantReplay(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "sess-no-interrupted-replay" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-no-interrupted-replay",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	thoughtType := string(session.ProtocolUpdateTypeAgentThought)
	thought := session.MarkUIOnly(&session.Event{
		Type: session.EventTypeAssistant,
		Text: "partial thought",
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: thoughtType,
				Content:       session.ProtocolTextContent("partial thought"),
			},
		},
	})
	answerType := string(session.ProtocolUpdateTypeAgentMessage)
	chunk := session.MarkUIOnly(&session.Event{
		Type: session.EventTypeAssistant,
		Text: "partial answer",
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: answerType,
				Content:       session.ProtocolTextContent("partial answer"),
			},
		},
	})
	factory := &attemptFactory{agents: []agent.Agent{seqAgent{
		events: []*session.Event{thought, chunk},
		err:    context.Canceled,
	}}}
	runtime, err := New(Config{
		Sessions:       sessions,
		AgentFactory:   factory,
		RunIDGenerator: func() string { return "run-no-interrupted-replay" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		AgentSpec:  agent.AgentSpec{Name: "seq"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, seqErr := drainRunnerEvents(t, result.Handle); !errors.Is(seqErr, context.Canceled) {
		t.Fatalf("runner error = %v, want context canceled", seqErr)
	}

	history, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(history), 1; got != want {
		t.Fatalf("canonical history event count = %d, want %d", got, want)
	}

	transcript, err := sessions.Events(context.Background(), session.EventsRequest{
		SessionRef:       activeSession.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatalf("Events(include transient) error = %v", err)
	}
	for _, event := range transcript {
		if session.EventTypeOf(event) == session.EventTypeAssistant && event.Visibility == session.VisibilityMirror {
			t.Fatalf("found unexpected VisibilityMirror event in transcript: %#v", event)
		}
	}
}

func TestRuntimeACPControllerReturnsLiveRunnerBeforeTurnCompletion(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "sess-acp-live" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-acp-live",
			CWD: "/tmp/project",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	activeSession, err = sessions.BindController(context.Background(), session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:         session.ControllerKindACP,
			ControllerID: "acp-main",
			Label:        "ACP Main",
			EpochID:      "epoch-live",
			Source:       "test",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}

	releaseFinal := make(chan struct{})
	streamSeen := make(chan bool, 1)
	testController := stubACPController{
		runTurn: func(ctx context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
			streamSeen <- req.Stream
			handle := newTestControllerTurnHandle(nil)
			go func() {
				handle.publishEvent(session.MarkUIOnly(&session.Event{
					Type: session.EventTypeAssistant,
					Text: "hel",
					Protocol: &session.EventProtocol{
						Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage)},
					},
				}))
				<-releaseFinal
				event := assistantEvent("hello")
				event.Protocol = &session.EventProtocol{
					Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage)},
				}
				handle.publishEvent(event)
				handle.finish()
			}()
			return controller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  testController,
		RunIDGenerator: func() string {
			return "run-acp-live"
		},
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	type runResult struct {
		result agent.RunResult
		err    error
	}
	runDone := make(chan runResult, 1)
	go func() {
		result, err := runtime.Run(context.Background(), agent.RunRequest{
			SessionRef: activeSession.SessionRef,
			Input:      "hello",
			Request: agent.ModelRequestOptions{
				Stream: boolPtr(true),
			},
		})
		runDone <- runResult{result: result, err: err}
	}()

	var result agent.RunResult
	select {
	case got := <-runDone:
		if got.err != nil {
			t.Fatalf("Run() error = %v", got.err)
		}
		result = got.result
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Run() did not return before ACP final completion")
	}
	select {
	case stream := <-streamSeen:
		if !stream {
			t.Fatal("TurnRequest.Stream = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not observe stream request")
	}

	eventCh := make(chan *session.Event, 8)
	errCh := make(chan error, 1)
	go func() {
		for event, seqErr := range result.Handle.Events() {
			if seqErr != nil {
				errCh <- seqErr
				return
			}
			eventCh <- event
		}
		close(eventCh)
	}()

	var sawUser bool
	var sawChunk bool
	deadline := time.After(2 * time.Second)
	for !sawUser || !sawChunk {
		select {
		case seqErr := <-errCh:
			t.Fatalf("runner error = %v", seqErr)
		case event := <-eventCh:
			if event == nil {
				t.Fatal("runner yielded nil event before final completion")
			}
			switch {
			case session.EventTypeOf(event) == session.EventTypeUser:
				sawUser = true
			case session.ProtocolSessionUpdateType(event) == string(session.ProtocolUpdateTypeAgentMessage) && event.Visibility == session.VisibilityUIOnly:
				sawChunk = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for live ACP user + chunk events (sawUser=%v sawChunk=%v)", sawUser, sawChunk)
		}
	}

	close(releaseFinal)

	var final *session.Event
	for event := range eventCh {
		if event != nil && session.EventTypeOf(event) == session.EventTypeAssistant && strings.TrimSpace(session.EventText(event)) == "hello" {
			final = event
		}
	}
	if final == nil {
		t.Fatal("final ACP assistant event was not emitted")
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if loaded.Events[1].Visibility != session.VisibilityCanonical || session.EventText(loaded.Events[1]) != "hello" {
		t.Fatalf("loaded final event = %+v, want canonical assistant hello", loaded.Events[1])
	}
}

func TestRuntimeACPControllerTurnSendsUnsyncedSharedDialogue(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-acp-shared-delta-turn")
	if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event:      userTextEvent("already synced"),
	}); err != nil {
		t.Fatalf("AppendEvent(initial) error = %v", err)
	}
	activeSession, err := sessions.BindController(context.Background(), session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:           session.ControllerKindACP,
			ControllerID:   "acp-main",
			Label:          "ACP Main",
			EpochID:        "epoch-shared-delta",
			Source:         "test",
			ContextSyncSeq: 1,
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	sideEvent := assistantEvent("side result")
	sideEvent.Actor = session.ActorRef{Kind: session.ActorKindParticipant, Name: "jeff"}
	sideEvent.Scope = &session.EventScope{
		Participant: session.ParticipantRef{
			ID:   "side-1",
			Kind: session.ParticipantKindSubagent,
			Role: session.ParticipantRoleSidecar,
		},
	}
	if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event:      sideEvent,
	}); err != nil {
		t.Fatalf("AppendEvent(side) error = %v", err)
	}

	turnReqCh := make(chan controller.TurnRequest, 1)
	testController := stubACPController{
		runTurn: func(ctx context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
			turnReqCh <- req
			handle := newTestControllerTurnHandle(nil)
			go func() {
				event := assistantEvent("main done")
				event.Protocol = &session.EventProtocol{
					Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage)},
				}
				handle.publishEvent(event)
				handle.finish()
			}()
			return controller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  testController,
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "next prompt",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("drain runner: %v", err)
	}
	var turnReq controller.TurnRequest
	select {
	case turnReq = <-turnReqCh:
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not receive RunTurn request")
	}
	if turnReq.ContextSyncSeq != 2 {
		t.Fatalf("ContextSyncSeq = %d, want checkpoint 2", turnReq.ContextSyncSeq)
	}
	if !strings.Contains(turnReq.ContextPrelude, "side result") || !strings.Contains(turnReq.ContextPrelude, "shared_dialogue_delta:") {
		t.Fatalf("ContextPrelude = %q, want unsynced side dialogue", turnReq.ContextPrelude)
	}
	if strings.Contains(turnReq.ContextPrelude, "next prompt") {
		t.Fatalf("ContextPrelude = %q, should not duplicate current user prompt", turnReq.ContextPrelude)
	}
	updated, err := sessions.Session(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if updated.Controller.ContextSyncSeq < 4 {
		t.Fatalf("controller ContextSyncSeq = %d, want current shared ledger checkpoint", updated.Controller.ContextSyncSeq)
	}
}

func TestRuntimePromptParticipantPersistsPublicDialogue(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-acp-side-dialogue")
	activeSession, err := sessions.PutParticipant(context.Background(), session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:        "emma",
			Kind:      session.ParticipantKindACP,
			Role:      session.ParticipantRoleSidecar,
			Label:     "@emma",
			AgentName: "claude",
			Source:    "tui_agent_add",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}
	lease, err := sessions.(session.SessionLeaseService).AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: activeSession.SessionRef,
		OwnerID:    "participant-turn-owner",
		TTL:        time.Minute,
	})
	if err != nil {
		t.Fatalf("AcquireSessionLease() error = %v", err)
	}
	turnCtx := session.ContextWithRuntimeLease(context.Background(), lease)
	turnReqCh := make(chan controller.ParticipantPromptRequest, 1)
	testController := stubACPController{
		promptParticipant: func(ctx context.Context, req controller.ParticipantPromptRequest) (controller.TurnResult, error) {
			turnReqCh <- req
			handle := newTestControllerTurnHandle(nil)
			go func() {
				defer handle.finish()
				handle.publishEvent(&session.Event{
					Type:       session.EventTypeUser,
					Visibility: session.VisibilityCanonical,
					Text:       req.Input,
					Scope: &session.EventScope{
						Source: "acp_participant",
						Participant: session.ParticipantRef{
							ID:   req.ParticipantID,
							Kind: session.ParticipantKindACP,
							Role: session.ParticipantRoleSidecar,
						},
					},
					Protocol: &session.EventProtocol{
						Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeUserMessage)},
					},
				})
				handle.publishEvent(&session.Event{
					Type:       session.EventTypeToolCall,
					Visibility: session.VisibilityUIOnly,
					Text:       "running external command",
					Actor:      session.ActorRef{Kind: session.ActorKindParticipant, ID: "emma", Name: "@emma"},
					Scope: &session.EventScope{
						Source: "acp_participant",
						Participant: session.ParticipantRef{
							ID:   req.ParticipantID,
							Kind: session.ParticipantKindACP,
							Role: session.ParticipantRoleSidecar,
						},
					},
					Protocol: &session.EventProtocol{
						Update: &session.ProtocolUpdate{
							SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
							ToolCallID:    "external-command",
							Kind:          "RUN_COMMAND",
							Status:        "completed",
						},
					},
				})
				handle.publishEvent(&session.Event{
					Type:       session.EventTypeAssistant,
					Visibility: session.VisibilityUIOnly,
					Text:       "emma summary",
					Actor:      session.ActorRef{Kind: session.ActorKindParticipant, ID: "emma", Name: "@emma"},
					Scope: &session.EventScope{
						Source: "acp_participant",
						Participant: session.ParticipantRef{
							ID:   req.ParticipantID,
							Kind: session.ParticipantKindACP,
							Role: session.ParticipantRoleSidecar,
						},
					},
					Protocol: &session.EventProtocol{
						Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage)},
					},
				})
			}()
			return controller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  testController,
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	updated, err := runtime.PromptParticipant(turnCtx, agent.PromptParticipantRequest{
		SessionRef:    activeSession.SessionRef,
		ParticipantID: "emma",
		Input:         "刚才都做了什么？总结一下",
		Source:        "tui_agent_ask",
	})
	if err != nil {
		t.Fatalf("PromptParticipant() error = %v", err)
	}
	select {
	case req := <-turnReqCh:
		if req.TurnID == "" {
			t.Fatal("participant prompt TurnID is empty")
		}
		if strings.Contains(req.ContextPrelude, "current_user_request") || strings.Contains(req.ContextPrelude, req.Input) {
			t.Fatalf("participant context prelude duplicated current prompt:\n%s", req.ContextPrelude)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("participant prompt request was not sent")
	}
	if updated.Handle != nil {
		for range updated.Handle.Events() {
		}
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: updated.Session.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var sideUsers, sideAssistants int
	for _, event := range loaded.Events {
		if event == nil || event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) != "emma" {
			continue
		}
		if session.EventTypeOf(event) == session.EventTypeToolCall {
			t.Fatalf("external ACP process event was persisted into main session: %#v", event)
		}
		switch session.EventTypeOf(event) {
		case session.EventTypeUser:
			sideUsers++
			if got := strings.TrimSpace(session.EventText(event)); got != "刚才都做了什么？总结一下" {
				t.Fatalf("side user text = %q", got)
			}
			if !session.IsMainInvocationVisibleEvent(event) {
				t.Fatalf("side user event is not visible to main invocation: %#v", event)
			}
		case session.EventTypeAssistant:
			sideAssistants++
			if got := strings.TrimSpace(session.EventText(event)); got != "emma summary" {
				t.Fatalf("side assistant text = %q", got)
			}
			if !session.IsMainInvocationVisibleEvent(event) {
				t.Fatalf("side assistant event is not visible to main invocation: %#v", event)
			}
		}
	}
	if sideUsers != 1 {
		t.Fatalf("side user event count = %d, want one local public prompt and no ACP echo duplicate", sideUsers)
	}
	if sideAssistants != 1 {
		t.Fatalf("side assistant event count = %d, want one final side answer", sideAssistants)
	}
}

func TestRuntimePromptParticipantRehydratesPersistedBinding(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-acp-side-rehydrate")
	activeSession, err := sessions.PutParticipant(context.Background(), session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:             "codex-3",
			Kind:           session.ParticipantKindACP,
			Role:           session.ParticipantRoleSidecar,
			Label:          "@tova",
			AgentName:      "tova",
			SessionID:      "remote-old",
			Source:         "tui_agent_add",
			ContextSyncSeq: 4,
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}
	attachReqCh := make(chan controller.AttachRequest, 1)
	promptReqCh := make(chan controller.ParticipantPromptRequest, 1)
	testController := stubACPController{
		attach: func(ctx context.Context, req controller.AttachRequest) (session.ParticipantBinding, error) {
			_ = ctx
			attachReqCh <- req
			binding := session.CloneParticipantBinding(req.Binding)
			binding.SessionID = "remote-new"
			binding.ContextSyncSeq = 0
			return binding, nil
		},
		promptParticipant: func(ctx context.Context, req controller.ParticipantPromptRequest) (controller.TurnResult, error) {
			_ = ctx
			promptReqCh <- req
			handle := newTestControllerTurnHandle(nil)
			handle.finish()
			return controller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  testController,
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.PromptParticipant(context.Background(), agent.PromptParticipantRequest{
		SessionRef:    activeSession.SessionRef,
		ParticipantID: "codex-3",
		Input:         "please inspect the local diff",
		Source:        "tui_agent_ask",
	})
	if err != nil {
		t.Fatalf("PromptParticipant() error = %v", err)
	}
	select {
	case req := <-attachReqCh:
		if req.Binding.ID != "codex-3" || req.Binding.SessionID != "remote-old" || req.Agent != "tova" {
			t.Fatalf("Attach request = %#v, want persisted @tova binding", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("participant rehydrate attach request was not sent")
	}
	select {
	case req := <-promptReqCh:
		if req.ParticipantID != "codex-3" {
			t.Fatalf("ParticipantID = %q, want codex-3", req.ParticipantID)
		}
		if got := req.Session.Participants[0].SessionID; got != "remote-new" {
			t.Fatalf("prompt session participant remote = %q, want remote-new", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("participant prompt request was not sent")
	}
	if result.Handle != nil {
		for range result.Handle.Events() {
		}
	}
	updated, err := sessions.Session(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	binding, ok := participantBinding(updated, "codex-3")
	if !ok {
		t.Fatal("participant codex-3 missing after prompt")
	}
	if binding.SessionID != "remote-new" {
		t.Fatalf("persisted participant remote = %q, want remote-new", binding.SessionID)
	}
}

func TestRuntimePromptParticipantCancelCancelsControllerTurn(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-acp-side-cancel")
	activeSession, err := sessions.PutParticipant(context.Background(), session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:        "emma",
			Kind:      session.ParticipantKindACP,
			Role:      session.ParticipantRoleSidecar,
			Label:     "@emma",
			AgentName: "claude",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}
	controllerCancelled := make(chan struct{})
	controllerHandle := newTestControllerTurnHandle(func() {
		close(controllerCancelled)
	})
	turnReqCh := make(chan controller.ParticipantPromptRequest, 1)
	testController := stubACPController{
		promptParticipant: func(ctx context.Context, req controller.ParticipantPromptRequest) (controller.TurnResult, error) {
			_ = ctx
			turnReqCh <- req
			return controller.TurnResult{Handle: controllerHandle}, nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  testController,
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.PromptParticipant(context.Background(), agent.PromptParticipantRequest{
		SessionRef:    activeSession.SessionRef,
		ParticipantID: "emma",
		Input:         "stop me",
		Source:        "slash_claude",
	})
	if err != nil {
		t.Fatalf("PromptParticipant() error = %v", err)
	}
	select {
	case <-turnReqCh:
	case <-time.After(2 * time.Second):
		t.Fatal("participant prompt request was not sent")
	}
	if result.Handle == nil {
		t.Fatal("PromptParticipant() handle = nil")
	}
	if !result.Handle.Cancel().Cancelled() {
		t.Fatal("participant handle Cancel().Cancelled() = false, want true")
	}
	select {
	case <-controllerCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("participant handle cancel did not cancel controller turn")
	}
	controllerHandle.finish()
	for range result.Handle.Events() {
	}
}

func TestRuntimeACPControllerPublishesChunksAsLiveDeltas(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "sess-acp-deltas" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-acp-deltas",
			CWD: "/tmp/project",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	activeSession, err = sessions.BindController(context.Background(), session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:         session.ControllerKindACP,
			ControllerID: "acp-main",
			Label:        "ACP Main",
			EpochID:      "epoch-delta",
			Source:       "test",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}

	testController := stubACPController{
		runTurn: func(context.Context, controller.TurnRequest) (controller.TurnResult, error) {
			handle := newTestControllerTurnHandle(nil)
			go func() {
				handle.publishEvent(acpControllerChunk("hel"))
				handle.publishEvent(&session.Event{
					Type:       session.EventTypeToolCall,
					Visibility: session.VisibilityUIOnly,
					Text:       "external search",
					Scope: &session.EventScope{
						Source: "acp",
					},
					Protocol: &session.EventProtocol{
						Update: &session.ProtocolUpdate{
							SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
							ToolCallID:    "external-search",
							Kind:          "Search",
							Status:        "completed",
						},
					},
				})
				handle.publishEvent(acpControllerChunk("hello"))
				handle.finish()
			}()
			return controller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:       sessions,
		AgentFactory:   chat.Factory{SystemPrompt: "Be terse."},
		Controllers:    testController,
		RunIDGenerator: func() string { return "run-acp-deltas" },
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	lease, err := sessions.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: activeSession.SessionRef,
		OwnerID:    "acp-controller-turn-owner",
		TTL:        time.Minute,
	})
	if err != nil {
		t.Fatalf("AcquireSessionLease() error = %v", err)
	}
	runCtx := session.ContextWithRuntimeLease(context.Background(), lease)

	result, err := runtime.Run(runCtx, agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		Request: agent.ModelRequestOptions{
			Stream: boolPtr(true),
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events, err := drainRunnerEvents(t, result.Handle)
	if err != nil {
		t.Fatalf("runner error = %v", err)
	}
	var liveTexts []string
	for _, event := range events {
		if event == nil || event.Protocol == nil || event.Scope == nil {
			continue
		}
		if session.ProtocolSessionUpdateType(event) == string(session.ProtocolUpdateTypeAgentMessage) && strings.HasPrefix(event.Scope.Source, "acp") && event.Visibility == session.VisibilityUIOnly {
			liveTexts = append(liveTexts, session.EventText(event))
			if event.SessionID != activeSession.SessionID {
				t.Fatalf("live ACP chunk session ID = %q, want %q", event.SessionID, activeSession.SessionID)
			}
			if strings.TrimSpace(event.ID) != "" {
				t.Fatalf("live ACP chunk ID = %q, want empty live event ID", event.ID)
			}
		}
	}
	if !reflect.DeepEqual(liveTexts, []string{"hel", "hello"}) {
		t.Fatalf("live ACP texts = %#v, want assistant step chunks", liveTexts)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var persistedTexts []string
	for _, event := range loaded.Events {
		if event == nil || event.Scope == nil {
			continue
		}
		if session.EventTypeOf(event) == session.EventTypeToolCall {
			t.Fatalf("persisted external ACP process event: %#v", event)
		}
		if session.EventTypeOf(event) == session.EventTypeAssistant && strings.HasPrefix(event.Scope.Source, "acp") {
			persistedTexts = append(persistedTexts, session.EventText(event))
			if strings.TrimSpace(event.ID) == "" {
				t.Fatalf("persisted ACP chunk missing event ID")
			}
		}
	}
	if !reflect.DeepEqual(persistedTexts, []string{"hello"}) {
		t.Fatalf("persisted ACP texts = %#v, want final assistant step only", persistedTexts)
	}
}

func TestRuntimeACPControllerHonorsRequestedStreamMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		request agent.ModelRequestOptions
		want    bool
	}{
		{name: "default false", request: agent.ModelRequestOptions{}, want: false},
		{name: "explicit false", request: agent.ModelRequestOptions{Stream: boolPtr(false)}, want: false},
		{name: "explicit true", request: agent.ModelRequestOptions{Stream: boolPtr(true)}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sessions, activeSession := newTestSessionService(t, "sess-acp-stream-mode")
			activeSession, err := sessions.BindController(context.Background(), session.BindControllerRequest{
				SessionRef: activeSession.SessionRef,
				Binding: session.ControllerBinding{
					Kind:         session.ControllerKindACP,
					ControllerID: "acp-main",
					Label:        "ACP Main",
					EpochID:      "epoch-stream",
					Source:       "test",
				},
			})
			if err != nil {
				t.Fatalf("BindController() error = %v", err)
			}
			streamSeen := make(chan bool, 1)
			testController := stubACPController{
				runTurn: func(_ context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
					streamSeen <- req.Stream
					handle := newTestControllerTurnHandle(nil)
					handle.finish()
					return controller.TurnResult{Handle: handle}, nil
				},
			}
			runtime, err := New(testConfigWithACPForwarder(Config{
				Sessions:       sessions,
				AgentFactory:   chat.Factory{SystemPrompt: "Be terse."},
				Controllers:    testController,
				RunIDGenerator: func() string { return "run-acp-stream-mode" },
			}))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			result, err := runtime.Run(context.Background(), agent.RunRequest{
				SessionRef: activeSession.SessionRef,
				Input:      "hello",
				Request:    tc.request,
			})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			for range result.Handle.Events() {
			}
			select {
			case got := <-streamSeen:
				if got != tc.want {
					t.Fatalf("controller stream = %v, want %v", got, tc.want)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for controller stream flag")
			}
		})
	}
}

func TestRuntimeACPControllerInterruptedTurnDoesNotPersistLocalReplaySnapshot(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-acp-interrupted-no-local-replay")
	activeSession, err := sessions.BindController(context.Background(), session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:            session.ControllerKindACP,
			ControllerID:    "acp-main",
			AgentName:       "codex",
			Label:           "ACP Main",
			RemoteSessionID: "remote-main",
			EpochID:         "epoch-interrupted",
			Source:          "test",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	testController := stubACPController{
		runTurn: func(context.Context, controller.TurnRequest) (controller.TurnResult, error) {
			handle := newTestControllerTurnHandle(nil)
			go func() {
				handle.publishEvent(acpControllerChunk("partial answer"))
				handle.publishError(errors.New("remote stream interrupted"))
				handle.finish()
			}()
			return controller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  testController,
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "resume me later",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err == nil {
		t.Fatal("runner error = nil, want interrupted external ACP turn")
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	for _, event := range loaded.Events {
		if session.EventTypeOf(event) == session.EventTypeAssistant {
			t.Fatalf("external ACP interrupted assistant replay was persisted locally: %#v", event)
		}
	}
	if got, want := len(loaded.Events), 1; got != want {
		t.Fatalf("len(loaded.Events) = %d, want only the user prompt", got)
	}
}

func TestRuntimeRunReplaysPersistedHistoryFromFileStore(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-file-replay" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-file-replay",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	runtime1, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		RunIDGenerator: func() string { return "run-1" },
	})
	if err != nil {
		t.Fatalf("New(runtime1) error = %v", err)
	}

	result1, err := runtime1.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: staticModel{text: "world"},
		},
	})
	if err != nil {
		t.Fatalf("runtime1.Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result1.Handle); err != nil {
		t.Fatalf("runtime1 runner error = %v", err)
	}

	reopenedSessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	runtime2, err := New(Config{
		Sessions: reopenedSessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		RunIDGenerator: func() string { return "run-2" },
	})
	if err != nil {
		t.Fatalf("New(runtime2) error = %v", err)
	}

	replayModel := &historyReplayModel{
		t:         t,
		wantTexts: []string{"hello", "world", "again"},
		replyText: "history ok",
	}
	result, err := runtime2.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "again",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: replayModel,
		},
	})
	if err != nil {
		t.Fatalf("runtime2.Run() error = %v", err)
	}

	events, seqErr := drainRunnerEvents(t, result.Handle)
	if seqErr != nil {
		t.Fatalf("runner error = %v", seqErr)
	}
	finalText := lastAssistantText(events)
	if finalText != "history ok" {
		t.Fatalf("final assistant text = %q, want %q", finalText, "history ok")
	}
	if replayModel.calls != 1 {
		t.Fatalf("history replay model calls = %d, want %d", replayModel.calls, 1)
	}

	loaded, err := reopenedSessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 4; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if got := session.EventText(loaded.Events[3]); got != "history ok" {
		t.Fatalf("assistant replay text = %q, want %q", got, "history ok")
	}
}

func TestRuntimeRecoveryInterruptsOrphanedCommandTask(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workdir := t.TempDir()
	sessionStore := sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-orphan-command" },
	})
	sessions := sessionfile.NewService(sessionStore)
	tasks := sessionfile.NewTaskStore(sessionStore)
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: workdir,
			CWD: workdir,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	runtime1, err := New(Config{
		Sessions:  sessions,
		TaskStore: tasks,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
	})
	if err != nil {
		t.Fatalf("New(runtime1) error = %v", err)
	}
	snapshot, err := runtime1.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, hostRuntimeForTest(t, workdir), taskapi.CommandStartRequest{
		Command:    shellSleepThenPrintForTest("late output", 5*time.Second),
		Workdir:    workdir,
		Yield:      5 * time.Millisecond,
		ParentCall: "command-1",
		ParentTool: shell.RunCommandToolName,
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	if !snapshot.Running {
		t.Fatalf("snapshot.Running = %v, want true", snapshot.Running)
	}
	t.Cleanup(func() {
		runtime1.tasks.mu.RLock()
		task := runtime1.tasks.tasks[snapshot.Ref.TaskID]
		runtime1.tasks.mu.RUnlock()
		if task != nil && task.session != nil {
			_ = task.session.Terminate(context.Background())
		}
	})

	reopenedStore := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	reopenedSessions := sessionfile.NewService(reopenedStore)
	runtime2, err := New(Config{
		Sessions:  reopenedSessions,
		TaskStore: sessionfile.NewTaskStore(reopenedStore),
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{Enabled: true, WatermarkRatio: 0.8, ForceWatermarkRatio: 0.9, DefaultContextWindowTokens: 4096},
	})
	if err != nil {
		t.Fatalf("New(runtime2) error = %v", err)
	}
	result2, err := runtime2.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "resume after orphaned task",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: staticModel{text: "ok"},
		},
	})
	if err != nil {
		t.Fatalf("runtime2.Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result2.Handle); err != nil {
		t.Fatalf("runtime2 runner error = %v", err)
	}

	entry, err := tasks.Get(context.Background(), snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("tasks.Get() error = %v", err)
	}
	if entry == nil {
		t.Fatal("tasks.Get() returned nil entry")
		return
	}
	if entry.Running {
		t.Fatalf("entry.Running = %v, want false", entry.Running)
	}
	if entry.State != taskapi.StateInterrupted {
		t.Fatalf("entry.State = %q, want %q", entry.State, taskapi.StateInterrupted)
	}
	if got, _ := entry.Result["result"].(string); !strings.Contains(got, "interrupted during resume") {
		t.Fatalf("entry.Result[result] = %q, want interrupted summary", got)
	}
}

func TestRuntimeRunDoesNotRetryAgentLoopBeforeAnyEventIsEmitted(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-retry")
	factory := &attemptFactory{
		agents: []agent.Agent{
			seqAgent{err: errors.New("model: http status 529 body={\"error\":\"overloaded_error\"}")},
		},
	}
	runtime, err := New(Config{
		Sessions:       sessions,
		AgentFactory:   factory,
		RunIDGenerator: func() string { return "run-retry" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		AgentSpec:  agent.AgentSpec{Name: "chat"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	events, seqErr := drainRunnerEvents(t, result.Handle)
	if seqErr == nil {
		t.Fatal("runner error = nil, want model failure")
	}
	if !strings.Contains(seqErr.Error(), "overloaded_error") {
		t.Fatalf("runner error = %v, want original model failure", seqErr)
	}
	for _, event := range events {
		if session.IsNotice(event) {
			t.Fatalf("unexpected retry notice event: %q", session.EventText(event))
		}
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("runner event count = %d, want %d", got, want)
	}
	if got, want := factory.Calls(), 1; got != want {
		t.Fatalf("factory calls = %d, want %d", got, want)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 1; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	for _, event := range loaded.Events {
		if session.IsNotice(event) {
			t.Fatal("retry notice must not be persisted")
		}
	}

	state, stateErr := runtime.RunState(context.Background(), activeSession.SessionRef)
	if stateErr != nil {
		t.Fatalf("RunState() error = %v", stateErr)
	}
	if state.Status != agent.RunLifecycleStatusFailed {
		t.Fatalf("state.Status = %q, want %q", state.Status, agent.RunLifecycleStatusFailed)
	}
}

func TestRuntimeRunDoesNotRetryAfterAnyEventIsEmitted(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-no-retry")
	factory := &attemptFactory{
		agents: []agent.Agent{
			seqAgent{
				events: []*session.Event{assistantEvent("partial")},
				err:    errors.New("model stream interrupted"),
			},
			seqAgent{events: []*session.Event{assistantEvent("should-not-run")}},
		},
	}
	runtime, err := New(Config{
		Sessions:       sessions,
		AgentFactory:   factory,
		RunIDGenerator: func() string { return "run-no-retry" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		AgentSpec:  agent.AgentSpec{Name: "chat"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, seqErr := drainRunnerEvents(t, result.Handle); seqErr == nil {
		t.Fatal("runner error = nil, want failure")
	}
	if got, want := factory.Calls(), 1; got != want {
		t.Fatalf("factory calls = %d, want %d", got, want)
	}

	loaded, loadErr := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if loadErr != nil {
		t.Fatalf("LoadSession() error = %v", loadErr)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if got := session.EventText(loaded.Events[1]); got != "partial" {
		t.Fatalf("assistant text = %q, want %q", got, "partial")
	}

	state, stateErr := runtime.RunState(context.Background(), activeSession.SessionRef)
	if stateErr != nil {
		t.Fatalf("RunState() error = %v", stateErr)
	}
	if state.Status != agent.RunLifecycleStatusFailed {
		t.Fatalf("state.Status = %q, want %q", state.Status, agent.RunLifecycleStatusFailed)
	}
}

func TestRuntimeRunPersistsToolLoopEvents(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-tools")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		PolicyRegistry: staticPolicyRegistry{mode: policy.NamedMode{
			ID: "allow",
			Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
				return policy.Decision{Action: policy.ActionAllow}, nil
			},
		}},
		DefaultPolicyMode: "allow",
		RunIDGenerator:    func() string { return "run-tools" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	testModel := &toolLoopRuntimeModel{}
	targetTool := tool.NamedTool{
		Def: tool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []model.Part{
					model.NewJSONPart([]byte(`{"value":"pong"}`)),
				},
			}, nil
		},
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "say pong",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
			Tools: []tool.Tool{targetTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var count int
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil {
			count++
		}
	}
	if got, want := count, 4; got != want {
		t.Fatalf("runner event count = %d, want %d", got, want)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 4; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if loaded.Events[1].Type != session.EventTypeToolCall {
		t.Fatalf("loaded.Events[1].Type = %q, want tool_call", loaded.Events[1].Type)
	}
	toolCallMessage, ok := session.ModelMessageOf(loaded.Events[1])
	if !ok || len(toolCallMessage.ToolCalls()) != 1 {
		t.Fatalf("ModelMessageOf(loaded.Events[1]) = %+v, %v; want durable tool call message projection", toolCallMessage, ok)
	}
	if loaded.Events[2].Type != session.EventTypeToolResult {
		t.Fatalf("loaded.Events[2].Type = %q, want tool_result", loaded.Events[2].Type)
	}
	toolResultPayload := session.EventToolProjection(loaded.Events[2])
	if toolResultPayload == nil || toolResultPayload.Name != "ECHO" || toolResultPayload.Status == "" {
		t.Fatalf("EventToolProjection(loaded.Events[2]) = %+v, want durable ECHO tool result projection", toolResultPayload)
	}
	if got := session.EventText(loaded.Events[3]); got != "pong" {
		t.Fatalf("final assistant text = %q, want %q", got, "pong")
	}
	userMessage, ok := session.ModelMessageOf(loaded.Events[0])
	if !ok || userMessage.TextContent() != "say pong" {
		t.Fatalf("ModelMessageOf(loaded.Events[0]) = %+v, %v; want durable user message projection", userMessage, ok)
	}
	assistantMessage, ok := session.ModelMessageOf(loaded.Events[3])
	if !ok || assistantMessage.TextContent() != "pong" {
		t.Fatalf("ModelMessageOf(loaded.Events[3]) = %+v, %v; want durable assistant message projection", assistantMessage, ok)
	}
	allEvents, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: activeSession.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatalf("Events(include journal) error = %v", err)
	}
	var toolStatuses []session.ToolExecutionStatus
	var stepStatuses []session.ExecutionStatus
	runTurnStatuses := map[session.JournalKind][]session.ExecutionStatus{}
	for _, event := range allEvents {
		if event.Journal == nil {
			continue
		}
		if event.Journal.ToolExecution != nil {
			toolStatuses = append(toolStatuses, event.Journal.ToolExecution.Status)
		}
		if event.Journal.Execution != nil && event.Journal.Execution.Kind == session.JournalKindStep {
			stepStatuses = append(stepStatuses, event.Journal.Execution.Status)
		}
		if event.Journal.Execution != nil && (event.Journal.Execution.Kind == session.JournalKindRun || event.Journal.Execution.Kind == session.JournalKindTurn) {
			runTurnStatuses[event.Journal.Execution.Kind] = append(runTurnStatuses[event.Journal.Execution.Kind], event.Journal.Execution.Status)
		}
	}
	if want := []session.ToolExecutionStatus{session.ToolExecutionPrepared, session.ToolExecutionApproved, session.ToolExecutionStarted, session.ToolExecutionSucceeded}; !reflect.DeepEqual(toolStatuses, want) {
		t.Fatalf("tool execution journal = %v, want %v", toolStatuses, want)
	}
	if want := []session.ExecutionStatus{session.ExecutionPrepared, session.ExecutionStarted, session.ExecutionSucceeded}; !reflect.DeepEqual(stepStatuses, want) {
		t.Fatalf("step journal = %v, want %v", stepStatuses, want)
	}
	for _, kind := range []session.JournalKind{session.JournalKindRun, session.JournalKindTurn} {
		if want := []session.ExecutionStatus{session.ExecutionPrepared, session.ExecutionStarted, session.ExecutionSucceeded}; !reflect.DeepEqual(runTurnStatuses[kind], want) {
			t.Fatalf("%s journal = %v, want %v", kind, runTurnStatuses[kind], want)
		}
	}
}

func TestRuntimeRunDoesNotFailWhenCanonicalTaskIndexSyncFails(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-task-index-sync-fails")
	baseStore := newFileTaskStoreForTest(t)
	if err := baseStore.Upsert(context.Background(), &taskapi.Entry{
		TaskID:  "task-sync-fails",
		Kind:    taskapi.KindCommand,
		Session: activeSession.SessionRef,
		State:   taskapi.StateCompleted,
		Result:  map[string]any{"state": string(taskapi.StateCompleted), "exit_code": 0},
		Terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendHost,
			SessionID:  "sync-fails-session",
			TerminalID: "sync-fails-terminal",
		},
	}); err != nil {
		t.Fatalf("preseed task store: %v", err)
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		TaskStore:    failingUpsertTaskStore{Store: baseStore, err: errors.New("task index unavailable")},
		AgentFactory: chat.Factory{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	run, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "emit canonical tool result",
		Agent: seqAgent{events: []*session.Event{{
			Type: session.EventTypeToolResult,
			Tool: &session.EventTool{
				Name:   "RUN_COMMAND",
				Status: "completed",
				Output: map[string]any{
					"task_id":   "task-sync-fails",
					"state":     string(taskapi.StateCompleted),
					"result":    "canonical survives\n",
					"exit_code": 0,
				},
			},
		}}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events, seqErr := drainRunnerEvents(t, run.Handle)
	if seqErr != nil {
		t.Fatalf("runner error = %v", seqErr)
	}
	var sawToolResult bool
	for _, event := range events {
		if event != nil && event.Type == session.EventTypeToolResult {
			sawToolResult = true
		}
	}
	if !sawToolResult {
		t.Fatalf("runner events = %#v, want canonical tool result despite task index sync failure", events)
	}
}

type failingUpsertTaskStore struct {
	taskapi.Store
	err error
}

func (s failingUpsertTaskStore) Upsert(context.Context, *taskapi.Entry) error {
	return s.err
}

func TestRuntimeRunPersistsPlanLoopAndState(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-plan")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use PLAN when asked to organize work.",
		},
		RunIDGenerator: func() string { return "run-plan" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	testModel := &planLoopRuntimeModel{}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "make a plan",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
			Tools: []tool.Tool{plan.New()},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var sawPlan bool
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypePlan {
			sawPlan = true
		}
	}
	if !sawPlan {
		t.Fatal("expected plan event in runner output")
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var planEvent *session.Event
	for _, event := range loaded.Events {
		switch session.EventTypeOf(event) {
		case session.EventTypeUser, session.EventTypeToolCall, session.EventTypeToolResult, session.EventTypePlan:
			if strings.TrimSpace(event.IdempotencyKey) == "" {
				t.Fatalf("runtime fact %s has no stable retry identity: %+v", session.EventTypeOf(event), event)
			}
		}
		if event != nil && event.Type == session.EventTypePlan {
			planEvent = event
			break
		}
	}
	planPayload := session.PlanPayloadOf(planEvent)
	if planEvent == nil || planPayload == nil {
		t.Fatalf("plan event = %+v, want semantic plan payload", planEvent)
	}
	if got, want := len(planPayload.Entries), 2; got != want {
		t.Fatalf("len(plan entries) = %d, want %d", got, want)
	}
	state, err := sessions.SnapshotState(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	planState, ok := state["plan"].(map[string]any)
	if !ok {
		t.Fatalf("state[plan] = %#v, want plan map", state["plan"])
	}
	entries, _ := planState["entries"].([]map[string]any)
	if len(entries) == 0 {
		rawEntries, _ := planState["entries"].([]any)
		if got, want := len(rawEntries), 2; got != want {
			t.Fatalf("len(state plan entries) = %d, want %d", got, want)
		}
	}
}

func TestRuntimePolicyWriteOutsideAllowedRootsRequiresApproval(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-policy-default")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: presets.ModeAutoReview,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	writeTool, err := filesystem.NewWrite(hostRuntimeForTest(t, activeSession.CWD))
	if err != nil {
		t.Fatalf("filesystem.NewWrite() error = %v", err)
	}
	outsidePath := policyOutsidePathForRuntimeTest()
	testModel := &denyWriteRuntimeModel{}
	var sawApproval bool
	requester := approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		sawApproval = true
		if req.Approval == nil || req.Approval.ToolCall.Name != filesystem.WriteToolName {
			t.Fatalf("approval request = %+v, want WRITE tool call", req.Approval)
		}
		if got := strings.TrimSpace(fmt.Sprint(req.Metadata["risk_class"])); got != "path_escape" {
			t.Fatalf("approval metadata risk_class = %q, want path_escape", got)
		}
		return agent.ApprovalResponse{
			Outcome:  "selected",
			OptionID: "reject_once",
			Approved: false,
			Reason:   "outside write rejected in test",
		}, nil
	})
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef:        activeSession.SessionRef,
		Input:             "write outside workspace",
		ApprovalRequester: requester,
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
			Tools: []tool.Tool{writeTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	if !sawApproval {
		t.Fatal("expected WRITE outside roots to request approval")
	}
	if _, err := os.Stat(outsidePath); err == nil {
		t.Fatalf("outside path %q exists, want approval rejection to skip write", outsidePath)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 4; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	toolResult := loaded.Events[2]
	if toolResult.Type != session.EventTypeToolResult {
		t.Fatalf("tool result type = %q, want tool_result", toolResult.Type)
	}
	payload := eventToolRawOutput(toolResult)
	if got := strings.TrimSpace(fmt.Sprint(payload["error"])); got != "outside write rejected in test" {
		t.Fatalf("error = %q, want Guardian rationale only", got)
	}
	if got, ok := payload["system_hint"]; ok {
		t.Fatalf("system_hint = %v, want omitted for Guardian denial", got)
	}
	if _, ok := eventToolRawOutput(toolResult)["policy_action"]; ok {
		t.Fatalf("policy_action present in model-facing payload, want omitted")
	}
}

func TestRuntimePolicyWriteOutsideAllowedRootsExecutesAfterApproval(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-policy-write-approve")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: presets.ModeAutoReview,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	writeTool, err := filesystem.NewWrite(hostRuntimeForTest(t, activeSession.CWD))
	if err != nil {
		t.Fatalf("filesystem.NewWrite() error = %v", err)
	}
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "approved-outside.txt")
	testModel := &writePathRuntimeModel{path: outsidePath, content: "approved-outside\n"}
	requester := approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		if req.Approval == nil || req.Approval.ToolCall.Name != filesystem.WriteToolName {
			t.Fatalf("approval request = %+v, want WRITE tool call", req.Approval)
		}
		return agent.ApprovalResponse{
			Outcome:  "selected",
			OptionID: "allow_once",
			Approved: true,
		}, nil
	})
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef:        activeSession.SessionRef,
		Input:             "write outside workspace after approval",
		ApprovalRequester: requester,
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
			Tools: []tool.Tool{writeTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	data, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", outsidePath, err)
	}
	if got := strings.TrimSpace(string(data)); got != "approved-outside" {
		t.Fatalf("written content = %q, want approved-outside", got)
	}
}

func TestRuntimePolicyModePreservesCustomRegistryMode(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-policy-custom-mode")
	_ = sessions
	var sawMode string
	registry, err := policy.NewMemory(policy.NamedMode{
		ID: "locked-down",
		Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
			sawMode = input.Mode
			return policy.Decision{Action: policy.ActionDeny, Reason: "custom denied"}, nil
		},
	})
	if err != nil {
		t.Fatalf("policy.NewMemory() error = %v", err)
	}
	runtime := &Runtime{
		policies:          registry,
		defaultPolicyMode: "locked-down",
	}
	targetTool := tool.NamedTool{
		Def: tool.Definition{Name: "ECHO"},
		Invoke: func(context.Context, tool.Call) (tool.Result, error) {
			t.Fatal("custom policy should deny before invoking the tool")
			return tool.Result{}, nil
		},
	}
	wrapped := runtime.wrapToolsForPolicy(activeSession, activeSession.SessionRef, nil, agent.AgentSpec{
		Tools: []tool.Tool{targetTool},
	}, approvalContext{
		ctx:        context.Background(),
		session:    activeSession,
		sessionRef: activeSession.SessionRef,
	})
	if got := len(wrapped); got != 1 {
		t.Fatalf("len(wrapped) = %d, want 1", got)
	}
	result, err := wrapped[0].Call(context.Background(), tool.Call{ID: "call-1", Name: "ECHO"})
	if err != nil {
		t.Fatalf("wrapped tool Call() error = %v", err)
	}
	if sawMode != "locked-down" {
		t.Fatalf("policy mode seen by custom mode = %q, want locked-down", sawMode)
	}
	payload := testToolResultPayload(t, result)
	if got := payload["error"]; got != "custom denied" {
		t.Fatalf("result error = %v, want custom denied", got)
	}
	if got, _ := payload["system_hint"].(string); !strings.Contains(got, "Follow the policy error exactly") {
		t.Fatalf("result system_hint = %v, want policy-following guidance", got)
	}
	if _, ok := payload["policy_mode"]; ok {
		t.Fatalf("policy_mode present in model-facing payload, want omitted")
	}
}

func TestRuntimePolicyUnknownModeFailsClosed(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws"},
		CWD:        "/workspace",
	}
	registry, err := presets.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	runtime := &Runtime{
		policies:          registry,
		defaultPolicyMode: presets.ModeAutoReview,
	}
	targetTool := tool.NamedTool{
		Def: tool.Definition{Name: "WRITE"},
		Invoke: func(context.Context, tool.Call) (tool.Result, error) {
			t.Fatal("default policy should deny before invoking the tool")
			return tool.Result{}, nil
		},
	}
	wrapped := runtime.wrapToolsForPolicy(activeSession, activeSession.SessionRef, nil, agent.AgentSpec{
		Metadata: map[string]any{policy.MetadataPolicyProfile: "unknown-policy"},
		Tools:    []tool.Tool{targetTool},
	}, approvalContext{
		ctx:        context.Background(),
		session:    activeSession,
		sessionRef: activeSession.SessionRef,
	})
	if got := len(wrapped); got != 1 {
		t.Fatalf("len(wrapped) = %d, want 1", got)
	}
	_, err = wrapped[0].Call(context.Background(), tool.Call{
		ID:    "call-1",
		Name:  "WRITE",
		Input: []byte(`{"path":` + jsonStringForTest(policyOutsidePathForRuntimeTest()) + `}`),
	})
	var profileErr *policy.ProfileError
	if !errors.As(err, &profileErr) {
		t.Fatalf("wrapped tool Call() error = %v, want *policy.ProfileError", err)
	}
}

func TestRuntimePolicyRegistryErrorAndEmptyDecisionFailClosed(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{SessionRef: session.SessionRef{SessionID: "s1"}}
	targetTool := tool.NamedTool{
		Def: tool.Definition{Name: "ECHO"},
		Invoke: func(context.Context, tool.Call) (tool.Result, error) {
			t.Fatal("fail-closed policy must not invoke the tool")
			return tool.Result{}, nil
		},
	}
	tests := []struct {
		name     string
		registry policy.Registry
		wantType any
	}{
		{
			name:     "registry error",
			registry: errorPolicyRegistry{err: errors.New("registry unavailable")},
			wantType: &policy.ProfileError{},
		},
		{
			name: "empty decision",
			registry: staticPolicyRegistry{mode: policy.NamedMode{
				ID:     "empty",
				Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) { return policy.Decision{}, nil },
			}},
			wantType: &policy.DecisionError{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runtime := &Runtime{policies: tt.registry, defaultPolicyMode: "empty"}
			wrapped := runtime.wrapToolsForPolicy(activeSession, activeSession.SessionRef, nil, agent.AgentSpec{
				Tools: []tool.Tool{targetTool},
			}, approvalContext{ctx: context.Background(), session: activeSession, sessionRef: activeSession.SessionRef})
			_, err := wrapped[0].Call(context.Background(), tool.Call{ID: "call-1", Name: "ECHO"})
			switch tt.wantType.(type) {
			case *policy.ProfileError:
				var target *policy.ProfileError
				if !errors.As(err, &target) {
					t.Fatalf("Call() error = %v, want *policy.ProfileError", err)
				}
			case *policy.DecisionError:
				var target *policy.DecisionError
				if !errors.As(err, &target) {
					t.Fatalf("Call() error = %v, want *policy.DecisionError", err)
				}
			}
		})
	}
}

type errorPolicyRegistry struct{ err error }

func (r errorPolicyRegistry) Lookup(context.Context, string) (policy.Mode, bool, error) {
	return nil, false, r.err
}

type staticPolicyRegistry struct{ mode policy.Mode }

func (r staticPolicyRegistry) Lookup(context.Context, string) (policy.Mode, bool, error) {
	return r.mode, r.mode != nil, nil
}

func TestNormalizePolicyModeHandlesDefaultAliasesAndPreservesCustomNames(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":                "workspace-write",
		"auto":            "workspace-write",
		"auto_review":     "workspace-write",
		"manual":          "workspace-write",
		"default":         "workspace-write",
		"plan":            "workspace-write",
		"full_access":     "workspace-write",
		"full_control":    "workspace-write",
		"workspace_write": "workspace-write",
		"locked-down":     "locked-down",
		"TeamStrict":      "TeamStrict",
	}
	for input, want := range tests {
		if got := normalizePolicyMode(input); got != want {
			t.Fatalf("normalizePolicyMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRuntimePolicyFullAccessBlocksDangerousCommand(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-policy-full")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: presets.ModeAutoReview,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	runCommandTool, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: hostRuntimeForTest(t, activeSession.CWD)})
	if err != nil {
		t.Fatalf("shell.NewRunCommand() error = %v", err)
	}
	testModel := &denyCommandRuntimeModel{}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "run dangerous command",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
			Tools: []tool.Tool{runCommandTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	toolResult := loaded.Events[2]
	if got := eventToolRawOutput(toolResult)["system_hint"]; got == nil || strings.TrimSpace(fmt.Sprint(got)) == "" {
		t.Fatalf("system_hint = %v, want non-empty policy guidance", got)
	}
	if _, ok := eventToolRawOutput(toolResult)["policy_action"]; ok {
		t.Fatalf("policy_action present in model-facing payload, want omitted")
	}
}

func TestRuntimePolicyDefaultCommandEscalationWaitsApprovalThenExecutes(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-policy-approval")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: presets.ModeAutoReview,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	runCommandTool, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: hostRuntimeForTest(t, activeSession.CWD)})
	if err != nil {
		t.Fatalf("shell.NewRunCommand() error = %v", err)
	}
	target := filepath.Join(activeSession.CWD, "approved.txt")
	testModel := &approveEscalatedCommandRuntimeModel{command: shellWriteFileForTest(target, "approved\n")}
	requester := approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		state, err := runtime.RunState(ctx, activeSession.SessionRef)
		if err != nil {
			t.Fatalf("RunState() during approval error = %v", err)
		}
		if state.Status != agent.RunLifecycleStatusWaitingApproval || !state.WaitingApproval {
			t.Fatalf("run state during approval = %+v, want waiting_approval", state)
		}
		if req.Approval == nil || req.Approval.ToolCall.Name != shell.RunCommandToolName {
			t.Fatalf("approval request = %+v, want RUN_COMMAND tool call", req.Approval)
		}
		return agent.ApprovalResponse{
			Outcome:  "selected",
			OptionID: "allow_once",
			Approved: true,
		}, nil
	})
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef:        activeSession.SessionRef,
		Input:             "write inside workspace",
		ApprovalRequester: requester,
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
			Tools: []tool.Tool{runCommandTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "approved" {
		t.Fatalf("written content = %q, want %q", got, "approved")
	}
	state, err := runtime.RunState(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("RunState() error = %v", err)
	}
	if state.Status != agent.RunLifecycleStatusCompleted {
		t.Fatalf("final run state = %+v, want completed", state)
	}
}

func TestRuntimeDurableApprovalResolveAndAttachLiveRun(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root, SessionIDGenerator: func() string { return "sess-durable-approval" }}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	ask := staticPolicyRegistry{mode: policy.NamedMode{
		ID: "ask",
		Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
			return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{ID: input.Call.ID, Name: input.Tool.Name, Status: "pending"},
				Options:  []session.ProtocolApprovalOption{{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}},
			}}, nil
		},
	}}
	traceSink := &concurrentTraceSink{}
	runtime, err := New(Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, PolicyRegistry: ask, DefaultPolicyMode: "ask",
		RunIDGenerator: func() string { return "run-durable-approval" },
		TraceSink:      traceSink,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var calls int
	targetTool := tool.NamedTool{
		Def: tool.Definition{Name: "ECHO", EffectClass: tool.EffectNonIdempotent, InputSchema: map[string]any{"type": "object"}},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			calls++
			return tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewJSONPart([]byte(`{"value":"pong"}`))}}, nil
		},
	}
	run, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef, Input: "approve echo",
		AgentSpec: agent.AgentSpec{Name: "chat", Model: &toolLoopRuntimeModel{}, Tools: []tool.Tool{targetTool}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	defer func() {
		run.Handle.Cancel()
		if waiter, ok := run.Handle.(agent.RunnerCompletionWaiter); ok {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := waiter.WaitCompletion(cleanupCtx); err != nil {
				t.Errorf("WaitCompletion(cleanup) error = %v", err)
			}
		}
	}()
	var waiting agent.RunState
	deadline := time.After(10 * time.Second)
	for {
		waiting, err = runtime.RunState(context.Background(), activeSession.SessionRef)
		if err == nil && waiting.WaitingApproval && waiting.PauseTokenID != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("RunState() = %+v, %v; want durable waiting approval", waiting, err)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	resumed, err := runtime.AttachLiveRun(context.Background(), agent.AttachLiveRunRequest{SessionRef: activeSession.SessionRef, RunID: run.Handle.RunID()})
	if err != nil || resumed.Handle != run.Handle {
		t.Fatalf("AttachLiveRun() = %+v, %v; want same live runner", resumed, err)
	}
	decision := agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true, Reason: "approved by user"}
	if err := runtime.ResolveApproval(context.Background(), agent.ResolveApprovalRequest{SessionRef: activeSession.SessionRef, TokenID: waiting.PauseTokenID, Decision: decision}); err != nil {
		t.Fatalf("ResolveApproval() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, resumed.Handle); err != nil {
		t.Fatalf("resumed runner error = %v", err)
	}
	if !traceSink.saw(agent.LifecycleApproval, agent.TraceStarted) || !traceSink.saw(agent.LifecycleApproval, agent.TraceCompleted) {
		t.Fatalf("trace records = %#v, want approval lifecycle", traceSink.snapshot())
	}
	if calls != 1 {
		t.Fatalf("tool calls = %d, want 1", calls)
	}
	if err := runtime.ResolveApproval(context.Background(), agent.ResolveApprovalRequest{SessionRef: activeSession.SessionRef, TokenID: waiting.PauseTokenID, Decision: decision}); err != nil {
		t.Fatalf("idempotent ResolveApproval() error = %v", err)
	}
	events, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: activeSession.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	var pauseStatuses []session.PauseTokenStatus
	for _, event := range events {
		if event.Journal != nil && event.Journal.PauseToken != nil {
			pauseStatuses = append(pauseStatuses, event.Journal.PauseToken.Status)
		}
	}
	if want := []session.PauseTokenStatus{session.PauseTokenPending, session.PauseTokenResolved}; !reflect.DeepEqual(pauseStatuses, want) {
		t.Fatalf("pause token journal = %v, want %v", pauseStatuses, want)
	}

	reopened, err := New(Config{Sessions: sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root})), AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New(reopened) error = %v", err)
	}
	state, err := reopened.RunState(context.Background(), activeSession.SessionRef)
	if err != nil || state.Status != agent.RunLifecycleStatusCompleted || state.ActiveRunID != "run-durable-approval" {
		t.Fatalf("reopened RunState() = %+v, %v; want durable completed run", state, err)
	}
	_, err = reopened.AttachLiveRun(context.Background(), agent.AttachLiveRunRequest{SessionRef: activeSession.SessionRef, RunID: "run-durable-approval"})
	var notResumable *agent.RunNotAttachableError
	if !errors.As(err, &notResumable) {
		t.Fatalf("reopened AttachLiveRun() error = %v, want *RunNotAttachableError", err)
	}
}

func TestRuntimeRecoveryInterruptsOrphanedApprovalPause(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root, SessionIDGenerator: func() string { return "sess-orphaned-approval" }}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := runtime.startRunTurnJournal(context.Background(), activeSession.SessionRef, "run-orphaned", "turn-orphaned"); err != nil {
		t.Fatalf("startRunTurnJournal() error = %v", err)
	}
	if err := runtime.transitionRunTurnJournal(context.Background(), activeSession.SessionRef, "run-orphaned", "turn-orphaned", session.ExecutionWaitingApproval, "approval required"); err != nil {
		t.Fatalf("transitionRunTurnJournal(waiting) error = %v", err)
	}
	now := time.Unix(500, 0).UTC()
	token := session.PauseToken{
		Schema: session.ExecutionJournalSchemaVersion, TokenID: "pause-orphaned", SessionID: activeSession.SessionID,
		RunID: "run-orphaned", TurnID: "turn-orphaned", ToolCallID: "call-orphaned", ToolName: "WRITE",
		Revision: 1, Status: session.PauseTokenPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := runtime.appendPauseToken(context.Background(), activeSession.SessionRef, token); err != nil {
		t.Fatalf("appendPauseToken() error = %v", err)
	}

	reopenedSessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	reopened, err := New(Config{Sessions: reopenedSessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New(reopened) error = %v", err)
	}
	state, err := reopened.RunState(context.Background(), activeSession.SessionRef)
	if err != nil || !state.WaitingApproval || state.PauseTokenID != "pause-orphaned" {
		t.Fatalf("reopened RunState() = %+v, %v; want pending durable approval", state, err)
	}
	_, err = reopened.AttachLiveRun(context.Background(), agent.AttachLiveRunRequest{SessionRef: activeSession.SessionRef, RunID: "run-orphaned"})
	var notResumable *agent.RunNotAttachableError
	if !errors.As(err, &notResumable) {
		t.Fatalf("AttachLiveRun(orphaned) error = %v, want *RunNotAttachableError", err)
	}
	if err := reopened.recoverIncompleteExecutionJournal(context.Background(), activeSession.SessionRef); err != nil {
		t.Fatalf("recoverIncompleteExecutionJournal() error = %v", err)
	}
	state, err = reopened.persistedRunState(context.Background(), activeSession.SessionRef, "run-orphaned")
	if err != nil || state.Status != agent.RunLifecycleStatusInterrupted {
		t.Fatalf("recovered RunState() = %+v, %v; want interrupted", state, err)
	}
	recoveredToken, err := reopened.pauseToken(context.Background(), activeSession.SessionRef, "pause-orphaned")
	if err != nil || recoveredToken.Status != session.PauseTokenCancelled {
		t.Fatalf("recovered pause token = %+v, %v; want cancelled", recoveredToken, err)
	}
}

func TestControllerApprovalRequesterPreservesToolRawInput(t *testing.T) {
	t.Parallel()

	var captured agent.ApprovalRequest
	requester := controllerApprovalRequester{
		requester: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			captured = req
			return agent.ApprovalResponse{
				Outcome:  "selected",
				OptionID: "allow_once",
				Approved: true,
			}, nil
		}),
		sessionRef: session.SessionRef{SessionID: "sess-approval"},
		session:    session.Session{SessionRef: session.SessionRef{SessionID: "sess-approval"}},
		runID:      "run-1",
		turnID:     "turn-1",
	}
	_, err := requester.RequestControllerApproval(context.Background(), controller.ApprovalRequest{
		Agent: "codex",
		Mode:  "default",
		ToolCall: controller.ApprovalToolCall{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Kind:   "execute",
			Title:  "Run command",
			Status: "pending",
			RawInput: map[string]any{
				"command": "pwd",
				"workdir": "/tmp/project",
			},
			RawOutput: map[string]any{
				"preview": "would run pwd",
			},
			Content: []session.ProtocolToolCallContent{{
				Type:    "content",
				Content: session.ProtocolTextContent("permission detail"),
			}},
		},
	})
	if err != nil {
		t.Fatalf("RequestControllerApproval() error = %v", err)
	}
	if captured.Approval == nil {
		t.Fatal("captured approval = nil")
	}
	if captured.Approval.ToolCall.RawInput["command"] != "pwd" {
		t.Fatalf("Approval.ToolCall.RawInput[command] = %#v", captured.Approval.ToolCall.RawInput["command"])
	}
	if captured.Approval.ToolCall.RawInput["workdir"] != "/tmp/project" {
		t.Fatalf("Approval.ToolCall.RawInput[workdir] = %#v", captured.Approval.ToolCall.RawInput["workdir"])
	}
	if captured.Approval.ToolCall.RawOutput["preview"] != "would run pwd" {
		t.Fatalf("Approval.ToolCall.RawOutput[preview] = %#v", captured.Approval.ToolCall.RawOutput["preview"])
	}
	if len(captured.Approval.ToolCall.Content) != 1 {
		t.Fatalf("Approval.ToolCall.Content = %#v, want one content part", captured.Approval.ToolCall.Content)
	}
	if captured.Call.Input == nil || !strings.Contains(string(captured.Call.Input), `"command":"pwd"`) {
		t.Fatalf("Call.Input = %s, want command JSON", string(captured.Call.Input))
	}
}

func TestControllerApprovalRequesterMarksParticipantScope(t *testing.T) {
	t.Parallel()

	var captured agent.ApprovalRequest
	requester := controllerApprovalRequester{
		requester: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			captured = req
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}, nil
		}),
		sessionRef:           session.SessionRef{SessionID: "sess-approval"},
		session:              session.Session{SessionRef: session.SessionRef{SessionID: "sess-approval"}},
		runID:                "run-1",
		turnID:               "participant-turn-1",
		participantID:        "side-1",
		participantKind:      string(session.ParticipantKindACP),
		participantSessionID: "remote-side-1",
	}
	_, err := requester.RequestControllerApproval(context.Background(), controller.ApprovalRequest{
		Agent: "claude",
		Mode:  "default",
		ToolCall: controller.ApprovalToolCall{
			ID:   "call-1",
			Name: "RUN_COMMAND",
		},
	})
	if err != nil {
		t.Fatalf("RequestControllerApproval() error = %v", err)
	}
	for key, want := range map[string]string{
		"scope":                  "participant",
		"scope_id":               "participant-turn-1",
		"participant_id":         "side-1",
		"participant_kind":       string(session.ParticipantKindACP),
		"participant_session_id": "remote-side-1",
		"source":                 "acp_participant",
	} {
		if got := taskStringValue(captured.Metadata[key]); got != want {
			t.Fatalf("metadata[%s] = %q, want %q; metadata=%#v", key, got, want, captured.Metadata)
		}
	}
}

func TestRuntimeCommandYieldThenTaskWaitLoop(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-command-task-loop")
	taskStore := newFileTaskStoreForTest(t)
	runtime, err := New(Config{
		Sessions:  sessions,
		TaskStore: taskStore,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: presets.ModeAutoReview,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	runCommandTool, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: hostRuntimeForTest(t, activeSession.CWD)})
	if err != nil {
		t.Fatalf("shell.NewRunCommand() error = %v", err)
	}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "run async command",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: &commandTaskLoopRuntimeModel{t: t},
			Tools: []tool.Tool{runCommandTool, tasktool.New()},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var finalText string
	var runningToolUpdate bool
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event == nil {
			continue
		}
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Status == "running" {
			runningToolUpdate = true
		}
		if event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(session.EventText(event))
		}
	}
	if !runningToolUpdate {
		t.Fatal("expected running tool update after yielded RUN_COMMAND")
	}
	if finalText != "async command done" {
		t.Fatalf("finalText = %q, want %q", finalText, "async command done")
	}
	runtime.tasks.mu.RLock()
	activeCount := len(runtime.tasks.tasks)
	runtime.tasks.mu.RUnlock()
	if activeCount != 0 {
		t.Fatalf("active task cache = %d, want 0 after completion", activeCount)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if len(loaded.Events) < 6 {
		t.Fatalf("len(loaded.Events) = %d, want >= 6", len(loaded.Events))
	}
	var sawTaskID bool
	for _, event := range loaded.Events {
		if event == nil || event.Type != session.EventTypeToolResult {
			continue
		}
		if taskID := taskIDFromSessionEvent(event); strings.TrimSpace(taskID) != "" {
			sawTaskID = true
			break
		}
	}
	if !sawTaskID {
		t.Fatal("expected persisted tool result with task_id metadata")
	}
	task, err := runtime.tasks.lookupCommand(context.Background(), activeSession.SessionRef, mustSessionTaskID(t, loaded.Events))
	if err != nil {
		t.Fatalf("task fallback lookup error = %v", err)
	}
	status, err := task.session.Status(context.Background())
	if err != nil {
		t.Fatalf("task session Status() error = %v", err)
	}
	if status.Running {
		t.Fatalf("rehydrated completed task still running: %+v", status)
	}
	resultPayload, _ := task.result["result"].(string)
	if !strings.Contains(resultPayload, "async command done") {
		t.Fatalf("rehydrated task result = %q, want async command done", resultPayload)
	}
	terminals := runtime.Streams()
	if terminals == nil {
		t.Fatal("Streams() = nil")
	}
	snap, err := terminals.Read(context.Background(), stream.ReadRequest{
		Ref: stream.Ref{
			SessionID: activeSession.SessionID,
			TaskID:    mustSessionTaskID(t, loaded.Events),
		},
	})
	if err != nil {
		t.Fatalf("terminal Read() error = %v", err)
	}
	if snap.Running {
		t.Fatalf("terminal snapshot still running: %+v", snap)
	}
	terminalText := snap.FinalText
	if terminalText == "" {
		terminalText = terminalFramesText(snap.Frames)
	}
	if !strings.Contains(terminalText, "async command done") {
		t.Fatalf("terminal snapshot text = %q, want async command done", terminalText)
	}
}

func TestRuntimeTaskWriteAddsLineTerminatorForInteractiveCommand(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	runCommandTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, hostRuntimeForTest(t, activeSession.CWD)),
		session:    activeSession,
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	runCommandResult := callRuntimeRunCommandTool(t, runCommandTool, map[string]any{
		"command":       shellInteractiveGreetingForTest(),
		"workdir":       ".",
		"yield_time_ms": shellRunningYieldMillisForTest(0),
	})
	taskID, _ := testToolResultRuntimeMeta(t, runCommandResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("command result metadata = %#v, want task_id", runCommandResult.Metadata)
	}

	taskResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":        "write",
		"task_id":       taskID,
		"input":         "Codex",
		"yield_time_ms": shellCompletionYieldMillisForTest(250),
	})
	if len(taskResult.Content) == 0 || taskResult.Content[0].JSON == nil {
		t.Fatalf("task result content = %#v, want json payload", taskResult.Content)
	}
	payload := string(taskResult.Content[0].JSON.Value)
	if !strings.Contains(payload, "hello Codex") {
		t.Fatalf("task write result = %s, want interactive read to receive input line", payload)
	}
}

func TestTaskToolPayloadReturnsCompletedCommandTerminalStreams(t *testing.T) {
	payload := taskToolPayload(taskapi.Snapshot{
		Ref:     taskapi.Ref{TaskID: "task-1", TerminalID: "term-1"},
		Kind:    taskapi.KindCommand,
		State:   taskapi.StateCompleted,
		Running: false,
		Result: map[string]any{
			"result":    "waiting\nhello Codex\n",
			"exit_code": 0,
		},
	})
	if got, _ := payload["result"].(string); !strings.Contains(got, "hello Codex") {
		t.Fatalf("taskToolPayload result = %q, want terminal text", got)
	}
}

func TestCompactLatestOutputKeepsTailOnly(t *testing.T) {
	got := compactLatestOutput("line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\n")
	want := "...2 lines hidden...\nline 3\nline 4\nline 5\nline 6\nline 7\n"
	if got != want {
		t.Fatalf("compactLatestOutput() = %q, want %q", got, want)
	}
}

func TestRuntimeTerminalSubscribeStreamsRunningTask(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-terminal-subscribe")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: presets.ModeAutoReview,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sandbox := hostRuntimeForTest(t, activeSession.CWD)
	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, sandbox, taskapi.CommandStartRequest{
		Command: shellPrintThenSleepForTest("stream terminal", 50*time.Millisecond),
		Workdir: activeSession.CWD,
		Yield:   1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	terminals := runtime.Streams()
	if terminals == nil {
		t.Fatal("Streams() = nil")
	}
	subscribeTimeout := 2 * time.Second
	if goruntime.GOOS == "windows" {
		subscribeTimeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), subscribeTimeout)
	defer cancel()

	var (
		text        strings.Builder
		closedFrame *stream.Frame
	)
	for frame, seqErr := range terminals.Subscribe(ctx, stream.SubscribeRequest{
		Ref: stream.Ref{
			SessionID: activeSession.SessionID,
			TaskID:    snapshot.Ref.TaskID,
		},
		PollInterval: 10 * time.Millisecond,
	}) {
		if seqErr != nil {
			t.Fatalf("terminal Subscribe() error = %v", seqErr)
		}
		if frame == nil {
			continue
		}
		text.WriteString(frame.Text)
		if frame.Closed {
			cloned := stream.CloneFrame(*frame)
			closedFrame = &cloned
		}
	}
	if closedFrame == nil {
		t.Fatal("expected terminal subscription to emit closed frame")
	}
	if got := text.String(); !strings.Contains(got, "stream terminal") {
		t.Fatalf("terminal text = %q, want %q", got, "stream terminal")
	}
	if got := strings.Count(text.String(), "stream terminal"); got != 1 {
		t.Fatalf("terminal text = %q, want streamed output once", text.String())
	}
	if got := closedFrame.Text; got != "" {
		t.Fatalf("closed frame text = %q, want contentless final after streamed output", got)
	}
}

func TestRuntimeTerminalSubscribePreservesEchoNewlines(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-terminal-newlines")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: presets.ModeAutoReview,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sandbox := hostRuntimeForTest(t, activeSession.CWD)
	command := `for i in 1 2 3; do echo "Step ${i}/3"; sleep 0.02; done`
	want := "Step 1/3\nStep 2/3\nStep 3/3\n"
	if goruntime.GOOS == "windows" {
		command = `1..3 | ForEach-Object { Write-Output "Step $_/3"; Start-Sleep -Milliseconds 20 }`
		want = "Step 1/3\r\nStep 2/3\r\nStep 3/3\r\n"
	}
	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, sandbox, taskapi.CommandStartRequest{
		Command: command,
		Workdir: activeSession.CWD,
		Yield:   1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	terminals := runtime.Streams()
	if terminals == nil {
		t.Fatal("Streams() = nil")
	}
	subscribeTimeout := 2 * time.Second
	if goruntime.GOOS == "windows" {
		subscribeTimeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), subscribeTimeout)
	defer cancel()

	var text strings.Builder
	for frame, seqErr := range terminals.Subscribe(ctx, stream.SubscribeRequest{
		Ref: stream.Ref{
			SessionID: activeSession.SessionID,
			TaskID:    snapshot.Ref.TaskID,
		},
		PollInterval: 5 * time.Millisecond,
	}) {
		if seqErr != nil {
			t.Fatalf("terminal Subscribe() error = %v", seqErr)
		}
		if frame == nil {
			continue
		}
		text.WriteString(frame.Text)
	}
	if got := text.String(); got != want {
		t.Fatalf("terminal stream text = %q, want %q", got, want)
	}
}

func TestRuntimeTerminalSubscribePreservesCompletionTailDuringTaskWait(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workdir := t.TempDir()
	sessionStore := sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-terminal-task-wait-tail" },
	})
	sessions := sessionfile.NewService(sessionStore)
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: workdir,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	runtime, err := New(Config{
		Sessions:  sessions,
		TaskStore: sessionfile.NewTaskStore(sessionStore),
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	backend := hostRuntimeForTest(t, workdir)
	runtime.tasks.registerSandboxRuntime(backend)

	const prefix = "步骤 5/5: 处理中...\n"
	const tail = "✅ 任务完成！\n"
	delay := 250 * time.Millisecond
	command := "printf " + shellQuoteForTest(prefix) + "; sleep 0.250; printf " + shellQuoteForTest(tail)
	yield := 5 * time.Millisecond
	waitBudget := 3 * time.Second
	if goruntime.GOOS == "windows" {
		delay = 2 * time.Second
		command = fmt.Sprintf("[Console]::Out.Write(%s); Start-Sleep -Milliseconds %d; [Console]::Out.Write(%s)",
			powershellQuoteForTest(prefix), delay.Milliseconds(), powershellQuoteForTest(tail))
		yield = 100 * time.Millisecond
		waitBudget = 8 * time.Second
	}
	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, taskapi.CommandStartRequest{
		Command:    command,
		Workdir:    workdir,
		Yield:      yield,
		ParentCall: "call-terminal-task-wait-tail",
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	if !snapshot.Running {
		t.Fatalf("StartCommand() snapshot = %#v, want running task", snapshot)
	}
	initial := taskRawStringValue(snapshot.Result["latest_output"])
	cursor, _ := taskInt64Value(snapshot.Metadata["output_cursor"])

	type subscriptionResult struct {
		frames []stream.Frame
		err    error
	}
	streamCtx, cancel := context.WithTimeout(context.Background(), waitBudget)
	defer cancel()
	subscriptionDone := make(chan subscriptionResult, 1)
	go func() {
		var result subscriptionResult
		for frame, streamErr := range runtime.Streams().Subscribe(streamCtx, stream.SubscribeRequest{
			Ref: stream.Ref{
				SessionID: activeSession.SessionID,
				TaskID:    snapshot.Ref.TaskID,
			},
			Cursor:       stream.Cursor{Output: cursor},
			PollInterval: 5 * time.Millisecond,
		}) {
			if streamErr != nil {
				result.err = streamErr
				break
			}
			if frame != nil {
				result.frames = append(result.frames, stream.CloneFrame(*frame))
			}
		}
		subscriptionDone <- result
	}()

	time.Sleep(minDuration(delay/4, 50*time.Millisecond))
	waited, err := runtime.tasks.Wait(context.Background(), activeSession.SessionRef, taskapi.ControlRequest{
		TaskID:    snapshot.Ref.TaskID,
		Yield:     waitBudget,
		Principal: session.ActorKindController,
	})
	if err != nil {
		t.Fatalf("TASK wait error = %v", err)
	}
	if waited.Running || waited.State != taskapi.StateCompleted {
		t.Fatalf("TASK wait snapshot = %#v, want completed", waited)
	}

	var subscribed subscriptionResult
	select {
	case subscribed = <-subscriptionDone:
	case <-streamCtx.Done():
		t.Fatalf("terminal subscription did not close: %v", streamCtx.Err())
	}
	if subscribed.err != nil {
		t.Fatalf("terminal subscription error = %v", subscribed.err)
	}
	var streamed strings.Builder
	closeIndex := -1
	tailIndex := -1
	for i, frame := range subscribed.frames {
		streamed.WriteString(frame.Text)
		if strings.Contains(frame.Text, tail) && tailIndex < 0 {
			tailIndex = i
		}
		if frame.Closed && closeIndex < 0 {
			closeIndex = i
		}
	}
	want := taskRawStringValue(waited.Result["result"])
	if got := initial + streamed.String(); got != want || want != prefix+tail {
		t.Fatalf("initial + streamed output = %q, TASK result = %q, want %q", got, want, prefix+tail)
	}
	if tailIndex < 0 || closeIndex < 0 || tailIndex >= closeIndex {
		t.Fatalf("frames = %#v, want completion tail before terminal close", subscribed.frames)
	}
}

func TestRuntimeRunCommandToolUsesDefaultYieldWhenOmitted(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	targetTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeRunCommandTool(t, targetTool, map[string]any{
		"command": "printf 'ok'",
		"workdir": activeSession.CWD,
	})

	if got := fake.session.lastWait; got != defaultCommandYield {
		t.Fatalf("omitted yield wait = %v, want %v", got, defaultCommandYield)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestRuntimeRunCommandToolKeepsExplicitZeroYield(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	targetTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeRunCommandTool(t, targetTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})

	if got := fake.session.lastWait; got != 0 {
		t.Fatalf("explicit zero yield wait = %v, want 0", got)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestRuntimeRunCommandToolPassesExplicitYieldThrough(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	targetTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeRunCommandTool(t, targetTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 125,
	})

	if got := fake.session.lastWait; got != 125*time.Millisecond {
		t.Fatalf("explicit yield wait = %v, want %v", got, 125*time.Millisecond)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestRuntimeRunCommandToolPassesConfiguredTimeoutThrough(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	base, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: fake, Timeout: 60 * time.Second})
	if err != nil {
		t.Fatalf("shell.NewRunCommand() error = %v", err)
	}
	targetTool := runtimeCommandTool{
		base:       base,
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeRunCommandTool(t, targetTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})

	if got := fake.session.timeout; got != 60*time.Second {
		t.Fatalf("command timeout = %v, want %v", got, 60*time.Second)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestStartCommandReconcilesTerminalStatusWhenInitialWaitErrors(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	waitErr := errors.New("terminal session failed")
	fake := &yieldProbeSandboxRuntime{session: &yieldProbeSandboxSession{waitErr: waitErr, statusRunning: boolPtr(false)}}
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore

	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, fake, taskapi.CommandStartRequest{
		Command: "echo hello",
		Workdir: activeSession.CWD,
		Yield:   0,
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	if snapshot.Running {
		t.Fatalf("snapshot.Running = true, want false")
	}
	if snapshot.State != taskapi.StateCompleted {
		t.Fatalf("snapshot.State = %q, want completed status confirmed independently", snapshot.State)
	}
	if fake.session.terminated {
		t.Fatal("session.terminated = true, want no speculative termination after terminal status")
	}
	runtime.tasks.mu.RLock()
	_, active := runtime.tasks.tasks[snapshot.Ref.TaskID]
	runtime.tasks.mu.RUnlock()
	if active {
		t.Fatalf("task %q still active after terminal reconciliation", snapshot.Ref.TaskID)
	}
	entry, err := taskStore.Get(context.Background(), snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get() error = %v", err)
	}
	if entry == nil || entry.Running || entry.State != taskapi.StateCompleted {
		t.Fatalf("persisted entry = %#v, want completed non-running task", entry)
	}
}

func TestStartCommandDoesNotExposePlainExitSummaryAsError(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	completed := false
	fakeSession := &yieldProbeSandboxSession{
		statusRunning: &completed,
		waitErr:       sandbox.MarkCommandExit(errors.New("process exited with code 1")),
		result:        sandbox.CommandResult{ExitCode: 1},
		resultErr:     sandbox.MarkCommandExit(errors.New("process exited with code 1")),
	}
	fake := &yieldProbeSandboxRuntime{session: fakeSession}
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore

	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, fake, taskapi.CommandStartRequest{
		Command: "Get-Command py -ErrorAction SilentlyContinue",
		Workdir: activeSession.CWD,
		Yield:   0,
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	if snapshot.Running {
		t.Fatalf("snapshot.Running = true, want false")
	}
	if snapshot.State != taskapi.StateFailed {
		t.Fatalf("snapshot.State = %q, want failed", snapshot.State)
	}
	if got, exists := snapshot.Result["result"]; exists {
		t.Fatalf("snapshot.Result[result] = %#v, want no durable no-output placeholder", got)
	}
	if got, _ := snapshot.Result["exit_code"].(int); got != 1 {
		t.Fatalf("snapshot.Result[exit_code] = %v, want 1", snapshot.Result["exit_code"])
	}
	if _, exists := snapshot.Result["error"]; exists {
		t.Fatalf("snapshot.Result[error] = %#v, want omitted for plain exit summary", snapshot.Result["error"])
	}
	if fakeSession.terminated {
		t.Fatal("session.terminated = true, want ordinary command exit to remain result-only")
	}
}

func TestRuntimeTaskWaitErrorDoesNotTerminateRunningCommand(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fakeSession := newYieldProbeSandboxSession()
	fake := &yieldProbeSandboxRuntime{session: fakeSession}
	runCommandTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	runCommandResult := callRuntimeRunCommandTool(t, runCommandTool, map[string]any{
		"command":       "sleep 60",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := testToolResultRuntimeMeta(t, runCommandResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("command result metadata = %#v, want task_id", runCommandResult.Metadata)
	}

	waitErr := errors.New("transient wait failure")
	fakeSession.waitErr = waitErr
	raw, err := json.Marshal(map[string]any{
		"action":  "wait",
		"task_id": taskID,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	_, err = (runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}).Call(context.Background(), tool.Call{
		ID:    "task-control-test",
		Name:  tasktool.ToolName,
		Input: raw,
	})
	if !errors.Is(err, waitErr) {
		t.Fatalf("TASK wait error = %v, want %v", err, waitErr)
	}
	if fakeSession.terminated {
		t.Fatal("session.terminated = true, want running task left alone")
	}
	runtime.tasks.mu.RLock()
	_, active := runtime.tasks.tasks[taskID]
	runtime.tasks.mu.RUnlock()
	if !active {
		t.Fatalf("task %q not active after wait-side error", taskID)
	}
}

func TestRuntimeTaskWaitUsesDefaultYieldWhenOmitted(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	runCommandTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	runCommandResult := callRuntimeRunCommandTool(t, runCommandTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := testToolResultRuntimeMeta(t, runCommandResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("command result metadata = %#v, want task_id", runCommandResult.Metadata)
	}

	taskResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":  "wait",
		"task_id": taskID,
	})

	if got := fake.session.lastWait; got != defaultCommandYield {
		t.Fatalf("omitted TASK wait yield = %v, want %v", got, defaultCommandYield)
	}
	toolMeta := testToolResultRuntimeMeta(t, taskResult, "tool")
	if got := toolMeta["effective_yield_time_ms"]; got != float64(7000) && got != 7000 {
		t.Fatalf("effective_yield_time_ms = %#v, want 7000", got)
	}
	if got := toolMeta["yield_time_ms_defaulted"]; got != true {
		t.Fatalf("yield_time_ms_defaulted = %#v, want true", got)
	}
	payload := testToolResultPayload(t, taskResult)
	if _, ok := payload["actual_wait_time_ms"]; !ok {
		t.Fatalf("payload missing actual_wait_time_ms: %#v", payload)
	}
}

func TestRuntimeCommandTaskIDUsesShortUID(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	taskID := startProbeCommandTask(t, activeSession, runtime, fake)

	if !isShortHexTaskID(taskID) {
		t.Fatalf("task_id = %q, want short hex uid", taskID)
	}
	if strings.HasPrefix(taskID, "task-") || strings.HasPrefix(taskID, "task_") {
		t.Fatalf("task_id = %q, should not use task prefix", taskID)
	}
}

func isShortHexTaskID(taskID string) bool {
	if len(taskID) != taskIDRandomBytes*2 {
		return false
	}
	for _, ch := range taskID {
		if ch >= '0' && ch <= '9' {
			continue
		}
		if ch >= 'a' && ch <= 'f' {
			continue
		}
		return false
	}
	return true
}

func TestRuntimeTaskWaitNegativeOneWaitsUntilDone(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	taskID := startProbeCommandTask(t, activeSession, runtime, fake)
	completed := false
	fake.session.statusRunning = &completed

	taskResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":        "wait",
		"task_id":       taskID,
		"yield_time_ms": -1,
	})

	toolMeta := testToolResultRuntimeMeta(t, taskResult, "tool")
	if got := fake.session.lastWait; got > defaultTaskWaitUntilDoneYield || got < defaultTaskWaitUntilDoneYield-time.Millisecond {
		t.Fatalf("yield_time_ms=-1 wait = %v, want %v", got, defaultTaskWaitUntilDoneYield)
	}
	if got := toolMeta["effective_yield_time_ms"]; got != float64(300000) && got != 300000 {
		t.Fatalf("effective_yield_time_ms = %#v, want 300000", got)
	}
	if got := toolMeta["yield_time_ms_defaulted"]; got == true {
		t.Fatalf("yield_time_ms_defaulted = %#v, want false when yield_time_ms=-1 is explicit", got)
	}
	if got := toolMeta["yield_time_ms"]; got != float64(-1) && got != -1 {
		t.Fatalf("yield_time_ms meta = %#v, want -1", got)
	}
	if got := toolMeta["wait_until_done"]; got != nil {
		t.Fatalf("wait_until_done meta = %#v, want omitted", got)
	}
	payload := testToolResultPayload(t, taskResult)
	if _, ok := payload["actual_wait_time_ms"]; !ok {
		t.Fatalf("payload missing actual_wait_time_ms: %#v", payload)
	}
}

func TestRuntimeTaskWaitPositiveYieldDoesNotPollUntilDone(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	startResult := callRuntimeRunCommandTool(t, runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"command":       "printf 'still-running'",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := testToolResultRuntimeMeta(t, startResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("command result metadata = %#v, want task_id", startResult.Metadata)
	}

	taskResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":        "wait",
		"task_id":       taskID,
		"yield_time_ms": 25,
	})

	toolMeta := testToolResultRuntimeMeta(t, taskResult, "tool")
	if got := fake.session.lastWait; got != 25*time.Millisecond {
		t.Fatalf("positive TASK wait yield = %v, want 25ms", got)
	}
	if got := toolMeta["wait_timed_out"]; got != nil {
		t.Fatalf("wait_timed_out = %#v, want omitted for positive yield", got)
	}
	if got := toolMeta["wait_until_done"]; got != nil {
		t.Fatalf("wait_until_done = %#v, want omitted for positive yield", got)
	}
	if len(taskResult.Content) == 0 || taskResult.Content[0].JSON == nil {
		t.Fatalf("task result content = %#v, want json payload", taskResult.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(taskResult.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(task payload) error = %v", err)
	}
	if _, ok := payload["actual_wait_time_ms"]; !ok {
		t.Fatalf("payload missing actual_wait_time_ms: %#v", payload)
	}
	if got := payload["still_running"]; got != nil {
		t.Fatalf("payload still_running = %#v, want omitted for positive yield", got)
	}
	if got := payload["wait_timed_out"]; got != nil {
		t.Fatalf("payload wait_timed_out = %#v, want omitted for positive yield", got)
	}
}

func TestRuntimeTaskWaitAcceptsCommaSeparatedTaskIDs(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fakeOne := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	fakeTwo := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	taskOne := startProbeCommandTask(t, activeSession, runtime, fakeOne)
	taskTwo := startProbeCommandTask(t, activeSession, runtime, fakeTwo)

	taskResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":        "wait",
		"task_id":       taskOne + ", " + taskTwo,
		"yield_time_ms": 125,
	})

	if fakeOne.session.lastWait > 125*time.Millisecond || fakeTwo.session.lastWait > 125*time.Millisecond {
		t.Fatalf("wait durations = %v/%v, want both <=125ms", fakeOne.session.lastWait, fakeTwo.session.lastWait)
	}
	payload := testToolResultPayload(t, taskResult)
	if got, _ := payload["action"].(string); got != "wait" {
		t.Fatalf("payload[action] = %q, want wait", got)
	}
	if _, ok := payload["actual_wait_time_ms"]; !ok {
		t.Fatalf("payload missing actual_wait_time_ms: %#v", payload)
	}
	tasks, _ := payload["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("payload[tasks] = %#v, want 2 tasks", payload["tasks"])
	}
	for _, item := range tasks {
		mapped, _ := item.(map[string]any)
		if _, ok := mapped["actual_wait_time_ms"]; !ok {
			t.Fatalf("batch item missing actual_wait_time_ms: %#v", item)
		}
	}
	toolMeta := testToolResultRuntimeMeta(t, taskResult, "tool")
	if got := stringSliceFromAny(toolMeta["target_ids"]); !reflect.DeepEqual(got, []string{taskOne, taskTwo}) {
		t.Fatalf("target_ids = %#v, want [%s %s]", toolMeta["target_ids"], taskOne, taskTwo)
	}
}

func TestRuntimeTaskBatchWaitUsesSharedYieldBudget(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fakeOne := &yieldProbeSandboxRuntime{session: &yieldProbeSandboxSession{waitDelay: 35 * time.Millisecond}}
	fakeTwo := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	taskOne := startProbeCommandTask(t, activeSession, runtime, fakeOne)
	taskTwo := startProbeCommandTask(t, activeSession, runtime, fakeTwo)

	_ = callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":        "wait",
		"task_id":       taskOne + "," + taskTwo,
		"yield_time_ms": 50,
	})

	if len(fakeOne.session.waitCalls) < 2 || len(fakeTwo.session.waitCalls) < 2 {
		t.Fatalf("wait calls = %#v/%#v, want start and TASK wait calls", fakeOne.session.waitCalls, fakeTwo.session.waitCalls)
	}
	batchFirst := fakeOne.session.waitCalls[len(fakeOne.session.waitCalls)-1]
	batchSecond := fakeTwo.session.waitCalls[len(fakeTwo.session.waitCalls)-1]
	if batchFirst > 50*time.Millisecond || batchFirst < 40*time.Millisecond {
		t.Fatalf("first batch wait = %v, want near 50ms", batchFirst)
	}
	if batchSecond >= 50*time.Millisecond {
		t.Fatalf("second batch wait = %v, want remaining budget below 50ms", batchSecond)
	}
}

func TestRuntimeTaskCancelAcceptsCommaSeparatedTaskIDs(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fakeOne := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	fakeTwo := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	taskOne := startProbeCommandTask(t, activeSession, runtime, fakeOne)
	taskTwo := startProbeCommandTask(t, activeSession, runtime, fakeTwo)

	taskResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":  "cancel",
		"task_id": taskOne + "," + taskTwo,
	})

	if !fakeOne.session.terminated || !fakeTwo.session.terminated {
		t.Fatalf("terminated = %v/%v, want both true", fakeOne.session.terminated, fakeTwo.session.terminated)
	}
	payload := testToolResultPayload(t, taskResult)
	if got, _ := payload["action"].(string); got != "cancel" {
		t.Fatalf("payload[action] = %q, want cancel", got)
	}
	tasks, _ := payload["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("payload[tasks] = %#v, want 2 tasks", payload["tasks"])
	}
	toolMeta := testToolResultRuntimeMeta(t, taskResult, "tool")
	if got := stringSliceFromAny(toolMeta["target_ids"]); !reflect.DeepEqual(got, []string{taskOne, taskTwo}) {
		t.Fatalf("target_ids = %#v, want [%s %s]", toolMeta["target_ids"], taskOne, taskTwo)
	}
}

func TestRuntimeTaskBatchCancelReturnsPartialFailurePayload(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fakeOne := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	taskOne := startProbeCommandTask(t, activeSession, runtime, fakeOne)

	taskResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":  "cancel",
		"task_id": taskOne + ",stale-id",
	})

	if !fakeOne.session.terminated {
		t.Fatal("first task was not cancelled before stale-id failure")
	}
	if !taskResult.IsError {
		t.Fatal("batch cancel partial failure IsError = false, want true")
	}
	payload := testToolResultPayload(t, taskResult)
	if got, _ := payload["failed"].(float64); got != 1 {
		t.Fatalf("payload[failed] = %#v, want 1", payload["failed"])
	}
	tasks, _ := payload["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("payload[tasks] = %#v, want success and error entries", payload["tasks"])
	}
	second, _ := tasks[1].(map[string]any)
	if got, _ := second["task_id"].(string); got != "stale-id" {
		t.Fatalf("second task_id = %q, want stale-id", got)
	}
	if errText, _ := second["error"].(string); !strings.Contains(errText, "not found") {
		t.Fatalf("second error = %q, want not found", errText)
	}
	toolMeta := testToolResultRuntimeMeta(t, taskResult, "tool")
	if got := stringSliceFromAny(toolMeta["target_ids"]); !reflect.DeepEqual(got, []string{taskOne, "stale-id"}) {
		t.Fatalf("target_ids = %#v, want [%s stale-id]", toolMeta["target_ids"], taskOne)
	}
	if got := toolMeta["failed_count"]; got != 1 {
		t.Fatalf("failed_count = %#v, want 1", got)
	}
}

func TestRuntimeTaskWriteWithCommaSeparatedTaskIDsUsesFirst(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fakeOne := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	fakeTwo := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	taskOne := startProbeCommandTask(t, activeSession, runtime, fakeOne)
	taskTwo := startProbeCommandTask(t, activeSession, runtime, fakeTwo)

	raw, err := json.Marshal(map[string]any{
		"action":  "write",
		"task_id": taskOne + "," + taskTwo,
		"input":   "hello",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := (runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}).Call(context.Background(), tool.Call{ID: "task-write", Name: tasktool.ToolName, Input: raw})
	if err != nil {
		t.Fatalf("TASK write with multiple task ids error = %v", err)
	}
	toolMeta := testToolResultRuntimeMeta(t, result, "tool")
	if got := toolMeta["target_id"]; got != taskOne {
		t.Fatalf("target_id = %#v, want first task %q", got, taskOne)
	}
}

func TestRuntimeTaskWaitZeroUsesDefaultYield(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	runCommandTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	runCommandResult := callRuntimeRunCommandTool(t, runCommandTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := testToolResultRuntimeMeta(t, runCommandResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("command result metadata = %#v, want task_id", runCommandResult.Metadata)
	}

	taskResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":        "wait",
		"task_id":       taskID,
		"yield_time_ms": 0,
	})

	if got := fake.session.lastWait; got != defaultCommandYield {
		t.Fatalf("explicit zero TASK wait yield = %v, want default %v", got, defaultCommandYield)
	}
	toolMeta := testToolResultRuntimeMeta(t, taskResult, "tool")
	if got := toolMeta["effective_yield_time_ms"]; got != float64(7000) && got != 7000 {
		t.Fatalf("effective_yield_time_ms = %#v, want 7000", got)
	}
	if got := toolMeta["yield_time_ms_defaulted"]; got != true {
		t.Fatalf("yield_time_ms_defaulted = %#v, want true", got)
	}
}

func TestRuntimeTaskWaitReturnsTailWhileRunningAndFullWhenCompleted(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fakeSession := newYieldProbeSandboxSession()
	fake := &yieldProbeSandboxRuntime{session: fakeSession}
	runCommandTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	runCommandResult := callRuntimeRunCommandTool(t, runCommandTool, map[string]any{
		"command":       "for i in $(seq 1 8); do echo line $i; done",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := testToolResultRuntimeMeta(t, runCommandResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("command result metadata = %#v, want task_id", runCommandResult.Metadata)
	}

	fakeSession.stdout = "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\n"
	if fakeSession.onOutput == nil {
		t.Fatal("fake session output callback is nil")
	}
	fakeSession.onOutput(sandbox.OutputChunk{Text: fakeSession.stdout})
	taskTool := runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	runningResult := callRuntimeTaskTool(t, taskTool, map[string]any{
		"action":        "wait",
		"task_id":       taskID,
		"yield_time_ms": 100,
	})
	runningPayload := testToolResultPayload(t, runningResult)
	wantTail := "...3 lines hidden...\nline 4\nline 5\nline 6\nline 7\nline 8\n"
	if got, _ := runningPayload["latest_output"].(string); got != wantTail {
		t.Fatalf("running latest_output = %q, want %q", got, wantTail)
	}
	if _, exists := runningPayload["result"]; exists {
		t.Fatalf("running payload[result] = %#v, want omitted", runningPayload["result"])
	}

	completed := false
	fakeSession.statusRunning = &completed
	fakeSession.result = sandbox.CommandResult{Stdout: fakeSession.stdout, ExitCode: 0}
	completedResult := callRuntimeTaskTool(t, taskTool, map[string]any{
		"action":        "wait",
		"task_id":       taskID,
		"yield_time_ms": 12000,
	})
	completedPayload := testToolResultPayload(t, completedResult)
	if got, _ := completedPayload["result"].(string); got != fakeSession.stdout {
		t.Fatalf("completed result = %q, want full output %q", got, fakeSession.stdout)
	}
	if _, exists := completedPayload["latest_output"]; exists {
		t.Fatalf("completed payload[latest_output] = %#v, want omitted", completedPayload["latest_output"])
	}
}

func TestRuntimeTaskWaitAddsWindowsMSYSSSHSignalPipeHintWhenCompleted(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fakeSession := newYieldProbeSandboxSession()
	fake := &yieldProbeSandboxRuntime{session: fakeSession}
	runCommandTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	runCommandResult := callRuntimeRunCommandTool(t, runCommandTool, map[string]any{
		"command":       "go build ./...",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := testToolResultRuntimeMeta(t, runCommandResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("command result metadata = %#v, want task_id", runCommandResult.Metadata)
	}

	completed := false
	fakeSession.statusRunning = &completed
	sshFailure := `      0 [main] ssh (17912) D:\xue\Git\usr\bin\ssh.exe: *** fatal error - couldn't create signal pipe, Win32 error 5
fatal: Could not read from remote repository.`
	fakeSession.result = sandbox.CommandResult{
		Stderr:   sshFailure,
		ExitCode: 128,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendWindows,
	}
	fakeSession.resultErr = sandbox.MarkCommandExit(fmt.Errorf("exit status 128"))

	taskTool := runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	completedResult := callRuntimeTaskTool(t, taskTool, map[string]any{
		"action":        "wait",
		"task_id":       taskID,
		"yield_time_ms": 12000,
	})
	payload := testToolResultPayload(t, completedResult)
	if text, _ := payload["result"].(string); !strings.Contains(text, "couldn't create signal pipe") {
		t.Fatalf("result = %q, want original ssh diagnostic", text)
	}
	if got, _ := payload["system_hint"].(string); !strings.Contains(got, "GIT_SSH_COMMAND=C:/Windows/System32/OpenSSH/ssh.exe") {
		t.Fatalf("system_hint = %q, want native OpenSSH guidance", got)
	}
	if _, ok := payload["hint_code"]; ok {
		t.Fatalf("hint_code = %#v, want omitted from model-facing payload", payload["hint_code"])
	}
}

func TestCompactLatestOutputPreservesTrailingNewlineForDeltaBoundaries(t *testing.T) {
	t.Parallel()

	first := compactLatestOutput("requests 2.34.2\r\n")
	second := compactLatestOutput("HTTP 200\r\n")
	got := first + second
	if got != "requests 2.34.2\nHTTP 200\n" {
		t.Fatalf("combined compact latest output = %q, want line boundary preserved", got)
	}
}

func TestTaskSnapshotToolResultKeepsTerminalStreamsInPayloadOnly(t *testing.T) {
	t.Parallel()

	result := taskSnapshotToolResult(
		tool.Call{ID: "call-1", Name: shell.RunCommandToolName},
		tool.Definition{Name: shell.RunCommandToolName},
		taskapi.Snapshot{
			Ref:     taskapi.Ref{TaskID: "task-1", SessionID: "session-1"},
			State:   taskapi.StateCompleted,
			Running: false,
			Result: map[string]any{
				"result":    "done\n",
				"exit_code": 0,
			},
			Metadata: map[string]any{
				"session_id":     "session-1",
				"supports_input": true,
			},
		},
	)

	var payload map[string]any
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want JSON payload", result.Content)
	}
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	if got, _ := payload["result"].(string); got != "done\n" {
		t.Fatalf("payload[result] = %q, want full terminal text", got)
	}
	if _, exists := result.Meta["text"]; exists {
		t.Fatalf("result.Meta duplicated terminal text: %#v", result.Meta)
	}
	if _, exists := result.Meta["exit_code"]; exists {
		t.Fatalf("result.Meta duplicated exit_code output: %#v", result.Meta)
	}
}

func TestTaskSnapshotToolResultKeepsRawStreamsAndConciseError(t *testing.T) {
	t.Parallel()

	result := taskSnapshotToolResult(
		tool.Call{ID: "call-1", Name: shell.RunCommandToolName},
		tool.Definition{Name: shell.RunCommandToolName},
		taskapi.Snapshot{
			Ref:     taskapi.Ref{TaskID: "task-1", SessionID: "session-1"},
			State:   taskapi.StateFailed,
			Running: false,
			Result: map[string]any{
				"result":    "go: writing stat cache: open /home/test/go/pkg/mod/cache: read-only file system",
				"exit_code": 1,
				"error":     sandbox.SandboxPermissionDeniedMessage,
			},
		},
	)
	if result.IsError {
		t.Fatal("result.IsError = true for shell command exit status, want false")
	}
	var payload map[string]any
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want JSON payload", result.Content)
	}
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	if text, _ := payload["result"].(string); !strings.Contains(text, "/home/test/go/pkg/mod/cache") {
		t.Fatalf("payload[result] = %q, want original terminal text denied path", text)
	}
	if got, _ := payload["error"].(string); got != sandbox.SandboxPermissionDeniedMessage {
		t.Fatalf("payload error = %q, want concise sandbox permission hint", got)
	}
	if _, exists := result.Meta["error"]; exists {
		t.Fatalf("result.Meta duplicated error output: %#v", result.Meta)
	}
}

func TestTaskControlSnapshotToolResultSimplifiesCancelPayload(t *testing.T) {
	t.Parallel()

	result := taskControlSnapshotToolResult(
		tool.Call{ID: "task-cancel-1", Name: tasktool.ToolName, Input: mustJSONRaw(map[string]any{
			"action":  "cancel",
			"task_id": "task-1",
		})},
		tool.Definition{Name: tasktool.ToolName},
		taskapi.Snapshot{
			Ref:     taskapi.Ref{TaskID: "task-1", SessionID: "session-1"},
			Kind:    taskapi.KindCommand,
			State:   taskapi.StateCancelled,
			Running: false,
			Result: map[string]any{
				"result":    "partial command output\n",
				"error":     "context canceled",
				"exit_code": -1,
			},
		},
		"cancel",
		false,
		false,
		0,
	)

	payload := testToolResultPayload(t, result)
	if got, _ := payload["task_id"].(string); got != "task-1" {
		t.Fatalf("payload[task_id] = %q, want task-1", got)
	}
	if got, _ := payload["state"].(string); got != string(taskapi.StateCancelled) {
		t.Fatalf("payload[state] = %q, want cancelled", got)
	}
	for _, key := range []string{"result", "latest_output", "output_preview", "final_message", "error", "exit_code"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("payload contains %q: %#v", key, payload)
		}
	}
}

func TestTaskSnapshotToolResultLeavesTruncationToAgentLoop(t *testing.T) {
	t.Parallel()

	hugeStderr := strings.Repeat("permission denied\n", tool.DefaultTruncationPolicy().ByteBudget()/2)
	result := taskSnapshotToolResult(
		tool.Call{ID: "call-1", Name: shell.RunCommandToolName},
		tool.Definition{Name: shell.RunCommandToolName},
		taskapi.Snapshot{
			Ref:     taskapi.Ref{TaskID: "task-1", SessionID: "session-1"},
			State:   taskapi.StateFailed,
			Running: false,
			Result: map[string]any{
				"result":    hugeStderr,
				"exit_code": 1,
			},
		},
	)

	var payload map[string]any
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want JSON payload", result.Content)
	}
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	gotText, _ := payload["result"].(string)
	if gotText != hugeStderr {
		t.Fatalf("payload result len = %d, want untruncated tool output len %d", len(gotText), len(hugeStderr))
	}
	if _, exists := result.Meta["text"]; exists {
		t.Fatalf("result.Meta duplicated terminal text: %#v", result.Meta)
	}
	if payload["_tool_truncation"] != nil || payload["output_meta"] != nil {
		t.Fatalf("payload = %#v, should not expose truncation metadata", payload)
	}
}

func TestTaskSnapshotToolResultKeepsRunningTerminalCursorInMetaOnly(t *testing.T) {
	t.Parallel()

	result := taskSnapshotToolResult(
		tool.Call{ID: "call-1", Name: shell.RunCommandToolName},
		tool.Definition{Name: shell.RunCommandToolName},
		taskapi.Snapshot{
			Ref: taskapi.Ref{
				SessionID:  "session-1",
				TaskID:     "task-1",
				TerminalID: "terminal-1",
			},
			Terminal:       sandbox.TerminalRef{TerminalID: "terminal-1"},
			State:          taskapi.StateRunning,
			Running:        true,
			StdoutCursor:   12,
			StderrCursor:   3,
			SupportsInput:  true,
			SupportsCancel: true,
			Result: map[string]any{
				"latest_output": "line A\nline B\n",
				"result":        "already shown\nline A\nline B\n",
			},
		},
	)

	taskMeta := testToolResultRuntimeMeta(t, result, "task")
	if got := taskMeta["terminal_id"]; got != "terminal-1" {
		t.Fatalf("metadata terminal_id = %#v, want terminal-1", got)
	}
	if got := taskMeta["output_cursor"]; got != int64(len([]byte("already shown\nline A\nline B\n"))) {
		t.Fatalf("metadata output_cursor = %#v, want terminal text length", got)
	}
	var payload map[string]any
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want JSON payload", result.Content)
	}
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	if got := payload["terminal_id"]; got != nil {
		t.Fatalf("payload[terminal_id] = %#v, want omitted from model payload", got)
	}
	if got := payload["stdout_cursor"]; got != nil {
		t.Fatalf("payload[stdout_cursor] = %#v, want omitted from model payload", got)
	}
	if got := payload["stderr_cursor"]; got != nil {
		t.Fatalf("payload[stderr_cursor] = %#v, want omitted from model payload", got)
	}
	if got, _ := payload["latest_output"].(string); got != "line A\nline B\n" {
		t.Fatalf("payload[latest_output] = %q, want running terminal delta", got)
	}
	if _, exists := payload["result"]; exists {
		t.Fatalf("payload[result] = %#v, want omitted while running", payload["result"])
	}
	if _, exists := payload["supports_input"]; exists {
		t.Fatalf("payload[supports_input] = %#v, want omitted", payload["supports_input"])
	}
}

func TestTaskSnapshotToolResultSimplifiesSubagentPayload(t *testing.T) {
	t.Parallel()

	result := taskSnapshotToolResult(
		tool.Call{ID: "call-1", Name: spawn.ToolName},
		tool.Definition{Name: spawn.ToolName},
		taskapi.Snapshot{
			Ref:     taskapi.Ref{TaskID: "task-1", SessionID: "child-session"},
			Kind:    taskapi.KindSubagent,
			State:   taskapi.StateCompleted,
			Running: false,
			Metadata: map[string]any{
				"prompt": "summarize startup output",
			},
			Result: map[string]any{
				"handle":  "jeff",
				"mention": "@jeff",
				"agent":   "codex",
				"result":  "done",
			},
		},
	)
	var payload map[string]any
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want JSON payload", result.Content)
	}
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	if got := payload["task_id"]; got != "jeff" {
		t.Fatalf("payload[task_id] = %#v, want handle jeff", got)
	}
	if got := payload["final_message"]; got != "done" {
		t.Fatalf("payload[final_message] = %#v, want done", got)
	}
	taskMeta := testToolResultRuntimeMeta(t, result, "task")
	if got := taskMeta["task_id"]; got != "jeff" {
		t.Fatalf("metadata task_id = %#v, want handle jeff", got)
	}
	if got := taskMeta["prompt"]; got != "summarize startup output" {
		t.Fatalf("metadata prompt = %#v, want prompt preserved for SPAWN display", got)
	}
	if _, ok := payload["prompt"]; ok {
		t.Fatalf("payload contains prompt: %#v", payload)
	}
	for _, key := range []string{"result", "running", "supports_cancel"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("payload contains %q: %#v", key, payload)
		}
	}
	for _, key := range []string{"handle", "mention", "agent", "agent_id", "internal_task_id", "terminal_id"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("payload contains %q: %#v", key, payload)
		}
		if _, ok := taskMeta[key]; ok {
			t.Fatalf("metadata contains %q: %#v", key, taskMeta)
		}
	}
}

func TestTaskSnapshotToolResultKeepsSubagentTerminalRefInMetaOnly(t *testing.T) {
	t.Parallel()

	result := taskSnapshotToolResult(
		tool.Call{ID: "call-1", Name: spawn.ToolName},
		tool.Definition{Name: spawn.ToolName},
		taskapi.Snapshot{
			Ref: taskapi.Ref{
				TaskID:     "task-1",
				SessionID:  "root-session",
				TerminalID: "subagent-task-1",
			},
			Kind:         taskapi.KindSubagent,
			State:        taskapi.StateRunning,
			Running:      true,
			StdoutCursor: 42,
			Result: map[string]any{
				"output_preview": "child is working",
			},
		},
	)
	taskMeta := testToolResultRuntimeMeta(t, result, "task")
	if got := taskMeta["terminal_id"]; got != "subagent-task-1" {
		t.Fatalf("metadata terminal_id = %#v, want subagent-task-1", got)
	}
	if got := taskMeta["output_cursor"]; got != int64(42) {
		t.Fatalf("metadata output_cursor = %#v, want 42", got)
	}
	var payload map[string]any
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want JSON payload", result.Content)
	}
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	for _, key := range []string{"terminal_id", "output_cursor"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("payload contains %q: %#v", key, payload)
		}
	}
}

func TestRuntimeTaskToolResolvesSubagentHandle(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	runtime.tasks.mu.Lock()
	runtime.tasks.subagents["task-1"] = &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "child-session", TerminalID: "subagent-task-1"},
		sessionRef: activeSession.SessionRef,
		agent:      "codex",
		handle:     "ella",
		createdAt:  time.Now(),
		state:      taskapi.StateCompleted,
		running:    false,
		result: map[string]any{
			"handle": "ella",
			"result": "done",
		},
		metadata: map[string]any{
			"handle": "ella",
		},
	}
	runtime.tasks.mu.Unlock()

	result := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":  "wait",
		"task_id": "ella",
	})
	var payload map[string]any
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("task result content = %#v, want json payload", result.Content)
	}
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	if got := payload["task_id"]; got != "ella" {
		t.Fatalf("payload[task_id] = %#v, want handle ella", got)
	}
	taskMeta := testToolResultRuntimeMeta(t, result, "task")
	if _, ok := taskMeta["internal_task_id"]; ok {
		t.Fatalf("metadata internal_task_id = %#v, want omitted", taskMeta["internal_task_id"])
	}
}

func TestRuntimeTaskToolScopesSubagentHandleToSession(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	runtime.tasks.mu.Lock()
	for i := 0; i < 32; i++ {
		taskID := fmt.Sprintf("other-task-%02d", i)
		runtime.tasks.subagents[taskID] = &subagentTask{
			ref:        taskapi.Ref{TaskID: taskID, SessionID: "other-child"},
			sessionRef: session.SessionRef{SessionID: "other-session"},
			handle:     "ella",
			state:      taskapi.StateCompleted,
			result:     map[string]any{"handle": "ella", "result": "wrong"},
			metadata:   map[string]any{"handle": "ella"},
		}
	}
	runtime.tasks.subagents["task-current"] = &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-current", SessionID: "child-session"},
		sessionRef: activeSession.SessionRef,
		handle:     "ella",
		state:      taskapi.StateCompleted,
		result:     map[string]any{"handle": "ella", "result": "right"},
		metadata:   map[string]any{"handle": "ella"},
	}
	runtime.tasks.mu.Unlock()

	result := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":  "wait",
		"task_id": "ella",
	})
	var payload map[string]any
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("task result content = %#v, want json payload", result.Content)
	}
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	if got := payload["final_message"]; got != "right" {
		t.Fatalf("payload[final_message] = %#v, want current-session final message", got)
	}
}

func TestRuntimeTaskToolRejectsAmbiguousSubagentHandle(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	runtime.tasks.mu.Lock()
	runtime.tasks.subagents["task-a"] = &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-a", SessionID: "child-a"},
		sessionRef: activeSession.SessionRef,
		handle:     "ella",
		state:      taskapi.StateCompleted,
		result:     map[string]any{"handle": "ella", "result": "first"},
		metadata:   map[string]any{"handle": "ella"},
	}
	runtime.tasks.subagents["task-b"] = &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-b", SessionID: "child-b"},
		sessionRef: activeSession.SessionRef,
		handle:     "ella",
		state:      taskapi.StateCompleted,
		result:     map[string]any{"handle": "ella", "result": "second"},
		metadata:   map[string]any{"handle": "ella"},
	}
	runtime.tasks.mu.Unlock()

	target := runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	raw, err := json.Marshal(map[string]any{
		"action":  "wait",
		"task_id": "ella",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	_, err = target.Call(context.Background(), tool.Call{ID: "task-wait", Name: tasktool.ToolName, Input: raw})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("TASK wait ambiguous handle error = %v, want ambiguous", err)
	}
}

func TestRuntimeRunCommandToolDoesNotFetchResultWhileStillRunning(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &runningOnlyProbeSandboxRuntime{session: &runningOnlyProbeSandboxSession{}}
	targetTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeRunCommandTool(t, targetTool, map[string]any{
		"command": "printf 'still-running'",
		"workdir": activeSession.CWD,
	})

	if got := fake.session.lastWait; got != defaultCommandYield {
		t.Fatalf("omitted yield wait = %v, want %v", got, defaultCommandYield)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestResolveSpawnAgentAllowsRegisteredAgentNameWithoutSessionParticipant(t *testing.T) {
	activeSession := session.Session{}
	if got, err := resolveSpawnAgent(activeSession, ""); err != nil || got != "self" {
		t.Fatalf("resolveSpawnAgent(empty) = %q, %v; want self", got, err)
	}
	if got, err := resolveSpawnAgent(activeSession, "self"); err != nil || got != "self" {
		t.Fatalf("resolveSpawnAgent(self) = %q, %v; want self", got, err)
	}
	if got, err := resolveSpawnAgent(activeSession, "codex"); err != nil || got != "codex" {
		t.Fatalf("resolveSpawnAgent(codex) = %q, %v; want codex", got, err)
	}
}
