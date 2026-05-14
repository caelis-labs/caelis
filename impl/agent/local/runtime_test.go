package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	"github.com/OnslaughtSnail/caelis/impl/policy/presets"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	taskfile "github.com/OnslaughtSnail/caelis/impl/task/file"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/filesystem"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/plan"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/shell"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	tasktool "github.com/OnslaughtSnail/caelis/impl/tool/builtin/task"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

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

func TestAppendNarrativeTextKeepsTrueDeltaOverlap(t *testing.T) {
	t.Parallel()

	cumulative, delta := appendNarrativeText("hel", "lo")
	if cumulative != "hello" || delta != "lo" {
		t.Fatalf("append delta = (%q, %q), want (hello, lo)", cumulative, delta)
	}

	cumulative, delta = appendNarrativeText("hel", "hello")
	if cumulative != "hello" || delta != "lo" {
		t.Fatalf("append cumulative = (%q, %q), want (hello, lo)", cumulative, delta)
	}

	cumulative, delta = appendNarrativeText("hello", "hel")
	if cumulative != "hello" || delta != "" {
		t.Fatalf("append stale prefix = (%q, %q), want (hello, empty)", cumulative, delta)
	}
}

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
			case event.Protocol != nil && event.Protocol.UpdateType == string(session.ProtocolUpdateTypeAgentMessage) && session.EventText(event) == "hel":
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
		Kind: agent.SubmissionKindConversation,
		Text: "steer next step",
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
	if err := result.Handle.Submit(agent.Submission{Kind: agent.SubmissionKindConversation, Text: "too late"}); err == nil {
		t.Fatal("Submit() after runner completion error = nil, want closed-runner error")
	}
}

func TestRuntimePersistsInterruptedAssistantReplaySnapshot(t *testing.T) {
	t.Parallel()

	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-interrupted-replay" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-interrupted-replay",
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
			UpdateType: thoughtType,
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
			UpdateType: answerType,
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
		RunIDGenerator: func() string { return "run-interrupted-replay" },
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
	var replay *session.Event
	for _, event := range transcript {
		if session.EventTypeOf(event) == session.EventTypeAssistant && event.Visibility == session.VisibilityMirror {
			replay = event
			break
		}
	}
	if replay == nil {
		t.Fatalf("transcript events = %#v, want mirror replay snapshot", transcript)
	}
	if got := session.EventText(replay); got != "partial answer" {
		t.Fatalf("mirror replay text = %q, want partial answer", got)
	}
	update := session.ProtocolUpdateOf(replay)
	if update == nil {
		t.Fatalf("mirror replay protocol update = nil")
	}
	content, _ := update.Content.(map[string]any)
	if got, _ := content["reasoningText"].(string); got != "partial thought" {
		t.Fatalf("mirror replay reasoning content = %#v, want partial thought", update.Content)
	}
	if session.IsInvocationVisibleEvent(replay) {
		t.Fatalf("mirror replay snapshot must not be invocation-visible: %#v", replay)
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
						UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
					},
				}))
				<-releaseFinal
				handle.publishEvent(&session.Event{
					Type:       session.EventTypeAssistant,
					Visibility: session.VisibilityCanonical,
					Text:       "hello",
					Protocol: &session.EventProtocol{
						UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
					},
				})
				handle.finish()
			}()
			return controller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  testController,
		RunIDGenerator: func() string {
			return "run-acp-live"
		},
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
			case event.Protocol != nil && event.Protocol.UpdateType == string(session.ProtocolUpdateTypeAgentMessage) && event.Visibility == session.VisibilityUIOnly:
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
	if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Text:       "side result",
			Actor:      session.ActorRef{Kind: session.ActorKindParticipant, Name: "jeff"},
			Scope: &session.EventScope{
				Participant: session.ParticipantRef{
					ID:   "side-1",
					Kind: session.ParticipantKindSubagent,
					Role: session.ParticipantRoleSidecar,
				},
			},
		},
	}); err != nil {
		t.Fatalf("AppendEvent(side) error = %v", err)
	}

	turnReqCh := make(chan controller.TurnRequest, 1)
	testController := stubACPController{
		runTurn: func(ctx context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
			turnReqCh <- req
			handle := newTestControllerTurnHandle(nil)
			go func() {
				handle.publishEvent(&session.Event{
					Type:       session.EventTypeAssistant,
					Visibility: session.VisibilityCanonical,
					Text:       "main done",
					Protocol: &session.EventProtocol{
						UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
					},
				})
				handle.finish()
			}()
			return controller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  testController,
	})
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

