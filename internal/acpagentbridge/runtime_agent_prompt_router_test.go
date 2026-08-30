package acpagentbridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestRuntimeAgentNewRequiresSlashResultFormatterWithPromptRouter(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	_, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  &promptRouterRuntime{sessions: sessions},
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, nil
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "slash result formatter is required") {
		t.Fatalf("runtimeacp.New() error = %v, want missing slash result formatter", err)
	}
}

func TestRuntimeAgentPromptSlashCommandUsesPromptRouterBeforeMainRuntime(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &promptRouterRuntime{sessions: sessions}
	statusSlash := controlprompt.NewStatusSlashResult(controlstatus.StatusSnapshot{
		Session:     controlstatus.StatusSession{ID: "session-1"},
		ModelStatus: controlstatus.StatusModel{Display: "ollama/llama3"},
	})
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled:             true,
			SlashResult:         &statusSlash,
			SuppressTurnDivider: true,
		},
	}
	formatterCalled := false
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		SlashResultFormatter: func(result controlprompt.SlashCommandResult) string {
			formatterCalled = true
			return result.Status.ModelStatus.Display
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(activeSession.SessionId),
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/status"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/status) error = %v", err)
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acpsdk.StopReasonEndTurn)
	}
	if runtime.runCalled {
		t.Fatal("main runtime Run was called for handled slash command")
	}
	if !formatterCalled {
		t.Fatal("assembly-provided slash result formatter was not called")
	}
	if strings.TrimSpace(router.request.Submission.Text) != "/status" {
		t.Fatalf("prompt router request = %#v, want /status", router.request)
	}
	if got := firstAgentMessageChunk(cb.notifications); !strings.Contains(got, "ollama/llama3") {
		t.Fatalf("agent message updates = %#v, want slash output", cb.notifications)
	}
}

func TestRuntimeAgentPromptRouterSuppressesLiveUserMessageEcho(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &promptRouterRuntime{sessions: sessions}
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled: true,
			Events: []eventstream.Envelope{
				{
					Kind: eventstream.KindSessionUpdate,
					Update: eventstream.ContentChunk{
						SessionUpdate: eventstream.UpdateUserMessage,
						Content:       eventstream.TextContent{Type: "text", Text: "hello"},
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
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for routed prompt")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		SlashResultFormatter: testSlashResultFormatter,
		AppName:              "caelis",
		UserID:               "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(activeSession.SessionId),
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"hello"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acpsdk.StopReasonEndTurn)
	}
	for _, notification := range cb.notifications {
		if notification.Update.SessionUpdateType() == eventstream.UpdateUserMessage {
			t.Fatalf("notifications = %#v, live ACP prompt should not emit user_message_chunk", cb.notifications)
		}
	}
	if got := firstAgentMessageChunk(cb.notifications); got != "ok" {
		t.Fatalf("agent message updates = %#v, want router notice", cb.notifications)
	}
}

func TestRuntimeAgentPromptRouterHandlesSharedSlashWithImagePart(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
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
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for shared slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		SlashResultFormatter: testSlashResultFormatter,
		AppName:              "caelis",
		UserID:               "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(activeSession.SessionId),
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/review inspect the screenshot"}`),
			json.RawMessage(`{"type":"image","mimeType":"image/png","name":"shot.png","data":"` + imageData + `"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/review + image) error = %v", err)
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acpsdk.StopReasonEndTurn)
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
	sessions := inmemory.NewStore(inmemory.Config{})
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
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for dynamic slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		SlashResultFormatter: testSlashResultFormatter,
		Commands:             availableCommandProvider{{Name: "helper", Description: "bounded helper"}},
		AppName:              "caelis",
		UserID:               "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(activeSession.SessionId),
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/helper inspect the screenshot"}`),
			json.RawMessage(`{"type":"image","mimeType":"image/png","name":"shot.png","data":"` + imageData + `"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/helper + image) error = %v", err)
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acpsdk.StopReasonEndTurn)
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
	sessions := inmemory.NewStore(inmemory.Config{})
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
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for normal image prompt")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		SlashResultFormatter: testSlashResultFormatter,
		AppName:              "caelis",
		UserID:               "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(activeSession.SessionId),
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"inspect the screenshot"}`),
			json.RawMessage(`{"type":"image","mimeType":"image/png","name":"shot.png","data":"` + imageData + `"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(normal + image) error = %v", err)
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acpsdk.StopReasonEndTurn)
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
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
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
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for routed prompt")
		},
		PromptRouterFactory: func(_ context.Context, activeSession session.Session) (controlprompt.Router, error) {
			if activeSession.WorkspaceKey != "ws-b" {
				t.Fatalf("active session workspace = %q, want ws-b", activeSession.WorkspaceKey)
			}
			return router, nil
		},
		SlashResultFormatter: testSlashResultFormatter,
		AppName:              "caelis",
		UserID:               "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	resp, err := agent.Prompt(ctx, runtimeacp.PromptInput{
		SessionID: "shared-session",
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/status"}`),
		},
	}, &recordingPromptCallbacks{})
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acpsdk.StopReasonEndTurn)
	}
}

