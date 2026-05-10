package agentruntime_test

import (
	"context"
	"errors"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/acp"
	agentruntime "github.com/OnslaughtSnail/caelis/acpbridge/agentruntime"
	bridgeassembly "github.com/OnslaughtSnail/caelis/acpbridge/assembly"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	"github.com/OnslaughtSnail/caelis/sdk/runtime/agents/chat"
	"github.com/OnslaughtSnail/caelis/sdk/runtime/local"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	"github.com/OnslaughtSnail/caelis/sdk/session/inmemory"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
)

func TestRuntimeAgentInitializeCapabilitiesDefault(t *testing.T) {
	agent, _ := newRuntimeAgentWithConfig(t, agentruntime.Config{})
	resp, err := agent.Initialize(context.Background(), acp.InitializeRequest{})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if !resp.AgentCapabilities.LoadSession {
		t.Fatal("LoadSession capability = false, want true by default")
	}
	for _, capability := range []string{"list", "resume", "close"} {
		if _, ok := resp.AgentCapabilities.SessionCapabilities[capability]; !ok {
			t.Fatalf("sessionCapabilities[%q] missing", capability)
		}
	}
}

func TestRuntimeAgentInitializeFillsAgentInfoVersion(t *testing.T) {
	agent, _ := newRuntimeAgentWithConfig(t, agentruntime.Config{
		AgentInfo: &acp.Implementation{Name: "caelis"},
	})
	resp, err := agent.Initialize(context.Background(), acp.InitializeRequest{})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if resp.AgentInfo == nil {
		t.Fatal("AgentInfo = nil, want initialized metadata")
	}
	if resp.AgentInfo.Version == "" {
		t.Fatalf("AgentInfo.Version = %q, want non-empty version for ACP clients", resp.AgentInfo.Version)
	}
}

func TestRuntimeAgentNewSessionIncludesInjectedModesAndConfig(t *testing.T) {
	agent, _ := newRuntimeAgentWithConfig(t, agentruntime.Config{
		Modes:  testModeProvider{},
		Config: testConfigProvider{},
	})
	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		CWD:        t.TempDir(),
		MCPServers: nil,
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if resp.Modes == nil || resp.Modes.CurrentModeID != "default" {
		t.Fatalf("resp.Modes = %#v, want injected mode state", resp.Modes)
	}
	if got, want := len(resp.ConfigOptions), 1; got != want {
		t.Fatalf("len(resp.ConfigOptions) = %d, want %d", got, want)
	}
}

func TestRuntimeAgentNewSessionIncludesAssemblyModesAndConfig(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	modes, configs := bridgeassembly.ProvidersFromAssembly(bridgeassembly.ProviderConfig{
		Assembly: sdkplugin.ResolvedAssembly{
			Modes: []sdkplugin.ModeConfig{
				{ID: "default", Name: "Default"},
				{ID: "plan", Name: "Plan"},
			},
			Configs: []sdkplugin.ConfigOption{{
				ID:           "reasoning",
				Name:         "Reasoning",
				DefaultValue: "balanced",
				Options: []sdkplugin.ConfigSelectOption{
					{Value: "balanced", Name: "Balanced"},
					{Value: "deep", Name: "Deep"},
				},
			}},
		},
		Sessions: sessions,
		AppName:  "caelis",
		UserID:   "user-1",
	})
	agent, _ := newRuntimeAgentWithSessionsAndConfig(t, sessions, agentruntime.Config{
		Modes:  modes,
		Config: configs,
	})
	resp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		CWD: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if resp.Modes == nil || resp.Modes.CurrentModeID != "default" {
		t.Fatalf("resp.Modes = %#v, want assembly-backed mode state", resp.Modes)
	}
	if got, want := len(resp.ConfigOptions), 1; got != want {
		t.Fatalf("len(resp.ConfigOptions) = %d, want %d", got, want)
	}
	if got := resp.ConfigOptions[0].CurrentValue; got != "balanced" {
		t.Fatalf("resp.ConfigOptions[0].CurrentValue = %#v, want balanced", got)
	}
}