func TestRuntimePromptACPParticipantPersistsPublicDialogue(t *testing.T) {
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
						UpdateType: string(session.ProtocolUpdateTypeUserMessage),
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
						UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
					},
				})
			}()
			return controller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  testController,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	updated, err := runtime.PromptACPParticipant(context.Background(), agent.PromptACPParticipantRequest{
		SessionRef:    activeSession.SessionRef,
		ParticipantID: "emma",
		Input:         "刚才都做了什么？总结一下",
		Source:        "tui_agent_ask",
	})
	if err != nil {
		t.Fatalf("PromptACPParticipant() error = %v", err)
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

func TestRuntimePromptACPParticipantCancelCancelsControllerTurn(t *testing.T) {
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
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  testController,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.PromptACPParticipant(context.Background(), agent.PromptACPParticipantRequest{
		SessionRef:    activeSession.SessionRef,
		ParticipantID: "emma",
		Input:         "stop me",
		Source:        "slash_claude",
	})
	if err != nil {
		t.Fatalf("PromptACPParticipant() error = %v", err)
	}
	select {
	case <-turnReqCh:
	case <-time.After(2 * time.Second):
		t.Fatal("participant prompt request was not sent")
	}
	if result.Handle == nil {
		t.Fatal("PromptACPParticipant() handle = nil")
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
				handle.publishEvent(acpControllerChunk("hello"))
				handle.finish()
			}()
			return controller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(Config{
		Sessions:       sessions,
		AgentFactory:   chat.Factory{SystemPrompt: "Be terse."},
		Controllers:    testController,
		RunIDGenerator: func() string { return "run-acp-deltas" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
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
		if event.Protocol.UpdateType == string(session.ProtocolUpdateTypeAgentMessage) && strings.HasPrefix(event.Scope.Source, "acp") && event.Visibility == session.VisibilityUIOnly {
			liveTexts = append(liveTexts, session.EventText(event))
			if event.SessionID != activeSession.SessionID {
				t.Fatalf("live ACP chunk session ID = %q, want %q", event.SessionID, activeSession.SessionID)
			}
			if strings.TrimSpace(event.ID) != "" {
				t.Fatalf("live ACP chunk ID = %q, want empty live event ID", event.ID)
			}
		}
	}
	if !reflect.DeepEqual(liveTexts, []string{"hel", "lo"}) {
		t.Fatalf("live ACP texts = %#v, want delta chunks", liveTexts)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var persistedTexts []string
	for _, event := range loaded.Events {
		if event == nil || event.Protocol == nil || event.Scope == nil {
			continue
		}
		if event.Protocol.UpdateType == string(session.ProtocolUpdateTypeAgentMessage) && strings.HasPrefix(event.Scope.Source, "acp") {
			persistedTexts = append(persistedTexts, session.EventText(event))
			if strings.TrimSpace(event.ID) == "" {
				t.Fatalf("persisted ACP chunk missing event ID")
			}
		}
	}
	if !reflect.DeepEqual(persistedTexts, []string{"hello"}) {
		t.Fatalf("persisted ACP texts = %#v, want final assistant snapshot only", persistedTexts)
	}
}

func TestBuildControllerHandoffContextUsesSharedDialogueOnly(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-handoff-shared-ledger")
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	activeSession, err = sessions.PutParticipant(context.Background(), session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "participant-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			Label:        "@ella",
			AgentName:    "codex",
			DelegationID: "task-1",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}
	events := []*session.Event{
		{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Text: "user prompt"},
		{Type: session.EventTypeToolResult, Visibility: session.VisibilityCanonical, Text: "tool output"},
		{Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Text: "child answer", Actor: session.ActorRef{Kind: session.ActorKindParticipant, Name: "ella"}, Scope: &session.EventScope{Participant: session.ParticipantRef{ID: "participant-1", Kind: session.ParticipantKindSubagent}}},
		session.MarkUIOnly(&session.Event{Type: session.EventTypeAssistant, Text: "live chunk"}),
	}
	for _, event := range events {
		if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: activeSession.SessionRef, Event: event}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	text, seq := runtime.buildControllerHandoffContext(context.Background(), activeSession, activeSession.SessionRef, session.ControllerBinding{
		Kind:           session.ControllerKindACP,
		Label:          "old",
		ContextSyncSeq: 4,
	}, 0, "")
	if seq != 3 {
		t.Fatalf("context seq = %d, want latest shared event checkpoint 3", seq)
	}
	for _, want := range []string{"shared_ledger_checkpoint: 3", "shared_dialogue_delta:", "[1] user:\nuser prompt", "[3] assistant(ella):\nchild answer", "- @ella agent=codex"} {
		if !strings.Contains(text, want) {
			t.Fatalf("handoff context missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"canonical_tail", "tool output", "live chunk", "task-1"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("handoff context should not contain %q:\n%s", forbidden, text)
		}
	}
}

func TestSharedDialogueDeltaUsesCheckpointAndCompactBoundary(t *testing.T) {
	t.Parallel()

	compactMessage := model.NewTextMessage(model.RoleUser, "CONTEXT CHECKPOINT\nObjective: compacted baseline")
	events := []*session.Event{
		userTextEvent("old user"),
		assistantEvent("old assistant"),
		{
			Type:       session.EventTypeCompact,
			Visibility: session.VisibilityCanonical,
			Message:    &compactMessage,
			Text:       compactMessage.TextContent(),
		},
		userTextEvent("fresh user"),
		assistantEvent("fresh assistant"),
	}

	first := sharedDialogueDeltaFromEvents(events, 0)
	if first.Checkpoint != 5 {
		t.Fatalf("checkpoint = %d, want 5", first.Checkpoint)
	}
	rendered := renderSharedDialogueDeltaForTest(first)
	for _, want := range []string{
		"[3] compact:\nCONTEXT CHECKPOINT\nObjective: compacted baseline",
		"[4] user:\nfresh user",
		"[5] assistant:\nfresh assistant",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("delta missing %q:\n%s", want, rendered)
		}
	}
	for _, forbidden := range []string{"old user", "old assistant"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("delta should not replay pre-compact %q:\n%s", forbidden, rendered)
		}
	}

	empty := sharedDialogueDeltaFromEvents(events, first.Checkpoint)
	if len(empty.Entries) != 0 || empty.Checkpoint != first.Checkpoint {
		t.Fatalf("empty delta = %+v, want no repeated entries at checkpoint %d", empty, first.Checkpoint)
	}

	next := sharedDialogueDeltaFromEvents(append(events, userTextEvent("next user")), first.Checkpoint)
	rendered = renderSharedDialogueDeltaForTest(next)
	if strings.Contains(rendered, "fresh user") || strings.Contains(rendered, "fresh assistant") || !strings.Contains(rendered, "[6] user:\nnext user") {
		t.Fatalf("incremental delta should include only new event:\n%s", rendered)
	}
}

func renderSharedDialogueDeltaForTest(delta sharedDialogueDelta) string {
	var b strings.Builder
	appendSharedDialogueDelta(&b, delta)
	return b.String()
}

func TestRuntimeRunAppliesAssemblyModeAndConfigOverridesFromSessionState(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-assembly-overrides")
	if err := sessions.UpdateState(context.Background(), activeSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		state = assembly.SetCurrentModeID(state, "plan")
		state = assembly.SetCurrentConfigValue(state, "reasoning", "deep")
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	factory := &attemptFactory{
		agents: []agent.Agent{seqAgent{events: []*session.Event{assistantEvent("ok")}}},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: factory,
		Assembly: assembly.ResolvedAssembly{
			Modes: []assembly.ModeConfig{
				{
					ID: "default",
					Runtime: assembly.RuntimeOverrides{
						PolicyMode:   "default",
						SystemPrompt: "mode-default-marker",
					},
				},
				{
					ID: "plan",
					Runtime: assembly.RuntimeOverrides{
						PolicyMode:   "plan",
						SystemPrompt: "mode-plan-marker",
					},
				},
			},
			Configs: []assembly.ConfigOption{{
				ID:           "reasoning",
				DefaultValue: "balanced",
				Options: []assembly.ConfigSelectOption{
					{
						Value: "balanced",
						Runtime: assembly.RuntimeOverrides{
							Reasoning: model.ReasoningConfig{Effort: "medium"},
						},
					},
					{
						Value: "deep",
						Runtime: assembly.RuntimeOverrides{
							Reasoning: model.ReasoningConfig{Effort: "high"},
						},
					},
				},
			}},
		},
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
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	specs := factory.Specs()
	if got, want := len(specs), 1; got != want {
		t.Fatalf("factory specs len = %d, want %d", got, want)
	}
	spec := specs[0]
	if got := strings.TrimSpace(spec.Metadata["policy_mode"].(string)); got != "plan" {
		t.Fatalf("policy_mode = %q, want %q", got, "plan")
	}
	if got := strings.TrimSpace(spec.Metadata["system_prompt"].(string)); got != "mode-plan-marker" {
		t.Fatalf("system_prompt = %q, want %q", got, "mode-plan-marker")
	}
	if got := strings.TrimSpace(spec.Metadata["reasoning_effort"].(string)); got != "high" {
		t.Fatalf("reasoning_effort = %q, want %q", got, "high")
	}
}

func TestRuntimeRunAppliesConfigOverridesInDeclaredOrder(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-assembly-order")
	if err := sessions.UpdateState(context.Background(), activeSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		state = assembly.SetCurrentConfigValue(state, "first", "on")
		state = assembly.SetCurrentConfigValue(state, "second", "on")
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	factory := &attemptFactory{
		agents: []agent.Agent{seqAgent{events: []*session.Event{assistantEvent("ok")}}},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: factory,
		Assembly: assembly.ResolvedAssembly{
			Configs: []assembly.ConfigOption{
				{
					ID: "first",
					Options: []assembly.ConfigSelectOption{{
						Value: "on",
						Runtime: assembly.RuntimeOverrides{
							SystemPrompt: "first-prompt",
						},
					}},
				},
				{
					ID: "second",
					Options: []assembly.ConfigSelectOption{{
						Value: "on",
						Runtime: assembly.RuntimeOverrides{
							SystemPrompt: "second-prompt",
						},
					}},
				},
			},
		},
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
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	specs := factory.Specs()
	if got, want := len(specs), 1; got != want {
		t.Fatalf("factory specs len = %d, want %d", got, want)
	}
	if got := strings.TrimSpace(specs[0].Metadata["system_prompt"].(string)); got != "second-prompt" {
		t.Fatalf("system_prompt = %q, want %q", got, "second-prompt")
	}
}

func TestNewRejectsMixedAssemblyAndExplicitControlPlane(t *testing.T) {
	t.Parallel()

	sessions, _ := newTestSessionService(t, "sess-mixed-control-plane")
	_, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Assembly: assembly.ResolvedAssembly{
			Agents: []assembly.AgentConfig{{
				Name:    "self",
				Command: "bash",
				Args:    []string{"-lc", "echo ok"},
			}},
		},
		Controllers: stubACPController{},
	})
	if err == nil {
		t.Fatal("expected mixed assembly/control-plane config to fail")
	}
	if !strings.Contains(err.Error(), "Assembly.Agents cannot be combined") {
		t.Fatalf("New() error = %v, want mixed-configuration rejection", err)
	}
}

type stubACPController struct {
	runTurn           func(context.Context, controller.TurnRequest) (controller.TurnResult, error)
	promptParticipant func(context.Context, controller.ParticipantPromptRequest) (controller.TurnResult, error)
}

func (stubACPController) Activate(context.Context, controller.HandoffRequest) (session.ControllerBinding, error) {
	return session.ControllerBinding{}, nil
}

func (stubACPController) Deactivate(context.Context, session.SessionRef) error {
	return nil
}

func (s stubACPController) RunTurn(ctx context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
	if s.runTurn != nil {
		return s.runTurn(ctx, req)
	}
	handle := newTestControllerTurnHandle(nil)
	handle.finish()
	return controller.TurnResult{Handle: handle}, nil
}

func (stubACPController) Attach(context.Context, controller.AttachRequest) (session.ParticipantBinding, error) {
	return session.ParticipantBinding{}, nil
}

func (s stubACPController) PromptParticipant(ctx context.Context, req controller.ParticipantPromptRequest) (controller.TurnResult, error) {
	if s.promptParticipant != nil {
		return s.promptParticipant(ctx, req)
	}
	handle := newTestControllerTurnHandle(nil)
	handle.finish()
	return controller.TurnResult{Handle: handle}, nil
}

func (stubACPController) Detach(context.Context, controller.DetachRequest) error {
	return nil
}

type testControllerTurnHandle struct {
	cancelFn  context.CancelFunc
	eventsCh  chan testControllerTurnEvent
	closeOnce sync.Once
	mu        sync.Mutex
	cancelled bool
}

type testControllerTurnEvent struct {
	event *session.Event
	err   error
}

func newTestControllerTurnHandle(cancel context.CancelFunc) *testControllerTurnHandle {
	return &testControllerTurnHandle{
		cancelFn: cancel,
		eventsCh: make(chan testControllerTurnEvent, 16),
	}
}

func (h *testControllerTurnHandle) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for item := range h.eventsCh {
			if !yield(session.CloneEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (h *testControllerTurnHandle) Cancel() controller.CancelResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelled {
		return controller.CancelResult{Status: controller.CancelStatusAlreadyCancelled}
	}
	h.cancelled = true
	if h.cancelFn != nil {
		h.cancelFn()
	}
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
}

func (h *testControllerTurnHandle) Close() error { return nil }

func (h *testControllerTurnHandle) publishEvent(event *session.Event) {
	if h == nil || event == nil {
		return
	}
	h.eventsCh <- testControllerTurnEvent{event: session.CloneEvent(event)}
}

func (h *testControllerTurnHandle) publishError(err error) {
	if h == nil || err == nil {
		return
	}
	h.eventsCh <- testControllerTurnEvent{err: err}
}

func (h *testControllerTurnHandle) finish() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		close(h.eventsCh)
	})
}

func TestRuntimeRunFallsBackToDefaultForStaleConfigValue(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-assembly-stale")
	if err := sessions.UpdateState(context.Background(), activeSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		state = assembly.SetCurrentConfigValue(state, "reasoning", "stale")
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	factory := &attemptFactory{
		agents: []agent.Agent{seqAgent{events: []*session.Event{assistantEvent("ok")}}},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: factory,
		Assembly: assembly.ResolvedAssembly{
			Configs: []assembly.ConfigOption{{
				ID:           "reasoning",
				DefaultValue: "balanced",
				Options: []assembly.ConfigSelectOption{
					{
						Value: "balanced",
						Runtime: assembly.RuntimeOverrides{
							SystemPrompt: "balanced-prompt",
							Reasoning:    model.ReasoningConfig{Effort: "medium"},
						},
					},
					{
						Value: "deep",
						Runtime: assembly.RuntimeOverrides{
							SystemPrompt: "deep-prompt",
							Reasoning:    model.ReasoningConfig{Effort: "high"},
						},
					},
				},
			}},
		},
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
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	specs := factory.Specs()
	if got, want := len(specs), 1; got != want {
		t.Fatalf("factory specs len = %d, want %d", got, want)
	}
	spec := specs[0]
	if got := strings.TrimSpace(spec.Metadata["system_prompt"].(string)); got != "balanced-prompt" {
		t.Fatalf("system_prompt = %q, want %q", got, "balanced-prompt")
	}
	if got := strings.TrimSpace(spec.Metadata["reasoning_effort"].(string)); got != "medium" {
		t.Fatalf("reasoning_effort = %q, want %q", got, "medium")
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

func TestRuntimeCompactionInjectsCheckpointAndTrimsOldHistory(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-heuristic")
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Project objective: build compact runtime. Constraint: do not lose blocker continuity."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack objective"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Current blocker: provider intermittently returns 529 overloaded_error when histories get too large."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack blocker"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Next action: validate with real e2e tests and tune the compact prompt."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack next"))

	testModel := &contextProbeModel{
		t: t,
		wantMessageContains: []string{
			"CONTEXT CHECKPOINT",
			"build compact runtime",
			"529 overloaded_error",
		},
		wantMessagesOmit: []string{
			"Project objective: build compact runtime",
			"Current blocker: provider intermittently returns 529 overloaded_error",
		},
		replyText: "checkpoint ok",
	}

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 64,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	if testModel.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", testModel.compactionCalls)
	}
	if testModel.normalCalls != 1 {
		t.Fatalf("normalCalls = %d, want 1", testModel.normalCalls)
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	sawCompact := false
	var compactText string
	for _, event := range loaded.Events {
		if event != nil && event.Type == session.EventTypeCompact {
			sawCompact = true
			compactText = strings.TrimSpace(session.EventText(event))
			break
		}
	}
	if !sawCompact {
		t.Fatal("expected durable compact event in session history")
	}
	if !strings.Contains(compactText, "build compact runtime") {
		t.Fatalf("compact event text = %q, want compact objective", compactText)
	}
}

func TestRuntimeCompactionUsesModelGeneratedCheckpoint(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-model")
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Project objective: preserve context continuity during very long coding sessions."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Current blocker: checkpoint quality drops when summaries become too generic."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Next action: run realistic compact e2e tests and tune the summary prompt."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))

	testModel := &modelCheckpointProbe{
		t: t,
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 64,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	if testModel.compactionCalls == 0 {
		t.Fatal("expected at least one model-backed compaction call")
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var compactText string
	for _, event := range loaded.Events {
		if event != nil && event.Type == session.EventTypeCompact {
			compactText = strings.TrimSpace(session.EventText(event))
		}
	}
	if !strings.Contains(compactText, "model checkpoint objective") {
		t.Fatalf("compact event text = %q, want model-generated checkpoint objective", compactText)
	}
	compactEvent, ok := latestCompactEventForTest(loaded.Events)
	if !ok {
		t.Fatal("expected compact event in durable history")
	}
	data, ok := compact.CompactEventDataFromEvent(compactEvent)
	if !ok {
		t.Fatal("expected compact event metadata")
	}
	promptEvents := compact.PromptEventsFromLatestCompact(loaded.Events)
	if len(promptEvents) == 0 || !strings.Contains(strings.ToLower(session.EventText(promptEvents[0])), "model checkpoint objective") {
		t.Fatalf("prompt events after compact = %+v, want pure text checkpoint overlay", promptEvents)
	}
	if promptEvents[0].Message != nil || promptEvents[0].Protocol != nil {
		t.Fatalf("checkpoint overlay should stay pure text, got message=%+v protocol=%+v", promptEvents[0].Message, promptEvents[0].Protocol)
	}
	if data.Revision <= 0 {
		t.Fatalf("compact revision = %d, want > 0", data.Revision)
	}
	if data.ContractVersion != compact.CompactContractVersion {
		t.Fatalf("compact contract version = %d, want %d", data.ContractVersion, compact.CompactContractVersion)
	}
	if data.SourceEventCount == 0 {
		t.Fatalf("compact source event count = %d, want > 0", data.SourceEventCount)
	}
}

func TestRuntimeManualCompactUsesPureTextCheckpointOverlay(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-manual")
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Project objective: make manual compact preserve context instead of truncating history."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack objective"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Current blocker: bare compact events cause prompt replay to drop all prior context."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack blocker"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Next action: route manual compact through the model-backed compactor."))

	testModel := &contextProbeModel{
		t: t,
		wantCompactionInputContains: []string{
			"make manual compact preserve context",
		},
		compactBody: `CONTEXT CHECKPOINT

## Current Objective
- make manual compact preserve context instead of truncating history

## User Constraints And Corrections
- keep user-facing compact handoff as structured Markdown, not JSON

## Current Plan And Progress
- manual compact is being aligned with auto compact

## Key Files And Facts
- impl/agent/local/compaction.go:940-1120 owns checkpoint overlay rendering
- license.go:30-100 is a line-index fact that must survive checkpoint overlay

## Validation And Tool Results
- not run yet

## Open Questions Or Risks
- compact events without checkpoint overlay must not be emitted

## Next Actions
1. route manual compact through the model-backed compactor`,
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			SegmentTokenBudget: 80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Compact(context.Background(), CompactRequest{
		SessionRef: activeSession.SessionRef,
		Model:      testModel,
		Trigger:    "manual",
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if !result.Compacted {
		t.Fatal("Compact() did not compact")
	}
	if testModel.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", testModel.compactionCalls)
	}
	if testModel.normalCalls != 0 {
		t.Fatalf("normalCalls = %d, want 0", testModel.normalCalls)
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	compactEvent, ok := latestCompactEventForTest(loaded.Events)
	if !ok {
		t.Fatal("expected compact event")
	}
	data, ok := compact.CompactEventDataFromEvent(compactEvent)
	if !ok {
		t.Fatalf("compact event missing structured metadata: %+v", compactEvent.Meta)
	}
	if data.Trigger != "manual" {
		t.Fatalf("compact trigger = %q, want manual", data.Trigger)
	}
	if data.ContractVersion != compact.CompactContractVersion || data.SourceEventCount == 0 {
		t.Fatalf("compact metadata = version:%d source:%d, want contract metadata", data.ContractVersion, data.SourceEventCount)
	}
	promptEvents := compact.PromptEventsFromLatestCompact(loaded.Events)
	if len(promptEvents) == 0 {
		t.Fatal("prompt events empty after manual compact")
	}
	promptText := strings.Join(eventTextsForTest(promptEvents), "\n")
	if strings.Contains(promptText, "Project objective: make manual compact preserve context instead of truncating history.") {
		t.Fatalf("prompt events still replay raw pre-compact history: %+v", promptEvents)
	}
	for _, needle := range []string{
		"## Current Objective",
		"## Key Files And Facts",
		"license.go:30-100",
	} {
		if !strings.Contains(promptText, needle) {
			t.Fatalf("prompt events missing raw markdown checkpoint detail %q: %q", needle, promptText)
		}
	}
	if strings.Contains(promptText, "Objective: make manual compact preserve context instead of truncating history") {
		t.Fatalf("prompt events reconstructed labeled checkpoint fields instead of preserving markdown: %q", promptText)
	}
}

func TestRuntimeManualCompactIncludesConfirmedUserMessage(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-user-confirm")
	oldCompact := buildCompactEvent(activeSession, `CONTEXT CHECKPOINT

## Current Objective
- Remove gm_license legacy behavior

## Next Actions
1. wait for explicit implementation approval`, compact.CompactEventData{
		ContractVersion: compact.CompactContractVersion,
		Generator:       "model_markdown",
		Trigger:         "manual",
	})
	appendTestEvent(t, sessions, activeSession.SessionRef, oldCompact)
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("Plan prepared. Next action: wait for user confirmation before writing code."))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("开始实现"))

	testModel := &contextProbeModel{
		t: t,
		wantCompactionInputContains: []string{
			"# Existing Compact Checkpoint (reference only)",
			"wait for user confirmation before writing code",
			"开始实现",
		},
		compactBody: `CONTEXT CHECKPOINT

## Current Objective
- Implement the compact optimization now.

## User Constraints And Corrections
- 用户已经发送“开始实现”，下一步应立即实现，不再等待确认。

## Current Plan And Progress
- Plan was prepared before compact.

## Next Actions
1. Start editing impl/agent/local/compaction.go.`,
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			SegmentTokenBudget: 80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Compact(context.Background(), CompactRequest{
		SessionRef: activeSession.SessionRef,
		Model:      testModel,
		Trigger:    "manual",
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if !result.Compacted {
		t.Fatal("Compact() did not compact")
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	promptText := strings.Join(eventTextsForTest(compact.PromptEventsFromLatestCompact(loaded.Events)), "\n")
	for _, needle := range []string{"开始实现", "下一步应立即实现", "Start editing impl/agent/local/compaction.go"} {
		if !strings.Contains(promptText, needle) {
			t.Fatalf("prompt after compact missing %q: %q", needle, promptText)
		}
	}
	if strings.Contains(promptText, "Remove gm_license legacy behavior") {
		t.Fatalf("prompt after compact retained stale old checkpoint objective: %q", promptText)
	}
}

func TestRuntimeCompactionReplaysFromEventsAfterReload(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-compact-replay" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-compact-replay",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Project objective: replay compacted history strictly from append-only events."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Current blocker: raw transcript replay grows too large under long sessions."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Next action: verify reload from file-backed events only."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))

	runtime1, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 64,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New(runtime1) error = %v", err)
	}

	result1, err := runtime1.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue",
		AgentSpec: agent.AgentSpec{
			Name: "chat",
			Model: &contextProbeModel{
				t:         t,
				replyText: "seed ok",
				compactBody: `CONTEXT CHECKPOINT

Objective: replay compacted history strictly from append-only events
Blocker: raw transcript replay grows too large under long sessions
Next action: verify reload from file-backed events only

## Current Progress
- compact summary persisted as a durable event

## Next Actions
1. verify reload from file-backed events only`,
			},
		},
	})
	if err != nil {
		t.Fatalf("runtime1.Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result1.Handle); err != nil {
		t.Fatalf("runtime1 runner error = %v", err)
	}

	reopenedSessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	reopenedState, err := reopenedSessions.SnapshotState(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if len(reopenedState) != 0 {
		t.Fatalf("reopened state = %v, want compact replay to not depend on session state", reopenedState)
	}
	runtime2, err := New(Config{
		Sessions: reopenedSessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.95,
			ForceWatermarkRatio:        0.99,
			DefaultContextWindowTokens: 4096,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New(runtime2) error = %v", err)
	}

	replayModel := &contextProbeModel{
		t: t,
		wantMessageContains: []string{
			"CONTEXT CHECKPOINT",
			"replay compacted history strictly from append-only events",
			"verify reload from file-backed events only",
		},
		replyText: "replay ok",
	}
	result, err := runtime2.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue after reload",
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
	if finalText != "replay ok" {
		t.Fatalf("final assistant text = %q, want %q", finalText, "replay ok")
	}
}

func TestSnapshotUsageUsesPromptBaselinePlusReplayDelta(t *testing.T) {
	t.Parallel()

	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{
		Enabled:                    true,
		DefaultContextWindowTokens: 32000,
		ReserveOutputTokens:        5000,
		SafetyMarginTokens:         2048,
	})}
	assistant := assistantEvent("Short visible assistant reply.")
	assistant.ID = "assistant-1"
	assistant.Meta = map[string]any{
		"provider":          "stub",
		"model":             "test-model",
		"prompt_tokens":     120,
		"completion_tokens": 900,
		"total_tokens":      1020,
	}
	followUp := userTextEvent("Follow up with the latest status update.")
	followUp.ID = "user-2"
	events := []*session.Event{assistant, followUp}

	usage := compactor.snapshotUsage(compact.Request{}, events)
	want := 120 + estimatePromptEventTokens(assistant) + estimatePromptEventTokens(followUp)
	if usage.TotalTokens != want {
		t.Fatalf("usage.TotalTokens = %d, want %d", usage.TotalTokens, want)
	}
	if usage.Source != compact.UsageSourceProvider {
		t.Fatalf("usage.Source = %q, want provider", usage.Source)
	}
	if usage.AsOfEventID != "assistant-1" {
		t.Fatalf("usage.AsOfEventID = %q, want %q", usage.AsOfEventID, "assistant-1")
	}
}

func TestSnapshotUsageTotalOnlyFallbackDoesNotDoubleCountSnapshotGroup(t *testing.T) {
	t.Parallel()

	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{
		Enabled:                    true,
		DefaultContextWindowTokens: 32000,
	})}
	assistant := assistantEvent("Assistant reply already captured in transcript.")
	assistant.ID = "assistant-1"
	assistant.Meta = map[string]any{
		"provider":     "stub",
		"model":        "test-model",
		"total_tokens": 400,
	}
	followUp := userTextEvent("User turn added after the provider snapshot.")
	followUp.ID = "user-2"
	events := []*session.Event{assistant, followUp}

	usage := compactor.snapshotUsage(compact.Request{}, events)
	want := 400 + estimatePromptEventTokens(followUp)
	if usage.TotalTokens != want {
		t.Fatalf("usage.TotalTokens = %d, want %d", usage.TotalTokens, want)
	}
}

func TestSnapshotUsageClampsEffectiveBudgetForSmallWindows(t *testing.T) {
	t.Parallel()

	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{
		Enabled:                    true,
		DefaultContextWindowTokens: 2048,
		ReserveOutputTokens:        5000,
		SafetyMarginTokens:         2048,
	})}

	usage := compactor.snapshotUsage(compact.Request{}, []*session.Event{userTextEvent("small window probe")})
	if usage.EffectiveInputBudget != 1280 {
		t.Fatalf("usage.EffectiveInputBudget = %d, want %d", usage.EffectiveInputBudget, 1280)
	}
	if usage.EffectiveInputBudget <= 0 || usage.EffectiveInputBudget > usage.ContextWindowTokens {
		t.Fatalf("effective input budget out of range: %+v", usage)
	}
}

func TestSnapshotUsagePreservesConfiguredMarginsForLongWindows(t *testing.T) {
	t.Parallel()

	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{
		Enabled:                    true,
		DefaultContextWindowTokens: 200000,
		ReserveOutputTokens:        5000,
		SafetyMarginTokens:         2048,
	})}

	usage := compactor.snapshotUsage(compact.Request{}, []*session.Event{userTextEvent("long window probe")})
	if usage.EffectiveInputBudget != 192952 {
		t.Fatalf("usage.EffectiveInputBudget = %d, want %d", usage.EffectiveInputBudget, 192952)
	}
}

func TestPrepareCompactionFitsPendingInputWithinBudget(t *testing.T) {
	t.Parallel()

	compactor := &codexStyleCompactor{cfg: normalizeCompactionConfig(CompactionConfig{
		Enabled:                    true,
		WatermarkRatio:             0.6,
		ForceWatermarkRatio:        0.75,
		DefaultContextWindowTokens: 192,
		ReserveOutputTokens:        32,
		SafetyMarginTokens:         16,
		SegmentTokenBudget:         80,
	})}
	events := []*session.Event{
		userTextEvent(strings.Repeat("Objective continuity detail. ", 8)),
		assistantEvent("ack"),
		userTextEvent(strings.Repeat("Most recent blocker and progress detail. ", 8)),
	}
	pending := userTextEvent(strings.Repeat("New user turn that must still fit after compaction. ", 6))

	result, err := compactor.Prepare(context.Background(), compact.Request{
		Session: session.Session{
			SessionRef: session.SessionRef{
				AppName: "caelis",
				UserID:  "user-1",
			},
		},
		Events:        events,
		PendingEvents: []*session.Event{pending},
		Model: staticModel{text: `Objective: preserve compact budget
Blocker: pre-turn prompt is near the limit
Next action: fit the pending user turn inside the compacted prompt

- keep only the minimal continuity handoff`},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !result.Compacted {
		t.Fatal("expected compaction to trigger")
	}
	if result.Usage.TotalTokens > result.Usage.EffectiveInputBudget {
		t.Fatalf("usage.TotalTokens = %d, want <= effective budget %d", result.Usage.TotalTokens, result.Usage.EffectiveInputBudget)
	}
	data, ok := compact.CompactEventDataFromEvent(result.CompactEvent)
	if !ok {
		t.Fatal("expected compact event data")
	}
	if data.SourceEventCount == 0 {
		t.Fatalf("source event count = %d, want > 0", data.SourceEventCount)
	}
}

func TestRuntimeCompactionIgnoresStateOnlyPlanSnapshot(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-state-omit")
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Objective: keep compaction event-only."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Blocker: runtime state can drift away from durable events."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, activeSession.SessionRef, userTextEvent("Next action: compact only from canonical events and verify no state leakage."))
	appendTestEvent(t, sessions, activeSession.SessionRef, assistantEvent("ack"))

	if err := sessions.UpdateState(context.Background(), activeSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		if state == nil {
			state = map[string]any{}
		}
		state["plan"] = map[string]any{
			"version": 1,
			"entries": []any{
				map[string]any{
					"content": "state-only plan item that must never leak into compaction",
					"status":  "in_progress",
				},
			},
		}
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	testModel := &contextProbeModel{
		t: t,
		wantCompactionInputContains: []string{
			"Objective: keep compaction event-only.",
		},
		wantCompactionInputOmit: []string{
			"Current runtime state:",
			"state-only plan item that must never leak into compaction",
		},
		replyText: "ok",
	}

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 64,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	if testModel.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", testModel.compactionCalls)
	}
}

func TestRenderCompactionEventIncludesPlanEntries(t *testing.T) {
	t.Parallel()

	event := &session.Event{
		Type:       session.EventTypePlan,
		Visibility: session.VisibilityCanonical,
		Text:       "execution plan refreshed",
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypePlan),
			Plan: &session.ProtocolPlan{
				Entries: []session.ProtocolPlanEntry{
					{Content: "run provider compact e2e", Status: "in_progress"},
					{Content: "verify append-only replay", Status: "pending"},
					{Content: "preserve plan item three", Status: "pending"},
					{Content: "preserve plan item four", Status: "pending"},
					{Content: "preserve plan item five", Status: "pending"},
					{Content: "preserve plan item six", Status: "pending"},
				},
			},
		},
	}

	got := renderCompactionEvent(event)
	for _, needle := range []string{
		"## Plan Update",
		"execution plan refreshed",
		"- [in_progress] run provider compact e2e",
		"- [pending] verify append-only replay",
		"- [pending] preserve plan item six",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("renderCompactionEvent() = %q, want substring %q", got, needle)
		}
	}
}

func TestCompactableEventsIgnoreReplacementOverlayHistory(t *testing.T) {
	t.Parallel()

	retainedMsg := model.NewTextMessage(model.RoleUser, "Retained user text from the previous compact.")
	overlay := &session.Event{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityOverlay,
		Message:    &retainedMsg,
		Text:       retainedMsg.TextContent(),
	}
	canonical := userTextEvent("Fresh canonical user event after the latest compact.")
	events := []*session.Event{
		overlay,
		canonical,
	}

	got := compactableEvents(events)
	if len(got) != 1 {
		t.Fatalf("compactableEvents() count = %d, want 1 (%v)", len(got), got)
	}
	if text := eventTextForCompaction(got[0]); text != "Fresh canonical user event after the latest compact." {
		t.Fatalf("compactable event text = %q, want fresh canonical event", text)
	}
}

func TestRenderCompactionEventFallsBackToMessageText(t *testing.T) {
	t.Parallel()

	message := model.NewTextMessage(model.RoleAssistant, "message-only assistant text")
	event := &session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
	}

	got := renderCompactionEvent(event)
	if !strings.Contains(got, "message-only assistant text") {
		t.Fatalf("renderCompactionEvent() = %q, want message text fallback", got)
	}
}

func TestRuntimeRecoversFromContextOverflowByCompactingMidTurn(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-overflow")
	testModel := &overflowRecoveryModel{t: t}
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

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		Compaction: CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.95,
			ForceWatermarkRatio:        0.99,
			DefaultContextWindowTokens: 128,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "Use ECHO and then finish.",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
			Tools: []tool.Tool{targetTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var finalText string
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(session.EventText(event))
		}
	}
	if finalText != "recovered after compact" {
		t.Fatalf("finalText = %q, want %q", finalText, "recovered after compact")
	}
	if testModel.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", testModel.compactionCalls)
	}
	if !testModel.sawCheckpointOnRetry {
		t.Fatal("expected retry to see compact checkpoint with tool result continuity")
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	sawCompact := false
	for _, event := range loaded.Events {
		if event != nil && event.Type == session.EventTypeCompact {
			sawCompact = true
			if !strings.Contains(strings.ToLower(session.EventText(event)), "auto-review policy") {
				t.Fatalf("compact event text = %q, want retained tool result summary", session.EventText(event))
			}
		}
	}
	if !sawCompact {
		t.Fatal("expected compact event after overflow recovery")
	}
	compactEvent, ok := latestCompactEventForTest(loaded.Events)
	if !ok {
		t.Fatal("expected latest compact event")
	}
	data, ok := compact.CompactEventDataFromEvent(compactEvent)
	if !ok {
		t.Fatalf("compact metadata missing compact payload: %+v", compactEvent.Meta)
	}
	if data.SourceEventCount == 0 {
		t.Fatalf("compact source event count = %d, want > 0", data.SourceEventCount)
	}
	promptEvents := compact.PromptEventsFromLatestCompact(loaded.Events)
	if len(promptEvents) == 0 || !strings.Contains(strings.ToLower(session.EventText(promptEvents[0])), "auto-review policy") {
		t.Fatalf("prompt events after compact = %+v, want tool result continuity in checkpoint overlay", promptEvents)
	}
}

