package acpagentbridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	sdkchat "github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	bridgeassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	assemblyapi "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acp "github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestRuntimeAgentInitializeCapabilitiesDefault(t *testing.T) {
	agent, _ := newRuntimeAgentWithConfig(t, runtimeacp.Config{})
	resp, err := agent.Initialize(context.Background(), acpsdk.InitializeRequest{})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if !resp.AgentCapabilities.LoadSession {
		t.Fatal("LoadSession capability = false, want true by default")
	}
	capabilities := resp.AgentCapabilities.SessionCapabilities
	if capabilities.List == nil || capabilities.Resume == nil || capabilities.Close == nil {
		t.Fatalf("session capabilities = %#v, want list, resume, and close", capabilities)
	}
}

func TestRuntimeAgentInitializeFillsAgentInfoVersion(t *testing.T) {
	agent, _ := newRuntimeAgentWithConfig(t, runtimeacp.Config{
		AgentInfo: &acpsdk.Implementation{Name: "caelis"},
	})
	resp, err := agent.Initialize(context.Background(), acpsdk.InitializeRequest{})
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
	resp, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if resp.Modes == nil || resp.Modes.CurrentModeId != "default" {
		t.Fatalf("resp.Modes = %#v, want injected mode state", resp.Modes)
	}
	if got, want := len(resp.ConfigOptions), 1; got != want {
		t.Fatalf("len(resp.ConfigOptions) = %d, want %d", got, want)
	}
}

func TestRuntimeAgentNewSessionNormalizesManagedSubagentMetadata(t *testing.T) {
	t.Parallel()

	agent, sessions := newRuntimeAgentWithConfig(t, runtimeacp.Config{})
	meta := metautil.WithCompactRuntimeSection(map[string]any{
		"vendor": map[string]any{"untrusted": true},
	}, metautil.RuntimeSession, map[string]any{
		metautil.RuntimeSessionKind:     metautil.RuntimeSessionKindSubagent,
		metautil.RuntimeSessionParentID: "parent-session",
		metautil.RuntimeTaskID:          "task-1",
	})
	resp, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir(), Meta: testSDKRawMeta(t, meta)})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Session(context.Background(), session.SessionRef{SessionID: string(resp.SessionId)})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"system_managed_agent":             "subagent",
		"system_managed_parent_session_id": "parent-session",
		"system_managed_task_id":           "task-1",
	}
	if !reflect.DeepEqual(created.Metadata, want) {
		t.Fatalf("created Session metadata = %#v, want %#v", created.Metadata, want)
	}

	ordinary, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{
		Cwd:  t.TempDir(),
		Meta: testSDKRawMeta(t, map[string]any{"system_managed_agent": "guardian"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ordinarySession, err := sessions.Session(context.Background(), session.SessionRef{SessionID: string(ordinary.SessionId)})
	if err != nil {
		t.Fatal(err)
	}
	if len(ordinarySession.Metadata) != 0 {
		t.Fatalf("ordinary Session metadata = %#v, want no arbitrary ACP metadata", ordinarySession.Metadata)
	}
}

func TestRuntimeAgentManagedLoadAndResumeIgnoreMetaAndRequireTrustedOwnership(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewStore(inmemory.Config{})
	agent, _ := newRuntimeAgentWithSessionsAndConfig(t, sessions, runtimeacp.Config{})
	created, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{
		Cwd: t.TempDir(),
		Meta: testSDKRawMeta(t, metautil.WithCompactRuntimeSection(nil, metautil.RuntimeSession, map[string]any{
			metautil.RuntimeSessionKind:     metautil.RuntimeSessionKindSubagent,
			metautil.RuntimeSessionParentID: "parent-session",
			metautil.RuntimeTaskID:          "task-1",
		})),
	})
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := sessions.Session(context.Background(), session.SessionRef{SessionID: string(created.SessionId)})
	if err != nil {
		t.Fatal(err)
	}
	claim := metautil.WithCompactRuntimeSection(nil, metautil.RuntimeSession, map[string]any{
		metautil.RuntimeSessionKind:     metautil.RuntimeSessionKindSubagent,
		metautil.RuntimeSessionParentID: "parent-session",
		metautil.RuntimeTaskID:          "task-1",
	})
	if _, err := agent.LoadSession(context.Background(), acpsdk.LoadSessionRequest{
		SessionId: created.SessionId, Cwd: loaded.CWD,
	}, &recordingPromptCallbacks{}); err != nil {
		t.Fatalf("LoadSession(owned, no metadata) error = %v", err)
	}
	if _, err := agent.ResumeSession(context.Background(), acpsdk.ResumeSessionRequest{
		SessionId: created.SessionId, Cwd: loaded.CWD,
	}); err != nil {
		t.Fatalf("ResumeSession(owned, no metadata) error = %v", err)
	}

	for _, untrustedMeta := range []map[string]any{nil, claim} {
		isolated, _ := newRuntimeAgentWithSessionsAndConfig(t, sessions, runtimeacp.Config{})
		if _, err := isolated.LoadSession(context.Background(), acpsdk.LoadSessionRequest{
			SessionId: created.SessionId, Cwd: loaded.CWD, Meta: testSDKRawMeta(t, untrustedMeta),
		}, &recordingPromptCallbacks{}); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("LoadSession(unowned, metadata=%#v) error = %v, want Session not found", untrustedMeta, err)
		}
		if _, err := isolated.ResumeSession(context.Background(), acpsdk.ResumeSessionRequest{
			SessionId: created.SessionId, Cwd: loaded.CWD, Meta: testSDKRawMeta(t, untrustedMeta),
		}); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("ResumeSession(unowned, metadata=%#v) error = %v, want Session not found", untrustedMeta, err)
		}
	}
}