func TestRuntimeAgentPromptRouterAppliesSideEffectsWithoutTurn(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &promptRouterRuntime{sessions: sessions}
	commands := availableCommandProvider{{Name: "status", Description: "Show status"}}
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled:         true,
			ClearHistory:    true,
			RefreshCommands: true,
			StatusUpdate: &controlstatus.StatusSnapshot{
				Session: controlstatus.StatusSession{ID: "session-1"},
			},
			SuppressTurnDivider: true,
		},
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		SlashResultFormatter: testSlashResultFormatter,
		Modes:                testModeProvider{},
		Config:               testConfigProvider{},
		Commands:             commands,
		AppName:              "caelis",
		UserID:               "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	router.result.StatusUpdate.Session.ID = string(activeSession.SessionId)
	cb := &recordingPromptCallbacks{}
	resp, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(activeSession.SessionId),
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/model use fast"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/model use fast) error = %v", err)
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acpsdk.StopReasonEndTurn)
	}
	seenSessionInfo := false
	seenMode := false
	seenConfig := false
	seenCommands := false
	for _, notification := range cb.notifications {
		if notification.SessionID != string(activeSession.SessionId) {
			t.Fatalf("notification sessionID = %q, want %q: %#v", notification.SessionID, string(activeSession.SessionId), notification)
		}
		switch update := notification.Update.(type) {
		case eventstream.RawUpdate:
			switch update.SessionUpdate {
			case eventstream.UpdateSessionInfo:
				var decoded acpsdk.SessionSessionInfoUpdate
				seenSessionInfo = json.Unmarshal(update.Raw, &decoded) == nil && decoded.SessionUpdate == eventstream.UpdateSessionInfo
			case eventstream.UpdateCurrentMode:
				var decoded acpsdk.SessionCurrentModeUpdate
				seenMode = json.Unmarshal(update.Raw, &decoded) == nil &&
					decoded.SessionUpdate == eventstream.UpdateCurrentMode && decoded.CurrentModeId == "default"
			case eventstream.UpdateAvailableCmds:
				var decoded acpsdk.SessionAvailableCommandsUpdate
				seenCommands = json.Unmarshal(update.Raw, &decoded) == nil &&
					decoded.SessionUpdate == eventstream.UpdateAvailableCmds &&
					len(decoded.AvailableCommands) == 1 && decoded.AvailableCommands[0].Name == "status"
			case eventstream.UpdateConfigOption:
				var decoded acpsdk.SessionConfigOptionUpdate
				seenConfig = json.Unmarshal(update.Raw, &decoded) == nil &&
					decoded.SessionUpdate == eventstream.UpdateConfigOption && len(decoded.ConfigOptions) == 1
			}
		}
	}
	if !seenSessionInfo || !seenMode || !seenConfig || !seenCommands {
		t.Fatalf("notifications = %#v, want session info, mode, config, and available commands updates", cb.notifications)
	}
	if runtime.runCalled {
		t.Fatal("main runtime Run was called for handled slash command")
	}
}

