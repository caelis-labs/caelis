package acp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	runtimeacp "github.com/OnslaughtSnail/caelis/impl/agent/acp"
	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	controlprompt "github.com/OnslaughtSnail/caelis/protocol/acp/control/prompt"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestRuntimeAgentPromptSlashCommandUsesPromptRouterBeforeMainRuntime(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &promptRouterRuntime{sessions: sessions}
	statusSlash := control.NewStatusSlashResult(control.StatusSnapshot{
		Session:     control.StatusSession{ID: "session-1"},
		ModelStatus: control.StatusModel{Display: "ollama/llama3"},
	})
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled:             true,
			SlashResult:         &statusSlash,
			SuppressTurnDivider: true,
		},
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (runtimeacp.PromptRouter, error) {
			return router, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/status"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/status) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if runtime.runCalled {
		t.Fatal("main runtime Run was called for handled slash command")
	}
	if strings.TrimSpace(router.request.Submission.Text) != "/status" {
		t.Fatalf("prompt router request = %#v, want /status", router.request)
	}
	if got := firstAgentMessageChunk(cb.notifications); !strings.Contains(got, "ollama/llama3") {
		t.Fatalf("agent message updates = %#v, want slash output", cb.notifications)
	}
}

func TestRuntimeAgentPromptRouterSuppressesLiveUserMessageEcho(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &promptRouterRuntime{sessions: sessions}
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled: true,
			Events: []eventstream.Envelope{
				{
					Kind: eventstream.KindSessionUpdate,
					Update: acp.ContentChunk{
						SessionUpdate: acp.UpdateUserMessage,
						Content:       acp.TextContent{Type: "text", Text: "hello"},
					},
				},
				{
					Kind:   eventstream.KindNotice,
					Notice: "ok",
				},
			},
			SuppressTurnDivider: true,
		},
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for routed prompt")
		},
		PromptRouterFactory: func(context.Context, session.Session) (runtimeacp.PromptRouter, error) {
			return router, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"hello"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	for _, notification := range cb.notifications {
		if notification.Update.SessionUpdateType() == acp.UpdateUserMessage {
			t.Fatalf("notifications = %#v, live ACP prompt should not emit user_message_chunk", cb.notifications)
		}
	}
	if got := firstAgentMessageChunk(cb.notifications); got != "ok" {
		t.Fatalf("agent message updates = %#v, want router notice", cb.notifications)
	}
}