func TestRuntimeAgentNewSessionIncludesAssemblyModesAndConfig(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	providers := bridgeassembly.ProvidersFromAssembly(bridgeassembly.ProviderConfig{
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
		Modes:  providers.Modes,
		Config: providers.Config,
	})
	resp, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{
		Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if resp.Modes == nil || resp.Modes.CurrentModeId != "default" {
		t.Fatalf("resp.Modes = %#v, want assembly-backed mode state", resp.Modes)
	}
	if got, want := len(resp.ConfigOptions), 1; got != want {
		t.Fatalf("len(resp.ConfigOptions) = %d, want %d", got, want)
	}
	if got := string(resp.ConfigOptions[0].Select.CurrentValue); got != "balanced" {
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
			Meta: map[string]any{
				"usage": map[string]any{
					"prompt_tokens":     11,
					"completion_tokens": 6,
					"total_tokens":      17,
				},
			},
			Invocation: &session.EventInvocation{Provider: "deepseek", Model: "deepseek-v4-flash", ContextWindowTokens: 128000},
			Protocol: &session.EventProtocol{
				Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage)},
			},
		},
	}); err != nil {
		t.Fatalf("AppendEvent(assistant) error = %v", err)
	}

	cb := &recordingPromptCallbacks{}
	resp, err := agent.LoadSession(ctx, acpsdk.LoadSessionRequest{
		SessionId: acpsdk.SessionId(activeSession.SessionID),
		Cwd:       activeSession.CWD,
	}, cb)
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if resp.Modes != nil || len(resp.ConfigOptions) != 0 {
		t.Fatalf("LoadSession() returned unexpected optional metadata: %#v", resp)
	}
	if got, want := len(cb.notifications), 3; got != want {
		t.Fatalf("len(notifications) = %d, want %d", got, want)
	}
	if got := cb.notifications[0].Update.SessionUpdateType(); got != acp.UpdateUserMessage {
		t.Fatalf("first replay update = %q, want %q", got, acp.UpdateUserMessage)
	}
	if got := cb.notifications[1].Update.SessionUpdateType(); got != acp.UpdateAgentMessage {
		t.Fatalf("second replay update = %q, want %q", got, acp.UpdateAgentMessage)
	}
	if got := cb.notifications[2].Update.SessionUpdateType(); got != acp.UpdateUsage {
		t.Fatalf("third replay update = %q, want usage_update", got)
	}
	usage, ok := cb.notifications[2].Update.(acp.UsageUpdate)
	if !ok || usage.Used != 17 || usage.Size != 128000 {
		t.Fatalf("usage replay update = %#v, want usage_update size=128000 used=17", cb.notifications[2].Update)
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
	_, err = sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "/tmp/acp-list",
			CWD: "/tmp/acp-list",
		},
		Title: "Managed child",
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent: "subagent",
		},
	})
	if err != nil {
		t.Fatalf("StartSession(managed) error = %v", err)
	}

	list, err := agent.ListSessions(ctx, acpsdk.ListSessionsRequest{Cwd: testStringPointer("/tmp/acp-list")})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(list.Sessions) != 1 || string(list.Sessions[0].SessionId) != activeSession.SessionID || testOptionalStringValue(list.Sessions[0].Title) != "Listed session" {
		t.Fatalf("ListSessions() = %#v, want listed session", list)
	}

	resumed, err := agent.ResumeSession(ctx, acpsdk.ResumeSessionRequest{SessionId: acpsdk.SessionId(activeSession.SessionID), Cwd: activeSession.CWD})
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if resumed.Modes == nil || resumed.Modes.CurrentModeId != "default" {
		t.Fatalf("ResumeSession().Modes = %#v, want default mode", resumed.Modes)
	}
	if got, want := len(resumed.ConfigOptions), 1; got != want {
		t.Fatalf("len(ResumeSession().ConfigOptions) = %d, want %d", got, want)
	}

	if _, err := agent.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: acpsdk.SessionId(activeSession.SessionID)}); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
}

