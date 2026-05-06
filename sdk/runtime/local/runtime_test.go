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

	sdkcompact "github.com/OnslaughtSnail/caelis/sdk/compact"
	sdkcontroller "github.com/OnslaughtSnail/caelis/sdk/controller"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdkpolicy "github.com/OnslaughtSnail/caelis/sdk/policy"
	policypresets "github.com/OnslaughtSnail/caelis/sdk/policy/presets"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	"github.com/OnslaughtSnail/caelis/sdk/runtime/agents/chat"
	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/host"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sessionfile "github.com/OnslaughtSnail/caelis/sdk/session/file"
	"github.com/OnslaughtSnail/caelis/sdk/session/inmemory"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
	sdktask "github.com/OnslaughtSnail/caelis/sdk/task"
	taskfile "github.com/OnslaughtSnail/caelis/sdk/task/file"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/filesystem"
	sdkplan "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/plan"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/shell"
	spawntool "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/spawn"
	tasktool "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/task"
)

func TestRuntimeRunPersistsMinimalChatTurn(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "sess-1" },
	}))
	session, err := sessions.StartSession(context.Background(), sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
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

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "hello",
		AgentSpec: sdkruntime.AgentSpec{
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

	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if got := sdksession.EventText(loaded.Events[1]); got != "world" {
		t.Fatalf("assistant text = %q, want %q", got, "world")
	}

	state, err := runtime.RunState(context.Background(), session.SessionRef)
	if err != nil {
		t.Fatalf("RunState() error = %v", err)
	}
	if state.Status != sdkruntime.RunLifecycleStatusCompleted {
		t.Fatalf("state.Status = %q, want %q", state.Status, sdkruntime.RunLifecycleStatusCompleted)
	}
}

func drainRunnerEvents(t *testing.T, handle sdkruntime.Runner) ([]*sdksession.Event, error) {
	t.Helper()
	if handle == nil {
		return nil, nil
	}
	var events []*sdksession.Event
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

func lastAssistantText(events []*sdksession.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event != nil && event.Type == sdksession.EventTypeAssistant {
			return strings.TrimSpace(sdksession.EventText(event))
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
	session, err := sessions.StartSession(context.Background(), sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-live",
			CWD: "/tmp/project",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	model := &gatedStreamingModel{
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
		result sdkruntime.RunResult
		err    error
	}
	runDone := make(chan runResult, 1)
	go func() {
		result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
			SessionRef: session.SessionRef,
			Input:      "hello",
			Request: sdkruntime.ModelRequestOptions{
				Stream: boolPtr(true),
			},
			AgentSpec: sdkruntime.AgentSpec{
				Name:  "chat",
				Model: model,
			},
		})
		runDone <- runResult{result: result, err: err}
	}()

	select {
	case <-model.started:
	case <-time.After(2 * time.Second):
		t.Fatal("model did not start")
	}

	var result sdkruntime.RunResult
	select {
	case got := <-runDone:
		if got.err != nil {
			t.Fatalf("Run() error = %v", got.err)
		}
		result = got.result
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Run() did not return before model completion")
	}

	state, err := runtime.RunState(context.Background(), session.SessionRef)
	if err != nil {
		t.Fatalf("RunState() error = %v", err)
	}
	if state.Status != sdkruntime.RunLifecycleStatusRunning {
		t.Fatalf("state.Status = %q, want %q while final response is gated", state.Status, sdkruntime.RunLifecycleStatusRunning)
	}

	eventCh := make(chan *sdksession.Event, 8)
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
			case sdksession.EventTypeOf(event) == sdksession.EventTypeUser:
				sawUser = true
			case event.Protocol != nil && event.Protocol.UpdateType == string(sdksession.ProtocolUpdateTypeAgentMessage) && sdksession.EventText(event) == "hel":
				sawChunk = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for live user + chunk events (sawUser=%v sawChunk=%v)", sawUser, sawChunk)
		}
	}

	close(model.releaseFinal)

	var final *sdksession.Event
	for event := range eventCh {
		if event != nil && sdksession.EventTypeOf(event) == sdksession.EventTypeAssistant && strings.TrimSpace(sdksession.EventText(event)) == "hello" {
			final = event
		}
	}
	if final == nil {
		t.Fatal("final assistant event was not emitted")
	}

	state, err = runtime.RunState(context.Background(), session.SessionRef)
	if err != nil {
		t.Fatalf("RunState() after completion error = %v", err)
	}
	if state.Status != sdkruntime.RunLifecycleStatusCompleted {
		t.Fatalf("state.Status = %q, want %q after completion", state.Status, sdkruntime.RunLifecycleStatusCompleted)
	}

	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d (chunk events must stay transient)", got, want)
	}
}

func TestRuntimeACPControllerReturnsLiveRunnerBeforeTurnCompletion(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "sess-acp-live" },
	}))
	session, err := sessions.StartSession(context.Background(), sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-acp-live",
			CWD: "/tmp/project",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	session, err = sessions.BindController(context.Background(), sdksession.BindControllerRequest{
		SessionRef: session.SessionRef,
		Binding: sdksession.ControllerBinding{
			Kind:         sdksession.ControllerKindACP,
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
	controller := stubACPController{
		runTurn: func(ctx context.Context, req sdkcontroller.TurnRequest) (sdkcontroller.TurnResult, error) {
			streamSeen <- req.Stream
			handle := newTestControllerTurnHandle(nil)
			go func() {
				handle.publishEvent(sdksession.MarkUIOnly(&sdksession.Event{
					Type: sdksession.EventTypeAssistant,
					Text: "hel",
					Protocol: &sdksession.EventProtocol{
						UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage),
					},
				}))
				<-releaseFinal
				handle.publishEvent(&sdksession.Event{
					Type:       sdksession.EventTypeAssistant,
					Visibility: sdksession.VisibilityCanonical,
					Text:       "hello",
					Protocol: &sdksession.EventProtocol{
						UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage),
					},
				})
				handle.finish()
			}()
			return sdkcontroller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  controller,
		RunIDGenerator: func() string {
			return "run-acp-live"
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	type runResult struct {
		result sdkruntime.RunResult
		err    error
	}
	runDone := make(chan runResult, 1)
	go func() {
		result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
			SessionRef: session.SessionRef,
			Input:      "hello",
			Request: sdkruntime.ModelRequestOptions{
				Stream: boolPtr(true),
			},
		})
		runDone <- runResult{result: result, err: err}
	}()

	var result sdkruntime.RunResult
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

	eventCh := make(chan *sdksession.Event, 8)
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
			case sdksession.EventTypeOf(event) == sdksession.EventTypeUser:
				sawUser = true
			case event.Protocol != nil && event.Protocol.UpdateType == string(sdksession.ProtocolUpdateTypeAgentMessage) && event.Visibility == sdksession.VisibilityUIOnly:
				sawChunk = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for live ACP user + chunk events (sawUser=%v sawChunk=%v)", sawUser, sawChunk)
		}
	}

	close(releaseFinal)

	var final *sdksession.Event
	for event := range eventCh {
		if event != nil && sdksession.EventTypeOf(event) == sdksession.EventTypeAssistant && strings.TrimSpace(sdksession.EventText(event)) == "hello" {
			final = event
		}
	}
	if final == nil {
		t.Fatal("final ACP assistant event was not emitted")
	}

	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if loaded.Events[1].Visibility != sdksession.VisibilityCanonical || sdksession.EventText(loaded.Events[1]) != "hello" {
		t.Fatalf("loaded final event = %+v, want canonical assistant hello", loaded.Events[1])
	}
}