func TestRuntimeAgentPromptRouterHandlesSharedSlashWithImagePart(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &promptRouterRuntime{sessions: sessions}
	imageData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled: true,
			Events: []eventstream.Envelope{{
				Kind:   eventstream.KindNotice,
				Notice: "review started",
			}},
			SuppressTurnDivider: true,
		},
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for shared slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (runtimeacp.PromptRouter, error) {
			return router, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/review inspect the screenshot"}`),
			json.RawMessage(`{"type":"image","mimeType":"image/png","name":"shot.png","data":"` + imageData + `"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/review + image) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if runtime.runCalled {
		t.Fatal("main runtime Run was called for shared slash command with image")
	}
	if strings.TrimSpace(router.request.Submission.Text) != "/review inspect the screenshot" {
		t.Fatalf("prompt router request = %#v, want /review text", router.request)
	}
	attachments := router.request.Submission.Attachments
	if len(attachments) != 1 {
		t.Fatalf("router attachments = %#v, want one image attachment", attachments)
	}
	if wantOffset := len([]rune("/review inspect the screenshot")); attachments[0].Offset != wantOffset {
		t.Fatalf("router attachment offset = %d, want %d", attachments[0].Offset, wantOffset)
	}
	if attachments[0].Name != "shot.png" || attachments[0].MimeType != "image/png" || attachments[0].Data != imageData {
		t.Fatalf("router attachment = %#v, want inline png attachment", attachments[0])
	}
	if got := firstAgentMessageChunk(cb.notifications); got != "review started" {
		t.Fatalf("agent message updates = %#v, want router notice", cb.notifications)
	}
}

func TestRuntimeAgentPromptRouterHandlesDynamicSlashWithImagePart(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &promptRouterRuntime{sessions: sessions}
	imageData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled: true,
			Events: []eventstream.Envelope{{
				Kind:   eventstream.KindNotice,
				Notice: "helper started",
			}},
			SuppressTurnDivider: true,
		},
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for dynamic slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (runtimeacp.PromptRouter, error) {
			return router, nil
		},
		Commands: availableCommandProvider{{Name: "helper", Description: "bounded helper"}},
		AppName:  "caelis",
		UserID:   "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/helper inspect the screenshot"}`),
			json.RawMessage(`{"type":"image","mimeType":"image/png","name":"shot.png","data":"` + imageData + `"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/helper + image) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if runtime.attach.Agent != "" || runtime.runCalled {
		t.Fatalf("runtime attach=%#v runCalled=%v, want prompt router before main runtime", runtime.attach, runtime.runCalled)
	}
	if strings.TrimSpace(router.request.Submission.Text) != "/helper inspect the screenshot" {
		t.Fatalf("prompt router request = %#v, want /helper text", router.request)
	}
	attachments := router.request.Submission.Attachments
	if len(attachments) != 1 || attachments[0].Name != "shot.png" || attachments[0].Data != imageData {
		t.Fatalf("router attachments = %#v, want inline png attachment", attachments)
	}
	if got := firstAgentMessageChunk(cb.notifications); got != "helper started" {
		t.Fatalf("agent message updates = %#v, want router notice", cb.notifications)
	}
}

func TestRuntimeAgentPromptRouterHandlesNormalPromptWithImagePart(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &promptRouterRuntime{sessions: sessions}
	imageData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled: true,
			Events: []eventstream.Envelope{{
				Kind:   eventstream.KindNotice,
				Notice: "submitted",
			}},
			SuppressTurnDivider: true,
		},
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for normal image prompt")
		},
		PromptRouterFactory: func(context.Context, session.Session) (runtimeacp.PromptRouter, error) {
			return router, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"inspect the screenshot"}`),
			json.RawMessage(`{"type":"image","mimeType":"image/png","name":"shot.png","data":"` + imageData + `"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(normal + image) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if runtime.runCalled {
		t.Fatal("main runtime Run was called for normal image prompt")
	}
	if strings.TrimSpace(router.request.Submission.Text) != "inspect the screenshot" {
		t.Fatalf("prompt router request = %#v, want normal prompt text", router.request)
	}
	attachments := router.request.Submission.Attachments
	if len(attachments) != 1 {
		t.Fatalf("router attachments = %#v, want one image attachment", attachments)
	}
	if wantOffset := len([]rune("inspect the screenshot")); attachments[0].Offset != wantOffset {
		t.Fatalf("router attachment offset = %d, want %d", attachments[0].Offset, wantOffset)
	}
	if attachments[0].Name != "shot.png" || attachments[0].MimeType != "image/png" || attachments[0].Data != imageData {
		t.Fatalf("router attachment = %#v, want inline png attachment", attachments[0])
	}
}

func TestRuntimeAgentPromptResolvesSessionByGlobalID(t *testing.T) {
	ctx := context.Background()
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()}))
	if _, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "user-1",
		PreferredSessionID: "shared-session",
		Workspace: session.WorkspaceRef{
			Key: "ws-b",
			CWD: "/tmp/ws-b",
		},
	}); err != nil {
		t.Fatalf("StartSession(ws-b) error = %v", err)
	}
	runtime := &promptRouterRuntime{sessions: sessions}
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled: true,
			Events: []eventstream.Envelope{{
				Kind:   eventstream.KindNotice,
				Notice: "routed",
			}},
			SuppressTurnDivider: true,
		},
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:      runtime,
		Sessions:     sessions,
		WorkspaceKey: "ws-a",
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for routed prompt")
		},
		PromptRouterFactory: func(_ context.Context, activeSession session.Session) (runtimeacp.PromptRouter, error) {
			if activeSession.WorkspaceKey != "ws-b" {
				t.Fatalf("active session workspace = %q, want ws-b", activeSession.WorkspaceKey)
			}
			return router, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	resp, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionID: "shared-session",
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/status"}`),
		},
	}, &recordingPromptCallbacks{})
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
}