func TestRuntimeAgentPromptPassesResolvedWorkspaceRefToMainRuntime(t *testing.T) {
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
	runtime := &recordingRunRuntime{}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:      runtime,
		Sessions:     sessions,
		WorkspaceKey: "ws-a",
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "chat"}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	resp, err := agent.Prompt(ctx, runtimeacp.PromptInput{
		SessionID: "shared-session",
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"hello"}`),
		},
	}, &recordingPromptCallbacks{})
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acpsdk.StopReasonEndTurn)
	}
	if got := runtime.request.SessionRef.WorkspaceKey; got != "ws-b" {
		t.Fatalf("runtime SessionRef.WorkspaceKey = %q, want ws-b", got)
	}
	if got := runtime.request.SessionRef.SessionID; got != "shared-session" {
		t.Fatalf("runtime SessionRef.SessionID = %q, want shared-session", got)
	}
}

func TestRuntimeAgentResumeSessionIgnoresCWDForIdentity(t *testing.T) {
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
	modes := &recordingModeProvider{}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:      &recordingRunRuntime{},
		Sessions:     sessions,
		Modes:        modes,
		WorkspaceKey: "ws-a",
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "chat"}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	if _, err := agent.ResumeSession(ctx, acpsdk.ResumeSessionRequest{
		SessionId: "shared-session",
		Cwd:       "ws-a",
	}); err != nil {
		t.Fatalf("ResumeSession(ws-a) error = %v", err)
	}
	if _, err := agent.ResumeSession(ctx, acpsdk.ResumeSessionRequest{
		SessionId: "shared-session",
		Cwd:       "ws-b",
	}); err != nil {
		t.Fatalf("ResumeSession(ws-b) error = %v", err)
	}
	if got, want := len(modes.sessions), 2; got != want {
		t.Fatalf("recorded mode sessions = %d, want %d", got, want)
	}
	if got := modes.sessions[0].WorkspaceKey; got != "ws-b" {
		t.Fatalf("first ResumeSession workspace = %q, want ws-b", got)
	}
	if got := modes.sessions[1].WorkspaceKey; got != "ws-b" {
		t.Fatalf("second ResumeSession workspace = %q, want ws-b", got)
	}
}

func TestRuntimeAgentUnscopedPromptUsesGlobalSessionIDAfterResumeWithDifferentCWD(t *testing.T) {
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
	runtime := &recordingRunRuntime{}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "chat"}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	for _, workspaceKey := range []string{"ws-a", "ws-b", "ws-b"} {
		if _, err := agent.ResumeSession(ctx, acpsdk.ResumeSessionRequest{
			SessionId: "shared-session",
			Cwd:       workspaceKey,
		}); err != nil {
			t.Fatalf("ResumeSession(%s) error = %v", workspaceKey, err)
		}
	}
	resp, err := agent.Prompt(ctx, runtimeacp.PromptInput{
		SessionID: "shared-session",
		Prompt: []json.RawMessage{
			json.RawMessage(`{"type":"text","text":"hello"}`),
		},
	}, &recordingPromptCallbacks{})
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, acpsdk.StopReasonEndTurn)
	}
	if got := runtime.request.SessionRef.WorkspaceKey; got != "ws-b" {
		t.Fatalf("runtime SessionRef.WorkspaceKey = %q, want ws-b", got)
	}
}

func TestRuntimeAgentPromptConvertsLocalTerminalTextToTerminalMetaForACPStdio(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  terminalBridgeRuntime{includeFinalEvent: true},
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
		AgentInfo: &acpsdk.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	if _, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{SessionID: string(activeSession.SessionId)}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if !hasTerminalInfo(cb.notifications, "call-1", "call-1") {
		t.Fatalf("notifications = %#v, want local terminal info for ACP stdio", cb.notifications)
	}
	if got := terminalOutputPayloads(cb.notifications, "call-1"); strings.Join(got, "") != "streamed output\n" {
		t.Fatalf("terminal output payloads = %#v, want terminal output meta", got)
	}
}

func TestRuntimeAgentPromptContinuesAfterObservationGap(t *testing.T) {
	t.Parallel()

	sessions := inmemory.NewStore(inmemory.Config{})
	bridge, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  observationGapRuntime{},
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	active, err := bridge.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	callbacks := &recordingPromptCallbacks{}
	response, err := bridge.Prompt(context.Background(), runtimeacp.PromptInput{SessionID: string(active.SessionId)}, callbacks)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if response.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want %q", response.StopReason, acpsdk.StopReasonEndTurn)
	}
	chunks := agentMessageChunks(callbacks.notifications)
	if len(chunks) != 2 || chunks[0] != acpbridge.RuntimeObservationGapNotice || chunks[1] != "durable final" {
		t.Fatalf("agent messages = %#v, want gap notice then final message", chunks)
	}
	gapUpdate, ok := callbacks.notifications[0].Update.(acp.ContentChunk)
	if !ok || strings.TrimSpace(gapUpdate.MessageID) == "" {
		t.Fatalf("gap update = %#v, want independently keyed ACP notice", callbacks.notifications[0].Update)
	}
	observation := metautil.RuntimeSection(gapUpdate.Meta, metautil.RuntimeObservation)
	if observation[metautil.RuntimeObservationCode] != metautil.RuntimeObservationGap ||
		observation[metautil.RuntimeObservationDropped] != uint64(17) {
		t.Fatalf("gap metadata = %#v", observation)
	}
}

func TestRuntimeAgentPromptForwardsNarrativeChunksWithoutContentRewriting(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  narrativeReplayRuntime{},
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
		AgentInfo: &acpsdk.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	if _, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{SessionID: string(activeSession.SessionId)}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	got := agentMessageChunks(cb.notifications)
	want := []string{"hello", "hello world"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("assistant chunks = %#v, want %#v", got, want)
	}
	ids := agentMessageIDs(cb.notifications)
	if len(ids) != 2 || ids[0] != "shared-message-1" || ids[0] != ids[1] {
		t.Fatalf("assistant messageIds = %#v, want shared-message-1 for every stream chunk", ids)
	}
}

func TestRuntimeAgentPromptOmitsOwnedFinalReasoningMaterialization(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  narrativeThoughtReplayRuntime{},
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
		AgentInfo: &acpsdk.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	if _, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{SessionID: string(activeSession.SessionId)}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	got := agentThoughtChunks(cb.notifications)
	want := []string{"任务已", "启动"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("thought chunks = %#v, want final replay suppressed with %#v", got, want)
	}
}

func TestRuntimeAgentPromptOmitsOwnedNarrativeFinalAcrossToolBoundary(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  narrativeToolBoundaryReplayRuntime{},
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "fake"}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
		AgentInfo: &acpsdk.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
	})
	if err != nil {
		t.Fatalf("runtimeacp.New() error = %v", err)
	}
	activeSession, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &recordingPromptCallbacks{}
	if _, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{SessionID: string(activeSession.SessionId)}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if got, want := agentThoughtChunks(cb.notifications), []string{"thinking"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("thought chunks = %#v, want %#v", got, want)
	}
	if got, want := agentMessageChunks(cb.notifications), []string{"hello"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("assistant chunks = %#v, want final replay across tool boundary suppressed with %#v", got, want)
	}
	thoughtIDs := agentThoughtIDs(cb.notifications)
	if len(thoughtIDs) != 1 || thoughtIDs[0] != "pre-tool-message" {
		t.Fatalf("thought messageIds = %#v, want shared pre-tool identity", thoughtIDs)
	}
	ids := agentMessageIDs(cb.notifications)
	if len(ids) != 1 || ids[0] != "pre-tool-message" {
		t.Fatalf("assistant messageIds = %#v, want one stable pre-tool identity", ids)
	}
}

func TestRuntimeAgentOptionalMethodsUnsupportedByDefault(t *testing.T) {
	agent, _ := newRuntimeAgentWithConfig(t, runtimeacp.Config{})
	if _, err := agent.SetSessionMode(context.Background(), acpsdk.SetSessionModeRequest{}); !errors.Is(err, runtimeacp.ErrCapabilityUnsupported) {
		t.Fatalf("SetSessionMode() error = %v, want ErrCapabilityUnsupported", err)
	}
	if _, err := agent.SetSessionConfigOption(context.Background(), acpsdk.SetSessionConfigOptionRequest{}); !errors.Is(err, runtimeacp.ErrCapabilityUnsupported) {
		t.Fatalf("SetSessionConfigOption() error = %v, want ErrCapabilityUnsupported", err)
	}
}

func TestRuntimeAgentPromptAutoReviewUsesReviewerInsteadOfClientPermission(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &approvalReviewRuntime{}
	reviewer := &recordingApprovalReviewer{}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
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
	sessionResp, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &permissionCountingCallbacks{}
	if _, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(sessionResp.SessionId),
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
	if !runtime.response.Approved || runtime.response.OptionID != string(acpsdk.PermissionOptionKindAllowOnce) {
		t.Fatalf("approval response = %#v, want approved allow_once", runtime.response)
	}
	if reviewer.last.Model == nil {
		t.Fatal("reviewer request model = nil, want resolved session model")
	}
	if reviewer.last.Approval == nil || reviewer.last.Approval.ToolName != "RunCommand" {
		t.Fatalf("reviewer approval payload = %#v, want RUN_COMMAND payload", reviewer.last.Approval)
	}
	if got := reviewer.last.Approval.RawInput["command"]; got != "git restore hello.py" {
		t.Fatalf("reviewer approval raw command = %#v, want git restore hello.py", got)
	}
}

func TestRuntimeAgentPromptAutoReviewNormalizesTextAfterSelectedOption(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &approvalReviewRuntime{}
	reviewResult := approval.ReviewResult{
		Approved:      true,
		Outcome:       string(approval.StatusApproved),
		OptionID:      string(acpsdk.PermissionOptionKindRejectOnce),
		Risk:          "low",
		Authorization: "explicit",
		Rationale:     "model selected reject option",
	}
	reviewer := &recordingApprovalReviewer{
		result: &reviewResult,
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
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
	sessionResp, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &permissionCountingCallbacks{}
	if _, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(sessionResp.SessionId),
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"clean workspace"}`)},
	}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	if runtime.response.Approved || runtime.response.OptionID != string(acpsdk.PermissionOptionKindRejectOnce) {
		t.Fatalf("approval response = %#v, want selected reject_once denial", runtime.response)
	}
	if !strings.Contains(runtime.response.ReviewText, "denied") {
		t.Fatalf("ReviewText = %q, want normalized denial text", runtime.response.ReviewText)
	}
}

