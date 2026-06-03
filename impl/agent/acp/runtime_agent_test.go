package acp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	runtimeacp "github.com/OnslaughtSnail/caelis/impl/agent/acp"
	bridgeassembly "github.com/OnslaughtSnail/caelis/impl/agent/acp/assembly"
	"github.com/OnslaughtSnail/caelis/impl/agent/local"
	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/approval"
	assemblyapi "github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/ports/tool"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
)

func TestRuntimeAgentInitializeCapabilitiesDefault(t *testing.T) {
	agent, _ := newRuntimeAgentWithConfig(t, runtimeacp.Config{})
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
	agent, _ := newRuntimeAgentWithConfig(t, runtimeacp.Config{
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
	agent, _ := newRuntimeAgentWithConfig(t, runtimeacp.Config{
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
		Assembly: assemblyapi.ResolvedAssembly{
			Modes: []assemblyapi.ModeConfig{
				{ID: "default", Name: "Default"},
				{ID: "plan", Name: "Plan"},
			},
			Configs: []assemblyapi.ConfigOption{{
				ID:           "reasoning",
				Name:         "Reasoning",
				DefaultValue: "balanced",
				Options: []assemblyapi.ConfigSelectOption{
					{Value: "balanced", Name: "Balanced"},
					{Value: "deep", Name: "Deep"},
				},
			}},
		},
		Sessions: sessions,
		AppName:  "caelis",
		UserID:   "user-1",
	})
	agent, _ := newRuntimeAgentWithSessionsAndConfig(t, sessions, runtimeacp.Config{
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
	agent, sessions := newRuntimeAgentWithConfig(t, runtimeacp.Config{})
	ctx := context.Background()

	activeSession, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "/tmp/acp-load",
			CWD: "/tmp/acp-load",
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

	cb := &recordingPromptCallbacks{}
	resp, err := agent.LoadSession(ctx, acp.LoadSessionRequest{
		SessionID: activeSession.SessionID,
		CWD:       activeSession.CWD,
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
	agent, sessions := newRuntimeAgentWithConfig(t, runtimeacp.Config{
		Modes:  testModeProvider{},
		Config: testConfigProvider{},
	})
	ctx := context.Background()
	activeSession, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
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
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != activeSession.SessionID || list.Sessions[0].Title != "Listed session" {
		t.Fatalf("ListSessions() = %#v, want listed session", list)
	}

	resumed, err := agent.ResumeSession(ctx, acp.ResumeSessionRequest{SessionID: activeSession.SessionID, CWD: activeSession.CWD})
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if resumed.Modes == nil || resumed.Modes.CurrentModeID != "default" {
		t.Fatalf("ResumeSession().Modes = %#v, want default mode", resumed.Modes)
	}
	if got, want := len(resumed.ConfigOptions), 1; got != want {
		t.Fatalf("len(ResumeSession().ConfigOptions) = %d, want %d", got, want)
	}

	if _, err := agent.CloseSession(ctx, acp.CloseSessionRequest{SessionID: activeSession.SessionID}); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
}

func TestRuntimeAgentPromptSlashCommandRunsSideACPAndForwardsEvents(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &sideACPCommandRuntime{sessions: sessions}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for side ACP slash command")
		},
		Commands: sideACPCommandProvider{{Name: "helper", Description: "bounded helper"}},
		AppName:  "caelis",
		UserID:   "user-1",
		AgentInfo: &acp.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
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
			json.RawMessage(`{"type":"text","text":"/helper inspect the repo"}`),
		},
	}, cb)
	if err != nil {
		t.Fatalf("Prompt(/helper) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if runtime.runCalled {
		t.Fatal("main runtime Run was called for side ACP slash command")
	}
	if runtime.attach.Agent != "helper" || runtime.attach.Source != "slash_helper" {
		t.Fatalf("attach request = %#v, want helper slash attach", runtime.attach)
	}
	if runtime.prompt.Input != "inspect the repo" || runtime.prompt.Source != "slash_helper" {
		t.Fatalf("prompt request = %#v, want trimmed side ACP prompt", runtime.prompt)
	}
	if got := firstAgentMessageChunk(cb.notifications); got != "side acp output" {
		t.Fatalf("agent message updates = %#v, want side ACP output", cb.notifications)
	}
}

func TestRuntimeAgentPromptSlashCommandPreservesRegisteredACPAgentName(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &sideACPCommandRuntime{sessions: sessions, expectedAgent: "MixedHelper"}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{}, errors.New("main agent spec should not be built for side ACP slash command")
		},
		Commands: sideACPCommandProvider{{Name: "MixedHelper", Description: "case-sensitive helper"}},
		AppName:  "caelis",
		UserID:   "user-1",
		AgentInfo: &acp.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	resp, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: activeSession.SessionID,
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"/MixedHelper inspect the repo"}`),
		},
	}, &recordingPromptCallbacks{})
	if err != nil {
		t.Fatalf("Prompt(/MixedHelper) error = %v", err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acp.StopReasonEndTurn)
	}
	if runtime.attach.Agent != "MixedHelper" {
		t.Fatalf("attach agent = %q, want canonical registered name", runtime.attach.Agent)
	}
}

func TestRuntimeAgentBridgesTerminalStreamToACPTerminalContent(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := terminalBridgeRuntime{}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
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
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &terminalBridgeCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionID: activeSession.SessionID}, cb); err != nil {
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
			t.Fatalf("notifications = %#v, want terminal info, streamed terminal content, and completed terminal status", notes)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRuntimeAgentBridgesSpawnTerminalStreamToACPTerminalContent(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := terminalBridgeRuntime{toolName: "SPAWN", taskID: "task-spawn-1", terminalID: "subagent-task-spawn-1"}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
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
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &terminalBridgeCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionID: activeSession.SessionID}, cb); err != nil {
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
			t.Fatalf("notifications = %#v, want SPAWN terminal info, streamed terminal content, and completed terminal status", notes)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRuntimeAgentDoesNotRepeatTerminalFinalTextAfterStreamOutput(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := terminalBridgeRuntime{closedText: "final summary\n"}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
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
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &terminalBridgeCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionID: activeSession.SessionID}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		notes := cb.snapshot()
		if hasCompletedTerminalExit(notes, "call-1", 0) {
			if hasTerminalOutput(notes, "call-1", "final summary\n") {
				t.Fatalf("notifications = %#v, terminal bridge should not append final summary after streamed output", notes)
			}
			if !hasTerminalOutput(notes, "call-1", "streamed output\n") {
				t.Fatalf("notifications = %#v, want streamed output", notes)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("notifications = %#v, want completed terminal status", notes)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRuntimeAgentSuppressesProjectedTerminalOutputWhenBridgeOwnsTerminal(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := terminalBridgeFinalRuntime{}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
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
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &terminalBridgeCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionID: activeSession.SessionID}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		notes := cb.snapshot()
		if hasCompletedTerminalExit(notes, "call-1", 0) {
			outputs := terminalOutputPayloads(notes, "call-1")
			if got, want := len(outputs), 1; got != want {
				t.Fatalf("terminal outputs = %#v, want exactly one bridged output update", outputs)
			}
			if outputs[0] != "streamed output\n" {
				t.Fatalf("terminal output = %q, want streamed output", outputs[0])
			}
			outputIndex := firstTerminalOutputIndex(notes, "call-1")
			completedIndex := firstCompletedToolUpdateIndex(notes, "call-1")
			if outputIndex < 0 || completedIndex < 0 || outputIndex > completedIndex {
				t.Fatalf("terminal output index = %d, first completed status index = %d; output must arrive before completion: %#v", outputIndex, completedIndex, notes)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("notifications = %#v, want completed terminal status", notes)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRuntimeAgentDoesNotEmitNoOutputTerminalPlaceholder(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := terminalBridgeRuntime{omitStreamedOutput: true}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
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
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &terminalBridgeCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionID: activeSession.SessionID}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		notes := cb.snapshot()
		if hasCompletedTerminalExit(notes, "call-1", 0) {
			if hasAnyTerminalOutput(notes, "call-1") {
				t.Fatalf("notifications = %#v, terminal bridge must not synthesize terminal output", notes)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("notifications = %#v, want completed terminal status without synthetic output", notes)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRuntimeAgentMapsCancelledTerminalCloseToFailed(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := terminalBridgeRuntime{closedState: "cancelled"}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
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
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &terminalBridgeCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionID: activeSession.SessionID}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		notes := cb.snapshot()
		if hasFailedTerminalExit(notes, "call-1") {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("notifications = %#v, want cancelled terminal close mapped to failed status", notes)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRuntimeAgentPromptDeduplicatesCumulativeNarrativeChunks(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  narrativeReplayRuntime{},
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
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
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionID: activeSession.SessionID}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	got := agentMessageChunks(cb.notifications)
	want := []string{"hello", " world"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("assistant chunks = %#v, want %#v", got, want)
	}
}

func TestRuntimeAgentPromptDeduplicatesFinalReasoningReplay(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  narrativeThoughtReplayRuntime{},
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
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
	activeSession, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionID: activeSession.SessionID}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	got := agentThoughtChunks(cb.notifications)
	want := []string{"任务已", "启动"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("thought chunks = %#v, want final replay suppressed with %#v", got, want)
	}
}

func TestRuntimeAgentOptionalMethodsUnsupportedByDefault(t *testing.T) {
	agent, _ := newRuntimeAgentWithConfig(t, runtimeacp.Config{})
	if _, err := agent.SetSessionMode(context.Background(), acp.SetSessionModeRequest{}); !errors.Is(err, acp.ErrCapabilityUnsupported) {
		t.Fatalf("SetSessionMode() error = %v, want ErrCapabilityUnsupported", err)
	}
	if _, err := agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{}); !errors.Is(err, acp.ErrCapabilityUnsupported) {
		t.Fatalf("SetSessionConfigOption() error = %v, want ErrCapabilityUnsupported", err)
	}
}

func TestRuntimeAgentPromptAutoReviewUsesReviewerInsteadOfClientPermission(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &approvalReviewRuntime{}
	reviewer := &recordingApprovalReviewer{}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "chat", Metadata: map[string]any{"policy_mode": "workspace-write"}}, nil
		},
		AppName:               "caelis",
		UserID:                "user-1",
		ApprovalReviewer:      reviewer,
		ApprovalModelResolver: staticApprovalModelResolver{model: runtimeAgentTestModel{text: "review"}},
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	sessionResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &permissionCountingCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: sessionResp.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"clean workspace"}`)},
	}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if cb.permissions != 0 {
		t.Fatalf("client permission requests = %d, want 0 under auto-review", cb.permissions)
	}
	if reviewer.calls != 1 {
		t.Fatalf("reviewer calls = %d, want 1", reviewer.calls)
	}
	if !runtime.response.Approved || runtime.response.OptionID != acp.PermAllowOnce {
		t.Fatalf("approval response = %#v, want approved allow_once", runtime.response)
	}
	if reviewer.last.Model == nil {
		t.Fatal("reviewer request model = nil, want resolved session model")
	}
	if reviewer.last.Approval == nil || reviewer.last.Approval.ToolName != "RUN_COMMAND" {
		t.Fatalf("reviewer approval payload = %#v, want RUN_COMMAND payload", reviewer.last.Approval)
	}
	if got := reviewer.last.Approval.RawInput["command"]; got != "git restore hello.py" {
		t.Fatalf("reviewer approval raw command = %#v, want git restore hello.py", got)
	}
}