func TestRuntimeAgentPromptRouterAppliesSideEffectsWithoutTurn(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &promptRouterRuntime{sessions: sessions}
	commands := availableCommandProvider{{Name: "status", Description: "Show status"}}
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled:         true,
			ClearHistory:    true,
			RefreshCommands: true,
			StatusUpdate: &control.StatusSnapshot{
				Session: control.StatusSession{ID: "session-1"},
			},
			SuppressTurnDivider: true,
		},
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (runtimeacp.PromptRouter, error) {
			return router, nil
		},
		Modes:    testModeProvider{},
		Config:   testConfigProvider{},
		Commands: commands,
		AppName:  "caelis",
		UserID:   "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	router.result.StatusUpdate.Session.ID = activeSession.SessionID
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/model use fast"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/model use fast) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	seenSessionInfo := false
	seenMode := false
	seenConfig := false
	seenCommands := false
	for _, notification := range cb.notifications {
		if notification.SessionID != activeSession.SessionID {
			t.Fatalf("notification sessionID = %q, want %q: %#v", notification.SessionID, activeSession.SessionID, notification)
		}
		switch update := notification.Update.(type) {
		case acp.SessionInfoUpdate:
			seenSessionInfo = update.SessionUpdate == acp.UpdateSessionInfo
		case acp.CurrentModeUpdate:
			seenMode = update.SessionUpdate == acp.UpdateCurrentMode && update.CurrentModeID == "default"
		case acp.ConfigOptionUpdate:
			seenConfig = update.SessionUpdate == acp.UpdateConfigOption && len(update.ConfigOptions) == 1
		case acp.AvailableCommandsUpdate:
			seenCommands = update.SessionUpdate == acp.UpdateAvailableCmds && len(update.AvailableCommands) == 1 && update.AvailableCommands[0].Name == "status"
		}
	}
	if !seenSessionInfo || !seenMode || !seenConfig || !seenCommands {
		t.Fatalf("notifications = %#v, want session info, mode, config, and available commands updates", cb.notifications)
	}
	if runtime.runCalled {
		t.Fatal("main runtime Run was called for handled slash command")
	}
}

func TestRuntimeAgentPromptRouterStreamBridgeReturnsEmitErrors(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &promptRouterRuntime{sessions: sessions}
	status := acp.ToolStatusInProgress
	turn := newTestControlTurn(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &status,
		},
	})
	streamer := testPromptStreamSubscriber{events: []eventstream.Envelope{{
		Kind:   eventstream.KindNotice,
		Notice: "stream output",
	}}}
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled: true,
			Turn:    turn,
		},
		streamer: streamer,
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (runtimeacp.PromptRouter, error) {
			return router, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	wantErr := errors.New("session update failed")
	cb := &errorOnAgentMessageCallbacks{err: wantErr}
	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/review"}`),
		},
	}, cb)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Prompt(/review) error = %v, want %v", err, wantErr)
	}
	if !turn.closed {
		t.Fatal("prompt router turn was not closed")
	}
}

func TestRuntimeAgentPromptRouterStreamBridgeEmitsTerminalMetaForACPStdio(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &promptRouterRuntime{sessions: sessions}
	running := acp.ToolStatusInProgress
	completed := acp.ToolStatusCompleted
	turn := newTestControlTurn(
		eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: acp.ToolCallUpdate{
				SessionUpdate: acp.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Status:        &running,
				Content: []acp.ToolCallContent{{
					Type:       "terminal",
					TerminalID: "call-1",
				}},
			},
		},
		eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: acp.ToolCallUpdate{
				SessionUpdate: acp.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Status:        &completed,
				Content: []acp.ToolCallContent{{
					Type:       "terminal",
					TerminalID: "call-1",
				}},
			},
		},
	)
	streamer := testPromptStreamSubscriber{events: []eventstream.Envelope{{
		Kind: eventstream.KindSessionUpdate,
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Content: []acp.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "call-1",
				Content:    acp.TextContent{Type: "text", Text: "streamed output\n"},
			}},
			Meta: transientTerminalStreamMetaForTest("append"),
		},
	}}}
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled: true,
			Turn:    turn,
		},
		streamer: streamer,
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (runtimeacp.PromptRouter, error) {
			return router, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/review"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/review) error = %v", err)
	}
	if outputs := terminalOutputPayloads(cb.notifications, "call-1"); strings.Join(outputs, "") != "streamed output\n" {
		t.Fatalf("terminal outputs = %#v, want streamed output terminal meta", outputs)
	}
	if !hasTerminalInfo(cb.notifications, "call-1", "call-1") {
		t.Fatalf("notifications = %#v, want local terminal info for ACP stdio", cb.notifications)
	}
	var finalUpdate *acp.ToolCallUpdate
	for _, notification := range cb.notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok || strings.TrimSpace(update.ToolCallID) != "call-1" || update.Status == nil || *update.Status != acp.ToolStatusCompleted {
			continue
		}
		finalUpdate = &update
		break
	}
	if finalUpdate == nil {
		t.Fatalf("notifications = %#v, want completed update", cb.notifications)
	}
	for _, item := range finalUpdate.Content {
		if text := schema.ExtractTextValue(item.Content); text != "" {
			t.Fatalf("completed update = %#v, final status should not repeat streamed terminal content", *finalUpdate)
		}
	}
}