func TestRuntimeAgentLoadSessionReplaysDurableEvents(t *testing.T) {
	agent, sessions := newRuntimeAgentWithConfig(t, agentruntime.Config{})
	ctx := context.Background()

	session, err := sessions.StartSession(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "/tmp/acp-load",
			CWD: "/tmp/acp-load",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	user := sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")
	if _, err := sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: session.SessionRef,
		Event: &sdksession.Event{
			Type:    sdksession.EventTypeUser,
			Message: &user,
			Text:    "hello",
		},
	}); err != nil {
		t.Fatalf("AppendEvent(user) error = %v", err)
	}
	assistant := sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "world")
	if _, err := sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: session.SessionRef,
		Event: &sdksession.Event{
			Type:    sdksession.EventTypeAssistant,
			Message: &assistant,
			Text:    "world",
			Protocol: &sdksession.EventProtocol{
				UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage),
			},
		},
	}); err != nil {
		t.Fatalf("AppendEvent(assistant) error = %v", err)
	}

	cb := &recordingPromptCallbacks{}
	resp, err := agent.LoadSession(ctx, acp.LoadSessionRequest{
		SessionID: session.SessionID,
		CWD:       session.CWD,
	}, cb)
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if resp.Modes != nil || len(resp.ConfigOptions) != 0 {
		t.Fatalf("LoadSession() returned unexpected optional metadata: %#v", resp)
	}
	if got, want := len(cb.notifications), 2; got != want {
		t.Fatalf("len(notifications) = %d, want %d", got, want)
	}
	if got := cb.notifications[0].Update.SessionUpdateType(); got != acp.UpdateUserMessage {
		t.Fatalf("first replay update = %q, want %q", got, acp.UpdateUserMessage)
	}
	if got := cb.notifications[1].Update.SessionUpdateType(); got != acp.UpdateAgentMessage {
		t.Fatalf("second replay update = %q, want %q", got, acp.UpdateAgentMessage)
	}
}

func TestRuntimeAgentListResumeAndCloseSession(t *testing.T) {
	agent, sessions := newRuntimeAgentWithConfig(t, agentruntime.Config{
		Modes:  testModeProvider{},
		Config: testConfigProvider{},
	})
	ctx := context.Background()
	session, err := sessions.StartSession(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "/tmp/acp-list",
			CWD: "/tmp/acp-list",
		},
		Title: "Listed session",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	list, err := agent.ListSessions(ctx, acp.SessionListRequest{CWD: "/tmp/acp-list"})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != session.SessionID || list.Sessions[0].Title != "Listed session" {
		t.Fatalf("ListSessions() = %#v, want listed session", list)
	}

	resumed, err := agent.ResumeSession(ctx, acp.ResumeSessionRequest{SessionID: session.SessionID, CWD: session.CWD})
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if resumed.Modes == nil || resumed.Modes.CurrentModeID != "default" {
		t.Fatalf("ResumeSession().Modes = %#v, want default mode", resumed.Modes)
	}
	if got, want := len(resumed.ConfigOptions), 1; got != want {
		t.Fatalf("len(ResumeSession().ConfigOptions) = %d, want %d", got, want)
	}

	if _, err := agent.CloseSession(ctx, acp.CloseSessionRequest{SessionID: session.SessionID}); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
}

func TestRuntimeAgentBridgesTerminalStreamToACPDisplayMeta(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := terminalBridgeRuntime{}
	agent, err := agentruntime.New(agentruntime.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, sdksession.Session, acp.PromptRequest) (sdkruntime.AgentSpec, error) {
			return sdkruntime.AgentSpec{Name: "fake"}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
		AgentInfo: &acp.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
	})
	if err != nil {
		t.Fatalf("agentruntime.New() error = %v", err)
	}
	session, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &terminalBridgeCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionID: session.SessionID}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		notes := cb.snapshot()
		if hasTerminalInfo(notes, "call-1") && hasTerminalOutput(notes, "call-1", "streamed output\n") && hasCompletedTerminalExit(notes, "call-1", 0) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("notifications = %#v, want terminal info, streamed output, and exit meta", notes)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRuntimeAgentOptionalMethodsUnsupportedByDefault(t *testing.T) {
	agent, _ := newRuntimeAgentWithConfig(t, agentruntime.Config{})
	if _, err := agent.SetSessionMode(context.Background(), acp.SetSessionModeRequest{}); !errors.Is(err, acp.ErrCapabilityUnsupported) {
		t.Fatalf("SetSessionMode() error = %v, want ErrCapabilityUnsupported", err)
	}
	if _, err := agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{}); !errors.Is(err, acp.ErrCapabilityUnsupported) {
		t.Fatalf("SetSessionConfigOption() error = %v, want ErrCapabilityUnsupported", err)
	}
}