func TestRuntimeAgentPromptRouterTurnFeedReturnsEmitErrors(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &promptRouterRuntime{sessions: sessions}
	status := eventstream.ToolStatusInProgress
	turn := newTestControlTurn(
		eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Status:        &status,
			},
		},
		eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "stream output"},
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
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		SlashResultFormatter: testSlashResultFormatter,
		AppName:              "caelis",
		UserID:               "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	wantErr := errors.New("session update failed")
	cb := &errorOnAgentMessageCallbacks{err: wantErr}
	_, err = agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(activeSession.SessionId),
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

func TestRuntimeAgentPromptRouterTurnFeedEmitsTerminalMetaForACPStdio(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &promptRouterRuntime{sessions: sessions}
	running := eventstream.ToolStatusInProgress
	completed := eventstream.ToolStatusCompleted
	turn := newTestControlTurn(
		eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Status:        &running,
				Content: []eventstream.ToolCallContent{{
					Type:       "terminal",
					TerminalID: "call-1",
				}},
			},
		},
		eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Content: []eventstream.ToolCallContent{{
					Type:       "terminal",
					TerminalID: "call-1",
					Content:    eventstream.TextContent{Type: "text", Text: "streamed output\n"},
				}},
				Meta: transientTerminalStreamMetaForTest("append"),
			},
		},
		eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Status:        &completed,
				Content: []eventstream.ToolCallContent{{
					Type:       "terminal",
					TerminalID: "call-1",
				}},
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
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		SlashResultFormatter: testSlashResultFormatter,
		AppName:              "caelis",
		UserID:               "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	_, err = agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(activeSession.SessionId),
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
	var finalUpdate *eventstream.ToolCallUpdate
	for _, notification := range cb.notifications {
		update, ok := notification.Update.(eventstream.ToolCallUpdate)
		if !ok || strings.TrimSpace(update.ToolCallID) != "call-1" || update.Status == nil || *update.Status != eventstream.ToolStatusCompleted {
			continue
		}
		finalUpdate = &update
		break
	}
	if finalUpdate == nil {
		t.Fatalf("notifications = %#v, want completed update", cb.notifications)
		return
	}
	for _, item := range finalUpdate.Content {
		if text := session.ExtractProtocolText(item.Content); text != "" {
			t.Fatalf("completed update = %#v, final status should not repeat streamed terminal content", *finalUpdate)
		}
	}
}

func TestRuntimeAgentPromptRouterDoesNotRewriteCumulativeFinalContent(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &promptRouterRuntime{sessions: sessions}
	turn := newTestControlTurn(
		eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "好的！"},
			},
		},
		eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "让我"},
			},
		},
		eventstream.Envelope{
			Kind:  eventstream.KindSessionUpdate,
			Final: true,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "好的！让我"},
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
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		SlashResultFormatter: testSlashResultFormatter,
		AppName:              "caelis",
		UserID:               "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	_, err = agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(activeSession.SessionId),
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/review"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/review) error = %v", err)
	}
	if got, want := agentMessageChunks(cb.notifications), []string{"好的！", "让我", "好的！让我"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("assistant chunks = %#v, want exact producer payloads %#v", got, want)
	}
}

