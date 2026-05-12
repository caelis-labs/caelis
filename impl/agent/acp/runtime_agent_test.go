package acp_test

import (
	"context"
	"encoding/json"
	"errors"
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

func TestRuntimeAgentBridgesTerminalStreamToACPDisplayMeta(t *testing.T) {
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
			t.Fatalf("notifications = %#v, want terminal info, streamed output, and exit meta", notes)
		case <-time.After(10 * time.Millisecond):
		}
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
			return agent.AgentSpec{Name: "chat", Metadata: map[string]any{"policy_mode": "auto-review"}}, nil
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
	if reviewer.last.Approval == nil || reviewer.last.Approval.ToolName != "BASH" {
		t.Fatalf("reviewer approval payload = %#v, want BASH payload", reviewer.last.Approval)
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
			return agent.AgentSpec{Name: "chat", Metadata: map[string]any{"policy_mode": "manual"}}, nil
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
			Tool:       tool.Definition{Name: "BASH"},
			Call:       tool.Call{ID: "call-1", Name: "BASH"},
			Approval: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{
					ID:   "call-1",
					Name: "BASH",
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

type terminalBridgeRuntime struct{}

func (terminalBridgeRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	sessionID := req.SessionRef.SessionID
	return agent.RunResult{
		Handle: terminalBridgeRun{events: []*session.Event{
			{
				SessionID: sessionID,
				Type:      session.EventTypeToolCall,
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeToolCall),
					ToolCall: &session.ProtocolToolCall{
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
				Type:       session.EventTypeToolResult,
				Visibility: session.VisibilityUIOnly,
				Protocol: &session.EventProtocol{
					UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
					ToolCall: &session.ProtocolToolCall{
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

func (terminalBridgeRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (terminalBridgeRuntime) Streams() stream.Service { return terminalBridgeStream{} }

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

type terminalBridgeStream struct{}

func (terminalBridgeStream) Read(context.Context, stream.ReadRequest) (stream.Snapshot, error) {
	return stream.Snapshot{}, nil
}

func (terminalBridgeStream) Subscribe(context.Context, stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(yield func(*stream.Frame, error) bool) {
		if !yield(&stream.Frame{Text: "streamed output\n"}, nil) {
			return
		}
		code := 0
		yield(&stream.Frame{Closed: true, ExitCode: &code}, nil)
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