func TestRuntimeAgentPromptManualModeUsesClientPermission(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &approvalReviewRuntime{mode: "manual"}
	reviewer := &recordingApprovalReviewer{}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "chat", Metadata: map[string]any{"policy_mode": "workspace-write"}}, nil
		},
		AppName:               "caelis",
		UserID:                "user-1",
		Modes:                 staticApprovalModeProvider{current: "manual"},
		ApprovalReviewer:      reviewer,
		ApprovalModelResolver: staticApprovalModelResolver{model: runtimeAgentTestModel{text: "review"}},
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	sessionResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &permissionCountingCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: sessionResp.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"clean workspace"}`)},
	}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if cb.permissions != 1 {
		t.Fatalf("client permission requests = %d, want 1 under manual mode", cb.permissions)
	}
	if reviewer.calls != 0 {
		t.Fatalf("reviewer calls = %d, want 0 under manual mode", reviewer.calls)
	}
	if !runtime.response.Approved || runtime.response.OptionID != acp.PermAllowOnce {
		t.Fatalf("approval response = %#v, want approved allow_once", runtime.response)
	}
}

func TestRuntimeAgentPromptUsesDedicatedApprovalModes(t *testing.T) {
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	runtime := &approvalReviewRuntime{}
	reviewer := &recordingApprovalReviewer{}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "chat", Metadata: map[string]any{"policy_mode": "workspace-write"}}, nil
		},
		AppName:               "caelis",
		UserID:                "user-1",
		Modes:                 staticApprovalModeProvider{current: "plan"},
		ApprovalModes:         staticApprovalModeProvider{current: "manual"},
		ApprovalReviewer:      reviewer,
		ApprovalModelResolver: staticApprovalModelResolver{model: runtimeAgentTestModel{text: "review"}},
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	sessionResp, err := agent.NewSession(context.Background(), acp.NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if sessionResp.Modes == nil || sessionResp.Modes.CurrentModeID != "plan" {
		t.Fatalf("NewSession().Modes = %#v, want client-visible plan mode", sessionResp.Modes)
	}
	cb := &permissionCountingCallbacks{}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionID: sessionResp.SessionID,
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"clean workspace"}`)},
	}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if cb.permissions != 1 {
		t.Fatalf("client permission requests = %d, want 1 from dedicated manual approval mode", cb.permissions)
	}
	if reviewer.calls != 0 {
		t.Fatalf("reviewer calls = %d, want 0 when dedicated approval mode is manual", reviewer.calls)
	}
}