func TestRuntimeAgentPromptManualModeUsesClientPermission(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &approvalReviewRuntime{mode: "manual"}
	reviewer := &recordingApprovalReviewer{}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
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
	sessionResp, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	cb := &permissionCountingCallbacks{}
	if _, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(sessionResp.SessionId),
		Prompt:    []json.RawMessage{json.RawMessage(`{"type":"text","text":"clean workspace"}`)},
	}, cb); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if cb.permissions != 1 {
		t.Fatalf("client permission requests = %d, want 1 under manual mode", cb.permissions)
	}
	if cb.last.SessionId != sessionResp.SessionId || cb.last.ToolCall.ToolCallId != "call-1" {
		t.Fatalf("client permission request = %#v, want normalized session and call identity", cb.last)
	}
	var toolMeta map[string]any
	rawToolMeta, err := json.Marshal(cb.last.ToolCall.Meta)
	if err != nil {
		t.Fatalf("marshal client permission tool metadata: %v", err)
	}
	if err := json.Unmarshal(rawToolMeta, &toolMeta); err != nil {
		t.Fatalf("decode client permission tool metadata: %v", err)
	}
	if got := metautil.String(toolMeta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName); got != "RunCommand" {
		t.Fatalf("client permission tool name = %q, want RUN_COMMAND", got)
	}
	if got := acp.NormalizeRawMap(cb.last.ToolCall.RawInput)["command"]; got != "git restore hello.py" {
		t.Fatalf("client permission raw command = %#v, want git restore hello.py", got)
	}
	if reviewer.calls != 0 {
		t.Fatalf("reviewer calls = %d, want 0 under manual mode", reviewer.calls)
	}
	if !runtime.response.Approved || runtime.response.OptionID != string(acpsdk.PermissionOptionKindAllowOnce) {
		t.Fatalf("approval response = %#v, want approved allow_once", runtime.response)
	}
}