func TestRuntimeACPControllerTurnSendsUnsyncedSharedDialogue(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-acp-shared-delta-turn")
	if _, err := sessions.AppendEvent(context.Background(), sdksession.AppendEventRequest{
		SessionRef: session.SessionRef,
		Event:      userTextEvent("already synced"),
	}); err != nil {
		t.Fatalf("AppendEvent(initial) error = %v", err)
	}
	session, err := sessions.BindController(context.Background(), sdksession.BindControllerRequest{
		SessionRef: session.SessionRef,
		Binding: sdksession.ControllerBinding{
			Kind:           sdksession.ControllerKindACP,
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
	if _, err := sessions.AppendEvent(context.Background(), sdksession.AppendEventRequest{
		SessionRef: session.SessionRef,
		Event: &sdksession.Event{
			Type:       sdksession.EventTypeAssistant,
			Visibility: sdksession.VisibilityCanonical,
			Text:       "side result",
			Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindParticipant, Name: "jeff"},
			Scope: &sdksession.EventScope{
				Participant: sdksession.ParticipantRef{
					ID:   "side-1",
					Kind: sdksession.ParticipantKindSubagent,
					Role: sdksession.ParticipantRoleSidecar,
				},
			},
		},
	}); err != nil {
		t.Fatalf("AppendEvent(side) error = %v", err)
	}

	turnReqCh := make(chan sdkcontroller.TurnRequest, 1)
	controller := stubACPController{
		runTurn: func(ctx context.Context, req sdkcontroller.TurnRequest) (sdkcontroller.TurnResult, error) {
			turnReqCh <- req
			handle := newTestControllerTurnHandle(nil)
			go func() {
				handle.publishEvent(&sdksession.Event{
					Type:       sdksession.EventTypeAssistant,
					Visibility: sdksession.VisibilityCanonical,
					Text:       "main done",
					Protocol: &sdksession.EventProtocol{
						UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage),
					},
				})
				handle.finish()
			}()
			return sdkcontroller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  controller,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "next prompt",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("drain runner: %v", err)
	}
	var turnReq sdkcontroller.TurnRequest
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
	updated, err := sessions.Session(context.Background(), session.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if updated.Controller.ContextSyncSeq < 4 {
		t.Fatalf("controller ContextSyncSeq = %d, want current shared ledger checkpoint", updated.Controller.ContextSyncSeq)
	}
}

func TestRuntimePromptACPParticipantPersistsPublicDialogue(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-acp-side-dialogue")
	session, err := sessions.PutParticipant(context.Background(), sdksession.PutParticipantRequest{
		SessionRef: session.SessionRef,
		Binding: sdksession.ParticipantBinding{
			ID:        "emma",
			Kind:      sdksession.ParticipantKindACP,
			Role:      sdksession.ParticipantRoleSidecar,
			Label:     "@emma",
			AgentName: "claude",
			Source:    "tui_agent_add",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}
	turnReqCh := make(chan sdkcontroller.ParticipantPromptRequest, 1)
	controller := stubACPController{
		promptParticipant: func(ctx context.Context, req sdkcontroller.ParticipantPromptRequest) (sdkcontroller.TurnResult, error) {
			turnReqCh <- req
			handle := newTestControllerTurnHandle(nil)
			go func() {
				defer handle.finish()
				handle.publishEvent(&sdksession.Event{
					Type:       sdksession.EventTypeUser,
					Visibility: sdksession.VisibilityCanonical,
					Text:       req.Input,
					Scope: &sdksession.EventScope{
						Source: "acp_participant",
						Participant: sdksession.ParticipantRef{
							ID:   req.ParticipantID,
							Kind: sdksession.ParticipantKindACP,
							Role: sdksession.ParticipantRoleSidecar,
						},
					},
					Protocol: &sdksession.EventProtocol{
						UpdateType: string(sdksession.ProtocolUpdateTypeUserMessage),
					},
				})
				handle.publishEvent(&sdksession.Event{
					Type:       sdksession.EventTypeAssistant,
					Visibility: sdksession.VisibilityUIOnly,
					Text:       "emma summary",
					Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindParticipant, ID: "emma", Name: "@emma"},
					Scope: &sdksession.EventScope{
						Source: "acp_participant",
						Participant: sdksession.ParticipantRef{
							ID:   req.ParticipantID,
							Kind: sdksession.ParticipantKindACP,
							Role: sdksession.ParticipantRoleSidecar,
						},
					},
					Protocol: &sdksession.EventProtocol{
						UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage),
					},
				})
			}()
			return sdkcontroller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  controller,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	updated, err := runtime.PromptACPParticipant(context.Background(), sdkruntime.PromptACPParticipantRequest{
		SessionRef:    session.SessionRef,
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
	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{SessionRef: updated.Session.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var sideUsers, sideAssistants int
	for _, event := range loaded.Events {
		if event == nil || event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) != "emma" {
			continue
		}
		switch sdksession.EventTypeOf(event) {
		case sdksession.EventTypeUser:
			sideUsers++
			if got := strings.TrimSpace(sdksession.EventText(event)); got != "刚才都做了什么？总结一下" {
				t.Fatalf("side user text = %q", got)
			}
			if !sdksession.IsMainInvocationVisibleEvent(event) {
				t.Fatalf("side user event is not visible to main invocation: %#v", event)
			}
		case sdksession.EventTypeAssistant:
			sideAssistants++
			if got := strings.TrimSpace(sdksession.EventText(event)); got != "emma summary" {
				t.Fatalf("side assistant text = %q", got)
			}
			if !sdksession.IsMainInvocationVisibleEvent(event) {
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

	sessions, session := newTestSessionService(t, "sess-acp-side-cancel")
	session, err := sessions.PutParticipant(context.Background(), sdksession.PutParticipantRequest{
		SessionRef: session.SessionRef,
		Binding: sdksession.ParticipantBinding{
			ID:        "emma",
			Kind:      sdksession.ParticipantKindACP,
			Role:      sdksession.ParticipantRoleSidecar,
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
	turnReqCh := make(chan sdkcontroller.ParticipantPromptRequest, 1)
	controller := stubACPController{
		promptParticipant: func(ctx context.Context, req sdkcontroller.ParticipantPromptRequest) (sdkcontroller.TurnResult, error) {
			_ = ctx
			turnReqCh <- req
			return sdkcontroller.TurnResult{Handle: controllerHandle}, nil
		},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  controller,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.PromptACPParticipant(context.Background(), sdkruntime.PromptACPParticipantRequest{
		SessionRef:    session.SessionRef,
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
	if !result.Handle.Cancel() {
		t.Fatal("participant handle Cancel() = false, want true")
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
	session, err := sessions.StartSession(context.Background(), sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-acp-deltas",
			CWD: "/tmp/project",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	session, err = sessions.BindController(context.Background(), sdksession.BindControllerRequest{
		SessionRef: session.SessionRef,
		Binding: sdksession.ControllerBinding{
			Kind:         sdksession.ControllerKindACP,
			ControllerID: "acp-main",
			Label:        "ACP Main",
			EpochID:      "epoch-delta",
			Source:       "test",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}

	controller := stubACPController{
		runTurn: func(context.Context, sdkcontroller.TurnRequest) (sdkcontroller.TurnResult, error) {
			handle := newTestControllerTurnHandle(nil)
			go func() {
				handle.publishEvent(acpControllerChunk("hel"))
				handle.publishEvent(acpControllerChunk("hello"))
				handle.finish()
			}()
			return sdkcontroller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(Config{
		Sessions:       sessions,
		AgentFactory:   chat.Factory{SystemPrompt: "Be terse."},
		Controllers:    controller,
		RunIDGenerator: func() string { return "run-acp-deltas" },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "hello",
		Request: sdkruntime.ModelRequestOptions{
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
		if event.Protocol.UpdateType == string(sdksession.ProtocolUpdateTypeAgentMessage) && strings.HasPrefix(event.Scope.Source, "acp") && event.Visibility == sdksession.VisibilityUIOnly {
			liveTexts = append(liveTexts, sdksession.EventText(event))
			if event.SessionID != session.SessionID {
				t.Fatalf("live ACP chunk session ID = %q, want %q", event.SessionID, session.SessionID)
			}
			if strings.TrimSpace(event.ID) != "" {
				t.Fatalf("live ACP chunk ID = %q, want empty live event ID", event.ID)
			}
		}
	}
	if !reflect.DeepEqual(liveTexts, []string{"hel", "lo"}) {
		t.Fatalf("live ACP texts = %#v, want delta chunks", liveTexts)
	}

	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{SessionRef: session.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var persistedTexts []string
	for _, event := range loaded.Events {
		if event == nil || event.Protocol == nil || event.Scope == nil {
			continue
		}
		if event.Protocol.UpdateType == string(sdksession.ProtocolUpdateTypeAgentMessage) && strings.HasPrefix(event.Scope.Source, "acp") {
			persistedTexts = append(persistedTexts, sdksession.EventText(event))
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

	sessions, session := newTestSessionService(t, "sess-handoff-shared-ledger")
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	session, err = sessions.PutParticipant(context.Background(), sdksession.PutParticipantRequest{
		SessionRef: session.SessionRef,
		Binding: sdksession.ParticipantBinding{
			ID:           "participant-1",
			Kind:         sdksession.ParticipantKindSubagent,
			Role:         sdksession.ParticipantRoleDelegated,
			Label:        "@ella",
			AgentName:    "codex",
			DelegationID: "task-1",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}
	events := []*sdksession.Event{
		{Type: sdksession.EventTypeUser, Visibility: sdksession.VisibilityCanonical, Text: "user prompt"},
		{Type: sdksession.EventTypeToolResult, Visibility: sdksession.VisibilityCanonical, Text: "tool output"},
		{Type: sdksession.EventTypeAssistant, Visibility: sdksession.VisibilityCanonical, Text: "child answer", Actor: sdksession.ActorRef{Kind: sdksession.ActorKindParticipant, Name: "ella"}, Scope: &sdksession.EventScope{Participant: sdksession.ParticipantRef{ID: "participant-1", Kind: sdksession.ParticipantKindSubagent}}},
		sdksession.MarkUIOnly(&sdksession.Event{Type: sdksession.EventTypeAssistant, Text: "live chunk"}),
	}
	for _, event := range events {
		if _, err := sessions.AppendEvent(context.Background(), sdksession.AppendEventRequest{SessionRef: session.SessionRef, Event: event}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	text, seq := runtime.buildControllerHandoffContext(context.Background(), session, session.SessionRef, sdksession.ControllerBinding{
		Kind:           sdksession.ControllerKindACP,
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

	compactMessage := sdkmodel.NewTextMessage(sdkmodel.RoleUser, "CONTEXT CHECKPOINT\nObjective: compacted baseline")
	events := []*sdksession.Event{
		userTextEvent("old user"),
		assistantEvent("old assistant"),
		{
			Type:       sdksession.EventTypeCompact,
			Visibility: sdksession.VisibilityCanonical,
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

	sessions, session := newTestSessionService(t, "sess-assembly-overrides")
	if err := sessions.UpdateState(context.Background(), session.SessionRef, func(state map[string]any) (map[string]any, error) {
		state = sdkplugin.SetCurrentModeID(state, "plan")
		state = sdkplugin.SetCurrentConfigValue(state, "reasoning", "deep")
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	factory := &attemptFactory{
		agents: []sdkruntime.Agent{seqAgent{events: []*sdksession.Event{assistantEvent("ok")}}},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: factory,
		Assembly: sdkplugin.ResolvedAssembly{
			Modes: []sdkplugin.ModeConfig{
				{
					ID: "default",
					Runtime: sdkplugin.RuntimeOverrides{
						PolicyMode:   "default",
						SystemPrompt: "mode-default-marker",
					},
				},
				{
					ID: "plan",
					Runtime: sdkplugin.RuntimeOverrides{
						PolicyMode:   "plan",
						SystemPrompt: "mode-plan-marker",
					},
				},
			},
			Configs: []sdkplugin.ConfigOption{{
				ID:           "reasoning",
				DefaultValue: "balanced",
				Options: []sdkplugin.ConfigSelectOption{
					{
						Value: "balanced",
						Runtime: sdkplugin.RuntimeOverrides{
							Reasoning: sdkmodel.ReasoningConfig{Effort: "medium"},
						},
					},
					{
						Value: "deep",
						Runtime: sdkplugin.RuntimeOverrides{
							Reasoning: sdkmodel.ReasoningConfig{Effort: "high"},
						},
					},
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "hello",
		AgentSpec:  sdkruntime.AgentSpec{Name: "chat"},
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

	sessions, session := newTestSessionService(t, "sess-assembly-order")
	if err := sessions.UpdateState(context.Background(), session.SessionRef, func(state map[string]any) (map[string]any, error) {
		state = sdkplugin.SetCurrentConfigValue(state, "first", "on")
		state = sdkplugin.SetCurrentConfigValue(state, "second", "on")
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	factory := &attemptFactory{
		agents: []sdkruntime.Agent{seqAgent{events: []*sdksession.Event{assistantEvent("ok")}}},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: factory,
		Assembly: sdkplugin.ResolvedAssembly{
			Configs: []sdkplugin.ConfigOption{
				{
					ID: "first",
					Options: []sdkplugin.ConfigSelectOption{{
						Value: "on",
						Runtime: sdkplugin.RuntimeOverrides{
							SystemPrompt: "first-prompt",
						},
					}},
				},
				{
					ID: "second",
					Options: []sdkplugin.ConfigSelectOption{{
						Value: "on",
						Runtime: sdkplugin.RuntimeOverrides{
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

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "hello",
		AgentSpec:  sdkruntime.AgentSpec{Name: "chat"},
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
		Assembly: sdkplugin.ResolvedAssembly{
			Agents: []sdkplugin.AgentConfig{{
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
	runTurn           func(context.Context, sdkcontroller.TurnRequest) (sdkcontroller.TurnResult, error)
	promptParticipant func(context.Context, sdkcontroller.ParticipantPromptRequest) (sdkcontroller.TurnResult, error)
}

func (stubACPController) Activate(context.Context, sdkcontroller.HandoffRequest) (sdksession.ControllerBinding, error) {
	return sdksession.ControllerBinding{}, nil
}

func (stubACPController) Deactivate(context.Context, sdksession.SessionRef) error {
	return nil
}

func (s stubACPController) RunTurn(ctx context.Context, req sdkcontroller.TurnRequest) (sdkcontroller.TurnResult, error) {
	if s.runTurn != nil {
		return s.runTurn(ctx, req)
	}
	handle := newTestControllerTurnHandle(nil)
	handle.finish()
	return sdkcontroller.TurnResult{Handle: handle}, nil
}

func (stubACPController) Attach(context.Context, sdkcontroller.AttachRequest) (sdksession.ParticipantBinding, error) {
	return sdksession.ParticipantBinding{}, nil
}

func (s stubACPController) PromptParticipant(ctx context.Context, req sdkcontroller.ParticipantPromptRequest) (sdkcontroller.TurnResult, error) {
	if s.promptParticipant != nil {
		return s.promptParticipant(ctx, req)
	}
	handle := newTestControllerTurnHandle(nil)
	handle.finish()
	return sdkcontroller.TurnResult{Handle: handle}, nil
}

func (stubACPController) Detach(context.Context, sdkcontroller.DetachRequest) error {
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
	event *sdksession.Event
	err   error
}

func newTestControllerTurnHandle(cancel context.CancelFunc) *testControllerTurnHandle {
	return &testControllerTurnHandle{
		cancelFn: cancel,
		eventsCh: make(chan testControllerTurnEvent, 16),
	}
}

func (h *testControllerTurnHandle) Events() iter.Seq2[*sdksession.Event, error] {
	return func(yield func(*sdksession.Event, error) bool) {
		for item := range h.eventsCh {
			if !yield(sdksession.CloneEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (h *testControllerTurnHandle) Cancel() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelled {
		return false
	}
	h.cancelled = true
	if h.cancelFn != nil {
		h.cancelFn()
	}
	return true
}

func (h *testControllerTurnHandle) Close() error { return nil }

func (h *testControllerTurnHandle) publishEvent(event *sdksession.Event) {
	if h == nil || event == nil {
		return
	}
	h.eventsCh <- testControllerTurnEvent{event: sdksession.CloneEvent(event)}
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

	sessions, session := newTestSessionService(t, "sess-assembly-stale")
	if err := sessions.UpdateState(context.Background(), session.SessionRef, func(state map[string]any) (map[string]any, error) {
		state = sdkplugin.SetCurrentConfigValue(state, "reasoning", "stale")
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	factory := &attemptFactory{
		agents: []sdkruntime.Agent{seqAgent{events: []*sdksession.Event{assistantEvent("ok")}}},
	}
	runtime, err := New(Config{
		Sessions:     sessions,
		AgentFactory: factory,
		Assembly: sdkplugin.ResolvedAssembly{
			Configs: []sdkplugin.ConfigOption{{
				ID:           "reasoning",
				DefaultValue: "balanced",
				Options: []sdkplugin.ConfigSelectOption{
					{
						Value: "balanced",
						Runtime: sdkplugin.RuntimeOverrides{
							SystemPrompt: "balanced-prompt",
							Reasoning:    sdkmodel.ReasoningConfig{Effort: "medium"},
						},
					},
					{
						Value: "deep",
						Runtime: sdkplugin.RuntimeOverrides{
							SystemPrompt: "deep-prompt",
							Reasoning:    sdkmodel.ReasoningConfig{Effort: "high"},
						},
					},
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "hello",
		AgentSpec:  sdkruntime.AgentSpec{Name: "chat"},
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
	session, err := sessions.StartSession(context.Background(), sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
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

	result1, err := runtime1.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "hello",
		AgentSpec: sdkruntime.AgentSpec{
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
	result, err := runtime2.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "again",
		AgentSpec: sdkruntime.AgentSpec{
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

	loaded, err := reopenedSessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 4; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if got := sdksession.EventText(loaded.Events[3]); got != "history ok" {
		t.Fatalf("assistant replay text = %q, want %q", got, "history ok")
	}
}

func TestRuntimeCompactionInjectsCheckpointAndTrimsOldHistory(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-compact-heuristic")
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Project objective: build compact runtime. Constraint: do not lose blocker continuity."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack objective"))
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Current blocker: provider intermittently returns 529 overloaded_error when histories get too large."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack blocker"))
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Next action: validate with real e2e tests and tune the compact prompt."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack next"))

	model := &contextProbeModel{
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
			RetainedUserTokenLimit:     24,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "continue",
		AgentSpec: sdkruntime.AgentSpec{
			Name:  "chat",
			Model: model,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	if model.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", model.compactionCalls)
	}
	if model.normalCalls != 1 {
		t.Fatalf("normalCalls = %d, want 1", model.normalCalls)
	}
	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	sawCompact := false
	var compactText string
	for _, event := range loaded.Events {
		if event != nil && event.Type == sdksession.EventTypeCompact {
			sawCompact = true
			compactText = strings.TrimSpace(sdksession.EventText(event))
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

	sessions, session := newTestSessionService(t, "sess-compact-model")
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Project objective: preserve context continuity during very long coding sessions."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Current blocker: checkpoint quality drops when summaries become too generic."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Next action: run realistic compact e2e tests and tune the summary prompt."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack"))

	model := &modelCheckpointProbe{
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
			RetainedUserTokenLimit:     24,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "continue",
		AgentSpec: sdkruntime.AgentSpec{
			Name:  "chat",
			Model: model,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	if model.compactionCalls == 0 {
		t.Fatal("expected at least one model-backed compaction call")
	}
	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var compactText string
	for _, event := range loaded.Events {
		if event != nil && event.Type == sdksession.EventTypeCompact {
			compactText = strings.TrimSpace(sdksession.EventText(event))
		}
	}
	if !strings.Contains(compactText, "preserve context continuity during very long coding sessions") {
		t.Fatalf("compact event text = %q, want canonical continuity objective", compactText)
	}
	compactEvent, ok := latestCompactEventForTest(loaded.Events)
	if !ok {
		t.Fatal("expected compact event in durable history")
	}
	data, ok := sdkcompact.CompactEventDataFromEvent(compactEvent)
	if !ok {
		t.Fatal("expected compact event metadata")
	}
	if len(data.ReplacementHistory) == 0 {
		t.Fatal("expected replacement history on compact event")
	}
	last := data.ReplacementHistory[len(data.ReplacementHistory)-1]
	if last == nil || !strings.Contains(strings.ToLower(sdksession.EventText(last)), "preserve context continuity during very long coding sessions") {
		t.Fatalf("replacement history summary = %+v, want compact continuity objective", last)
	}
	if data.Revision <= 0 {
		t.Fatalf("compact revision = %d, want > 0", data.Revision)
	}
}

func TestRuntimeManualCompactUsesStructuredReplacementHistory(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-compact-manual")
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Project objective: make manual compact preserve context instead of truncating history."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack objective"))
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Current blocker: bare compact events cause prompt replay to drop all prior context."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack blocker"))
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Next action: route manual compact through the model-backed compactor."))

	model := &contextProbeModel{
		t: t,
		wantCompactionInputContains: []string{
			"make manual compact preserve context",
		},
		compactBody: `CONTEXT CHECKPOINT
Objective: make manual compact preserve context instead of truncating history
Blocker: bare compact events cause prompt replay to drop all prior context
Next action: route manual compact through the model-backed compactor

## Current Progress
- manual compact is being aligned with auto compact

## Open Questions / Risks
- compact events without replacement history must not be emitted`,
	}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Be terse.",
		},
		Compaction: CompactionConfig{
			RetainedUserTokenLimit: 24,
			SegmentTokenBudget:     80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Compact(context.Background(), CompactRequest{
		SessionRef: session.SessionRef,
		Model:      model,
		Trigger:    "manual",
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if !result.Compacted {
		t.Fatal("Compact() did not compact")
	}
	if model.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", model.compactionCalls)
	}
	if model.normalCalls != 0 {
		t.Fatalf("normalCalls = %d, want 0", model.normalCalls)
	}
	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	compactEvent, ok := latestCompactEventForTest(loaded.Events)
	if !ok {
		t.Fatal("expected compact event")
	}
	data, ok := sdkcompact.CompactEventDataFromEvent(compactEvent)
	if !ok {
		t.Fatalf("compact event missing structured metadata: %+v", compactEvent.Meta)
	}
	if data.Trigger != "manual" {
		t.Fatalf("compact trigger = %q, want manual", data.Trigger)
	}
	if len(data.ReplacementHistory) == 0 {
		t.Fatal("manual compact replacement history is empty")
	}
	promptEvents := sdkcompact.PromptEventsFromLatestCompact(loaded.Events)
	if len(promptEvents) == 0 {
		t.Fatal("prompt events empty after manual compact")
	}
	if strings.Contains(strings.Join(eventTextsForTest(promptEvents), "\n"), "Project objective: make manual compact preserve context instead of truncating history.") {
		t.Fatalf("prompt events still replay raw pre-compact history: %+v", promptEvents)
	}
}

func TestRuntimeCompactionReplaysFromEventsAfterReload(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "sess-compact-replay" },
	}))
	session, err := sessions.StartSession(context.Background(), sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-compact-replay",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Project objective: replay compacted history strictly from append-only events."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Current blocker: raw transcript replay grows too large under long sessions."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Next action: verify reload from file-backed events only."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack"))

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
			RetainedUserTokenLimit:     48,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New(runtime1) error = %v", err)
	}

	result1, err := runtime1.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "continue",
		AgentSpec: sdkruntime.AgentSpec{
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
	reopenedState, err := reopenedSessions.SnapshotState(context.Background(), session.SessionRef)
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
			RetainedUserTokenLimit:     48,
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
	result, err := runtime2.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "continue after reload",
		AgentSpec: sdkruntime.AgentSpec{
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

func TestSelectRetainedUserInputsIncludesNonContiguousRecentUsers(t *testing.T) {
	t.Parallel()

	keepBlocker := "Keep blocker continuity exact across compact."
	validateE2E := "Validate real compact e2e output before changing heuristics."
	events := []*sdksession.Event{
		{ID: "user-1", Type: sdksession.EventTypeUser, Text: "Very old objective turn that should not be retained."},
		{ID: "assistant-1", Type: sdksession.EventTypeAssistant, Text: "ack"},
		{ID: "user-2", Type: sdksession.EventTypeUser, Text: keepBlocker},
		{ID: "assistant-2", Type: sdksession.EventTypeAssistant, Text: "ack"},
		{ID: "user-3", Type: sdksession.EventTypeUser, Text: validateE2E},
	}

	got, selected := selectRetainedUserInputs(events, estimateTextTokens(keepBlocker)+estimateTextTokens(validateE2E)+4)
	if len(got) < 2 {
		t.Fatalf("retained users = %v, want at least the two most recent user turns", got)
	}
	if !reflect.DeepEqual(got[len(got)-2:], []string{keepBlocker, validateE2E}) {
		t.Fatalf("retained users tail = %v, want %v", got[len(got)-2:], []string{keepBlocker, validateE2E})
	}
	if len(selected) < 2 {
		t.Fatalf("selected retained indexes = %v, want at least 2", selected)
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
	events := []*sdksession.Event{assistant, followUp}

	usage := compactor.snapshotUsage(sdkcompact.Request{}, events)
	want := 120 + estimatePromptEventTokens(assistant) + estimatePromptEventTokens(followUp)
	if usage.TotalTokens != want {
		t.Fatalf("usage.TotalTokens = %d, want %d", usage.TotalTokens, want)
	}
	if usage.Source != sdkcompact.UsageSourceProvider {
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
	events := []*sdksession.Event{assistant, followUp}

	usage := compactor.snapshotUsage(sdkcompact.Request{}, events)
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

	usage := compactor.snapshotUsage(sdkcompact.Request{}, []*sdksession.Event{userTextEvent("small window probe")})
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

	usage := compactor.snapshotUsage(sdkcompact.Request{}, []*sdksession.Event{userTextEvent("long window probe")})
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
		RetainedUserTokenLimit:     96,
		SegmentTokenBudget:         80,
	})}
	events := []*sdksession.Event{
		userTextEvent(strings.Repeat("Objective continuity detail. ", 8)),
		assistantEvent("ack"),
		userTextEvent(strings.Repeat("Most recent blocker and progress detail. ", 8)),
	}
	pending := userTextEvent(strings.Repeat("New user turn that must still fit after compaction. ", 6))

	result, err := compactor.Prepare(context.Background(), sdkcompact.Request{
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{
				AppName: "caelis",
				UserID:  "user-1",
			},
		},
		Events:        events,
		PendingEvents: []*sdksession.Event{pending},
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
	data, ok := sdkcompact.CompactEventDataFromEvent(result.CompactEvent)
	if !ok {
		t.Fatal("expected compact event data")
	}
	if len(data.ReplacementHistory) == 0 {
		t.Fatal("expected replacement history after compaction")
	}
}

func TestRuntimeCompactionIgnoresStateOnlyPlanSnapshot(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-compact-state-omit")
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Objective: keep compaction event-only."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Blocker: runtime state can drift away from durable events."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack"))
	appendTestEvent(t, sessions, session.SessionRef, userTextEvent("Next action: compact only from canonical events and verify no state leakage."))
	appendTestEvent(t, sessions, session.SessionRef, assistantEvent("ack"))

	if err := sessions.UpdateState(context.Background(), session.SessionRef, func(state map[string]any) (map[string]any, error) {
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

	model := &contextProbeModel{
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
			RetainedUserTokenLimit:     24,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "continue",
		AgentSpec: sdkruntime.AgentSpec{
			Name:  "chat",
			Model: model,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	if model.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", model.compactionCalls)
	}
}

func TestRenderCompactionEventIncludesPlanEntries(t *testing.T) {
	t.Parallel()

	event := &sdksession.Event{
		Type:       sdksession.EventTypePlan,
		Visibility: sdksession.VisibilityCanonical,
		Text:       "execution plan refreshed",
		Protocol: &sdksession.EventProtocol{
			UpdateType: string(sdksession.ProtocolUpdateTypePlan),
			Plan: &sdksession.ProtocolPlan{
				Entries: []sdksession.ProtocolPlanEntry{
					{Content: "run provider compact e2e", Status: "in_progress"},
					{Content: "verify append-only replay", Status: "pending"},
				},
			},
		},
	}

	got := renderCompactionEvent(event)
	for _, needle := range []string{
		"PLAN:",
		"execution plan refreshed",
		"run provider compact e2e [in_progress]",
		"verify append-only replay [pending]",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("renderCompactionEvent() = %q, want substring %q", got, needle)
		}
	}
}

func TestPreferredCompactionAnchorsUseLatestExplicitHistory(t *testing.T) {
	t.Parallel()

	baseText := `CONTEXT CHECKPOINT

Objective: stale compact objective
Blocker: stale compact blocker
Next action: stale compact next action

- old noisy detail`
	events := []*sdksession.Event{
		userTextEvent("Objective: even older transcript objective"),
		userTextEvent(`CONTEXT CHECKPOINT

Objective: synthetic summary objective
Blocker: synthetic summary blocker
Next action: synthetic summary next action`),
		userTextEvent("Objective: fresh runtime objective\nBlocker: waiting for e2e confirmation\nNext action: run the provider continuity test"),
	}

	anchors := preferredCompactionAnchors(baseText, events)
	if anchors.Objective != "fresh runtime objective" {
		t.Fatalf("anchors.Objective = %q, want %q", anchors.Objective, "fresh runtime objective")
	}
	if anchors.Blocker != "waiting for e2e confirmation" {
		t.Fatalf("anchors.Blocker = %q, want %q", anchors.Blocker, "waiting for e2e confirmation")
	}
	if anchors.NextAction != "run the provider continuity test" {
		t.Fatalf("anchors.NextAction = %q, want %q", anchors.NextAction, "run the provider continuity test")
	}
}

func TestCompactableEventsIgnoreReplacementOverlayHistory(t *testing.T) {
	t.Parallel()

	retainedMsg := sdkmodel.NewTextMessage(sdkmodel.RoleUser, "Retained user text from the previous compact.")
	overlay := &sdksession.Event{
		Type:       sdksession.EventTypeUser,
		Visibility: sdksession.VisibilityOverlay,
		Message:    &retainedMsg,
		Text:       retainedMsg.TextContent(),
	}
	canonical := userTextEvent("Fresh canonical user event after the latest compact.")
	events := []*sdksession.Event{
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

	message := sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "message-only assistant text")
	event := &sdksession.Event{
		Type:       sdksession.EventTypeAssistant,
		Visibility: sdksession.VisibilityCanonical,
		Message:    &message,
	}

	got := renderCompactionEvent(event)
	if !strings.Contains(got, "message-only assistant text") {
		t.Fatalf("renderCompactionEvent() = %q, want message text fallback", got)
	}
}

func TestSelectRetainedUserInputsTruncatesLongRecentUser(t *testing.T) {
	t.Parallel()

	longUser := strings.Repeat("latest user continuity detail ", 24)
	events := []*sdksession.Event{
		{ID: "user-long", Type: sdksession.EventTypeUser, Text: longUser},
	}

	got, selected := selectRetainedUserInputs(events, max(estimateTextTokens(longUser)/4, 8))
	if len(got) != 1 {
		t.Fatalf("retained user count = %d, want 1 (%v)", len(got), got)
	}
	if got[0] == longUser {
		t.Fatalf("retained user text was not truncated: %q", got[0])
	}
	if !strings.Contains(got[0], "...") {
		t.Fatalf("retained user text = %q, want ellipsis truncation", got[0])
	}
	if len(selected) != 1 {
		t.Fatalf("selected retained indexes = %v, want 1", selected)
	}
	if estimateTextTokens(got[0]) > max(estimateTextTokens(longUser)/4, 8)+2 {
		t.Fatalf("truncated retained text still exceeds budget: %q", got[0])
	}
}

func TestRuntimeRecoversFromContextOverflowByCompactingMidTurn(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-compact-overflow")
	model := &overflowRecoveryModel{t: t}
	tool := sdktool.NamedTool{
		Def: sdktool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call sdktool.Call) (sdktool.Result, error) {
			return sdktool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []sdkmodel.Part{
					sdkmodel.NewJSONPart([]byte(`{"value":"pong"}`)),
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
			RetainedUserTokenLimit:     32,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "Use ECHO and then finish.",
		AgentSpec: sdkruntime.AgentSpec{
			Name:  "chat",
			Model: model,
			Tools: []sdktool.Tool{tool},
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
		if event != nil && event.Type == sdksession.EventTypeAssistant {
			finalText = strings.TrimSpace(sdksession.EventText(event))
		}
	}
	if finalText != "recovered after compact" {
		t.Fatalf("finalText = %q, want %q", finalText, "recovered after compact")
	}
	if model.compactionCalls != 1 {
		t.Fatalf("compactionCalls = %d, want 1", model.compactionCalls)
	}
	if !model.sawCheckpointOnRetry {
		t.Fatal("expected retry to see compact checkpoint with tool result continuity")
	}

	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	sawCompact := false
	for _, event := range loaded.Events {
		if event != nil && event.Type == sdksession.EventTypeCompact {
			sawCompact = true
			if !strings.Contains(strings.ToLower(sdksession.EventText(event)), "pong") {
				t.Fatalf("compact event text = %q, want retained tool result summary", sdksession.EventText(event))
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
	data, ok := sdkcompact.CompactEventDataFromEvent(compactEvent)
	if !ok || len(data.ReplacementHistory) == 0 {
		t.Fatalf("compact metadata missing replacement history: %+v", compactEvent.Meta)
	}
	foundPong := false
	for _, event := range data.ReplacementHistory {
		if event != nil && strings.Contains(strings.ToLower(sdksession.EventText(event)), "pong") {
			foundPong = true
			break
		}
	}
	if !foundPong {
		t.Fatalf("replacement history = %+v, want tool result continuity", data.ReplacementHistory)
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
	session, err := sessions.StartSession(context.Background(), sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
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
	snapshot, err := runtime1.tasks.StartBash(context.Background(), session, session.SessionRef, hostRuntimeForTest(t, workdir), sdktask.BashStartRequest{
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
	result2, err := runtime2.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "resume after orphaned task",
		AgentSpec: sdkruntime.AgentSpec{
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
	if entry.State != sdktask.StateInterrupted {
		t.Fatalf("entry.State = %q, want %q", entry.State, sdktask.StateInterrupted)
	}
	if got, _ := entry.Result["result"].(string); !strings.Contains(got, "interrupted during resume") {
		t.Fatalf("entry.Result[result] = %q, want interrupted summary", got)
	}
}

func TestRuntimeRunRetriesBeforeAnyEventIsEmitted(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-retry")
	factory := &attemptFactory{
		agents: []sdkruntime.Agent{
			seqAgent{err: errors.New("model: http status 529 body={\"error\":\"overloaded_error\"}")},
			seqAgent{events: []*sdksession.Event{
				assistantEvent("world"),
			}},
		},
	}
	var delays []time.Duration
	runtime, err := New(Config{
		Sessions:       sessions,
		AgentFactory:   factory,
		RunIDGenerator: func() string { return "run-retry" },
		Sleep: func(context.Context, time.Duration) error {
			delays = append(delays, 0)
			return nil
		},
		Retry: RetryConfig{
			MaxRetries: 2,
			BaseDelay:  25 * time.Millisecond,
			MaxDelay:   25 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "hello",
		AgentSpec:  sdkruntime.AgentSpec{Name: "chat"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var (
		count       int
		noticeCount int
	)
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event == nil {
			continue
		}
		count++
		if sdksession.IsNotice(event) {
			noticeCount++
			if !strings.Contains(sdksession.EventText(event), "retrying") {
				t.Fatalf("notice text = %q, want retry warning", sdksession.EventText(event))
			}
		}
	}
	if got, want := count, 3; got != want {
		t.Fatalf("runner event count = %d, want %d", got, want)
	}
	if got, want := noticeCount, 1; got != want {
		t.Fatalf("notice count = %d, want %d", got, want)
	}
	if got, want := factory.Calls(), 2; got != want {
		t.Fatalf("factory calls = %d, want %d", got, want)
	}
	if got, want := len(delays), 1; got != want {
		t.Fatalf("sleep call count = %d, want %d", got, want)
	}

	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	for _, event := range loaded.Events {
		if sdksession.IsNotice(event) {
			t.Fatal("retry notice must not be persisted")
		}
	}
}

func TestRuntimeRunDoesNotRetryAfterAnyEventIsEmitted(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-no-retry")
	factory := &attemptFactory{
		agents: []sdkruntime.Agent{
			seqAgent{
				events: []*sdksession.Event{assistantEvent("partial")},
				err:    errors.New("model stream interrupted"),
			},
			seqAgent{events: []*sdksession.Event{assistantEvent("should-not-run")}},
		},
	}
	runtime, err := New(Config{
		Sessions:       sessions,
		AgentFactory:   factory,
		RunIDGenerator: func() string { return "run-no-retry" },
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("sleep should not be called after emitted event")
			return nil
		},
		Retry: RetryConfig{
			MaxRetries: 2,
			BaseDelay:  time.Millisecond,
			MaxDelay:   time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "hello",
		AgentSpec:  sdkruntime.AgentSpec{Name: "chat"},
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

	loaded, loadErr := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if loadErr != nil {
		t.Fatalf("LoadSession() error = %v", loadErr)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if got := sdksession.EventText(loaded.Events[1]); got != "partial" {
		t.Fatalf("assistant text = %q, want %q", got, "partial")
	}

	state, stateErr := runtime.RunState(context.Background(), session.SessionRef)
	if stateErr != nil {
		t.Fatalf("RunState() error = %v", stateErr)
	}
	if state.Status != sdkruntime.RunLifecycleStatusFailed {
		t.Fatalf("state.Status = %q, want %q", state.Status, sdkruntime.RunLifecycleStatusFailed)
	}
}

func TestRuntimeRunPersistsToolLoopEvents(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-tools")
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

	model := &toolLoopRuntimeModel{}
	tool := sdktool.NamedTool{
		Def: sdktool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call sdktool.Call) (sdktool.Result, error) {
			return sdktool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []sdkmodel.Part{
					sdkmodel.NewJSONPart([]byte(`{"value":"pong"}`)),
				},
			}, nil
		},
	}

	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "say pong",
		AgentSpec: sdkruntime.AgentSpec{
			Name:  "chat",
			Model: model,
			Tools: []sdktool.Tool{tool},
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

	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 4; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if loaded.Events[1].Type != sdksession.EventTypeToolCall {
		t.Fatalf("loaded.Events[1].Type = %q, want tool_call", loaded.Events[1].Type)
	}
	if loaded.Events[1].Protocol == nil || loaded.Events[1].Protocol.ToolCall == nil || loaded.Events[1].Protocol.UpdateType != string(sdksession.ProtocolUpdateTypeToolCall) {
		t.Fatalf("loaded.Events[1].Protocol = %+v, want tool_call protocol payload", loaded.Events[1].Protocol)
	}
	if loaded.Events[2].Type != sdksession.EventTypeToolResult {
		t.Fatalf("loaded.Events[2].Type = %q, want tool_result", loaded.Events[2].Type)
	}
	if loaded.Events[2].Protocol == nil || loaded.Events[2].Protocol.ToolCall == nil || loaded.Events[2].Protocol.UpdateType != string(sdksession.ProtocolUpdateTypeToolUpdate) {
		t.Fatalf("loaded.Events[2].Protocol = %+v, want tool_call_update protocol payload", loaded.Events[2].Protocol)
	}
	if got := sdksession.EventText(loaded.Events[3]); got != "pong" {
		t.Fatalf("final assistant text = %q, want %q", got, "pong")
	}
	if loaded.Events[0].Protocol == nil || loaded.Events[0].Protocol.UpdateType != string(sdksession.ProtocolUpdateTypeUserMessage) {
		t.Fatalf("loaded.Events[0].Protocol = %+v, want user_message protocol payload", loaded.Events[0].Protocol)
	}
	if loaded.Events[3].Protocol == nil || loaded.Events[3].Protocol.UpdateType != string(sdksession.ProtocolUpdateTypeAgentMessage) {
		t.Fatalf("loaded.Events[3].Protocol = %+v, want agent_message protocol payload", loaded.Events[3].Protocol)
	}
}

func TestRuntimeRunPersistsPlanLoopAndState(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-plan")
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

	model := &planLoopRuntimeModel{}
	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "make a plan",
		AgentSpec: sdkruntime.AgentSpec{
			Name:  "chat",
			Model: model,
			Tools: []sdktool.Tool{sdkplan.New()},
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
		if event != nil && event.Type == sdksession.EventTypePlan {
			sawPlan = true
		}
	}
	if !sawPlan {
		t.Fatal("expected plan event in runner output")
	}

	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var planEvent *sdksession.Event
	for _, event := range loaded.Events {
		if event != nil && event.Type == sdksession.EventTypePlan {
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
	state, err := sessions.SnapshotState(context.Background(), session.SessionRef)
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

	sessions, session := newTestSessionService(t, "sess-policy-default")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: policypresets.ModeDefault,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	writeTool, err := filesystem.NewWrite(hostRuntimeForTest(t, session.CWD))
	if err != nil {
		t.Fatalf("filesystem.NewWrite() error = %v", err)
	}
	model := &denyWriteRuntimeModel{}
	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "write outside workspace",
		AgentSpec: sdkruntime.AgentSpec{
			Name:  "chat",
			Model: model,
			Tools: []sdktool.Tool{writeTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 4; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	toolResult := loaded.Events[2]
	if toolResult.Type != sdksession.EventTypeToolResult {
		t.Fatalf("tool result type = %q, want tool_result", toolResult.Type)
	}
	if got := eventToolRawOutput(toolResult)["policy_action"]; got != "deny" {
		t.Fatalf("policy_action = %v, want %q", got, "deny")
	}
}

func TestRuntimePolicyModePreservesCustomRegistryMode(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-policy-custom-mode")
	_ = sessions
	var sawMode string
	registry, err := sdkpolicy.NewMemory(sdkpolicy.NamedMode{
		ID: "locked-down",
		Decide: func(_ context.Context, input sdkpolicy.ToolContext) (sdkpolicy.Decision, error) {
			sawMode = input.Mode
			return sdkpolicy.Decision{Action: sdkpolicy.ActionDeny, Reason: "custom denied"}, nil
		},
	})
	if err != nil {
		t.Fatalf("sdkpolicy.NewMemory() error = %v", err)
	}
	runtime := &Runtime{
		policies:          registry,
		defaultPolicyMode: "locked-down",
	}
	tool := sdktool.NamedTool{
		Def: sdktool.Definition{Name: "ECHO"},
		Invoke: func(context.Context, sdktool.Call) (sdktool.Result, error) {
			t.Fatal("custom policy should deny before invoking the tool")
			return sdktool.Result{}, nil
		},
	}
	wrapped := runtime.wrapToolsForPolicy(session, session.SessionRef, nil, sdkruntime.AgentSpec{
		Tools: []sdktool.Tool{tool},
	}, approvalContext{
		ctx:        context.Background(),
		session:    session,
		sessionRef: session.SessionRef,
	})
	if got := len(wrapped); got != 1 {
		t.Fatalf("len(wrapped) = %d, want 1", got)
	}
	result, err := wrapped[0].Call(context.Background(), sdktool.Call{ID: "call-1", Name: "ECHO"})
	if err != nil {
		t.Fatalf("wrapped tool Call() error = %v", err)
	}
	if sawMode != "locked-down" {
		t.Fatalf("policy mode seen by custom mode = %q, want locked-down", sawMode)
	}
	if got := result.Meta["policy_mode"]; got != "locked-down" {
		t.Fatalf("result policy_mode = %v, want locked-down", got)
	}
}

func TestNormalizePolicyModeCollapsesLegacyAliasesButPreservesCustomNames(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":            "auto-review",
		"default":     "auto-review",
		"plan":        "auto-review",
		"full_access": "auto-review",
		"auto_review": "auto-review",
		"manual":      "manual",
		"locked-down": "locked-down",
		"TeamStrict":  "TeamStrict",
	}
	for input, want := range tests {
		if got := normalizePolicyMode(input); got != want {
			t.Fatalf("normalizePolicyMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRuntimeReservesRequestPermissionsToolName(t *testing.T) {
	t.Parallel()

	_, session := newTestSessionService(t, "sess-request-permissions-reserved")
	customCalled := false
	custom := sdktool.NamedTool{
		Def: sdktool.Definition{Name: "request_permissions"},
		Invoke: func(context.Context, sdktool.Call) (sdktool.Result, error) {
			customCalled = true
			return sdktool.Result{Meta: map[string]any{"custom": true}}, nil
		},
	}
	runtime := &Runtime{}
	wrapped := runtime.wrapToolsForRuntime(session, session.SessionRef, sdkruntime.AgentSpec{
		Tools: []sdktool.Tool{custom},
	}, runtimeToolContext{
		grants: newPermissionGrantStore(),
	})
	if got := len(wrapped); got != 1 {
		t.Fatalf("len(wrapped) = %d, want 1", got)
	}
	if _, ok := wrapped[0].(requestPermissionsTool); !ok {
		t.Fatalf("wrapped request_permissions tool = %T, want built-in requestPermissionsTool", wrapped[0])
	}
	raw := []byte(`{"reason":"need network","permissions":{"network":{"enabled":true}}}`)
	result, err := wrapped[0].Call(context.Background(), sdktool.Call{ID: "perm-1", Name: "request_permissions", Input: raw})
	if err != nil {
		t.Fatalf("request_permissions Call() error = %v", err)
	}
	if customCalled {
		t.Fatal("colliding custom request_permissions tool was invoked")
	}
	if !result.IsError {
		t.Fatalf("request_permissions result IsError = false, want true without approval requester")
	}
	if !strings.Contains(fmt.Sprint(result.Meta["error"]), "no approval requester") {
		t.Fatalf("request_permissions error = %v, want built-in approval requester error", result.Meta["error"])
	}
}

func TestRuntimePolicyFullAccessBlocksDangerousBash(t *testing.T) {
	t.Parallel()

	sessions, session := newTestSessionService(t, "sess-policy-full")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: policypresets.ModeFullAccess,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	bashTool, err := shell.NewBash(shell.BashConfig{Runtime: hostRuntimeForTest(t, session.CWD)})
	if err != nil {
		t.Fatalf("shell.NewBash() error = %v", err)
	}
	model := &denyBashRuntimeModel{}
	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "run dangerous bash",
		AgentSpec: sdkruntime.AgentSpec{
			Name:  "chat",
			Model: model,
			Tools: []sdktool.Tool{bashTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}

	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
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

	sessions, session := newTestSessionService(t, "sess-policy-approval")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: policypresets.ModeDefault,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	bashTool, err := shell.NewBash(shell.BashConfig{Runtime: hostRuntimeForTest(t, session.CWD)})
	if err != nil {
		t.Fatalf("shell.NewBash() error = %v", err)
	}
	target := filepath.Join(session.CWD, "approved.txt")
	model := &approveEscalatedBashRuntimeModel{command: "printf 'approved\\n' > " + shellQuoteForTest(target)}
	requester := approvalRequesterFunc(func(ctx context.Context, req sdkruntime.ApprovalRequest) (sdkruntime.ApprovalResponse, error) {
		state, err := runtime.RunState(ctx, session.SessionRef)
		if err != nil {
			t.Fatalf("RunState() during approval error = %v", err)
		}
		if state.Status != sdkruntime.RunLifecycleStatusWaitingApproval || !state.WaitingApproval {
			t.Fatalf("run state during approval = %+v, want waiting_approval", state)
		}
		if req.Approval == nil || req.Approval.ToolCall.Name != shell.BashToolName {
			t.Fatalf("approval request = %+v, want BASH tool call", req.Approval)
		}
		return sdkruntime.ApprovalResponse{
			Outcome:  "selected",
			OptionID: "allow_once",
			Approved: true,
		}, nil
	})
	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef:        session.SessionRef,
		Input:             "write inside workspace",
		ApprovalRequester: requester,
		AgentSpec: sdkruntime.AgentSpec{
			Name:  "chat",
			Model: model,
			Tools: []sdktool.Tool{bashTool},
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
	state, err := runtime.RunState(context.Background(), session.SessionRef)
	if err != nil {
		t.Fatalf("RunState() error = %v", err)
	}
	if state.Status != sdkruntime.RunLifecycleStatusCompleted {
		t.Fatalf("final run state = %+v, want completed", state)
	}
}

func TestControllerApprovalRequesterPreservesToolRawInput(t *testing.T) {
	t.Parallel()

	var captured sdkruntime.ApprovalRequest
	requester := controllerApprovalRequester{
		requester: approvalRequesterFunc(func(_ context.Context, req sdkruntime.ApprovalRequest) (sdkruntime.ApprovalResponse, error) {
			captured = req
			return sdkruntime.ApprovalResponse{
				Outcome:  "selected",
				OptionID: "allow_once",
				Approved: true,
			}, nil
		}),
		sessionRef: sdksession.SessionRef{SessionID: "sess-approval"},
		session:    sdksession.Session{SessionRef: sdksession.SessionRef{SessionID: "sess-approval"}},
		runID:      "run-1",
		turnID:     "turn-1",
	}
	_, err := requester.RequestControllerApproval(context.Background(), sdkcontroller.ApprovalRequest{
		Agent: "codex",
		Mode:  "default",
		ToolCall: sdkcontroller.ApprovalToolCall{
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

	sessions, session := newTestSessionService(t, "sess-bash-task-loop")
	taskStore := taskfile.NewStore(taskfile.Config{RootDir: t.TempDir()})
	runtime, err := New(Config{
		Sessions:  sessions,
		TaskStore: taskStore,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: policypresets.ModeFullAccess,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	bashTool, err := shell.NewBash(shell.BashConfig{Runtime: hostRuntimeForTest(t, session.CWD)})
	if err != nil {
		t.Fatalf("shell.NewBash() error = %v", err)
	}
	result, err := runtime.Run(context.Background(), sdkruntime.RunRequest{
		SessionRef: session.SessionRef,
		Input:      "run async bash",
		AgentSpec: sdkruntime.AgentSpec{
			Name:  "chat",
			Model: &bashTaskLoopRuntimeModel{t: t},
			Tools: []sdktool.Tool{bashTool, tasktool.New()},
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
		if event.Type == sdksession.EventTypeToolResult && event.Protocol != nil && event.Protocol.ToolCall != nil && event.Protocol.ToolCall.Status == "running" {
			runningToolUpdate = true
		}
		if event.Type == sdksession.EventTypeAssistant {
			finalText = strings.TrimSpace(sdksession.EventText(event))
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

	loaded, err := sessions.LoadSession(context.Background(), sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if len(loaded.Events) < 6 {
		t.Fatalf("len(loaded.Events) = %d, want >= 6", len(loaded.Events))
	}
	var sawTaskID bool
	for _, event := range loaded.Events {
		if event == nil || event.Type != sdksession.EventTypeToolResult {
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
	task, err := runtime.tasks.lookupBash(context.Background(), session.SessionRef, mustSessionTaskID(t, loaded.Events))
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
	resultPayload, _ := task.result["stdout"].(string)
	if !strings.Contains(resultPayload, "async bash done") {
		t.Fatalf("rehydrated task stdout = %q, want async bash done", resultPayload)
	}
	terminals := runtime.Streams()
	if terminals == nil {
		t.Fatal("Streams() = nil")
	}
	snap, err := terminals.Read(context.Background(), sdkstream.ReadRequest{
		Ref: sdkstream.Ref{
			SessionID: session.SessionID,
			TaskID:    mustSessionTaskID(t, loaded.Events),
		},
	})
	if err != nil {
		t.Fatalf("terminal Read() error = %v", err)
	}
	if snap.Running {
		t.Fatalf("terminal snapshot still running: %+v", snap)
	}
	terminalText := terminalFramesText(snap.Frames)
	if !strings.Contains(terminalText, "async bash done") {
		t.Fatalf("terminal snapshot text = %q, want async bash done", terminalText)
	}
}

func TestRuntimeTaskWriteAddsLineTerminatorForInteractiveBash(t *testing.T) {
	t.Parallel()

	_, session, runtime := newRuntimeBashToolTestHarness(t)
	bashTool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, hostRuntimeForTest(t, session.CWD)),
		session:    session,
		sessionRef: session.SessionRef,
		tasks:      runtime.tasks,
	}
	bashResult := callRuntimeBashTool(t, bashTool, map[string]any{
		"command":       "printf 'waiting\\n'; read name; printf 'hello %s\\n' \"$name\"",
		"workdir":       ".",
		"yield_time_ms": 0,
	})
	taskID, _ := bashResult.Meta["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("bash result meta = %#v, want task_id", bashResult.Meta)
	}

	taskResult := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: session.SessionRef,
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

func TestTaskToolPayloadFallsBackToCompletedStdout(t *testing.T) {
	payload := taskToolPayload(sdktask.Snapshot{
		Ref:     sdktask.Ref{TaskID: "task-1", TerminalID: "term-1"},
		Kind:    sdktask.KindBash,
		State:   sdktask.StateCompleted,
		Running: false,
		Result: map[string]any{
			"stdout":    "waiting\nhello Codex\n",
			"exit_code": 0,
		},
	})
	if got, _ := payload["result"].(string); !strings.Contains(got, "hello Codex") {
		t.Fatalf("taskToolPayload result = %q, want fallback from stdout", got)
	}
}

func TestRuntimeTerminalSubscribeStreamsRunningTask(t *testing.T) {
	sessions, session := newTestSessionService(t, "sess-terminal-subscribe")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: policypresets.ModeFullAccess,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sandbox := hostRuntimeForTest(t, session.CWD)
	snapshot, err := runtime.tasks.StartBash(context.Background(), session, session.SessionRef, sandbox, sdktask.BashStartRequest{
		Command: "printf 'stream terminal'; sleep 0.05",
		Workdir: session.CWD,
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
		text   strings.Builder
		closed bool
	)
	for frame, seqErr := range terminals.Subscribe(ctx, sdkstream.SubscribeRequest{
		Ref: sdkstream.Ref{
			SessionID: session.SessionID,
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
			closed = true
		}
	}
	if !closed {
		t.Fatal("expected terminal subscription to emit closed frame")
	}
	if got := text.String(); !strings.Contains(got, "stream terminal") {
		t.Fatalf("terminal text = %q, want %q", got, "stream terminal")
	}
}

func TestRuntimeBashToolUsesDefaultYieldWhenOmitted(t *testing.T) {
	t.Parallel()

	_, session, runtime := newRuntimeBashToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	tool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    sdksession.CloneSession(session),
		sessionRef: session.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeBashTool(t, tool, map[string]any{
		"command": "printf 'ok'",
		"workdir": session.CWD,
	})

	if got := fake.session.lastWait; got != defaultBashYield {
		t.Fatalf("omitted yield wait = %v, want %v", got, defaultBashYield)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestRuntimeBashToolKeepsExplicitZeroYield(t *testing.T) {
	t.Parallel()

	_, session, runtime := newRuntimeBashToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	tool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    sdksession.CloneSession(session),
		sessionRef: session.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeBashTool(t, tool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       session.CWD,
		"yield_time_ms": 0,
	})

	if got := fake.session.lastWait; got != 0 {
		t.Fatalf("explicit zero yield wait = %v, want 0", got)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestRuntimeBashToolPassesExplicitYieldThrough(t *testing.T) {
	t.Parallel()

	_, session, runtime := newRuntimeBashToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	tool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    sdksession.CloneSession(session),
		sessionRef: session.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeBashTool(t, tool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       session.CWD,
		"yield_time_ms": 125,
	})

	if got := fake.session.lastWait; got != 125*time.Millisecond {
		t.Fatalf("explicit yield wait = %v, want %v", got, 125*time.Millisecond)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestRuntimeTaskWaitUsesDefaultYieldWhenOmitted(t *testing.T) {
	t.Parallel()

	_, session, runtime := newRuntimeBashToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	bashTool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    sdksession.CloneSession(session),
		sessionRef: session.SessionRef,
		tasks:      runtime.tasks,
	}
	bashResult := callRuntimeBashTool(t, bashTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       session.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := bashResult.Meta["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("bash result meta = %#v, want task_id", bashResult.Meta)
	}

	callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: session.SessionRef,
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

	_, session, runtime := newRuntimeBashToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	bashTool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    sdksession.CloneSession(session),
		sessionRef: session.SessionRef,
		tasks:      runtime.tasks,
	}
	bashResult := callRuntimeBashTool(t, bashTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       session.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := bashResult.Meta["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("bash result meta = %#v, want task_id", bashResult.Meta)
	}

	callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: session.SessionRef,
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

func TestTaskSnapshotToolResultPreservesTerminalStreamsInMeta(t *testing.T) {
	t.Parallel()

	result := taskSnapshotToolResult(
		sdktool.Call{ID: "call-1", Name: shell.BashToolName},
		sdktool.Definition{Name: shell.BashToolName},
		sdktask.Snapshot{
			Ref:     sdktask.Ref{TaskID: "task-1", SessionID: "session-1"},
			State:   sdktask.StateCompleted,
			Running: false,
			Result: map[string]any{
				"stdout":    "done\n",
				"stderr":    "",
				"result":    "done",
				"exit_code": 0,
			},
			Metadata: map[string]any{
				"session_id":     "session-1",
				"supports_input": true,
			},
		},
	)

	if got, _ := result.Meta["stdout"].(string); got != "done\n" {
		t.Fatalf("result.Meta[stdout] = %q, want terminal stdout", got)
	}
	if got := result.Meta["exit_code"]; got != 0 {
		t.Fatalf("result.Meta[exit_code] = %#v, want 0", got)
	}
}

func TestTaskSnapshotToolResultIncludesSandboxPermissionDetailInPayload(t *testing.T) {
	t.Parallel()

	result := taskSnapshotToolResult(
		sdktool.Call{ID: "call-1", Name: shell.BashToolName},
		sdktool.Definition{Name: shell.BashToolName},
		sdktask.Snapshot{
			Ref:     sdktask.Ref{TaskID: "task-1", SessionID: "session-1"},
			State:   sdktask.StateFailed,
			Running: false,
			Result: map[string]any{
				"result":                    "touch: cannot touch /home/test/go/pkg/mod/cache: Read-only file system",
				"exit_code":                 1,
				"error":                     "Sandbox permission denied. Use a writable workspace path or request elevated permissions.\ntouch: cannot touch /home/test/go/pkg/mod/cache: Read-only file system",
				"sandbox_permission_denied": true,
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
	if denied, _ := payload["sandbox_permission_denied"].(bool); !denied {
		t.Fatalf("payload[sandbox_permission_denied] = %#v, want true", payload["sandbox_permission_denied"])
	}
	if message, _ := payload["error"].(string); !strings.Contains(message, "Sandbox permission denied") ||
		!strings.Contains(message, "/home/test/go/pkg/mod/cache") {
		t.Fatalf("payload[error] = %q, want sandbox prefix plus original denied path", message)
	}
	if _, ok := payload["sandbox_diagnostic"]; ok {
		t.Fatalf("payload[sandbox_diagnostic] = %#v, want omitted", payload["sandbox_diagnostic"])
	}
}

func TestTaskSnapshotToolResultIncludesRunningTerminalCursor(t *testing.T) {
	t.Parallel()

	result := taskSnapshotToolResult(
		sdktool.Call{ID: "call-1", Name: shell.BashToolName},
		sdktool.Definition{Name: shell.BashToolName},
		sdktask.Snapshot{
			Ref: sdktask.Ref{
				SessionID:  "session-1",
				TaskID:     "task-1",
				TerminalID: "terminal-1",
			},
			Terminal:       sdksandbox.TerminalRef{TerminalID: "terminal-1"},
			State:          sdktask.StateRunning,
			Running:        true,
			StdoutCursor:   12,
			StderrCursor:   3,
			SupportsInput:  true,
			SupportsCancel: true,
			Result: map[string]any{
				"output_preview":  "already shown\n",
				"supports_input":  true,
				"supports_cancel": true,
			},
		},
	)

	if got := result.Meta["terminal_id"]; got != "terminal-1" {
		t.Fatalf("result.Meta[terminal_id] = %#v, want terminal-1", got)
	}
	if got := result.Meta["stdout_cursor"]; got != int64(12) {
		t.Fatalf("result.Meta[stdout_cursor] = %#v, want 12", got)
	}
	if got := result.Meta["stderr_cursor"]; got != int64(3) {
		t.Fatalf("result.Meta[stderr_cursor] = %#v, want 3", got)
	}
	var payload map[string]any
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want JSON payload", result.Content)
	}
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	if got := payload["terminal_id"]; got != "terminal-1" {
		t.Fatalf("payload[terminal_id] = %#v, want terminal-1", got)
	}
	if got := payload["stdout_cursor"]; got != float64(12) {
		t.Fatalf("payload[stdout_cursor] = %#v, want 12", got)
	}
	if got := payload["stderr_cursor"]; got != float64(3) {
		t.Fatalf("payload[stderr_cursor] = %#v, want 3", got)
	}
}

func TestTaskSnapshotToolResultSimplifiesSubagentPayload(t *testing.T) {
	t.Parallel()

	result := taskSnapshotToolResult(
		sdktool.Call{ID: "call-1", Name: spawntool.ToolName},
		sdktool.Definition{Name: spawntool.ToolName},
		sdktask.Snapshot{
			Ref:     sdktask.Ref{TaskID: "task-1", SessionID: "child-session"},
			Kind:    sdktask.KindSubagent,
			State:   sdktask.StateCompleted,
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
	if got := payload["result"]; got != "done" {
		t.Fatalf("payload[result] = %#v, want done", got)
	}
	if got := result.Meta["task_id"]; got != "jeff" {
		t.Fatalf("meta[task_id] = %#v, want handle jeff", got)
	}
	if got := result.Meta["prompt"]; got != "summarize startup output" {
		t.Fatalf("meta[prompt] = %#v, want prompt preserved for SPAWN display", got)
	}
	if _, ok := payload["prompt"]; ok {
		t.Fatalf("payload contains prompt: %#v", payload)
	}
	for _, key := range []string{"handle", "mention", "agent", "agent_id", "internal_task_id", "terminal_id"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("payload contains %q: %#v", key, payload)
		}
		if _, ok := result.Meta[key]; ok {
			t.Fatalf("meta contains %q: %#v", key, result.Meta)
		}
	}
}

func TestRuntimeTaskToolResolvesSubagentHandle(t *testing.T) {
	t.Parallel()

	_, session, runtime := newRuntimeBashToolTestHarness(t)
	runtime.tasks.mu.Lock()
	runtime.tasks.subagents["task-1"] = &subagentTask{
		ref:        sdktask.Ref{TaskID: "task-1", SessionID: "child-session", TerminalID: "subagent-task-1"},
		sessionRef: session.SessionRef,
		agent:      "codex",
		handle:     "ella",
		createdAt:  time.Now(),
		state:      sdktask.StateCompleted,
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
		sessionRef: session.SessionRef,
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
	if _, ok := result.Meta["internal_task_id"]; ok {
		t.Fatalf("meta[internal_task_id] = %#v, want omitted", result.Meta["internal_task_id"])
	}
}

func TestRuntimeTaskToolScopesSubagentHandleToSession(t *testing.T) {
	t.Parallel()

	_, session, runtime := newRuntimeBashToolTestHarness(t)
	runtime.tasks.mu.Lock()
	for i := 0; i < 32; i++ {
		taskID := fmt.Sprintf("other-task-%02d", i)
		runtime.tasks.subagents[taskID] = &subagentTask{
			ref:        sdktask.Ref{TaskID: taskID, SessionID: "other-child"},
			sessionRef: sdksession.SessionRef{SessionID: "other-session"},
			handle:     "ella",
			state:      sdktask.StateCompleted,
			result:     map[string]any{"handle": "ella", "result": "wrong"},
			metadata:   map[string]any{"handle": "ella"},
		}
	}
	runtime.tasks.subagents["task-current"] = &subagentTask{
		ref:        sdktask.Ref{TaskID: "task-current", SessionID: "child-session"},
		sessionRef: session.SessionRef,
		handle:     "ella",
		state:      sdktask.StateCompleted,
		result:     map[string]any{"handle": "ella", "result": "right"},
		metadata:   map[string]any{"handle": "ella"},
	}
	runtime.tasks.mu.Unlock()

	result := callRuntimeTaskTool(t, runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: session.SessionRef,
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
	if got := payload["result"]; got != "right" {
		t.Fatalf("payload[result] = %#v, want current-session result", got)
	}
}

func TestRuntimeBashToolDoesNotFetchResultWhileStillRunning(t *testing.T) {
	t.Parallel()

	_, session, runtime := newRuntimeBashToolTestHarness(t)
	fake := &runningOnlyProbeSandboxRuntime{session: &runningOnlyProbeSandboxSession{}}
	tool := runtimeBashTool{
		base:       mustRuntimeBashTool(t, fake),
		session:    sdksession.CloneSession(session),
		sessionRef: session.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeBashTool(t, tool, map[string]any{
		"command": "printf 'still-running'",
		"workdir": session.CWD,
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

func (m staticModel) Generate(context.Context, *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, m.text),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type gatedStreamingModel struct {
	started      chan struct{}
	releaseFinal chan struct{}
}

func (m *gatedStreamingModel) Name() string { return "gated-streaming" }

func (m *gatedStreamingModel) Generate(context.Context, *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		if m.started != nil {
			select {
			case <-m.started:
			default:
				close(m.started)
			}
		}
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventPartDelta,
			PartDelta: &sdkmodel.PartDelta{
				Kind:      sdkmodel.PartKindText,
				TextDelta: "hel",
			},
		}, nil)
		if m.releaseFinal != nil {
			<-m.releaseFinal
		}
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "hello"),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type historyReplayModel struct {
	t         *testing.T
	wantTexts []string
	replyText string
	calls     int
}

func (m *historyReplayModel) Name() string { return "history-replay" }

func (m *historyReplayModel) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
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
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, m.replyText),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type toolLoopRuntimeModel struct {
	calls int
}

func (m *toolLoopRuntimeModel) Name() string { return "tool-loop" }

func (m *toolLoopRuntimeModel) Generate(context.Context, *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
						ID:   "call-1",
						Name: "ECHO",
						Args: string(mustJSONRaw(tmap("value", "pong"))),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "pong"),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
				FinishReason: sdkmodel.FinishReasonStop,
			},
		}, nil)
	}
}

type planLoopRuntimeModel struct {
	calls int
}

func (m *planLoopRuntimeModel) Name() string { return "plan-loop" }

func (m *planLoopRuntimeModel) Generate(context.Context, *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
						ID:   "plan-1",
						Name: sdkplan.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"entries": []map[string]any{
								{"content": "Inspect repo", "status": "completed"},
								{"content": "Implement runtime bridge", "status": "in_progress"},
							},
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "plan ready"),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
				FinishReason: sdkmodel.FinishReasonStop,
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

func newTestSessionService(t *testing.T, sessionID string) (sdksession.Service, sdksession.Session) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return sessionID },
	}))
	session, err := sessions.StartSession(context.Background(), sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-1",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return sessions, session
}

func hostRuntimeForTest(t *testing.T, cwd string) *host.Runtime {
	t.Helper()
	rt, err := host.New(host.Config{CWD: cwd})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	return rt
}

func assistantEvent(text string) *sdksession.Event {
	message := sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, text)
	return &sdksession.Event{
		Type:       sdksession.EventTypeAssistant,
		Visibility: sdksession.VisibilityCanonical,
		Message:    &message,
		Text:       text,
	}
}

func acpControllerChunk(text string) *sdksession.Event {
	message := sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, text)
	return &sdksession.Event{
		Type:       sdksession.EventTypeAssistant,
		Visibility: sdksession.VisibilityCanonical,
		Message:    &message,
		Text:       text,
		Scope: &sdksession.EventScope{
			Source: "acp",
			ACP: sdksession.ACPRef{
				SessionID: "remote-acp-main",
				EventType: string(sdksession.ProtocolUpdateTypeAgentMessage),
			},
		},
		Protocol: &sdksession.EventProtocol{
			UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage),
		},
	}
}

func userTextEvent(text string) *sdksession.Event {
	message := sdkmodel.NewTextMessage(sdkmodel.RoleUser, text)
	return &sdksession.Event{
		Type:       sdksession.EventTypeUser,
		Visibility: sdksession.VisibilityCanonical,
		Message:    &message,
		Text:       strings.TrimSpace(text),
	}
}

func appendTestEvent(t *testing.T, sessions sdksession.Service, ref sdksession.SessionRef, event *sdksession.Event) {
	t.Helper()
	if _, err := sessions.AppendEvent(context.Background(), sdksession.AppendEventRequest{
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

func (m *contextProbeModel) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
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
- prefer compact event replacement history

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
- files touched: sdk/runtime/local/compaction.go
- commands run: go test ./sdk/...`
		}
		return func(yield func(*sdkmodel.StreamEvent, error) bool) {
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, body),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
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
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, m.replyText),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
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

func (m *modelCheckpointProbe) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
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
- files touched: sdk/runtime/local/runtime.go
- commands run: go test ./sdk/...`
		return func(yield func(*sdkmodel.StreamEvent, error) bool) {
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, body),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
				},
			}, nil)
		}
	}
	m.normalCalls++
	found := false
	for _, text := range requestMessageTexts(req) {
		if strings.Contains(text, "preserve context continuity during very long coding sessions") {
			found = true
			break
		}
	}
	if !found {
		m.t.Fatalf("normal call messages missing canonical checkpoint objective: %v", requestMessageTexts(req))
	}
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "ok"),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
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

func (m *overflowRecoveryModel) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.calls++
	instructions := requestInstructionsText(req)
	if strings.Contains(instructions, "CONTEXT CHECKPOINT COMPACTION") {
		m.compactionCalls++
		compactionInput := strings.Join(requestMessageTexts(req), "\n")
		if !strings.Contains(compactionInput, "TOOL_RESULT ECHO") || !strings.Contains(compactionInput, "pong") {
			m.t.Fatalf("compaction input missing tool result continuity: %q", compactionInput)
		}
		body := `CONTEXT CHECKPOINT

Objective: finish the tool-assisted turn after overflow
Blocker: normal prompt overflowed after the tool result
Next action: resume from the compact checkpoint and return the final answer

## Current Progress
- the ECHO tool already returned pong

## Next Actions
1. resume from the compact checkpoint and return the final answer`
		return func(yield func(*sdkmodel.StreamEvent, error) bool) {
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, body),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
				},
			}, nil)
		}
	}
	if requestHasToolResult(req, "ECHO") {
		return func(yield func(*sdkmodel.StreamEvent, error) bool) {
			yield(nil, &sdkmodel.ContextOverflowError{Cause: errors.New("prompt is too long after tool loop")})
		}
	}
	for _, text := range requestMessageTexts(req) {
		if strings.Contains(text, "CONTEXT CHECKPOINT") && strings.Contains(strings.ToLower(text), "pong") {
			m.sawCheckpointOnRetry = true
			return func(yield func(*sdkmodel.StreamEvent, error) bool) {
				yield(&sdkmodel.StreamEvent{
					Type: sdkmodel.StreamEventTurnDone,
					Response: &sdkmodel.Response{
						Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "recovered after compact"),
						TurnComplete: true,
						StepComplete: true,
						Status:       sdkmodel.ResponseStatusCompleted,
					},
				}, nil)
			}
		}
	}
	if m.calls != 1 {
		m.t.Fatalf("unexpected non-compaction request without checkpoint: %v", requestMessageTexts(req))
	}
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
					ID:   "call-overflow-1",
					Name: "ECHO",
					Args: string(mustJSONRaw(tmap("value", "pong"))),
				}}, ""),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
				FinishReason: sdkmodel.FinishReasonToolCalls,
			},
		}, nil)
	}
}

func requestInstructionsText(req *sdkmodel.Request) string {
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

func requestMessageTexts(req *sdkmodel.Request) []string {
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

func requestHasToolResult(req *sdkmodel.Request, name string) bool {
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

func latestCompactEventForTest(events []*sdksession.Event) (*sdksession.Event, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i] != nil && events[i].Type == sdksession.EventTypeCompact {
			return events[i], true
		}
	}
	return nil, false
}

func eventTextsForTest(events []*sdksession.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if text := strings.TrimSpace(sdksession.EventText(event)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

type denyWriteRuntimeModel struct{ calls int }

func (m *denyWriteRuntimeModel) Name() string { return "deny-write" }

func (m *denyWriteRuntimeModel) Generate(context.Context, *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
						ID:   "write-1",
						Name: filesystem.WriteToolName,
						Args: string(mustJSONRaw(map[string]any{"path": "/etc/blocked.txt", "content": "x"})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "denied"),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
				FinishReason: sdkmodel.FinishReasonStop,
			},
		}, nil)
	}
}

type denyBashRuntimeModel struct{ calls int }

func (m *denyBashRuntimeModel) Name() string { return "deny-bash" }

func (m *denyBashRuntimeModel) Generate(context.Context, *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
						ID:   "bash-1",
						Name: shell.BashToolName,
						Args: string(mustJSONRaw(map[string]any{"command": "rm -rf /"})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "blocked"),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
				FinishReason: sdkmodel.FinishReasonStop,
			},
		}, nil)
	}
}

type approveEscalatedBashRuntimeModel struct {
	calls   int
	command string
}

func (m *approveEscalatedBashRuntimeModel) Name() string { return "approve-escalated-bash" }

func (m *approveEscalatedBashRuntimeModel) Generate(context.Context, *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		if callIndex == 1 {
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
						ID:   "bash-approve-1",
						Name: shell.BashToolName,
						Args: string(mustJSONRaw(map[string]any{
							"command":         m.command,
							"workdir":         ".",
							"yield_time_ms":   200,
							"with_escalation": true,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "done"),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
				FinishReason: sdkmodel.FinishReasonStop,
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

func (m *bashTaskLoopRuntimeModel) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
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
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
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
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "async bash done"),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func mustFindTaskID(t *testing.T, req *sdkmodel.Request) string {
	t.Helper()
	if req == nil {
		t.Fatal("request = nil")
	}
	for _, message := range req.Messages {
		for _, result := range message.ToolResults() {
			for _, part := range result.Content {
				if part.Kind != sdkmodel.PartKindJSON || part.JSON == nil {
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

func (m *spawnTaskLoopRuntimeModel) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
						ID:   "spawn-1",
						Name: spawntool.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"agent":  "self",
							"prompt": "Reply with exactly: spawn child ok",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
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
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "spawn child ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *spawnApprovalTaskLoopRuntimeModel) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	agent := strings.TrimSpace(m.agent)
	if agent == "" {
		agent = "codex"
	}
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
						ID:   "spawn-approval-1",
						Name: spawntool.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"agent":  agent,
							"prompt": "Run the approval flow and reply with exactly: child approval ok",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
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
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "child approval ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *spawnProbeTaskLoopRuntimeModel) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	if callIndex == 2 {
		m.taskID = mustFindTaskID(m.t, req)
	}
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		switch callIndex {
		case 1:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
						ID:   "spawn-probe-1",
						Name: spawntool.ToolName,
						Args: string(mustJSONRaw(map[string]any{
							"agent":  "self",
							"prompt": "Check whether SPAWN is available and reply with exactly the result.",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
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
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "spawn disabled"),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func mustSessionTaskID(t *testing.T, events []*sdksession.Event) string {
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

func eventToolRawOutput(event *sdksession.Event) map[string]any {
	if update := sdksession.ProtocolUpdateOf(event); update != nil {
		return update.RawOutput
	}
	return nil
}

func eventToolRawInput(event *sdksession.Event) map[string]any {
	if update := sdksession.ProtocolUpdateOf(event); update != nil {
		return update.RawInput
	}
	return nil
}

func taskIDFromSessionEvent(event *sdksession.Event) string {
	for _, values := range []map[string]any{eventToolRawOutput(event), eventToolRawInput(event)} {
		if taskID, _ := values["task_id"].(string); strings.TrimSpace(taskID) != "" {
			return strings.TrimSpace(taskID)
		}
	}
	return ""
}

func terminalFramesText(frames []sdkstream.Frame) string {
	var out strings.Builder
	for _, frame := range frames {
		out.WriteString(frame.Text)
	}
	return out.String()
}

type approvalRequesterFunc func(context.Context, sdkruntime.ApprovalRequest) (sdkruntime.ApprovalResponse, error)

func (f approvalRequesterFunc) RequestApproval(ctx context.Context, req sdkruntime.ApprovalRequest) (sdkruntime.ApprovalResponse, error) {
	return f(ctx, req)
}

type attemptFactory struct {
	mu     sync.Mutex
	agents []sdkruntime.Agent
	specs  []sdkruntime.AgentSpec
	calls  int
}

func (f *attemptFactory) NewAgent(_ context.Context, spec sdkruntime.AgentSpec) (sdkruntime.Agent, error) {
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

func (f *attemptFactory) Specs() []sdkruntime.AgentSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sdkruntime.AgentSpec, len(f.specs))
	copy(out, f.specs)
	return out
}

type seqAgent struct {
	events []*sdksession.Event
	err    error
}

func (a seqAgent) Name() string { return "seq" }

func (a seqAgent) Run(sdkruntime.Context) iter.Seq2[*sdksession.Event, error] {
	return func(yield func(*sdksession.Event, error) bool) {
		for _, event := range a.events {
			if !yield(sdksession.CloneEvent(event), nil) {
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

func (r *yieldProbeSandboxRuntime) Describe() sdksandbox.Descriptor {
	return sdksandbox.Descriptor{
		Backend:   sdksandbox.BackendHost,
		Isolation: sdksandbox.IsolationHost,
		Capabilities: sdksandbox.CapabilitySet{
			CommandExec:   true,
			AsyncSessions: true,
		},
	}
}

func (r *yieldProbeSandboxRuntime) FileSystem() sdksandbox.FileSystem { return nil }

func (r *yieldProbeSandboxRuntime) FileSystemFor(sdksandbox.Constraints) sdksandbox.FileSystem {
	return nil
}

func (r *yieldProbeSandboxRuntime) Run(context.Context, sdksandbox.CommandRequest) (sdksandbox.CommandResult, error) {
	return sdksandbox.CommandResult{}, nil
}

func (r *yieldProbeSandboxRuntime) Start(_ context.Context, req sdksandbox.CommandRequest) (sdksandbox.Session, error) {
	if r.session == nil {
		r.session = newYieldProbeSandboxSession()
	}
	r.session.command = req.Command
	r.session.workdir = req.Dir
	return r.session, nil
}

func (r *yieldProbeSandboxRuntime) OpenSession(string) (sdksandbox.Session, error) {
	if r.session == nil {
		r.session = newYieldProbeSandboxSession()
	}
	return r.session, nil
}

func (r *yieldProbeSandboxRuntime) OpenSessionRef(ref sdksandbox.SessionRef) (sdksandbox.Session, error) {
	return r.OpenSession(ref.SessionID)
}

func (r *yieldProbeSandboxRuntime) SupportedBackends() []sdksandbox.Backend {
	return []sdksandbox.Backend{sdksandbox.BackendHost}
}

func (r *yieldProbeSandboxRuntime) Status() sdksandbox.Status {
	return sdksandbox.Status{
		RequestedBackend: sdksandbox.BackendHost,
		ResolvedBackend:  sdksandbox.BackendHost,
	}
}

func (r *yieldProbeSandboxRuntime) Close() error { return nil }

type yieldProbeSandboxSession struct {
	command  string
	workdir  string
	lastWait time.Duration
}

func newYieldProbeSandboxSession() *yieldProbeSandboxSession {
	return &yieldProbeSandboxSession{}
}

func (s *yieldProbeSandboxSession) Ref() sdksandbox.SessionRef {
	return sdksandbox.SessionRef{Backend: sdksandbox.BackendHost, SessionID: "yield-probe-session"}
}

func (s *yieldProbeSandboxSession) Terminal() sdksandbox.TerminalRef {
	return sdksandbox.TerminalRef{
		Backend:    sdksandbox.BackendHost,
		SessionID:  "yield-probe-session",
		TerminalID: "yield-probe-terminal",
	}
}

func (s *yieldProbeSandboxSession) WriteInput(context.Context, []byte) error { return nil }

func (s *yieldProbeSandboxSession) ReadOutput(context.Context, int64, int64) ([]byte, []byte, int64, int64, error) {
	return nil, nil, 0, 0, nil
}

func (s *yieldProbeSandboxSession) Status(context.Context) (sdksandbox.SessionStatus, error) {
	return sdksandbox.SessionStatus{
		SessionRef:    s.Ref(),
		Terminal:      s.Terminal(),
		Running:       true,
		SupportsInput: true,
		UpdatedAt:     time.Now(),
	}, nil
}

func (s *yieldProbeSandboxSession) Wait(_ context.Context, timeout time.Duration) (sdksandbox.SessionStatus, error) {
	s.lastWait = timeout
	return s.Status(context.Background())
}

func (s *yieldProbeSandboxSession) Result(context.Context) (sdksandbox.CommandResult, error) {
	return sdksandbox.CommandResult{}, nil
}

func (s *yieldProbeSandboxSession) Terminate(context.Context) error { return nil }

type runningOnlyProbeSandboxSession struct {
	lastWait time.Duration
}

type runningOnlyProbeSandboxRuntime struct {
	session *runningOnlyProbeSandboxSession
}

func (s *runningOnlyProbeSandboxSession) Ref() sdksandbox.SessionRef {
	return sdksandbox.SessionRef{Backend: sdksandbox.BackendHost, SessionID: "running-only-session"}
}

func (s *runningOnlyProbeSandboxSession) Terminal() sdksandbox.TerminalRef {
	return sdksandbox.TerminalRef{
		Backend:    sdksandbox.BackendHost,
		SessionID:  "running-only-session",
		TerminalID: "running-only-terminal",
	}
}

func (s *runningOnlyProbeSandboxSession) WriteInput(context.Context, []byte) error { return nil }

func (s *runningOnlyProbeSandboxSession) ReadOutput(context.Context, int64, int64) ([]byte, []byte, int64, int64, error) {
	return nil, nil, 0, 0, nil
}

func (s *runningOnlyProbeSandboxSession) Status(context.Context) (sdksandbox.SessionStatus, error) {
	return sdksandbox.SessionStatus{
		SessionRef:    s.Ref(),
		Terminal:      s.Terminal(),
		Running:       true,
		SupportsInput: true,
		UpdatedAt:     time.Now(),
	}, nil
}

func (s *runningOnlyProbeSandboxSession) Wait(_ context.Context, timeout time.Duration) (sdksandbox.SessionStatus, error) {
	s.lastWait = timeout
	return s.Status(context.Background())
}

func (s *runningOnlyProbeSandboxSession) Result(context.Context) (sdksandbox.CommandResult, error) {
	panic("waitBash should not request Result while task is still running")
}

func (s *runningOnlyProbeSandboxSession) Terminate(context.Context) error { return nil }

func (r *runningOnlyProbeSandboxRuntime) Describe() sdksandbox.Descriptor {
	return sdksandbox.Descriptor{
		Backend:   sdksandbox.BackendHost,
		Isolation: sdksandbox.IsolationHost,
		Capabilities: sdksandbox.CapabilitySet{
			CommandExec:   true,
			AsyncSessions: true,
		},
	}
}

func (r *runningOnlyProbeSandboxRuntime) FileSystem() sdksandbox.FileSystem { return nil }

func (r *runningOnlyProbeSandboxRuntime) FileSystemFor(sdksandbox.Constraints) sdksandbox.FileSystem {
	return nil
}

func (r *runningOnlyProbeSandboxRuntime) Run(context.Context, sdksandbox.CommandRequest) (sdksandbox.CommandResult, error) {
	return sdksandbox.CommandResult{}, nil
}

func (r *runningOnlyProbeSandboxRuntime) Start(_ context.Context, _ sdksandbox.CommandRequest) (sdksandbox.Session, error) {
	if r.session == nil {
		r.session = &runningOnlyProbeSandboxSession{}
	}
	return r.session, nil
}

func (r *runningOnlyProbeSandboxRuntime) OpenSession(string) (sdksandbox.Session, error) {
	if r.session == nil {
		r.session = &runningOnlyProbeSandboxSession{}
	}
	return r.session, nil
}

func (r *runningOnlyProbeSandboxRuntime) OpenSessionRef(ref sdksandbox.SessionRef) (sdksandbox.Session, error) {
	return r.OpenSession(ref.SessionID)
}

func (r *runningOnlyProbeSandboxRuntime) SupportedBackends() []sdksandbox.Backend {
	return []sdksandbox.Backend{sdksandbox.BackendHost}
}

func (r *runningOnlyProbeSandboxRuntime) Status() sdksandbox.Status {
	return sdksandbox.Status{
		RequestedBackend: sdksandbox.BackendHost,
		ResolvedBackend:  sdksandbox.BackendHost,
	}
}

func (r *runningOnlyProbeSandboxRuntime) Close() error { return nil }

func newRuntimeBashToolTestHarness(t *testing.T) (sdksession.Service, sdksession.Session, *Runtime) {
	t.Helper()

	sessions, session := newTestSessionService(t, "sess-bash-yield-default")
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		DefaultPolicyMode: policypresets.ModeFullAccess,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return sessions, session, runtime
}

func mustRuntimeBashTool(t *testing.T, runtime sdksandbox.Runtime) sdktool.Tool {
	t.Helper()

	tool, err := shell.NewBash(shell.BashConfig{Runtime: runtime})
	if err != nil {
		t.Fatalf("shell.NewBash() error = %v", err)
	}
	return tool
}

func callRuntimeBashTool(t *testing.T, tool runtimeBashTool, args map[string]any) sdktool.Result {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := tool.Call(context.Background(), sdktool.Call{
		ID:    "bash-yield-test",
		Name:  shell.BashToolName,
		Input: raw,
	})
	if err != nil {
		t.Fatalf("tool.Call() error = %v", err)
	}
	return result
}

func callRuntimeTaskTool(t *testing.T, tool runtimeTaskTool, args map[string]any) sdktool.Result {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := tool.Call(context.Background(), sdktool.Call{
		ID:    "task-control-test",
		Name:  tasktool.ToolName,
		Input: raw,
	})
	if err != nil {
		t.Fatalf("tool.Call() error = %v", err)
	}
	return result
}

func assertRunningTaskSnapshot(t *testing.T, result sdktool.Result) {
	t.Helper()

	if len(result.Content) == 0 {
		t.Fatal("result.Content = empty, want task snapshot payload")
	}
	part := result.Content[0]
	if part.Kind != sdkmodel.PartKindJSON || part.JSON == nil {
		t.Fatalf("result.Content[0] = %#v, want json part", part)
	}
	var payload map[string]any
	if err := json.Unmarshal(part.JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(snapshot) error = %v", err)
	}
	if got, _ := payload["state"].(string); got != string(sdktask.StateRunning) {
		t.Fatalf("snapshot state = %q, want %q", got, sdktask.StateRunning)
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
	session := sdksession.Session{}
	if got, err := resolveSpawnAgent(session, ""); err != nil || got != "self" {
		t.Fatalf("resolveSpawnAgent(empty) = %q, %v; want self", got, err)
	}
	if got, err := resolveSpawnAgent(session, "self"); err != nil || got != "self" {
		t.Fatalf("resolveSpawnAgent(self) = %q, %v; want self", got, err)
	}
	if got, err := resolveSpawnAgent(session, "codex"); err != nil || got != "codex" {
		t.Fatalf("resolveSpawnAgent(codex) = %q, %v; want codex", got, err)
	}
}
