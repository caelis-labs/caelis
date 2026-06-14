package acp_test

import (
	"context"
	"encoding/json"
	"iter"
	"slices"
	"testing"
	"time"

	runtimeacp "github.com/OnslaughtSnail/caelis/impl/agent/acp"
	"github.com/OnslaughtSnail/caelis/impl/agent/local"
	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/shell"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
	"github.com/OnslaughtSnail/caelis/protocol/acp/fixture"
)

func TestRuntimeAgentConformanceReplayOrdering(t *testing.T) {
	agent, sessions := newTestRuntimeAgent(t, staticModel{text: "ok"})
	ctx := context.Background()
	activeSession, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "/tmp/acp-fixture-load",
			CWD: "/tmp/acp-fixture-load",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	user := model.NewTextMessage(model.RoleUser, "hello")
	if _, err := sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type:    session.EventTypeUser,
			Message: &user,
			Text:    "hello",
		},
	}); err != nil {
		t.Fatalf("AppendEvent(user) error = %v", err)
	}
	assistant := model.NewTextMessage(model.RoleAssistant, "world")
	if _, err := sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type:    session.EventTypeAssistant,
			Message: &assistant,
			Text:    "world",
			Protocol: &session.EventProtocol{
				UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
			},
		},
	}); err != nil {
		t.Fatalf("AppendEvent(assistant) error = %v", err)
	}
	rec := fixture.NewRecorder(acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	})
	if _, err := agent.LoadSession(ctx, acp.LoadSessionRequest{
		SessionID: activeSession.SessionID,
		CWD:       activeSession.CWD,
	}, rec); err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := fixture.UpdateKinds(rec.Notifications()), []string{acp.UpdateUserMessage, acp.UpdateAgentMessage}; !slices.Equal(got, want) {
		t.Fatalf("replay update kinds = %v, want %v", got, want)
	}
}

func TestRuntimeAgentConformancePromptOrdering(t *testing.T) {
	agent, _ := newTestRuntimeAgent(t, staticModel{text: "ok"})
	rec := fixture.NewRecorder(acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	})
	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: resp.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"Reply with exactly: ok"}`)},
	}, rec); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	kinds := fixture.UpdateKinds(rec.Notifications())
	if len(kinds) < 2 {
		t.Fatalf("prompt update kinds = %v, want at least user + assistant", kinds)
	}
	if kinds[0] != acp.UpdateUserMessage {
		t.Fatalf("first prompt update = %q, want %q", kinds[0], acp.UpdateUserMessage)
	}
	if !slices.Contains(kinds, acp.UpdateAgentMessage) {
		t.Fatalf("prompt update kinds = %v, want assistant message update", kinds)
	}
}

func TestRuntimeAgentConformanceInitializeDoesNotDeclareMCP(t *testing.T) {
	agent, _ := newTestRuntimeAgent(t, staticModel{text: "ok"})
	resp, err := agent.Initialize(context.Background(), acp.InitializeRequest{})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if resp.AgentCapabilities.MCPCapabilities.HTTP || resp.AgentCapabilities.MCPCapabilities.SSE {
		t.Fatalf("mcp capabilities = %#v, want http+sse disabled until ACP mcpServers are wired", resp.AgentCapabilities.MCPCapabilities)
	}
}

func TestRuntimeAgentConformanceEmitsToolCallBeforeToolUpdate(t *testing.T) {
	llm := &toolThenTextModel{}
	echoTool := tool.NamedTool{
		Def: tool.Definition{
			Name:        "ECHO",
			Description: "Echo one value.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": true,
			},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []model.Part{model.NewJSONPart(json.RawMessage(`{"ok":true}`))},
			}, nil
		},
	}
	agent, _ := newTestRuntimeAgentWithTools(t, llm, []tool.Tool{echoTool})
	rec := fixture.NewRecorder(acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	})
	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: resp.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"call echo"}`)},
	}, rec); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	firstCall := -1
	firstUpdate := -1
	for i, notification := range rec.Notifications() {
		switch update := notification.Update.(type) {
		case acp.ToolCall:
			if update.ToolCallID == "call-echo" && firstCall < 0 {
				firstCall = i
			}
		case acp.ToolCallUpdate:
			if update.ToolCallID == "call-echo" && firstUpdate < 0 {
				firstUpdate = i
			}
		}
	}
	if firstCall < 0 {
		t.Fatalf("notifications = %#v, want tool_call for call-echo", rec.Notifications())
	}
	if firstUpdate < 0 {
		t.Fatalf("notifications = %#v, want tool_call_update for call-echo", rec.Notifications())
	}
	if firstUpdate < firstCall {
		t.Fatalf("tool_call_update index %d came before tool_call index %d", firstUpdate, firstCall)
	}
}