func newRuntimeAgentWithConfig(t *testing.T, override runtimeacp.Config) (*runtimeacp.RuntimeAgent, session.Service) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	return newRuntimeAgentWithSessionsAndConfig(t, sessions, override)
}

func newRuntimeAgentWithSessionsAndConfig(t *testing.T, sessions session.Service, override runtimeacp.Config) (*runtimeacp.RuntimeAgent, session.Service) {
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
	cfg := runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "chat", Model: runtimeAgentTestModel{text: "ok"}}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
		AgentInfo: &acp.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
		Loader:        override.Loader,
		Modes:         override.Modes,
		ApprovalModes: override.ApprovalModes,
		Config:        override.Config,
		Models:        override.Models,
		Commands:      override.Commands,
		PromptCaps:    override.PromptCaps,
	}
	if override.AgentInfo != nil {
		cfg.AgentInfo = override.AgentInfo
	}
	agent, err := runtimeacp.New(cfg)
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	return agent, sessions
}

type runtimeAgentTestModel struct{ text string }

func (m runtimeAgentTestModel) Name() string { return "stub" }

func (m runtimeAgentTestModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
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

type testModeProvider struct{}

func (testModeProvider) SessionModes(context.Context, session.Session) (*acp.SessionModeState, error) {
	return &acp.SessionModeState{
		AvailableModes: []acp.SessionMode{{ID: "default", Name: "Default"}},
		CurrentModeID:  "default",
	}, nil
}

func (testModeProvider) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

type staticApprovalModeProvider struct {
	current string
}

func (p staticApprovalModeProvider) SessionModes(context.Context, session.Session) (*acp.SessionModeState, error) {
	current := strings.TrimSpace(p.current)
	if current == "" {
		current = "auto-review"
	}
	return &acp.SessionModeState{
		AvailableModes: []acp.SessionMode{
			{ID: "auto-review", Name: "Auto Review"},
			{ID: "manual", Name: "Manual"},
		},
		CurrentModeID: current,
	}, nil
}

func (p staticApprovalModeProvider) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

type testConfigProvider struct{}

func (testConfigProvider) SessionConfigOptions(context.Context, session.Session) ([]acp.SessionConfigOption, error) {
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

type sideACPCommandProvider []acp.AvailableCommand

func (p sideACPCommandProvider) AvailableCommands(context.Context, string) ([]acp.AvailableCommand, error) {
	return append([]acp.AvailableCommand(nil), p...), nil
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

func firstAgentMessageChunk(notifications []acp.SessionNotification) string {
	for _, notification := range notifications {
		chunk, ok := notification.Update.(acp.ContentChunk)
		if !ok || chunk.SessionUpdate != acp.UpdateAgentMessage {
			continue
		}
		content, ok := chunk.Content.(acp.TextContent)
		if ok {
			return content.Text
		}
	}
	return ""
}

type sideACPCommandRuntime struct {
	sessions      session.Service
	expectedAgent string
	runCalled     bool
	attach        agent.AttachACPParticipantRequest
	prompt        agent.PromptACPParticipantRequest
}

func (r *sideACPCommandRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	r.runCalled = true
	return agent.RunResult{}, errors.New("main runtime should not run side ACP slash command")
}

func (r *sideACPCommandRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (r *sideACPCommandRuntime) AttachACPParticipant(ctx context.Context, req agent.AttachACPParticipantRequest) (session.Session, error) {
	r.attach = req
	if r.expectedAgent != "" && req.Agent != r.expectedAgent {
		return session.Session{}, fmt.Errorf("agent %q not found", req.Agent)
	}
	activeSession, err := r.sessions.Session(ctx, req.SessionRef)
	if err != nil {
		return session.Session{}, err
	}
	role := req.Role
	if role == "" {
		role = session.ParticipantRoleSidecar
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "@" + strings.TrimSpace(req.Agent)
	}
	return r.sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:        "participant-1",
			Kind:      session.ParticipantKindACP,
			Role:      role,
			AgentName: strings.TrimSpace(req.Agent),
			Label:     label,
			SessionID: "remote-helper",
			Source:    strings.TrimSpace(req.Source),
		},
	})
}

func (r *sideACPCommandRuntime) PromptACPParticipant(ctx context.Context, req agent.PromptACPParticipantRequest) (agent.RunResult, error) {
	r.prompt = req
	activeSession, err := r.sessions.Session(ctx, req.SessionRef)
	if err != nil {
		return agent.RunResult{}, err
	}
	msg := model.NewTextMessage(model.RoleAssistant, "side acp output")
	event := &session.Event{
		SessionID:  activeSession.SessionID,
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &msg,
		Text:       msg.TextContent(),
		Protocol: &session.EventProtocol{
			UpdateType: acp.UpdateAgentMessage,
		},
	}
	return agent.RunResult{Session: activeSession, Handle: sideACPCommandRun{event: event}}, nil
}

func (r *sideACPCommandRuntime) DetachACPParticipant(context.Context, agent.DetachACPParticipantRequest) (session.Session, error) {
	return session.Session{}, nil
}

func (r *sideACPCommandRuntime) HandoffController(context.Context, agent.HandoffControllerRequest) (session.Session, error) {
	return session.Session{}, errors.New("handoff not implemented")
}

type sideACPCommandRun struct {
	event *session.Event
}

func (r sideACPCommandRun) RunID() string { return "side-run-1" }

func (r sideACPCommandRun) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		yield(r.event, nil)
	}
}

