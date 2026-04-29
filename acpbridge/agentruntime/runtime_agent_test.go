package agentruntime_test

import (
	"context"
	"errors"
	"iter"
	"testing"

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
		Loader: override.Loader,
		Modes:  override.Modes,
		Config: override.Config,
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