func newRuntimeAgentWithConfig(t *testing.T, override agentruntime.Config) (*agentruntime.RuntimeAgent, sdksession.Service) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	return newRuntimeAgentWithSessionsAndConfig(t, sessions, override)
}

func newRuntimeAgentWithSessionsAndConfig(t *testing.T, sessions sdksession.Service, override agentruntime.Config) (*agentruntime.RuntimeAgent, sdksession.Service) {
	t.Helper()
	runtime, err := local.New(local.Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Answer tersely.",
		},
	})
	if err != nil {
		t.Fatalf("local.New() error = %v", err)
	}
	cfg := agentruntime.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, sdksession.Session, acp.PromptRequest) (sdkruntime.AgentSpec, error) {
			return sdkruntime.AgentSpec{Name: "chat", Model: runtimeAgentTestModel{text: "ok"}}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
		AgentInfo: &acp.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
		Loader:     override.Loader,
		Modes:      override.Modes,
		Config:     override.Config,
		Models:     override.Models,
		Commands:   override.Commands,
		PromptCaps: override.PromptCaps,
	}
	if override.AgentInfo != nil {
		cfg.AgentInfo = override.AgentInfo
	}
	agent, err := agentruntime.New(cfg)
	if err != nil {
		t.Fatalf("agentruntime.New() error = %v", err)
	}
	return agent, sessions
}

type runtimeAgentTestModel struct{ text string }

func (m runtimeAgentTestModel) Name() string { return "stub" }

func (m runtimeAgentTestModel) Generate(context.Context, *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
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

type testModeProvider struct{}

func (testModeProvider) SessionModes(context.Context, sdksession.Session) (*acp.SessionModeState, error) {
	return &acp.SessionModeState{
		AvailableModes: []acp.SessionMode{{ID: "default", Name: "Default"}},
		CurrentModeID:  "default",
	}, nil
}

func (testModeProvider) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

type testConfigProvider struct{}

func (testConfigProvider) SessionConfigOptions(context.Context, sdksession.Session) ([]acp.SessionConfigOption, error) {
	return []acp.SessionConfigOption{{
		Type:         "select",
		ID:           "mode",
		Name:         "Mode",
		CurrentValue: "default",
		Options: []acp.SessionConfigSelectOption{{
			Value: "default",
			Name:  "Default",
		}},
	}}, nil
}

func (testConfigProvider) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}

type recordingPromptCallbacks struct {
	notifications []acp.SessionNotification
}

func (c *recordingPromptCallbacks) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	c.notifications = append(c.notifications, notification)
	return nil
}

func (c *recordingPromptCallbacks) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	}, nil
}

type terminalBridgeRuntime struct{}

func (terminalBridgeRuntime) Run(_ context.Context, req sdkruntime.RunRequest) (sdkruntime.RunResult, error) {
	sessionID := req.SessionRef.SessionID
	return sdkruntime.RunResult{
		Handle: terminalBridgeRun{events: []*sdksession.Event{
			{
				SessionID: sessionID,
				Type:      sdksession.EventTypeToolCall,
				Protocol: &sdksession.EventProtocol{
					UpdateType: string(sdksession.ProtocolUpdateTypeToolCall),
					ToolCall: &sdksession.ProtocolToolCall{
						ID:     "call-1",
						Name:   "BASH",
						Status: "pending",
						RawInput: map[string]any{
							"command": "printf streamed",
						},
					},
				},
			},
			{
				SessionID:  sessionID,
				Type:       sdksession.EventTypeToolResult,
				Visibility: sdksession.VisibilityUIOnly,
				Protocol: &sdksession.EventProtocol{
					UpdateType: string(sdksession.ProtocolUpdateTypeToolUpdate),
					ToolCall: &sdksession.ProtocolToolCall{
						ID:     "call-1",
						Name:   "BASH",
						Status: "running",
						RawOutput: map[string]any{
							"task_id":     "task-1",
							"terminal_id": "terminal-1",
						},
					},
				},
				Meta: map[string]any{
					"task_id":     "task-1",
					"terminal_id": "terminal-1",
				},
			},
		}},
	}, nil
}