func (r sideACPCommandRun) Submit(agent.Submission) error { return nil }
func (r sideACPCommandRun) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (r sideACPCommandRun) Close() error { return nil }

type permissionCountingCallbacks struct {
	recordingPromptCallbacks
	permissions int
}

func (c *permissionCountingCallbacks) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.permissions++
	return acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: acp.PermAllowOnce},
	}, nil
}

type approvalReviewRuntime struct {
	response agent.ApprovalResponse
	mode     string
}

func (r *approvalReviewRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	activeSession := session.Session{
		SessionRef: req.SessionRef,
	}
	mode := strings.TrimSpace(r.mode)
	if mode == "" {
		mode = "auto-review"
	}
	if req.ApprovalRequester != nil {
		resp, err := req.ApprovalRequester.RequestApproval(ctx, agent.ApprovalRequest{
			SessionRef: req.SessionRef,
			Session:    activeSession,
			RunID:      "run-1",
			TurnID:     "turn-1",
			Mode:       mode,
			Tool:       tool.Definition{Name: "RUN_COMMAND"},
			Call:       tool.Call{ID: "call-1", Name: "RUN_COMMAND"},
			Approval: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{
					ID:   "call-1",
					Name: "RUN_COMMAND",
					RawInput: map[string]any{
						"command": "git restore hello.py",
					},
				},
				Options: []session.ProtocolApprovalOption{
					{ID: acp.PermAllowOnce, Name: "Allow once", Kind: "allow_once"},
					{ID: acp.PermRejectOnce, Name: "Reject once", Kind: "reject_once"},
				},
			},
		})
		if err != nil {
			return agent.RunResult{}, err
		}
		r.response = resp
	}
	return agent.RunResult{Session: activeSession, Handle: terminalBridgeRun{}}, nil
}