func TestRuntimeAgentPromptRouterProjectsOnlyChildFinalResponseIntoParentSpawnResult(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &promptRouterRuntime{sessions: sessions}
	status := eventstream.ToolStatusInProgress
	completed := eventstream.ToolStatusCompleted
	spawnKind := "Spawn"
	childTitle := "Apply child patch"
	childCommandTitle := "Run child command"
	line := 12
	parentTool := &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
	childDelivery := &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
	main := eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "spawn-1",
			Kind:          &spawnKind,
			Status:        &status,
		},
	}
	streamEvents := []eventstream.Envelope{
		{
			Kind:       eventstream.KindSessionUpdate,
			SessionID:  "session-1",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			ParentTool: parentTool,
			Delivery:   childDelivery,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				MessageID:     "child-message-1",
				Content:       eventstream.TextContent{Type: "text", Text: "child opening\n"},
			},
		},
		{
			Kind:       eventstream.KindSessionUpdate,
			SessionID:  "session-1",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			ParentTool: parentTool,
			Delivery:   childDelivery,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentThought,
				Content:       eventstream.TextContent{Type: "text", Text: "child thought"},
			},
		},
		{
			Kind:       eventstream.KindSessionUpdate,
			SessionID:  "session-1",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			ParentTool: parentTool,
			Delivery:   childDelivery,
			Update: eventstream.ToolCall{
				SessionUpdate: eventstream.UpdateToolCall,
				ToolCallID:    "child-list-1",
				Title:         childTitle,
				Kind:          "Patch",
				Status:        eventstream.ToolStatusInProgress,
			},
		},
		{
			Kind:       eventstream.KindSessionUpdate,
			SessionID:  "session-1",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			ParentTool: parentTool,
			Delivery:   childDelivery,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "child-list-1",
				Status:        &completed,
				Locations:     []eventstream.ToolCallLocation{{Path: "child.txt", Line: &line}},
				Content: []eventstream.ToolCallContent{{
					Type:    "diff",
					Path:    "child.txt",
					NewText: "new child text\n",
				}},
			},
		},
		{
			Kind:       eventstream.KindSessionUpdate,
			SessionID:  "session-1",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			ParentTool: parentTool,
			Delivery:   childDelivery,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "child-command-1",
				Title:         &childCommandTitle,
				Status:        &status,
				Content: []eventstream.ToolCallContent{{
					Type:       "terminal",
					TerminalID: "child-terminal-1",
					Content:    eventstream.TextContent{Type: "text", Text: "nested output\n"},
				}},
			},
		},
		{
			Kind:       eventstream.KindSessionUpdate,
			SessionID:  "session-1",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			ParentTool: parentTool,
			Delivery:   childDelivery,
			Update: eventstream.PlanUpdate{
				SessionUpdate: eventstream.UpdatePlan,
				Entries: []eventstream.PlanEntry{{
					Content: "inspect child output",
					Status:  "in_progress",
				}},
			},
		},
		{
			Kind:       eventstream.KindSessionUpdate,
			SessionID:  "session-1",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			ParentTool: parentTool,
			Delivery:   childDelivery,
			Final:      true,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				MessageID:     "child-message-final",
				Content:       eventstream.TextContent{Type: "text", Text: "child final"},
			},
		},
		{
			Kind:       eventstream.KindLifecycle,
			SessionID:  "session-1",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			TurnID:     "child-turn-1",
			ParentTool: parentTool,
			Delivery:   childDelivery,
			Final:      true,
			Lifecycle:  &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "spawn-1",
				Status:        &completed,
				Meta: map[string]any{"caelis": map[string]any{"runtime": map[string]any{"task": map[string]any{
					"task_id": "task-1", "running": false, "state": "completed", "result": "child final",
				}}}},
			},
		},
	}
	turn := newTestControlTurn(append([]eventstream.Envelope{main}, streamEvents...)...)
	router := &testPromptRouter{
		result: controlprompt.Result{
			Handled: true,
			Turn:    turn,
		},
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for handled slash command")
		},
		PromptRouterFactory: func(context.Context, session.Session) (controlprompt.Router, error) {
			return router, nil
		},
		SlashResultFormatter: testSlashResultFormatter,
		AppName:              "caelis",
		UserID:               "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	_, err = agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(activeSession.SessionId),
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/review"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/review) error = %v", err)
	}
	if got := agentMessageChunks(cb.notifications); len(got) != 0 {
		t.Fatalf("agent message chunks = %#v, want no child narrative flattened into the main transcript", got)
	}
	if got := agentThoughtChunks(cb.notifications); len(got) != 0 {
		t.Fatalf("agent thought chunks = %#v, want no child thought flattened into the main transcript", got)
	}
	if countToolCallNotifications(cb.notifications, "child-list-1") != 0 || countToolCallNotifications(cb.notifications, "child-command-1") != 0 {
		t.Fatalf("notifications = %#v, want no child tool call flattened into the main transcript", cb.notifications)
	}
	if countPlanNotifications(cb.notifications) != 0 {
		t.Fatalf("notifications = %#v, want no child plan flattened into the main transcript", cb.notifications)
	}
	if outputs := terminalOutputPayloads(cb.notifications, "spawn-1"); len(outputs) != 0 {
		t.Fatalf("parent terminal output = %#v, want no nested-child stream extension", outputs)
	}
	if got, want := standardToolResultPayloads(cb.notifications, "spawn-1"), []string{"child final"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("parent standard results = %#v, want only FinalResponse %#v", got, want)
	}
	if !hasCompletedToolUpdate(cb.notifications, "spawn-1") {
		t.Fatalf("notifications = %#v, want parent final result", cb.notifications)
	}
	if got := countTerminalExitsForTool(cb.notifications, "spawn-1"); got != 0 {
		t.Fatalf("terminal exits = %d; notifications = %#v, want no nested-child terminal extension", got, cb.notifications)
	}
}