func TestRuntimeAgentPromptUsesDedicatedApprovalModes(t *testing.T) {
	sessions := inmemory.NewStore(inmemory.Config{})
	runtime := &approvalReviewRuntime{}
	reviewer := &recordingApprovalReviewer{}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
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
	sessionResp, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if sessionResp.Modes == nil || sessionResp.Modes.CurrentModeId != "plan" {
		t.Fatalf("NewSession().Modes = %#v, want client-visible plan mode", sessionResp.Modes)
	}
	cb := &permissionCountingCallbacks{}
	if _, err := agent.Prompt(context.Background(), runtimeacp.PromptInput{
		SessionID: string(sessionResp.SessionId),
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
	sessions := inmemory.NewStore(inmemory.Config{})
	return newRuntimeAgentWithSessionsAndConfig(t, sessions, override)
}

func newRuntimeAgentWithSessionsAndConfig(t *testing.T, sessions session.Service, override runtimeacp.Config) (*runtimeacp.RuntimeAgent, session.Service) {
	t.Helper()
	runtime, err := sdkruntime.New(sdkruntime.Config{
		Sessions: sessions,
		AgentFactory: sdkchat.Factory{
			SystemPrompt: "Answer tersely.",
		},
	})
	if err != nil {
		t.Fatalf("sdkruntime.New() error = %v", err)
	}
	cfg := runtimeacp.Config{
		Runtime:  runtime,
		Sessions: sessions,
		BuildAgentSpec: func(context.Context, session.Session, runtimeacp.PromptInput) (agent.AgentSpec, error) {
			return agent.AgentSpec{Name: "chat", Model: runtimeAgentTestModel{text: "ok"}}, nil
		},
		AppName: "caelis",
		UserID:  "user-1",
		AgentInfo: &acpsdk.Implementation{
			Name:    "caelis-sdk",
			Version: "0.1.0",
		},
		Loader:               override.Loader,
		Modes:                override.Modes,
		ApprovalModes:        override.ApprovalModes,
		Config:               override.Config,
		Commands:             override.Commands,
		PromptRouterFactory:  override.PromptRouterFactory,
		SlashResultFormatter: override.SlashResultFormatter,
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

func (testModeProvider) SessionModes(context.Context, session.Session) (*acpsdk.SessionModeState, error) {
	return &acpsdk.SessionModeState{
		AvailableModes: []acpsdk.SessionMode{{Id: "default", Name: "Default"}},
		CurrentModeId:  "default",
	}, nil
}

func (testModeProvider) SetSessionMode(context.Context, acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, nil
}

type recordingModeProvider struct {
	sessions []session.Session
}

func (p *recordingModeProvider) SessionModes(_ context.Context, activeSession session.Session) (*acpsdk.SessionModeState, error) {
	p.sessions = append(p.sessions, session.CloneSession(activeSession))
	return &acpsdk.SessionModeState{
		AvailableModes: []acpsdk.SessionMode{{Id: "default", Name: "Default"}},
		CurrentModeId:  "default",
	}, nil
}

func (p *recordingModeProvider) SetSessionMode(context.Context, acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, nil
}

type staticApprovalModeProvider struct {
	current string
}

func (p staticApprovalModeProvider) SessionModes(context.Context, session.Session) (*acpsdk.SessionModeState, error) {
	current := strings.TrimSpace(p.current)
	if current == "" {
		current = "auto-review"
	}
	return &acpsdk.SessionModeState{
		AvailableModes: []acpsdk.SessionMode{
			{Id: "auto-review", Name: "Auto Review"},
			{Id: "manual", Name: "Manual"},
		},
		CurrentModeId: acpsdk.SessionModeId(current),
	}, nil
}

func (p staticApprovalModeProvider) SetSessionMode(context.Context, acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, nil
}

type testConfigProvider struct{}

func (testConfigProvider) SessionConfigOptions(context.Context, session.Session) ([]acpsdk.SessionConfigOption, error) {
	options := acpsdk.SessionConfigSelectOptionsUngrouped{{
		Value: "default",
		Name:  "Default",
	}}
	return []acpsdk.SessionConfigOption{{Select: &acpsdk.SessionConfigOptionSelect{
		Type:         "select",
		Id:           "mode",
		Name:         "Mode",
		CurrentValue: "default",
		Options:      acpsdk.SessionConfigSelectOptions{Ungrouped: &options},
	}}}, nil
}

func (testConfigProvider) SetSessionConfigOption(context.Context, acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	return acpsdk.SetSessionConfigOptionResponse{}, nil
}

type availableCommandProvider []acpsdk.AvailableCommand

func testSDKRawMeta(t *testing.T, meta map[string]any) map[string]json.RawMessage {
	t.Helper()
	if len(meta) == 0 {
		return nil
	}
	result := make(map[string]json.RawMessage, len(meta))
	for key, value := range meta {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal metadata %q: %v", key, err)
		}
		result[key] = raw
	}
	return result
}

func (p availableCommandProvider) AvailableCommands(context.Context, string) ([]acpsdk.AvailableCommand, error) {
	return append([]acpsdk.AvailableCommand(nil), p...), nil
}

type testControlTurn struct {
	events chan eventstream.Envelope
	closed bool
}

func newTestControlTurn(events ...eventstream.Envelope) *testControlTurn {
	ch := make(chan eventstream.Envelope, len(events))
	for _, env := range events {
		ch <- env
	}
	close(ch)
	return &testControlTurn{events: ch}
}

func (t *testControlTurn) HandleID() string { return "handle-1" }
func (t *testControlTurn) RunID() string    { return "run-1" }
func (t *testControlTurn) TurnID() string   { return "turn-1" }

func (t *testControlTurn) Events() <-chan eventstream.Envelope {
	return t.events
}

func (t *testControlTurn) SubmitApproval(context.Context, controlprompt.ApprovalDecision) error {
	return nil
}

func (t *testControlTurn) Cancel() {}

func (t *testControlTurn) Close() error {
	t.closed = true
	return nil
}

type recordingPromptCallbacks struct {
	mu            sync.Mutex
	notifications []acp.SessionNotification
}

func (c *recordingPromptCallbacks) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifications = append(c.notifications, notification)
	return nil
}

func (c *recordingPromptCallbacks) RequestPermission(context.Context, acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	return acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.NewRequestPermissionOutcomeSelected(acpsdk.PermissionOptionId(acpsdk.PermissionOptionKindAllowOnce)),
	}, nil
}

type errorOnAgentMessageCallbacks struct {
	err error
}

func (c *errorOnAgentMessageCallbacks) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	if chunk, ok := notification.Update.(acp.ContentChunk); ok && chunk.SessionUpdate == acp.UpdateAgentMessage {
		return c.err
	}
	return nil
}