func (*approvalReviewRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type recordingApprovalReviewer struct {
	calls int
	last  approval.ReviewRequest
}

func (r *recordingApprovalReviewer) ReviewApproval(_ context.Context, req approval.ReviewRequest) (approval.ReviewResult, error) {
	r.calls++
	r.last = req
	return approval.ReviewResult{
		Approved:      true,
		Risk:          "low",
		Authorization: "explicit",
		Rationale:     "command matches the user request",
	}, nil
}

type staticApprovalModelResolver struct {
	model model.LLM
}

func (r staticApprovalModelResolver) ResolveApprovalModel(context.Context, session.SessionRef) (model.LLM, error) {
	return r.model, nil
}

type terminalBridgeRuntime struct {
	closedState        string
	closedText         string
	toolName           string
	taskID             string
	terminalID         string
	omitStreamedOutput bool
}

func (r terminalBridgeRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	sessionID := req.SessionRef.SessionID
	toolName := strings.TrimSpace(r.toolName)
	if toolName == "" {
		toolName = "RUN_COMMAND"
	}
	taskID := strings.TrimSpace(r.taskID)
	if taskID == "" {
		taskID = "task-1"
	}
	terminalID := strings.TrimSpace(r.terminalID)
	if terminalID == "" {
		terminalID = "terminal-1"
	}
	rawInput := map[string]any{"command": "printf streamed"}
	if strings.EqualFold(toolName, "SPAWN") {
		rawInput = map[string]any{"agent": "claude", "prompt": "stream child output"}
	}
	return agent.RunResult{
		Handle: terminalBridgeRun{events: []*session.Event{
			{
				SessionID: sessionID,
				Type:      session.EventTypeToolCall,
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeToolCall),
					ToolCall: &session.ProtocolToolCall{
						ID:       "call-1",
						Name:     toolName,
						Status:   "pending",
						RawInput: rawInput,
					},
				},
			},
			{
				SessionID:  sessionID,
				Type:       session.EventTypeToolResult,
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
					ToolCall: &session.ProtocolToolCall{
						ID:      "call-1",
						Name:    toolName,
						Status:  "running",
						Content: []session.ProtocolToolCallContent{{Type: "terminal", TerminalID: terminalID}},
					},
				},
				Meta: map[string]any{
					"caelis": map[string]any{
						"runtime": map[string]any{
							"task": map[string]any{
								"task_id":     taskID,
								"terminal_id": terminalID,
								"running":     true,
							},
						},
					},
				},
			},
		}},
	}, nil
}