func TestRuntimeRecoveryInterruptsOrphanedBashTask(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workdir := t.TempDir()
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-orphan-bash" },
	}))
	tasks := taskfile.NewStore(taskfile.Config{RootDir: filepath.Join(root, "tasks")})
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
	snapshot, err := runtime1.tasks.StartBash(context.Background(), activeSession, activeSession.SessionRef, hostRuntimeForTest(t, workdir), taskapi.BashStartRequest{
		Command:    "sleep 5; printf 'late output'",
		Workdir:    workdir,
		Yield:      5 * time.Millisecond,
		ParentCall: "bash-1",
		ParentTool: shell.BashToolName,
	})
	if err != nil {
		t.Fatalf("StartBash() error = %v", err)
	}
	if !snapshot.Running {
		t.Fatalf("snapshot.Running = %v, want true", snapshot.Running)
	}

	reopenedSessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	runtime2, err := New(Config{
		Sessions:  reopenedSessions,
		TaskStore: tasks,
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
		RunIDGenerator: func() string { return "run-tools" },
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
	if loaded.Events[1].Protocol == nil || loaded.Events[1].Protocol.ToolCall == nil || loaded.Events[1].Protocol.UpdateType != string(session.ProtocolUpdateTypeToolCall) {
		t.Fatalf("loaded.Events[1].Protocol = %+v, want tool_call protocol payload", loaded.Events[1].Protocol)
	}
	if loaded.Events[2].Type != session.EventTypeToolResult {
		t.Fatalf("loaded.Events[2].Type = %q, want tool_result", loaded.Events[2].Type)
	}
	if loaded.Events[2].Protocol == nil || loaded.Events[2].Protocol.ToolCall == nil || loaded.Events[2].Protocol.UpdateType != string(session.ProtocolUpdateTypeToolUpdate) {
		t.Fatalf("loaded.Events[2].Protocol = %+v, want tool_call_update protocol payload", loaded.Events[2].Protocol)
	}
	if got := session.EventText(loaded.Events[3]); got != "pong" {
		t.Fatalf("final assistant text = %q, want %q", got, "pong")
	}
	if loaded.Events[0].Protocol == nil || loaded.Events[0].Protocol.UpdateType != string(session.ProtocolUpdateTypeUserMessage) {
		t.Fatalf("loaded.Events[0].Protocol = %+v, want user_message protocol payload", loaded.Events[0].Protocol)
	}
	if loaded.Events[3].Protocol == nil || loaded.Events[3].Protocol.UpdateType != string(session.ProtocolUpdateTypeAgentMessage) {
		t.Fatalf("loaded.Events[3].Protocol = %+v, want agent_message protocol payload", loaded.Events[3].Protocol)
	}
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
		if event != nil && event.Type == session.EventTypePlan {
			planEvent = event
			break
		}
	}
	if planEvent == nil || planEvent.Protocol == nil || planEvent.Protocol.Plan == nil {
		t.Fatalf("plan event = %+v, want protocol plan payload", planEvent)
	}
	if got, want := len(planEvent.Protocol.Plan.Entries), 2; got != want {
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

func TestRuntimePolicyDefaultDeniesWriteOutsideAllowedRoots(t *testing.T) {
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
	testModel := &denyWriteRuntimeModel{}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "write outside workspace",
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
	if got := eventToolRawOutput(toolResult)["policy_action"]; got != "deny" {
		t.Fatalf("policy_action = %v, want %q", got, "deny")
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
	if got := payload["policy_mode"]; got != "locked-down" {
		t.Fatalf("result policy_mode = %v, want locked-down", got)
	}
}

func TestRuntimePolicyUnknownModeFallsBackToDefaultPolicy(t *testing.T) {
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
		Metadata: map[string]any{"policy_mode": "unknown-policy"},
		Tools:    []tool.Tool{targetTool},
	}, approvalContext{
		ctx:        context.Background(),
		session:    activeSession,
		sessionRef: activeSession.SessionRef,
	})
	if got := len(wrapped); got != 1 {
		t.Fatalf("len(wrapped) = %d, want 1", got)
	}
	result, err := wrapped[0].Call(context.Background(), tool.Call{
		ID:    "call-1",
		Name:  "WRITE",
		Input: []byte(`{"path":"/etc/passwd"}`),
	})
	if err != nil {
		t.Fatalf("wrapped tool Call() error = %v", err)
	}
	if !result.IsError {
		t.Fatalf("result.IsError = false, want policy denial")
	}
	payload := testToolResultPayload(t, result)
	if got := payload["policy_mode"]; got != presets.ModeAutoReview {
		t.Fatalf("result policy_mode = %v, want %s", got, presets.ModeAutoReview)
	}
}

func TestNormalizePolicyModeHandlesDefaultAliasesAndPreservesCustomNames(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":             "auto-review",
		"auto":         "auto-review",
		"auto_review":  "auto-review",
		"manual":       "manual",
		"default":      "auto-review",
		"plan":         "auto-review",
		"full_access":  "auto-review",
		"full_control": "auto-review",
		"locked-down":  "locked-down",
		"TeamStrict":   "TeamStrict",
	}
	for input, want := range tests {
		if got := normalizePolicyMode(input); got != want {
			t.Fatalf("normalizePolicyMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRuntimeReservesRequestPermissionsToolName(t *testing.T) {
	t.Parallel()

	_, activeSession := newTestSessionService(t, "sess-request-permissions-reserved")
	customCalled := false
	custom := tool.NamedTool{
		Def: tool.Definition{Name: "request_permissions"},
		Invoke: func(context.Context, tool.Call) (tool.Result, error) {
			customCalled = true
			return tool.Result{Meta: map[string]any{"custom": true}}, nil
		},
	}
	runtime := &Runtime{}
	wrapped := runtime.wrapToolsForRuntime(activeSession, activeSession.SessionRef, agent.AgentSpec{
		Tools: []tool.Tool{custom},
	}, runtimeToolContext{
		grants: newPermissionGrantStore(),
	})
	if got := len(wrapped); got != 1 {
		t.Fatalf("len(wrapped) = %d, want 1", got)
	}
	if _, ok := wrapped[0].(requestPermissionsTool); !ok {
		t.Fatalf("wrapped request_permissions tool = %T, want built-in requestPermissionsTool", wrapped[0])
	}
	raw := []byte(`{"reason":"need network","network":true}`)
	result, err := wrapped[0].Call(context.Background(), tool.Call{ID: "perm-1", Name: "request_permissions", Input: raw})
	if err != nil {
		t.Fatalf("request_permissions Call() error = %v", err)
	}
	if customCalled {
		t.Fatal("colliding custom request_permissions tool was invoked")
	}
	if !result.IsError {
		t.Fatalf("request_permissions result IsError = false, want true without approval requester")
	}
	payload := testToolResultPayload(t, result)
	if !strings.Contains(fmt.Sprint(payload["error"]), "no approval requester") {
		t.Fatalf("request_permissions error = %v, want built-in approval requester error", payload["error"])
	}
}

func TestRuntimeRequestPermissionsSuccessIncludesGrantPayload(t *testing.T) {
	t.Parallel()

	_, activeSession := newTestSessionService(t, "sess-request-permissions-grant")
	createdAt := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	store := newPermissionGrantStore()
	runtime := &Runtime{clock: func() time.Time { return createdAt }}
	wrapped := runtime.wrapToolsForRuntime(activeSession, activeSession.SessionRef, agent.AgentSpec{
		Tools: []tool.Tool{tool.NamedTool{Def: tool.Definition{Name: "BASH"}}},
	}, runtimeToolContext{
		mode:   "manual",
		runID:  "run-1",
		turnID: "turn-1",
		now:    runtime.now,
		approvalRequester: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			return agent.ApprovalResponse{Approved: true}, nil
		}),
		grants: store,
	})
	var permissionsTool tool.Tool
	for _, candidate := range wrapped {
		if strings.EqualFold(candidate.Definition().Name, requestPermissionsToolName) {
			permissionsTool = candidate
			break
		}
	}
	if permissionsTool == nil {
		t.Fatal("wrapped tools missing request_permissions")
	}
	result, err := permissionsTool.Call(context.Background(), tool.Call{
		ID:    "perm-1",
		Name:  requestPermissionsToolName,
		Input: []byte(`{"reason":"need network","network":true}`),
	})
	if err != nil {
		t.Fatalf("request_permissions Call() error = %v", err)
	}
	toolMeta := testToolResultRuntimeMeta(t, result, "tool")
	grant, ok := toolMeta["grant"].(map[string]any)
	if !ok {
		t.Fatalf("grant metadata = %#v, want map", toolMeta["grant"])
	}
	if grant["reason"] != "need network" || grant["mode"] != "manual" || grant["run_id"] != "run-1" || grant["turn_id"] != "turn-1" {
		t.Fatalf("grant payload = %#v, want reason/mode/run/turn metadata", grant)
	}
	if store.snapshot().Count != 1 {
		t.Fatalf("grant snapshot count = %d, want 1", store.snapshot().Count)
	}
}

func TestRuntimePolicyFullAccessBlocksDangerousBash(t *testing.T) {
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

	bashTool, err := shell.NewBash(shell.BashConfig{Runtime: hostRuntimeForTest(t, activeSession.CWD)})
	if err != nil {
		t.Fatalf("shell.NewBash() error = %v", err)
	}
	testModel := &denyBashRuntimeModel{}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "run dangerous bash",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
			Tools: []tool.Tool{bashTool},
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
	if got := eventToolRawOutput(toolResult)["policy_action"]; got != "deny" {
		t.Fatalf("policy_action = %v, want %q", got, "deny")
	}
}

func TestRuntimePolicyDefaultBashEscalationWaitsApprovalThenExecutes(t *testing.T) {
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

	bashTool, err := shell.NewBash(shell.BashConfig{Runtime: hostRuntimeForTest(t, activeSession.CWD)})
	if err != nil {
		t.Fatalf("shell.NewBash() error = %v", err)
	}
	target := filepath.Join(activeSession.CWD, "approved.txt")
	testModel := &approveEscalatedBashRuntimeModel{command: "printf 'approved\\n' > " + shellQuoteForTest(target)}
	requester := approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		state, err := runtime.RunState(ctx, activeSession.SessionRef)
		if err != nil {
			t.Fatalf("RunState() during approval error = %v", err)
		}
		if state.Status != agent.RunLifecycleStatusWaitingApproval || !state.WaitingApproval {
			t.Fatalf("run state during approval = %+v, want waiting_approval", state)
		}
		if req.Approval == nil || req.Approval.ToolCall.Name != shell.BashToolName {
			t.Fatalf("approval request = %+v, want BASH tool call", req.Approval)
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
			Tools: []tool.Tool{bashTool},
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
			Name:   "BASH",
			Kind:   "execute",
			Title:  "Run command",
			Status: "pending",
			RawInput: map[string]any{
				"command": "pwd",
				"workdir": "/tmp/project",
			},
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
	if captured.Call.Input == nil || !strings.Contains(string(captured.Call.Input), `"command":"pwd"`) {
		t.Fatalf("Call.Input = %s, want command JSON", string(captured.Call.Input))
	}
}

func TestRuntimeBashYieldThenTaskWaitLoop(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-bash-task-loop")
	taskStore := taskfile.NewStore(taskfile.Config{RootDir: t.TempDir()})
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

	bashTool, err := shell.NewBash(shell.BashConfig{Runtime: hostRuntimeForTest(t, activeSession.CWD)})
	if err != nil {
		t.Fatalf("shell.NewBash() error = %v", err)
	}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "run async bash",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: &bashTaskLoopRuntimeModel{t: t},
			Tools: []tool.Tool{bashTool, tasktool.New()},
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
		if event.Type == session.EventTypeToolResult && event.Protocol != nil && event.Protocol.ToolCall != nil && event.Protocol.ToolCall.Status == "running" {
			runningToolUpdate = true
		}
		if event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(session.EventText(event))
		}
	}
	if !runningToolUpdate {
		t.Fatal("expected running tool update after yielded BASH")
	}
	if finalText != "async bash done" {
		t.Fatalf("finalText = %q, want %q", finalText, "async bash done")
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
	task, err := runtime.tasks.lookupBash(context.Background(), activeSession.SessionRef, mustSessionTaskID(t, loaded.Events))
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
	if !strings.Contains(resultPayload, "async bash done") {
		t.Fatalf("rehydrated task result = %q, want async bash done", resultPayload)
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
	if !strings.Contains(terminalText, "async bash done") {
		t.Fatalf("terminal snapshot text = %q, want async bash done", terminalText)
	}
}

func TestRuntimeTaskWriteAddsLineTerminatorForInteractiveBash(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeBashToolTestHarness(t)
	bashTool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, hostRuntimeForTest(t, activeSession.CWD)),
		session:    activeSession,
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	bashResult := callRuntimeBashTool(t, bashTool, map[string]any{
		"command":       "printf 'waiting\\n'; read name; printf 'hello %s\\n' \"$name\"",
		"workdir":       ".",
		"yield_time_ms": 0,
	})
	taskID, _ := testToolResultRuntimeMeta(t, bashResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("bash result metadata = %#v, want task_id", bashResult.Metadata)
	}

	taskResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":        "write",
		"task_id":       taskID,
		"input":         "Codex",
		"yield_time_ms": 250,
	})
	if len(taskResult.Content) == 0 || taskResult.Content[0].JSON == nil {
		t.Fatalf("task result content = %#v, want json payload", taskResult.Content)
	}
	payload := string(taskResult.Content[0].JSON.Value)
	if !strings.Contains(payload, "hello Codex") {
		t.Fatalf("task write result = %s, want interactive read to receive input line", payload)
	}
}