func countPlanNotifications(notifications []eventstream.SessionNotification) int {
	count := 0
	for _, notification := range notifications {
		if _, ok := notification.Update.(eventstream.PlanUpdate); ok {
			count++
		}
	}
	return count
}

func countToolCallNotifications(notifications []eventstream.SessionNotification, toolCallID string) int {
	count := 0
	for _, notification := range notifications {
		switch update := notification.Update.(type) {
		case eventstream.ToolCall:
			if strings.TrimSpace(update.ToolCallID) == toolCallID {
				count++
			}
		case eventstream.ToolCallUpdate:
			if strings.TrimSpace(update.ToolCallID) == toolCallID {
				count++
			}
		}
	}
	return count
}

func hasCompletedToolUpdate(notifications []eventstream.SessionNotification, toolCallID string) bool {
	for _, notification := range notifications {
		update, ok := notification.Update.(eventstream.ToolCallUpdate)
		if ok && strings.TrimSpace(update.ToolCallID) == toolCallID && update.Status != nil && *update.Status == eventstream.ToolStatusCompleted {
			return true
		}
	}
	return false
}

func hasTerminalExitForTool(notifications []eventstream.SessionNotification, toolCallID string) bool {
	return countTerminalExitsForTool(notifications, toolCallID) > 0
}

func countTerminalExitsForTool(notifications []eventstream.SessionNotification, toolCallID string) int {
	count := 0
	for _, notification := range notifications {
		update, ok := notification.Update.(eventstream.ToolCallUpdate)
		if !ok || strings.TrimSpace(update.ToolCallID) != toolCallID {
			continue
		}
		if exit, ok := acpmeta.ReadTerminalExit(update.Meta); ok && exit.TerminalID == toolCallID {
			count++
		}
	}
	return count
}

func testSlashResultFormatter(controlprompt.SlashCommandResult) string {
	return ""
}

type testPromptRouter struct {
	request controlprompt.Request
	result  controlprompt.Result
	err     error
}

func (r *testPromptRouter) Route(_ context.Context, req controlprompt.Request) (controlprompt.Result, error) {
	r.request = req
	return r.result, r.err
}