func (terminalBridgeRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (r terminalBridgeRuntime) Streams() stream.Service {
	return terminalBridgeStream{
		closedState:        r.closedState,
		closedText:         r.closedText,
		omitStreamedOutput: r.omitStreamedOutput,
	}
}

type terminalBridgeFinalRuntime struct{}

func (terminalBridgeFinalRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	sessionID := req.SessionRef.SessionID
	return agent.RunResult{
		Handle: terminalBridgeRun{events: []*session.Event{
			{
				SessionID: sessionID,
				Type:      session.EventTypeToolCall,
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeToolCall),
					ToolCall: &session.ProtocolToolCall{
						ID:       "call-1",
						Name:     "RUN_COMMAND",
						Status:   "pending",
						RawInput: map[string]any{"command": "printf streamed"},
					},
				},
			},
			{
				SessionID: sessionID,
				Type:      session.EventTypeToolResult,
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
					ToolCall: &session.ProtocolToolCall{
						ID:     "call-1",
						Name:   "RUN_COMMAND",
						Status: "completed",
						Content: []session.ProtocolToolCallContent{{
							Type:       "terminal",
							TerminalID: "terminal-1",
							Content:    session.ProtocolTextContent("streamed output\n"),
						}},
					},
				},
				Meta: map[string]any{
					"caelis": map[string]any{
						"runtime": map[string]any{
							"task": map[string]any{
								"task_id":     "task-1",
								"terminal_id": "terminal-1",
								"running":     false,
							},
						},
					},
				},
			},
		}},
	}, nil
}