func TestRuntimeAgentConformanceEmitsRunCommandToolCallBeforeTerminalUpdates(t *testing.T) {
	llm := &runCommandThenTextModel{}
	sandboxRuntime, err := host.New(host.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	runCommandTool, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: sandboxRuntime})
	if err != nil {
		t.Fatalf("shell.NewRunCommand() error = %v", err)
	}
	agent, _ := newTestRuntimeAgentWithTools(t, llm, []tool.Tool{runCommandTool})
	rec := fixture.NewRecorder(acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	})
	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: resp.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"run command"}`)},
	}, rec); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	firstCall := -1
	firstUpdate := -1
	for i, notification := range rec.Notifications() {
		switch update := notification.Update.(type) {
		case acp.ToolCall:
			if update.ToolCallID == "call-shell" && firstCall < 0 {
				firstCall = i
			}
		case acp.ToolCallUpdate:
			if update.ToolCallID == "call-shell" && firstUpdate < 0 {
				firstUpdate = i
			}
		}
	}
	if firstCall < 0 {
		t.Fatalf("notifications = %#v, want RUN_COMMAND tool_call for call-shell", rec.Notifications())
	}
	if firstUpdate < 0 {
		t.Fatalf("notifications = %#v, want RUN_COMMAND tool_call_update for call-shell", rec.Notifications())
	}
	if firstUpdate < firstCall {
		t.Fatalf("RUN_COMMAND tool_call_update index %d came before tool_call index %d", firstUpdate, firstCall)
	}
}

func TestRuntimeAgentConformanceStreamsDeltasWithoutFinalDuplicate(t *testing.T) {
	agent, _ := newTestRuntimeAgent(t, streamingTextModel{})
	rec := fixture.NewRecorder(acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	})
	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: resp.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"stream hello"}`)},
	}, rec); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	for _, notification := range rec.Notifications() {
		if notification.SessionID != resp.SessionID {
			t.Fatalf("notification sessionId = %q, want %q for update %#v", notification.SessionID, resp.SessionID, notification.Update)
		}
	}
	if got, want := agentMessageTexts(rec.Notifications()), []string{"hel", "lo"}; !slices.Equal(got, want) {
		t.Fatalf("agent message chunks = %#v, want streamed deltas only %#v", got, want)
	}
}

func TestRuntimeAgentConformanceDropsAdjacentDuplicateStreamChunks(t *testing.T) {
	agent, _ := newTestRuntimeAgent(t, duplicateStreamingTextModel{})
	rec := fixture.NewRecorder(acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	})
	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: resp.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"stream duplicate"}`)},
	}, rec); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if got, want := agentMessageTexts(rec.Notifications()), []string{"Pipeline is running. Let me cancel it."}; !slices.Equal(got, want) {
		t.Fatalf("agent message chunks = %#v, want adjacent duplicate dropped %#v", got, want)
	}
}

func TestRuntimeAgentConformanceCancellation(t *testing.T) {
	agent, _ := newTestRuntimeAgent(t, cancelModel{})
	sessionResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	rec := fixture.NewRecorder(acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	})
	done := make(chan acp.PromptResponse, 1)
	errs := make(chan error, 1)
	go func() {
		resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionID: sessionResp.SessionID,
			Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"hang until cancelled"}`)},
		}, rec)
		if err != nil {
			errs <- err
			return
		}
		done <- resp
	}()
	time.Sleep(50 * time.Millisecond)
	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionID: sessionResp.SessionID}); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	select {
	case err := <-errs:
		t.Fatalf("Prompt() error = %v, want cancelled response", err)
	case resp := <-done:
		if resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonCancelled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt() did not return after cancellation")
	}
}