func (c *errorOnAgentMessageCallbacks) RequestPermission(context.Context, acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	return acpsdk.RequestPermissionResponse{}, nil
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

type promptRouterRuntime struct {
	sessions      session.Service
	expectedAgent string
	runCalled     bool
	attach        agent.AttachParticipantRequest
	prompt        agent.PromptParticipantRequest
}

type recordingRunRuntime struct {
	request agent.RunRequest
}

type observationGapRuntime struct{}

func (observationGapRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	message := model.NewTextMessage(model.RoleAssistant, "durable final")
	return agent.RunResult{
		Session: session.Session{SessionRef: req.SessionRef},
		Handle: terminalBridgeRun{
			gapDropped: 17,
			events: []*session.Event{{
				SessionID: req.SessionRef.SessionID,
				Type:      session.EventTypeAssistant,
				Message:   &message,
				Text:      message.TextContent(),
			}},
		},
	}, nil
}

func (observationGapRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (r *recordingRunRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.request = req
	return agent.RunResult{
		Session: session.Session{SessionRef: req.SessionRef},
		Handle:  terminalBridgeRun{},
	}, nil
}

func (r *recordingRunRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (r *promptRouterRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	r.runCalled = true
	return agent.RunResult{}, errors.New("main runtime should not run side ACP slash command")
}

func (r *promptRouterRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (r *promptRouterRuntime) AttachParticipant(ctx context.Context, req agent.AttachParticipantRequest) (session.Session, error) {
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

func (r *promptRouterRuntime) PromptParticipant(ctx context.Context, req agent.PromptParticipantRequest) (agent.RunResult, error) {
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
			Update: &session.ProtocolUpdate{SessionUpdate: acp.UpdateAgentMessage},
		},
	}
	return agent.RunResult{Session: activeSession, Handle: promptRouterRun{event: event}}, nil
}

func (r *promptRouterRuntime) DetachParticipant(context.Context, agent.DetachParticipantRequest) (session.Session, error) {
	return session.Session{}, nil
}

func (r *promptRouterRuntime) HandoffController(context.Context, agent.HandoffControllerRequest) (session.Session, error) {
	return session.Session{}, errors.New("handoff not implemented")
}

type promptRouterRun struct {
	event *session.Event
}

func (r promptRouterRun) RunID() string { return "prompt-router-run-1" }

func (r promptRouterRun) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		yield(r.event, nil)
	}
}

func (r promptRouterRun) Submit(agent.Submission) error { return nil }
func (r promptRouterRun) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (r promptRouterRun) Close() error { return nil }

type permissionCountingCallbacks struct {
	recordingPromptCallbacks
	permissions int
	last        acpsdk.RequestPermissionRequest
}

func (c *permissionCountingCallbacks) RequestPermission(_ context.Context, req acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	c.permissions++
	c.last = req
	return acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.NewRequestPermissionOutcomeSelected(acpsdk.PermissionOptionId(acpsdk.PermissionOptionKindAllowOnce)),
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
	if req.ApprovalRequester != nil {
		resp, err := req.ApprovalRequester.RequestApproval(ctx, agent.ApprovalRequest{
			SessionRef: req.SessionRef,
			Session:    activeSession,
			RunID:      "run-1",
			TurnID:     "turn-1",
			Tool:       tool.Definition{Name: "RunCommand"},
			Call:       tool.Call{ID: "call-1", Name: "RunCommand"},
			Approval: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{
					ID:   "call-1",
					Name: "RunCommand",
					RawInput: map[string]any{
						"command": "git restore hello.py",
					},
				},
				Options: []session.ProtocolApprovalOption{
					{ID: string(acpsdk.PermissionOptionKindAllowOnce), Name: "Allow once", Kind: string(acpsdk.PermissionOptionKindAllowOnce)},
					{ID: string(acpsdk.PermissionOptionKindRejectOnce), Name: "Reject once", Kind: string(acpsdk.PermissionOptionKindRejectOnce)},
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
	calls  int
	last   approval.ReviewRequest
	result *approval.ReviewResult
}

func (r *recordingApprovalReviewer) ReviewApproval(_ context.Context, req approval.ReviewRequest) (approval.ReviewResult, error) {
	r.calls++
	r.last = req
	if r.result != nil {
		return *r.result, nil
	}
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
	includeFinalEvent  bool
}

func (r terminalBridgeRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	sessionID := req.SessionRef.SessionID
	toolName := strings.TrimSpace(r.toolName)
	if toolName == "" {
		toolName = "RunCommand"
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
	if strings.EqualFold(toolName, "Spawn") {
		rawInput = map[string]any{"agent": "claude", "prompt": "stream child output"}
	}
	events := []*session.Event{
		{
			SessionID: sessionID,
			Type:      session.EventTypeToolCall,
			Protocol: &session.EventProtocol{
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
					ToolCallID:    "call-1",
					Kind:          toolName,
					Status:        "pending",
					RawInput:      rawInput,
				},
			},
		},
		{
			SessionID:  sessionID,
			Type:       session.EventTypeToolResult,
			Visibility: session.VisibilityUIOnly,
			Protocol: &session.EventProtocol{
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
					ToolCallID:    "call-1",
					Kind:          toolName,
					Status:        "running",
					Content:       []session.ProtocolToolCallContent{{Type: "terminal", TerminalID: terminalID}},
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
	}
	if r.includeFinalEvent {
		events = append(events, &session.Event{
			SessionID: sessionID,
			Type:      session.EventTypeToolResult,
			Protocol: &session.EventProtocol{
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
					ToolCallID:    "call-1",
					Kind:          toolName,
					Status:        "completed",
					Content: []session.ProtocolToolCallContent{{
						Type:       "terminal",
						TerminalID: terminalID,
						Content:    session.ProtocolTextContent("streamed output\n"),
					}},
				},
			},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"task": map[string]any{
							"task_id":     taskID,
							"terminal_id": terminalID,
							"running":     false,
						},
					},
				},
			},
		})
	}
	return agent.RunResult{Handle: terminalBridgeRun{events: events}}, nil
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

type terminalBridgeFinalRuntime struct {
	toolName   string
	taskID     string
	terminalID string
}

func (r terminalBridgeFinalRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	sessionID := req.SessionRef.SessionID
	toolName := strings.TrimSpace(r.toolName)
	if toolName == "" {
		toolName = "RunCommand"
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
	if strings.EqualFold(toolName, "Spawn") {
		rawInput = map[string]any{"agent": "claude", "prompt": "stream child output"}
	}
	return agent.RunResult{
		Handle: terminalBridgeRun{events: []*session.Event{
			{
				SessionID: sessionID,
				Type:      session.EventTypeToolCall,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
						ToolCallID:    "call-1",
						Kind:          toolName,
						Status:        "pending",
						RawInput:      rawInput,
					},
				},
			},
			{
				SessionID: sessionID,
				Type:      session.EventTypeToolResult,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
						ToolCallID:    "call-1",
						Kind:          toolName,
						Status:        "completed",
						Content: []session.ProtocolToolCallContent{{
							Type:       "terminal",
							TerminalID: terminalID,
							Content:    session.ProtocolTextContent("streamed output\n"),
						}},
					},
				},
				Meta: map[string]any{
					"caelis": map[string]any{
						"runtime": map[string]any{
							"task": map[string]any{
								"task_id":     taskID,
								"terminal_id": terminalID,
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
	const messageID = "shared-message-1"
	liveHello := model.NewTextMessage(model.RoleAssistant, "hello")
	liveHelloWorld := model.NewTextMessage(model.RoleAssistant, "hello world")
	finalHelloWorld := model.NewTextMessage(model.RoleAssistant, "hello world")
	return agent.RunResult{
		Session: session.Session{SessionRef: req.SessionRef},
		Handle: terminalBridgeRun{events: []*session.Event{
			{
				SessionID:  sessionID,
				Type:       session.EventTypeAssistant,
				MessageID:  messageID,
				Message:    &liveHello,
				Text:       "hello",
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
						MessageID:     messageID,
					},
				},
			},
			{
				SessionID:  sessionID,
				Type:       session.EventTypeAssistant,
				MessageID:  messageID,
				Message:    &liveHelloWorld,
				Text:       "hello world",
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
						MessageID:     messageID,
					},
				},
			},
			{
				SessionID: sessionID,
				Type:      session.EventTypeAssistant,
				MessageID: messageID,
				Message:   &finalHelloWorld,
				Text:      "hello world",
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
						MessageID:     messageID,
					},
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
	const messageID = "thought-message-1"
	liveTask := model.NewReasoningMessage(model.RoleAssistant, "任务已", model.ReasoningVisibilityVisible)
	liveStarted := model.NewReasoningMessage(model.RoleAssistant, "启动", model.ReasoningVisibilityVisible)
	finalStarted := model.NewReasoningMessage(model.RoleAssistant, "任务已启动", model.ReasoningVisibilityVisible)
	return agent.RunResult{
		Session: session.Session{SessionRef: req.SessionRef},
		Handle: terminalBridgeRun{events: []*session.Event{
			{
				SessionID:  sessionID,
				Type:       session.EventTypeAssistant,
				MessageID:  messageID,
				Message:    &liveTask,
				Text:       "任务已",
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeAgentThought), MessageID: messageID,
					},
				},
			},
			{
				SessionID:  sessionID,
				Type:       session.EventTypeAssistant,
				MessageID:  messageID,
				Message:    &liveStarted,
				Text:       "启动",
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeAgentThought), MessageID: messageID,
					},
				},
			},
			{
				SessionID: sessionID,
				Type:      session.EventTypeAssistant,
				MessageID: messageID,
				Message:   &finalStarted,
				Text:      "任务已启动",
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeAgentThought), MessageID: messageID,
					},
				},
			},
		}},
	}, nil
}