func (terminalBridgeRuntime) RunState(context.Context, sdksession.SessionRef) (sdkruntime.RunState, error) {
	return sdkruntime.RunState{}, nil
}

func (terminalBridgeRuntime) Streams() sdkstream.Service { return terminalBridgeStream{} }

type terminalBridgeRun struct {
	events []*sdksession.Event
}

func (r terminalBridgeRun) RunID() string { return "run-1" }

func (r terminalBridgeRun) Events() iter.Seq2[*sdksession.Event, error] {
	return func(yield func(*sdksession.Event, error) bool) {
		for _, event := range r.events {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (terminalBridgeRun) Submit(sdkruntime.Submission) error { return nil }
func (terminalBridgeRun) Cancel() sdkruntime.CancelResult {
	return sdkruntime.CancelResult{Status: sdkruntime.CancelStatusCancelled}
}
func (terminalBridgeRun) Close() error { return nil }

type terminalBridgeStream struct{}

func (terminalBridgeStream) Read(context.Context, sdkstream.ReadRequest) (sdkstream.Snapshot, error) {
	return sdkstream.Snapshot{}, nil
}

func (terminalBridgeStream) Subscribe(context.Context, sdkstream.SubscribeRequest) iter.Seq2[*sdkstream.Frame, error] {
	return func(yield func(*sdkstream.Frame, error) bool) {
		if !yield(&sdkstream.Frame{Text: "streamed output\n"}, nil) {
			return
		}
		code := 0
		yield(&sdkstream.Frame{Closed: true, ExitCode: &code}, nil)
	}
}

type terminalBridgeCallbacks struct {
	mu            sync.Mutex
	notifications []acp.SessionNotification
}

func (c *terminalBridgeCallbacks) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifications = append(c.notifications, notification)
	return nil
}

func (c *terminalBridgeCallbacks) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, nil
}

func (c *terminalBridgeCallbacks) snapshot() []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]acp.SessionNotification(nil), c.notifications...)
}

func hasTerminalInfo(notifications []acp.SessionNotification, terminalID string) bool {
	for _, notification := range notifications {
		call, ok := notification.Update.(acp.ToolCall)
		if !ok {
			continue
		}
		info, ok := call.Meta["terminal_info"].(map[string]any)
		if ok && info["terminal_id"] == terminalID {
			return true
		}
	}
	return false
}

func hasTerminalOutput(notifications []acp.SessionNotification, terminalID string, data string) bool {
	for _, notification := range notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok {
			continue
		}
		output, ok := update.Meta["terminal_output"].(map[string]any)
		if ok && output["terminal_id"] == terminalID && output["data"] == data {
			return true
		}
	}
	return false
}

func hasTerminalExit(notifications []acp.SessionNotification, terminalID string, exitCode int) bool {
	return terminalExit(notifications, terminalID, exitCode) != nil
}

func hasCompletedTerminalExit(notifications []acp.SessionNotification, terminalID string, exitCode int) bool {
	update := terminalExit(notifications, terminalID, exitCode)
	return update != nil && update.Status != nil && *update.Status == acp.ToolStatusCompleted
}

func terminalExit(notifications []acp.SessionNotification, terminalID string, exitCode int) *acp.ToolCallUpdate {
	for _, notification := range notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok {
			continue
		}
		exit, ok := update.Meta["terminal_exit"].(map[string]any)
		if ok && exit["terminal_id"] == terminalID && exit["exit_code"] == exitCode {
			return &update
		}
	}
	return nil
}