func (terminalBridgeFinalRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (terminalBridgeFinalRuntime) Streams() stream.Service {
	return terminalBridgeStream{}
}

type narrativeReplayRuntime struct{}

func (narrativeReplayRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	sessionID := req.SessionRef.SessionID
	liveHello := model.NewTextMessage(model.RoleAssistant, "hello")
	liveHelloWorld := model.NewTextMessage(model.RoleAssistant, "hello world")
	finalHelloWorld := model.NewTextMessage(model.RoleAssistant, "hello world")
	return agent.RunResult{
		Session: session.Session{SessionRef: req.SessionRef},
		Handle: terminalBridgeRun{events: []*session.Event{
			{
				SessionID:  sessionID,
				Type:       session.EventTypeAssistant,
				Message:    &liveHello,
				Text:       "hello",
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				},
			},
			{
				SessionID:  sessionID,
				Type:       session.EventTypeAssistant,
				Message:    &liveHelloWorld,
				Text:       "hello world",
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				},
			},
			{
				SessionID: sessionID,
				Type:      session.EventTypeAssistant,
				Message:   &finalHelloWorld,
				Text:      "hello world",
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				},
			},
		}},
	}, nil
}

func (narrativeReplayRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type narrativeThoughtReplayRuntime struct{}

func (narrativeThoughtReplayRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	sessionID := req.SessionRef.SessionID
	liveTask := model.NewReasoningMessage(model.RoleAssistant, "任务已", model.ReasoningVisibilityVisible)
	liveStarted := model.NewReasoningMessage(model.RoleAssistant, "启动", model.ReasoningVisibilityVisible)
	finalStarted := model.NewReasoningMessage(model.RoleAssistant, "任务已启动", model.ReasoningVisibilityVisible)
	return agent.RunResult{
		Session: session.Session{SessionRef: req.SessionRef},
		Handle: terminalBridgeRun{events: []*session.Event{
			{
				SessionID:  sessionID,
				Type:       session.EventTypeAssistant,
				Message:    &liveTask,
				Text:       "任务已",
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeAgentThought),
				},
			},
			{
				SessionID:  sessionID,
				Type:       session.EventTypeAssistant,
				Message:    &liveStarted,
				Text:       "启动",
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeAgentThought),
				},
			},
			{
				SessionID: sessionID,
				Type:      session.EventTypeAssistant,
				Message:   &finalStarted,
				Text:      "任务已启动",
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeAgentThought),
				},
			},
		}},
	}, nil
}

func (narrativeThoughtReplayRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type terminalBridgeRun struct {
	events []*session.Event
}

func (r terminalBridgeRun) RunID() string { return "run-1" }

func (r terminalBridgeRun) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, event := range r.events {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (terminalBridgeRun) Submit(agent.Submission) error { return nil }
func (terminalBridgeRun) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (terminalBridgeRun) Close() error { return nil }

type terminalBridgeStream struct {
	closedState        string
	closedText         string
	omitStreamedOutput bool
}

func (s terminalBridgeStream) Read(context.Context, stream.ReadRequest) (stream.Snapshot, error) {
	state := strings.TrimSpace(s.closedState)
	if state == "" {
		state = "completed"
	}
	exitCode := 0
	if terminalFrameFailedForTest(state) {
		exitCode = 1
	}
	snap := stream.Snapshot{
		Running:  false,
		State:    state,
		ExitCode: &exitCode,
	}
	if !s.omitStreamedOutput {
		snap.Frames = append(snap.Frames, stream.Frame{Text: "streamed output\n"})
	}
	if s.closedText != "" {
		snap.FinalText = s.closedText
	}
	return snap, nil
}

func (s terminalBridgeStream) Subscribe(context.Context, stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(yield func(*stream.Frame, error) bool) {
		if !s.omitStreamedOutput {
			if !yield(&stream.Frame{Text: "streamed output\n"}, nil) {
				return
			}
		}
		state := strings.TrimSpace(s.closedState)
		if state == "" {
			state = "completed"
		}
		yield(&stream.Frame{Text: s.closedText, Closed: true, State: state}, nil)
	}
}

func terminalFrameFailedForTest(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "failed", "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout":
		return true
	default:
		return false
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
		if terminalOutputData(update.Meta, terminalID) == data {
			return true
		}
	}
	return false
}

func hasAnyTerminalOutput(notifications []acp.SessionNotification, terminalID string) bool {
	for _, notification := range notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok {
			continue
		}
		output, _ := update.Meta["terminal_output"].(map[string]any)
		if output != nil && output["terminal_id"] == terminalID {
			return true
		}
	}
	return false
}