func TestRuntimeAgentPromptRouterDeduplicatesFinalNarrativeReplay(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &promptRouterRuntime{sessions: sessions}
	turn := newTestControlTurn(
		eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: acp.ContentChunk{
				SessionUpdate: acp.UpdateAgentMessage,
				Content:       acp.TextContent{Type: "text", Text: "好的！"},
			},
		},
		eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: acp.ContentChunk{
				SessionUpdate: acp.UpdateAgentMessage,
				Content:       acp.TextContent{Type: "text", Text: "让我"},
			},
		},
		eventstream.Envelope{
			Kind:  eventstream.KindSessionUpdate,
			Final: true,
			Update: acp.ContentChunk{
				SessionUpdate: acp.UpdateAgentMessage,
				Content:       acp.TextContent{Type: "text", Text: "好的！让我"},
			},
		},
	)
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled: true,
			Turn:    turn,
		},
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (runtimeacp.PromptRouter, error) {
			return router, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/review"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/review) error = %v", err)
	}
	if got, want := agentMessageChunks(cb.notifications), []string{"好的！", "让我"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("assistant chunks = %#v, want final replay suppressed with %#v", got, want)
	}
}

func TestRuntimeAgentPromptRouterStreamBridgeSuppressesMirroredSubagentEvents(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &promptRouterRuntime{sessions: sessions}
	status := acp.ToolStatusInProgress
	spawnKind := "SPAWN"
	childTitle := "LIST /tmp/project"
	turn := newTestControlTurn(eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo,
			ToolCallID:    "spawn-1",
			Kind:          &spawnKind,
			Status:        &status,
			Content: []acp.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "spawn-1",
			}},
		},
	})
	streamer := testPromptStreamSubscriber{events: []eventstream.Envelope{
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			Scope:     eventstream.ScopeSubagent,
			ScopeID:   "task-1",
			Meta:      mirroredSubagentStreamMetaForTest("spawn-1", "SPAWN"),
			Update: acp.ContentChunk{
				SessionUpdate: acp.UpdateAgentMessage,
				Content:       acp.TextContent{Type: "text", Text: "child answer"},
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			Scope:     eventstream.ScopeSubagent,
			ScopeID:   "task-1",
			Meta:      mirroredSubagentStreamMetaForTest("spawn-1", "SPAWN"),
			Update: acp.ToolCallUpdate{
				SessionUpdate: acp.UpdateToolCallInfo,
				ToolCallID:    "child-list-1",
				Title:         &childTitle,
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			Update: acp.ToolCallUpdate{
				SessionUpdate: acp.UpdateToolCallInfo,
				ToolCallID:    "spawn-1",
				Content: []acp.ToolCallContent{{
					Type:       "terminal",
					TerminalID: "spawn-1",
					Content:    acp.TextContent{Type: "text", Text: "child answer"},
				}},
				Meta: transientTerminalStreamMetaForTest("append"),
			},
		},
	}}
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled: true,
			Turn:    turn,
		},
		streamer: streamer,
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (runtimeacp.PromptRouter, error) {
			return router, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	_, err = agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/review"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/review) error = %v", err)
	}
	if got := agentMessageChunks(cb.notifications); len(got) != 0 {
		t.Fatalf("agent message chunks = %#v, want mirrored child messages suppressed", got)
	}
	if hasToolCallNotification(cb.notifications, "child-list-1") {
		t.Fatalf("notifications = %#v, want child tool update suppressed", cb.notifications)
	}
	if outputs := terminalOutputPayloads(cb.notifications, "spawn-1"); strings.Join(outputs, "") != "child answer" {
		t.Fatalf("terminal outputs = %#v, want transient parent SPAWN stream terminal meta", outputs)
	}
	if !hasTerminalInfo(cb.notifications, "spawn-1", "spawn-1") {
		t.Fatalf("notifications = %#v, want parent SPAWN terminal info for ACP stdio", cb.notifications)
	}
}

type testPromptRouter struct {
	request  controlprompt.Request
	result   controlprompt.Result
	streamer control.StreamSubscriber
	err      error
}

func (r *testPromptRouter) Route(_ context.Context, req controlprompt.Request) (controlprompt.Result, error) {
	r.request = req
	return r.result, r.err
}

func (r *testPromptRouter) StreamSubscriber() (control.StreamSubscriber, bool) {
	if r.streamer == nil {
		return nil, false
	}
	return r.streamer, true
}