func TestTaskToolPayloadReturnsCompletedBashTerminalStreams(t *testing.T) {
	payload := taskToolPayload(taskapi.Snapshot{
		Ref:     taskapi.Ref{TaskID: "task-1", TerminalID: "term-1"},
		Kind:    taskapi.KindBash,
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
	want := "...2 lines hidden...\nline 3\nline 4\nline 5\nline 6\nline 7"
	if got != want {
		t.Fatalf("compactLatestOutput() = %q, want %q", got, want)
	}
}

func TestRuntimeTerminalSubscribeStreamsRunningTask(t *testing.T) {
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
	snapshot, err := runtime.tasks.StartBash(context.Background(), activeSession, activeSession.SessionRef, sandbox, taskapi.BashStartRequest{
		Command: "printf 'stream terminal'; sleep 0.05",
		Workdir: activeSession.CWD,
		Yield:   1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartBash() error = %v", err)
	}
	terminals := runtime.Streams()
	if terminals == nil {
		t.Fatal("Streams() = nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
		t.Fatalf("closed frame text = %q, want status-only close after streamed output", got)
	}
}

func TestRuntimeBashToolUsesDefaultYieldWhenOmitted(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeBashToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	targetTool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeBashTool(t, targetTool, map[string]any{
		"command": "printf 'ok'",
		"workdir": activeSession.CWD,
	})

	if got := fake.session.lastWait; got != defaultBashYield {
		t.Fatalf("omitted yield wait = %v, want %v", got, defaultBashYield)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestRuntimeBashToolKeepsExplicitZeroYield(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeBashToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	targetTool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeBashTool(t, targetTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})

	if got := fake.session.lastWait; got != 0 {
		t.Fatalf("explicit zero yield wait = %v, want 0", got)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestRuntimeBashToolPassesExplicitYieldThrough(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeBashToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	targetTool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeBashTool(t, targetTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 125,
	})

	if got := fake.session.lastWait; got != 125*time.Millisecond {
		t.Fatalf("explicit yield wait = %v, want %v", got, 125*time.Millisecond)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestStartBashMarksTaskFailedWhenInitialWaitErrors(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeBashToolTestHarness(t)
	waitErr := errors.New("terminal session failed")
	fake := &yieldProbeSandboxRuntime{session: &yieldProbeSandboxSession{waitErr: waitErr, statusRunning: boolPtr(false)}}
	taskStore := taskfile.NewStore(taskfile.Config{RootDir: t.TempDir()})
	runtime.tasks.store = taskStore

	snapshot, err := runtime.tasks.StartBash(context.Background(), activeSession, activeSession.SessionRef, fake, taskapi.BashStartRequest{
		Command: "echo hello",
		Workdir: activeSession.CWD,
		Yield:   0,
	})
	if err != nil {
		t.Fatalf("StartBash() error = %v", err)
	}
	if snapshot.Running {
		t.Fatalf("snapshot.Running = true, want false")
	}
	if snapshot.State != taskapi.StateFailed {
		t.Fatalf("snapshot.State = %q, want failed", snapshot.State)
	}
	if got, _ := snapshot.Result["error"].(string); got != waitErr.Error() {
		t.Fatalf("snapshot.Result[error] = %q, want %q", got, waitErr.Error())
	}
	if !fake.session.terminated {
		t.Fatal("session.terminated = false, want true")
	}
	runtime.tasks.mu.RLock()
	_, active := runtime.tasks.tasks[snapshot.Ref.TaskID]
	runtime.tasks.mu.RUnlock()
	if active {
		t.Fatalf("task %q still active after wait failure", snapshot.Ref.TaskID)
	}
	entry, err := taskStore.Get(context.Background(), snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get() error = %v", err)
	}
	if entry == nil || entry.Running || entry.State != taskapi.StateFailed {
		t.Fatalf("persisted entry = %#v, want failed non-running task", entry)
	}
}

func TestRuntimeTaskWaitErrorDoesNotTerminateRunningBash(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeBashToolTestHarness(t)
	fakeSession := newYieldProbeSandboxSession()
	fake := &yieldProbeSandboxRuntime{session: fakeSession}
	bashTool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	bashResult := callRuntimeBashTool(t, bashTool, map[string]any{
		"command":       "sleep 60",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := testToolResultRuntimeMeta(t, bashResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("bash result metadata = %#v, want task_id", bashResult.Metadata)
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

	_, activeSession, runtime := newRuntimeBashToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	bashTool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	bashResult := callRuntimeBashTool(t, bashTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := testToolResultRuntimeMeta(t, bashResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("bash result metadata = %#v, want task_id", bashResult.Metadata)
	}

	callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":  "wait",
		"task_id": taskID,
	})

	if got := fake.session.lastWait; got != defaultBashYield {
		t.Fatalf("omitted TASK wait yield = %v, want %v", got, defaultBashYield)
	}
}

func TestRuntimeTaskWaitKeepsExplicitZeroYield(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeBashToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	bashTool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	bashResult := callRuntimeBashTool(t, bashTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := testToolResultRuntimeMeta(t, bashResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("bash result metadata = %#v, want task_id", bashResult.Metadata)
	}

	callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}, map[string]any{
		"action":        "wait",
		"task_id":       taskID,
		"yield_time_ms": 0,
	})

	if got := fake.session.lastWait; got != 0 {
		t.Fatalf("explicit zero TASK wait yield = %v, want 0", got)
	}
}

func TestTaskSnapshotToolResultKeepsTerminalStreamsInPayloadOnly(t *testing.T) {
	t.Parallel()

	result := taskSnapshotToolResult(
		tool.Call{ID: "call-1", Name: shell.BashToolName},
		tool.Definition{Name: shell.BashToolName},
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
		tool.Call{ID: "call-1", Name: shell.BashToolName},
		tool.Definition{Name: shell.BashToolName},
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

func TestTaskSnapshotToolResultTruncatesTerminalStreamsForDisplayAndModel(t *testing.T) {
	t.Parallel()

	hugeStderr := strings.Repeat("permission denied\n", tool.DefaultTruncationPolicy().ByteBudget()/2)
	result := taskSnapshotToolResult(
		tool.Call{ID: "call-1", Name: shell.BashToolName},
		tool.Definition{Name: shell.BashToolName},
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
	gotText := taskStringValue(payload["result"])
	if gotText == hugeStderr {
		t.Fatalf("payload result kept original huge output, want canonical truncated result")
	}
	if len(gotText) > tool.DefaultTruncationPolicy().ByteBudget()+1024 {
		t.Fatalf("payload result len = %d, want bounded", len(gotText))
	}
	if !strings.Contains(gotText, "tokens truncated") {
		t.Fatalf("payload result = %q, want truncation marker", gotText)
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
		tool.Call{ID: "call-1", Name: shell.BashToolName},
		tool.Definition{Name: shell.BashToolName},
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

func TestRuntimeTaskToolResolvesSubagentHandle(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeBashToolTestHarness(t)
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

	_, activeSession, runtime := newRuntimeBashToolTestHarness(t)
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

func TestRuntimeBashToolDoesNotFetchResultWhileStillRunning(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeBashToolTestHarness(t)
	fake := &runningOnlyProbeSandboxRuntime{session: &runningOnlyProbeSandboxSession{}}
	targetTool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeBashTool(t, targetTool, map[string]any{
		"command": "printf 'still-running'",
		"workdir": activeSession.CWD,
	})

	if got := fake.session.lastWait; got != defaultBashYield {
		t.Fatalf("omitted yield wait = %v, want %v", got, defaultBashYield)
	}
	assertRunningTaskSnapshot(t, result)
}

type staticModel struct {
	text string
}

func (m staticModel) Name() string { return "stub" }

func (m staticModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, m.text),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type gatedStreamingModel struct {
	started      chan struct{}
	releaseFinal chan struct{}
}

func (m *gatedStreamingModel) Name() string { return "gated-streaming" }

func (m *gatedStreamingModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if m.started != nil {
			select {
			case <-m.started:
			default:
				close(m.started)
			}
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventPartDelta,
			PartDelta: &model.PartDelta{
				Kind:      model.PartKindText,
				TextDelta: "hel",
			},
		}, nil)
		if m.releaseFinal != nil {
			<-m.releaseFinal
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "hello"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type steerRuntimeModel struct {
	started      chan struct{}
	releaseFirst chan struct{}

	mu       sync.Mutex
	requests []model.Request
}

func (m *steerRuntimeModel) Name() string { return "steer-runtime" }

func (m *steerRuntimeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.mu.Lock()
	if req != nil {
		cp := *req
		cp.Messages = model.CloneMessages(req.Messages)
		cp.Instructions = model.CloneParts(req.Instructions)
		m.requests = append(m.requests, cp)
	}
	callIndex := len(m.requests)
	m.mu.Unlock()

	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			if m.started != nil {
				select {
				case <-m.started:
				default:
					close(m.started)
				}
			}
			if m.releaseFirst != nil {
				<-m.releaseFirst
			}
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "first answer"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "steered answer"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

func (m *steerRuntimeModel) Requests() []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Request, len(m.requests))
	for i, req := range m.requests {
		out[i] = req
		out[i].Messages = model.CloneMessages(req.Messages)
		out[i].Instructions = model.CloneParts(req.Instructions)
	}
	return out
}

type historyReplayModel struct {
	t         *testing.T
	wantTexts []string
	replyText string
	calls     int
}

func (m *historyReplayModel) Name() string { return "history-replay" }

func (m *historyReplayModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	if req == nil {
		m.t.Fatal("Generate() request = nil")
	}
	got := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		if text := strings.TrimSpace(message.TextContent()); text != "" {
			got = append(got, text)
		}
	}
	if len(got) != len(m.wantTexts) {
		m.t.Fatalf("replayed message count = %d, want %d (%v)", len(got), len(m.wantTexts), got)
	}
	for i := range m.wantTexts {
		if got[i] != m.wantTexts[i] {
			m.t.Fatalf("replayed message[%d] = %q, want %q (all=%v)", i, got[i], m.wantTexts[i], got)
		}
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, m.replyText),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type toolLoopRuntimeModel struct {
	calls int
}

func (m *toolLoopRuntimeModel) Name() string { return "tool-loop" }

func (m *toolLoopRuntimeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "call-1",
						Name: "ECHO",
						Args: string(mustJSONRaw(tmap("value", "pong"))),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "pong"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

type planLoopRuntimeModel struct {
	calls int
}

func (m *planLoopRuntimeModel) Name() string { return "plan-loop" }

func (m *planLoopRuntimeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "plan-1",
						Name: plan.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"entries": []map[string]any{
								{"content": "Inspect repo", "status": "completed"},
								{"content": "Implement runtime bridge", "status": "in_progress"},
							},
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "plan ready"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

func mustJSONRaw(value map[string]any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func tmap(key string, value any) map[string]any {
	return map[string]any{key: value}
}

func newTestSessionService(t *testing.T, sessionID string) (session.Service, session.Session) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return sessionID },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return sessions, activeSession
}

func hostRuntimeForTest(t *testing.T, cwd string) *host.Runtime {
	t.Helper()
	rt, err := host.New(host.Config{CWD: cwd})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	return rt
}

func assistantEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleAssistant, text)
	return &session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       text,
	}
}

func acpControllerChunk(text string) *session.Event {
	message := model.NewTextMessage(model.RoleAssistant, text)
	return &session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       text,
		Scope: &session.EventScope{
			Source: "acp",
			ACP: session.ACPRef{
				SessionID: "remote-acp-main",
				EventType: string(session.ProtocolUpdateTypeAgentMessage),
			},
		},
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
		},
	}
}

func userTextEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleUser, text)
	return &session.Event{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       strings.TrimSpace(text),
	}
}

func appendTestEvent(t *testing.T, sessions session.Service, ref session.SessionRef, event *session.Event) {
	t.Helper()
	if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref,
		Event:      event,
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
}

type contextProbeModel struct {
	t                           *testing.T
	calls                       int
	compactionCalls             int
	normalCalls                 int
	compactBody                 string
	wantCompactionInputContains []string
	wantCompactionInputOmit     []string
	wantMessageContains         []string
	wantMessagesOmit            []string
	replyText                   string
}

func (m *contextProbeModel) Name() string { return "context-probe" }

func (m *contextProbeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	instructions := requestInstructionsText(req)
	messages := requestMessageTexts(req)
	if strings.Contains(instructions, "CONTEXT CHECKPOINT COMPACTION") {
		m.compactionCalls++
		compactionInput := strings.Join(requestMessageTexts(req), "\n")
		for _, needle := range m.wantCompactionInputContains {
			if !strings.Contains(compactionInput, needle) {
				m.t.Fatalf("compaction input missing %q: %q", needle, compactionInput)
			}
		}
		for _, needle := range m.wantCompactionInputOmit {
			if strings.Contains(compactionInput, needle) {
				m.t.Fatalf("compaction input unexpectedly contains %q: %q", needle, compactionInput)
			}
		}
		body := strings.TrimSpace(m.compactBody)
		if body == "" {
			body = `CONTEXT CHECKPOINT

## Objective
- build compact runtime

## User Constraints
- do not lose blocker continuity

## Durable Decisions
- prefer compact event checkpoint overlay

## Verified Facts
- provider intermittently returns 529 overloaded_error when histories get too large

## Current Progress
- checkpoint event inserted into durable history

## Open Questions / Risks
- compaction quality must preserve blockers

## Next Actions
1. validate with real e2e tests and tune the compact prompt

## Active Tasks
- none

## Active Participants
- none

## Latest Blockers
- provider intermittently returns 529 overloaded_error

## Operational Notes
- files touched: impl/agent/local/compaction.go
- commands run: go test ./ports/...`
		}
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, body),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
				},
			}, nil)
		}
	}
	m.normalCalls++
	for _, needle := range m.wantMessageContains {
		found := false
		for _, text := range messages {
			if strings.Contains(text, needle) {
				found = true
				break
			}
		}
		if !found {
			m.t.Fatalf("messages missing %q: %v", needle, messages)
		}
	}
	for _, needle := range m.wantMessagesOmit {
		for _, text := range messages {
			if strings.Contains(text, needle) {
				m.t.Fatalf("messages still contain summarized text %q: %v", needle, messages)
			}
		}
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, m.replyText),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type modelCheckpointProbe struct {
	t               *testing.T
	compactionCalls int
	normalCalls     int
}

func (m *modelCheckpointProbe) Name() string { return "model-checkpoint-probe" }

func (m *modelCheckpointProbe) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	instructions := requestInstructionsText(req)
	if strings.Contains(instructions, "CONTEXT CHECKPOINT COMPACTION") {
		m.compactionCalls++
		body := `CONTEXT CHECKPOINT

## Objective
- model checkpoint objective

## User Constraints
- do not lose blocker continuity

## Durable Decisions
- compact before each turn when budget is exceeded

## Verified Facts
- provider intermittently returns 529 overloaded_error

## Current Progress
- checkpoint builder is being implemented

## Open Questions / Risks
- summary quality can drift if prompts are too generic

## Next Actions
1. run realistic compact e2e tests and tune the summary prompt

## Active Tasks
- none

## Active Participants
- none

## Latest Blockers
- checkpoint quality drops when summaries become too generic

## Operational Notes
- files touched: impl/agent/local/runtime.go
- commands run: go test ./ports/...`
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, body),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
				},
			}, nil)
		}
	}
	m.normalCalls++
	found := false
	for _, text := range requestMessageTexts(req) {
		if strings.Contains(text, "model checkpoint objective") {
			found = true
			break
		}
	}
	if !found {
		m.t.Fatalf("normal call messages missing canonical checkpoint objective: %v", requestMessageTexts(req))
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "ok"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type overflowRecoveryModel struct {
	t                    *testing.T
	calls                int
	compactionCalls      int
	sawCheckpointOnRetry bool
}

func (m *overflowRecoveryModel) Name() string { return "overflow-recovery" }

func (m *overflowRecoveryModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	instructions := requestInstructionsText(req)
	if strings.Contains(instructions, "CONTEXT CHECKPOINT COMPACTION") {
		m.compactionCalls++
		compactionInput := strings.Join(requestMessageTexts(req), "\n")
		if !strings.Contains(compactionInput, "## Tool Result") ||
			!strings.Contains(compactionInput, "tool: ECHO") ||
			!strings.Contains(compactionInput, "policy_action: deny") {
			m.t.Fatalf("compaction input missing tool result continuity: %q", compactionInput)
		}
		body := `CONTEXT CHECKPOINT

Objective: finish the tool-assisted turn after overflow
Blocker: normal prompt overflowed after the tool denial result
Next action: resume from the compact checkpoint and return the final answer

## Current Progress
- the ECHO tool result was denied by auto-review policy

## Next Actions
1. resume from the compact checkpoint and return the final answer`
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, body),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
				},
			}, nil)
		}
	}
	if requestHasToolResult(req, "ECHO") {
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(nil, &model.ContextOverflowError{Cause: errors.New("prompt is too long after tool loop")})
		}
	}
	for _, text := range requestMessageTexts(req) {
		if strings.Contains(text, "CONTEXT CHECKPOINT") && strings.Contains(strings.ToLower(text), "auto-review policy") {
			m.sawCheckpointOnRetry = true
			return func(yield func(*model.StreamEvent, error) bool) {
				yield(&model.StreamEvent{
					Type: model.StreamEventTurnDone,
					Response: &model.Response{
						Message:      model.NewTextMessage(model.RoleAssistant, "recovered after compact"),
						TurnComplete: true,
						StepComplete: true,
						Status:       model.ResponseStatusCompleted,
					},
				}, nil)
			}
		}
	}
	if m.calls != 1 {
		m.t.Fatalf("unexpected non-compaction request without checkpoint: %v", requestMessageTexts(req))
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
					ID:   "call-overflow-1",
					Name: "ECHO",
					Args: string(mustJSONRaw(tmap("value", "pong"))),
				}}, ""),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonToolCalls,
			},
		}, nil)
	}
}