func terminalOutputPayloads(notifications []acp.SessionNotification, terminalID string) []string {
	out := []string{}
	for _, notification := range notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok {
			continue
		}
		if text := terminalOutputData(update.Meta, terminalID); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func firstTerminalOutputIndex(notifications []acp.SessionNotification, terminalID string) int {
	for i, notification := range notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok {
			continue
		}
		if terminalOutputData(update.Meta, terminalID) != "" {
			return i
		}
	}
	return -1
}

func firstCompletedToolUpdateIndex(notifications []acp.SessionNotification, terminalID string) int {
	for i, notification := range notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok || strings.TrimSpace(update.ToolCallID) != terminalID || update.Status == nil {
			continue
		}
		if *update.Status == acp.ToolStatusCompleted {
			return i
		}
	}
	return -1
}

func hasTerminalExit(notifications []acp.SessionNotification, terminalID string, exitCode int) bool {
	return terminalExit(notifications, terminalID, "", exitCode, true) != nil
}

func hasCompletedTerminalExit(notifications []acp.SessionNotification, terminalID string, exitCode int) bool {
	return terminalExit(notifications, terminalID, acp.ToolStatusCompleted, exitCode, true) != nil
}

func hasFailedTerminalExit(notifications []acp.SessionNotification, terminalID string) bool {
	return terminalExit(notifications, terminalID, acp.ToolStatusFailed, 0, false) != nil
}

func terminalExit(notifications []acp.SessionNotification, terminalID string, status string, exitCode int, checkExitCode bool) *acp.ToolCallUpdate {
	for _, notification := range notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok {
			continue
		}
		if update.Status == nil {
			continue
		}
		if status != "" && *update.Status != status {
			continue
		}
		exit := terminalExitMeta(update.Meta, terminalID)
		if exit == nil {
			continue
		}
		if checkExitCode && metaInt(exit["exit_code"]) != exitCode {
			continue
		}
		if strings.TrimSpace(update.ToolCallID) == terminalID {
			return &update
		}
	}
	return nil
}

func terminalOutputData(meta map[string]any, terminalID string) string {
	output, _ := meta["terminal_output"].(map[string]any)
	if output == nil || output["terminal_id"] != terminalID {
		return ""
	}
	text, _ := output["data"].(string)
	return text
}

func terminalExitMeta(meta map[string]any, terminalID string) map[string]any {
	exit, _ := meta["terminal_exit"].(map[string]any)
	if exit == nil || exit["terminal_id"] != terminalID {
		return nil
	}
	return exit
}

func metaInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func agentMessageChunks(notifications []acp.SessionNotification) []string {
	out := make([]string, 0, len(notifications))
	for _, notification := range notifications {
		chunk, ok := notification.Update.(acp.ContentChunk)
		if !ok || chunk.SessionUpdate != acp.UpdateAgentMessage {
			continue
		}
		switch content := chunk.Content.(type) {
		case acp.TextContent:
			out = append(out, content.Text)
		case map[string]any:
			if text, _ := content["text"].(string); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func agentThoughtChunks(notifications []acp.SessionNotification) []string {
	out := make([]string, 0, len(notifications))
	for _, notification := range notifications {
		chunk, ok := notification.Update.(acp.ContentChunk)
		if !ok || chunk.SessionUpdate != acp.UpdateAgentThought {
			continue
		}
		switch content := chunk.Content.(type) {
		case acp.TextContent:
			out = append(out, content.Text)
		case map[string]any:
			if text, _ := content["text"].(string); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func terminalContentText(content []acp.ToolCallContent, terminalID string) string {
	for _, item := range content {
		if item.Type != "terminal" || item.TerminalID != terminalID {
			continue
		}
		text, ok := item.Content.(acp.TextContent)
		if !ok {
			continue
		}
		return text.Text
	}
	return ""
}