type streamingTextModel struct{}

func (streamingTextModel) Name() string { return "streaming-text" }

func (streamingTextModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		for _, text := range []string{"hel", "lo"} {
			if !yield(&model.StreamEvent{
				Type: model.StreamEventPartDelta,
				PartDelta: &model.PartDelta{
					Kind:      model.PartKindText,
					TextDelta: text,
				},
			}, nil) {
				return
			}
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

type duplicateStreamingTextModel struct{}

func (duplicateStreamingTextModel) Name() string { return "duplicate-streaming-text" }

func (duplicateStreamingTextModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		for _, text := range []string{"Pipeline is running. Let me cancel it.", "Pipeline is running. Let me cancel it."} {
			if !yield(&model.StreamEvent{
				Type: model.StreamEventPartDelta,
				PartDelta: &model.PartDelta{
					Kind:      model.PartKindText,
					TextDelta: text,
				},
			}, nil) {
				return
			}
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "Pipeline is running. Let me cancel it."),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type staticModel struct{ text string }

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

type cancelModel struct{}

func (cancelModel) Name() string { return "cancel-model" }

func (cancelModel) Generate(ctx context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		<-ctx.Done()
		yield(nil, ctx.Err())
	}
}

type toolThenTextModel struct {
	calls int
}

func (m *toolThenTextModel) Name() string { return "tool-then-text" }

func (m *toolThenTextModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls++
		if m.calls == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromAssistantParts("I will call ECHO.", "Need tool result.", []model.ToolCall{{
						ID:   "call-echo",
						Name: "ECHO",
						Args: `{"value":"hello"}`,
					}}),
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
				Message:      model.NewTextMessage(model.RoleAssistant, "done"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type runCommandThenTextModel struct {
	calls int
}

func (m *runCommandThenTextModel) Name() string { return "run-command-then-text" }

func (m *runCommandThenTextModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls++
		if m.calls == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromAssistantParts("I will run a command.", "Need command output.", []model.ToolCall{{
						ID:   "call-shell",
						Name: shell.RunCommandToolName,
						Args: `{"command":"printf acp-run-command-test"}`,
					}}),
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
				Message:      model.NewTextMessage(model.RoleAssistant, "done"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

func agentMessageTexts(notifications []acp.SessionNotification) []string {
	out := make([]string, 0, len(notifications))
	for _, notification := range notifications {
		chunk, ok := notification.Update.(acp.ContentChunk)
		if !ok || chunk.SessionUpdate != acp.UpdateAgentMessage {
			continue
		}
		content, ok := chunk.Content.(acp.TextContent)
		if !ok {
			continue
		}
		out = append(out, content.Text)
	}
	return out
}

func newTestRuntimeAgent(t *testing.T, model model.LLM) (*runtimeacp.RuntimeAgent, session.Service) {
	return newTestRuntimeAgentWithTools(t, model, nil)
}

func newTestRuntimeAgentWithTools(t *testing.T, model model.LLM, tools []tool.Tool) (*runtimeacp.RuntimeAgent, session.Service) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime, err := local.New(local.Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely.",
		},
	})
	if err != nil {
		t.Fatalf("local.New() error = %v", err)
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "chat", Model: model, Tools: tools}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
		AgentInfo: &acp.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	return agent, sessions
}