func requestInstructionsText(req *model.Request) string {
	if req == nil {
		return ""
	}
	parts := make([]string, 0, len(req.Instructions))
	for _, part := range req.Instructions {
		if part.Text != nil && strings.TrimSpace(part.Text.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func requestMessageTexts(req *model.Request) []string {
	if req == nil {
		return nil
	}
	out := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		if text := strings.TrimSpace(message.TextContent()); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func requestHasToolResult(req *model.Request, name string) bool {
	if req == nil {
		return false
	}
	for _, message := range req.Messages {
		for _, result := range message.ToolResults() {
			if strings.EqualFold(strings.TrimSpace(result.Name), strings.TrimSpace(name)) {
				return true
			}
		}
	}
	return false
}

func latestCompactEventForTest(events []*session.Event) (*session.Event, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i] != nil && events[i].Type == session.EventTypeCompact {
			return events[i], true
		}
	}
	return nil, false
}

func eventTextsForTest(events []*session.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if text := strings.TrimSpace(session.EventText(event)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

type denyWriteRuntimeModel struct{ calls int }

func (m *denyWriteRuntimeModel) Name() string { return "deny-write" }

func (m *denyWriteRuntimeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "write-1",
						Name: filesystem.WriteToolName,
						Args: string(mustJSONRaw(map[string]any{"path": "/etc/blocked.txt", "content": "x"})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "denied"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

type denyBashRuntimeModel struct{ calls int }

func (m *denyBashRuntimeModel) Name() string { return "deny-bash" }

func (m *denyBashRuntimeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "bash-1",
						Name: shell.BashToolName,
						Args: string(mustJSONRaw(map[string]any{"command": "rm -rf /"})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "blocked"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

type approveEscalatedBashRuntimeModel struct {
	calls   int
	command string
}

func (m *approveEscalatedBashRuntimeModel) Name() string { return "approve-escalated-bash" }

func (m *approveEscalatedBashRuntimeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "bash-approve-1",
						Name: shell.BashToolName,
						Args: string(mustJSONRaw(map[string]any{
							"command":             m.command,
							"workdir":             ".",
							"yield_time_ms":       200,
							"sandbox_permissions": "require_escalated",
							"justification":       "Do you want to run this command outside the sandbox?",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "done"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

func shellQuoteForTest(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

type bashTaskLoopRuntimeModel struct {
	t      *testing.T
	calls  int
	taskID string
}

func (m *bashTaskLoopRuntimeModel) Name() string { return "bash-task-loop" }

func (m *bashTaskLoopRuntimeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "bash-async-1",
						Name: shell.BashToolName,
						Args: string(mustJSONRaw(map[string]any{
							"command":       "sleep 0.05; printf 'async bash done'",
							"workdir":       ".",
							"yield_time_ms": 5,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-1",
						Name: tasktool.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": 250,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "async bash done"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func mustFindTaskID(t *testing.T, req *model.Request) string {
	t.Helper()
	if req == nil {
		t.Fatal("request = nil")
	}
	for _, message := range req.Messages {
		for _, result := range message.ToolResults() {
			for _, part := range result.Content {
				if part.Kind != model.PartKindJSON || part.JSON == nil {
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal(part.JSONValue(), &payload); err != nil {
					continue
				}
				if taskID, _ := payload["task_id"].(string); strings.TrimSpace(taskID) != "" {
					return strings.TrimSpace(taskID)
				}
			}
		}
	}
	raw, _ := json.MarshalIndent(req, "", "  ")
	t.Fatalf("did not find yielded task_id in request transcript:\n%s", string(raw))
	return ""
}

type spawnTaskLoopRuntimeModel struct {
	t      *testing.T
	calls  int
	taskID string
}

type spawnApprovalTaskLoopRuntimeModel struct {
	t      *testing.T
	agent  string
	calls  int
	taskID string
}

type spawnProbeTaskLoopRuntimeModel struct {
	t      *testing.T
	calls  int
	taskID string
}

func (m *spawnTaskLoopRuntimeModel) Name() string { return "spawn-task-loop" }

func (m *spawnApprovalTaskLoopRuntimeModel) Name() string { return "spawn-approval-task-loop" }

func (m *spawnProbeTaskLoopRuntimeModel) Name() string { return "spawn-probe-task-loop" }

func (m *spawnTaskLoopRuntimeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "spawn-1",
						Name: spawn.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"agent":  "self",
							"prompt": "Reply with exactly: spawn child ok",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-spawn-1",
						Name: tasktool.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": 300,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "spawn child ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *spawnApprovalTaskLoopRuntimeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	agent := strings.TrimSpace(m.agent)
	if agent == "" {
		agent = "codex"
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "spawn-approval-1",
						Name: spawn.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"agent":  agent,
							"prompt": "Run the approval flow and reply with exactly: child approval ok",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-spawn-approval-1",
						Name: tasktool.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": 600,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "child approval ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *spawnProbeTaskLoopRuntimeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "spawn-probe-1",
						Name: spawn.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"agent":  "self",
							"prompt": "Check whether SPAWN is available and reply with exactly the result.",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-spawn-probe-1",
						Name: tasktool.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"action":        "wait",
							"task_id":       m.taskID,
							"yield_time_ms": 300,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "spawn disabled"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func mustSessionTaskID(t *testing.T, events []*session.Event) string {
	t.Helper()
	for _, event := range events {
		if event == nil {
			continue
		}
		if taskID := taskIDFromSessionEvent(event); strings.TrimSpace(taskID) != "" {
			return taskID
		}
	}
	t.Fatal("did not find task_id in persisted session events")
	return ""
}

func eventToolRawOutput(event *session.Event) map[string]any {
	if update := session.ProtocolUpdateOf(event); update != nil {
		return update.RawOutput
	}
	return nil
}

func eventToolRawInput(event *session.Event) map[string]any {
	if update := session.ProtocolUpdateOf(event); update != nil {
		return update.RawInput
	}
	return nil
}

func taskIDFromSessionEvent(event *session.Event) string {
	for _, values := range []map[string]any{eventToolRawOutput(event), eventToolRawInput(event)} {
		if taskID, _ := values["task_id"].(string); strings.TrimSpace(taskID) != "" {
			return strings.TrimSpace(taskID)
		}
	}
	return ""
}

func terminalFramesText(frames []stream.Frame) string {
	var out strings.Builder
	for _, frame := range frames {
		out.WriteString(frame.Text)
	}
	return out.String()
}

type approvalRequesterFunc func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error)

func (f approvalRequesterFunc) RequestApproval(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	return f(ctx, req)
}

type attemptFactory struct {
	mu     sync.Mutex
	agents []agent.Agent
	specs  []agent.AgentSpec
	calls  int
}

func (f *attemptFactory) NewAgent(_ context.Context, spec agent.AgentSpec) (agent.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls >= len(f.agents) {
		return nil, errors.New("no more agents configured")
	}
	f.specs = append(f.specs, spec)
	agent := f.agents[f.calls]
	f.calls++
	return agent, nil
}

func (f *attemptFactory) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *attemptFactory) Specs() []agent.AgentSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]agent.AgentSpec, len(f.specs))
	copy(out, f.specs)
	return out
}

type seqAgent struct {
	events []*session.Event
	err    error
}

func (a seqAgent) Name() string { return "seq" }

func (a seqAgent) Run(agent.Context) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, event := range a.events {
			if !yield(session.CloneEvent(event), nil) {
				return
			}
		}
		if a.err != nil {
			yield(nil, a.err)
		}
	}
}

type yieldProbeSandboxRuntime struct {
	session *yieldProbeSandboxSession
}

func (r *yieldProbeSandboxRuntime) Describe() sandbox.Descriptor {
	return sandbox.Descriptor{
		Backend:   sandbox.BackendHost,
		Isolation: sandbox.IsolationHost,
		Capabilities: sandbox.CapabilitySet{
			CommandExec:   true,
			AsyncSessions: true,
		},
	}
}

func (r *yieldProbeSandboxRuntime) FileSystem() sandbox.FileSystem { return nil }

func (r *yieldProbeSandboxRuntime) FileSystemFor(sandbox.Constraints) sandbox.FileSystem {
	return nil
}

func (r *yieldProbeSandboxRuntime) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, nil
}

func (r *yieldProbeSandboxRuntime) Start(_ context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	if r.session == nil {
		r.session = newYieldProbeSandboxSession()
	}
	r.session.command = req.Command
	r.session.workdir = req.Dir
	return r.session, nil
}

func (r *yieldProbeSandboxRuntime) OpenSession(string) (sandbox.Session, error) {
	if r.session == nil {
		r.session = newYieldProbeSandboxSession()
	}
	return r.session, nil
}

func (r *yieldProbeSandboxRuntime) OpenSessionRef(ref sandbox.SessionRef) (sandbox.Session, error) {
	return r.OpenSession(ref.SessionID)
}

func (r *yieldProbeSandboxRuntime) SupportedBackends() []sandbox.Backend {
	return []sandbox.Backend{sandbox.BackendHost}
}

func (r *yieldProbeSandboxRuntime) Status() sandbox.Status {
	return sandbox.Status{
		RequestedBackend: sandbox.BackendHost,
		ResolvedBackend:  sandbox.BackendHost,
	}
}

func (r *yieldProbeSandboxRuntime) Close() error { return nil }

type yieldProbeSandboxSession struct {
	command       string
	workdir       string
	lastWait      time.Duration
	waitErr       error
	statusRunning *bool
	terminated    bool
}

func newYieldProbeSandboxSession() *yieldProbeSandboxSession {
	return &yieldProbeSandboxSession{}
}

func (s *yieldProbeSandboxSession) Ref() sandbox.SessionRef {
	return sandbox.SessionRef{Backend: sandbox.BackendHost, SessionID: "yield-probe-session"}
}

func (s *yieldProbeSandboxSession) Terminal() sandbox.TerminalRef {
	return sandbox.TerminalRef{
		Backend:    sandbox.BackendHost,
		SessionID:  "yield-probe-session",
		TerminalID: "yield-probe-terminal",
	}
}

func (s *yieldProbeSandboxSession) WriteInput(context.Context, []byte) error { return nil }

func (s *yieldProbeSandboxSession) ReadOutput(context.Context, int64, int64) ([]byte, []byte, int64, int64, error) {
	return nil, nil, 0, 0, nil
}

func (s *yieldProbeSandboxSession) Status(context.Context) (sandbox.SessionStatus, error) {
	running := true
	if s.statusRunning != nil {
		running = *s.statusRunning
	}
	return sandbox.SessionStatus{
		SessionRef:    s.Ref(),
		Terminal:      s.Terminal(),
		Running:       running,
		SupportsInput: true,
		UpdatedAt:     time.Now(),
	}, nil
}

func (s *yieldProbeSandboxSession) Wait(_ context.Context, timeout time.Duration) (sandbox.SessionStatus, error) {
	s.lastWait = timeout
	if s.waitErr != nil {
		return sandbox.SessionStatus{}, s.waitErr
	}
	return s.Status(context.Background())
}

func (s *yieldProbeSandboxSession) Result(context.Context) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, nil
}

func (s *yieldProbeSandboxSession) Terminate(context.Context) error {
	s.terminated = true
	return nil
}

type runningOnlyProbeSandboxSession struct {
	lastWait time.Duration
}

type runningOnlyProbeSandboxRuntime struct {
	session *runningOnlyProbeSandboxSession
}

func (s *runningOnlyProbeSandboxSession) Ref() sandbox.SessionRef {
	return sandbox.SessionRef{Backend: sandbox.BackendHost, SessionID: "running-only-session"}
}

func (s *runningOnlyProbeSandboxSession) Terminal() sandbox.TerminalRef {
	return sandbox.TerminalRef{
		Backend:    sandbox.BackendHost,
		SessionID:  "running-only-session",
		TerminalID: "running-only-terminal",
	}
}

func (s *runningOnlyProbeSandboxSession) WriteInput(context.Context, []byte) error { return nil }

func (s *runningOnlyProbeSandboxSession) ReadOutput(context.Context, int64, int64) ([]byte, []byte, int64, int64, error) {
	return nil, nil, 0, 0, nil
}

func (s *runningOnlyProbeSandboxSession) Status(context.Context) (sandbox.SessionStatus, error) {
	return sandbox.SessionStatus{
		SessionRef:    s.Ref(),
		Terminal:      s.Terminal(),
		Running:       true,
		SupportsInput: true,
		UpdatedAt:     time.Now(),
	}, nil
}

func (s *runningOnlyProbeSandboxSession) Wait(_ context.Context, timeout time.Duration) (sandbox.SessionStatus, error) {
	s.lastWait = timeout
	return s.Status(context.Background())
}

func (s *runningOnlyProbeSandboxSession) Result(context.Context) (sandbox.CommandResult, error) {
	panic("waitBash should not request Result while task is still running")
}

func (s *runningOnlyProbeSandboxSession) Terminate(context.Context) error { return nil }

func (r *runningOnlyProbeSandboxRuntime) Describe() sandbox.Descriptor {
	return sandbox.Descriptor{
		Backend:   sandbox.BackendHost,
		Isolation: sandbox.IsolationHost,
		Capabilities: sandbox.CapabilitySet{
			CommandExec:   true,
			AsyncSessions: true,
		},
	}
}

func (r *runningOnlyProbeSandboxRuntime) FileSystem() sandbox.FileSystem { return nil }

func (r *runningOnlyProbeSandboxRuntime) FileSystemFor(sandbox.Constraints) sandbox.FileSystem {
	return nil
}

func (r *runningOnlyProbeSandboxRuntime) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, nil
}

func (r *runningOnlyProbeSandboxRuntime) Start(_ context.Context, _ sandbox.CommandRequest) (sandbox.Session, error) {
	if r.session == nil {
		r.session = &runningOnlyProbeSandboxSession{}
	}
	return r.session, nil
}

func (r *runningOnlyProbeSandboxRuntime) OpenSession(string) (sandbox.Session, error) {
	if r.session == nil {
		r.session = &runningOnlyProbeSandboxSession{}
	}
	return r.session, nil
}

func (r *runningOnlyProbeSandboxRuntime) OpenSessionRef(ref sandbox.SessionRef) (sandbox.Session, error) {
	return r.OpenSession(ref.SessionID)
}

func (r *runningOnlyProbeSandboxRuntime) SupportedBackends() []sandbox.Backend {
	return []sandbox.Backend{sandbox.BackendHost}
}

func (r *runningOnlyProbeSandboxRuntime) Status() sandbox.Status {
	return sandbox.Status{
		RequestedBackend: sandbox.BackendHost,
		ResolvedBackend:  sandbox.BackendHost,
	}
}

func (r *runningOnlyProbeSandboxRuntime) Close() error { return nil }

func newRuntimeBashToolTestHarness(t *testing.T) (session.Service, session.Session, *Runtime) {
	t.Helper()

	sessions, activeSession := newTestSessionService(t, "sess-bash-yield-default")
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
	return sessions, activeSession, runtime
}

func mustRuntimeBashTool(t *testing.T, runtime sandbox.Runtime) tool.Tool {
	t.Helper()

	targetTool, err := shell.NewBash(shell.BashConfig{Runtime: runtime})
	if err != nil {
		t.Fatalf("shell.NewBash() error = %v", err)
	}
	return targetTool
}

func callRuntimeBashTool(t *testing.T, bashTool runtimeBashTool, args map[string]any) tool.Result {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := bashTool.Call(context.Background(), tool.Call{
		ID:    "bash-yield-test",
		Name:  shell.BashToolName,
		Input: raw,
	})
	if err != nil {
		t.Fatalf("bashTool.Call() error = %v", err)
	}
	return result
}

func callRuntimeTaskTool(t *testing.T, taskTool runtimeTaskTool, args map[string]any) tool.Result {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := taskTool.Call(context.Background(), tool.Call{
		ID:    "task-control-test",
		Name:  tasktool.ToolName,
		Input: raw,
	})
	if err != nil {
		t.Fatalf("taskTool.Call() error = %v", err)
	}
	return result
}

func testToolResultPayload(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want JSON payload", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	return payload
}

func testToolResultRuntimeMeta(t *testing.T, result tool.Result, section string) map[string]any {
	t.Helper()
	caelis, _ := result.Metadata["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	values, _ := runtimeMeta[section].(map[string]any)
	if values == nil {
		t.Fatalf("result.Metadata caelis.runtime.%s = %#v", section, result.Metadata)
	}
	return values
}

func assertRunningTaskSnapshot(t *testing.T, result tool.Result) {
	t.Helper()

	if len(result.Content) == 0 {
		t.Fatal("result.Content = empty, want task snapshot payload")
	}
	part := result.Content[0]
	if part.Kind != model.PartKindJSON || part.JSON == nil {
		t.Fatalf("result.Content[0] = %#v, want json part", part)
	}
	var payload map[string]any
	if err := json.Unmarshal(part.JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(snapshot) error = %v", err)
	}
	if got, _ := payload["state"].(string); got != string(taskapi.StateRunning) {
		t.Fatalf("snapshot state = %q, want %q", got, taskapi.StateRunning)
	}
	if strings.TrimSpace(testStringValue(payload["task_id"])) == "" {
		t.Fatalf("snapshot task_id missing: %#v", payload)
	}
}

func testStringValue(raw any) string {
	text, _ := raw.(string)
	return strings.TrimSpace(text)
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