func (narrativeThoughtReplayRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type narrativeToolBoundaryReplayRuntime struct{}

func (narrativeToolBoundaryReplayRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	sessionID := req.SessionRef.SessionID
	const messageID = "pre-tool-message"
	liveThought := model.NewReasoningMessage(model.RoleAssistant, "thinking", model.ReasoningVisibilityVisible)
	liveMessage := model.NewTextMessage(model.RoleAssistant, "hello")
	finalMessage := model.NewMessage(
		model.RoleAssistant,
		model.NewReasoningPart("thinking", model.ReasoningVisibilityVisible),
		model.NewTextPart("hello"),
	)
	return agent.RunResult{
		Session: session.Session{SessionRef: req.SessionRef},
		Handle: terminalBridgeRun{events: []*session.Event{
			{
				SessionID:  sessionID,
				Type:       session.EventTypeAssistant,
				MessageID:  messageID,
				Message:    &liveThought,
				Text:       "thinking",
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
						MessageID:     messageID,
					},
				},
			},
			{
				SessionID:  sessionID,
				Type:       session.EventTypeAssistant,
				MessageID:  messageID,
				Message:    &liveMessage,
				Text:       "hello",
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
						MessageID:     messageID,
					},
				},
			},
			{
				SessionID:  sessionID,
				Type:       session.EventTypeToolCall,
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
						ToolCallID:    "call-1",
						Kind:          "Read",
						Status:        "completed",
					},
				},
			},
			{
				SessionID: sessionID,
				Type:      session.EventTypeAssistant,
				MessageID: messageID,
				Message:   &finalMessage,
				Text:      "hello",
			},
		}},
	}, nil
}

func (narrativeToolBoundaryReplayRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type terminalBridgeRun struct {
	events     []*session.Event
	gapDropped uint64
}

func (r terminalBridgeRun) RunID() string { return "run-1" }

func (r terminalBridgeRun) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		if r.gapDropped > 0 && !yield(nil, &agent.EventStreamGapError{Dropped: r.gapDropped}) {
			return
		}
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

func (c *terminalBridgeCallbacks) RequestPermission(context.Context, acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	return acpsdk.RequestPermissionResponse{}, nil
}

func (c *terminalBridgeCallbacks) snapshot() []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]acp.SessionNotification(nil), c.notifications...)
}

func terminalOutputPayloads(notifications []acp.SessionNotification, toolCallID string) []string {
	out := []string{}
	for _, notification := range notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok || strings.TrimSpace(update.ToolCallID) != toolCallID {
			continue
		}
		if output, ok := metautil.TerminalOutput(update.Meta); ok {
			out = append(out, output.Data)
		}
	}
	return out
}

func standardToolResultPayloads(notifications []acp.SessionNotification, toolCallID string) []string {
	var out []string
	for _, notification := range notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok || strings.TrimSpace(update.ToolCallID) != toolCallID {
			continue
		}
		for _, item := range update.Content {
			if !strings.EqualFold(strings.TrimSpace(item.Type), "content") {
				continue
			}
			if text, ok := item.Content.(acp.TextContent); ok {
				out = append(out, text.Text)
			}
		}
	}
	return out
}

func hasToolUpdateContent(notifications []acp.SessionNotification, toolCallID string) bool {
	for _, notification := range notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok || strings.TrimSpace(update.ToolCallID) != toolCallID {
			continue
		}
		if len(update.Content) > 0 {
			return true
		}
	}
	return false
}

func hasToolCallNotification(notifications []acp.SessionNotification, toolCallID string) bool {
	for _, notification := range notifications {
		switch update := notification.Update.(type) {
		case acp.ToolCall:
			if strings.TrimSpace(update.ToolCallID) == toolCallID {
				return true
			}
		case acp.ToolCallUpdate:
			if strings.TrimSpace(update.ToolCallID) == toolCallID {
				return true
			}
		}
	}
	return false
}

func hasTerminalContent(notifications []acp.SessionNotification, toolCallID string, terminalID string, text string) bool {
	for _, notification := range notifications {
		update, ok := notification.Update.(acp.ToolCallUpdate)
		if !ok || strings.TrimSpace(update.ToolCallID) != toolCallID {
			continue
		}
		output, ok := metautil.TerminalOutput(update.Meta)
		if ok && strings.TrimSpace(output.TerminalID) == terminalID && strings.Contains(output.Data, text) {
			return true
		}
	}
	return false
}

func hasTerminalInfo(notifications []acp.SessionNotification, toolCallID string, terminalID string) bool {
	for _, notification := range notifications {
		switch update := notification.Update.(type) {
		case acp.ToolCall:
			if strings.TrimSpace(update.ToolCallID) != toolCallID {
				continue
			}
			if info, ok := metautil.TerminalInfo(update.Meta); ok && strings.TrimSpace(info.TerminalID) == terminalID {
				return true
			}
		case acp.ToolCallUpdate:
			if strings.TrimSpace(update.ToolCallID) != toolCallID {
				continue
			}
			if info, ok := metautil.TerminalInfo(update.Meta); ok && strings.TrimSpace(info.TerminalID) == terminalID {
				return true
			}
		}
	}
	return false
}

func transientTerminalStreamMetaForTest(mode string) map[string]any {
	return map[string]any{
		"caelis": map[string]any{
			"transient": true,
			"runtime": map[string]any{
				"stream": map[string]any{
					"mode": strings.TrimSpace(mode),
				},
			},
		},
	}
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

func agentMessageIDs(notifications []acp.SessionNotification) []string {
	return contentChunkMessageIDs(notifications, acp.UpdateAgentMessage)
}

func agentThoughtIDs(notifications []acp.SessionNotification) []string {
	return contentChunkMessageIDs(notifications, acp.UpdateAgentThought)
}

func contentChunkMessageIDs(notifications []acp.SessionNotification, updateType string) []string {
	out := make([]string, 0, len(notifications))
	for _, notification := range notifications {
		chunk, ok := notification.Update.(acp.ContentChunk)
		if !ok || chunk.SessionUpdate != updateType {
			continue
		}
		out = append(out, strings.TrimSpace(chunk.MessageID))
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

func testStringPointer(value string) *string {
	return &value
}

func testOptionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
