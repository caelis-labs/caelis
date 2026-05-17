package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/agent/acp/subagent"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
	"github.com/OnslaughtSnail/caelis/protocol/acp/jsonrpc"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestContentChunkTextPreservesStreamWhitespace(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(client.TextChunk{Type: "text", Text: "hello "})
	if err != nil {
		t.Fatal(err)
	}

	got := contentChunkText(client.ContentChunk{
		SessionUpdate: client.UpdateAgentMessage,
		Content:       raw,
	})
	if got != "hello " {
		t.Fatalf("contentChunkText() = %q, want trailing space preserved", got)
	}
}

func TestNormalizeACPUpdateEventKeepsCodexWebSearchToolIdentity(t *testing.T) {
	t.Parallel()

	event := normalizeACPUpdateEvent(func() time.Time { return time.Unix(0, 0) }, session.ControllerBinding{
		Kind:         session.ControllerKindACP,
		ControllerID: "codex",
		Label:        "codex",
	}, "remote-1", "turn-1", client.ToolCallUpdate{
		SessionUpdate: client.UpdateToolCallState,
		ToolCallID:    "ws_1",
		Kind:          testStringPtr("fetch"),
		Title:         testStringPtr("Searching for: weather: Shanghai, China"),
		Status:        testStringPtr("in_progress"),
		RawInput:      map[string]any{"query": "weather: Shanghai, China"},
	})
	if event == nil || event.Protocol == nil || event.Protocol.ToolCall == nil {
		t.Fatalf("event = %#v, want structured tool update", event)
	}
	if got := event.Protocol.ToolCall.Name; got != "fetch" {
		t.Fatalf("tool name = %q, want ACP kind", got)
	}
	if got := event.Protocol.ToolCall.Title; got != "Searching for: weather: Shanghai, China" {
		t.Fatalf("tool title = %q, want ACP title", got)
	}
	if got := event.Protocol.ToolCall.Kind; got != "fetch" {
		t.Fatalf("tool kind = %q, want fetch", got)
	}
	if got := event.Protocol.ToolCall.RawInput["query"]; got != "weather: Shanghai, China" {
		t.Fatalf("raw input query = %#v", got)
	}
}

func TestNormalizeACPUpdateEventPreservesToolUpdateMeta(t *testing.T) {
	t.Parallel()

	event := normalizeACPUpdateEvent(func() time.Time { return time.Unix(0, 0) }, session.ControllerBinding{
		Kind:         session.ControllerKindACP,
		ControllerID: "codex",
		Label:        "codex",
	}, "remote-1", "turn-1", client.ToolCallUpdate{
		SessionUpdate: client.UpdateToolCallState,
		ToolCallID:    "call-1",
		Kind:          testStringPtr("execute"),
		Status:        testStringPtr("in_progress"),
		Meta: map[string]any{
			"terminal_output": map[string]any{
				"terminal_id": "call-1",
				"data":        "line\n",
			},
		},
	})

	if event == nil || event.Protocol == nil || event.Protocol.Update == nil {
		t.Fatalf("event = %#v, want protocol update", event)
	}
	output, ok := event.Protocol.Update.Meta["terminal_output"].(map[string]any)
	if !ok {
		t.Fatalf("Protocol.Update.Meta = %#v, want terminal_output", event.Protocol.Update.Meta)
	}
	if output["terminal_id"] != "call-1" || output["data"] != "line\n" {
		t.Fatalf("terminal_output = %#v, want preserved ACP meta", output)
	}
}

func TestTranslateApprovalRequestPreservesToolRawInput(t *testing.T) {
	t.Parallel()

	req := translateApprovalRequest(session.Session{
		SessionRef: session.SessionRef{SessionID: "sess-1"},
	}, "codex", "default", client.RequestPermissionRequest{
		SessionID: "remote-1",
		ToolCall: client.ToolCallUpdate{
			ToolCallID: "call-1",
			Kind:       testStringPtr("execute"),
			Title:      testStringPtr("Run command"),
			Status:     testStringPtr("pending"),
			RawInput: map[string]any{
				"command": "pwd",
				"workdir": "/tmp/project",
			},
		},
	})
	if req.ToolCall.RawInput["command"] != "pwd" {
		t.Fatalf("ToolCall.RawInput[command] = %#v", req.ToolCall.RawInput["command"])
	}
	if req.ToolCall.RawInput["workdir"] != "/tmp/project" {
		t.Fatalf("ToolCall.RawInput[workdir] = %#v", req.ToolCall.RawInput["workdir"])
	}
}

func TestNormalizeACPUpdateEventMarksOnlySharedDialogueDurable(t *testing.T) {
	t.Parallel()

	clock := func() time.Time { return time.Unix(0, 0) }
	binding := session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "codex", Label: "codex"}
	textRaw, err := json.Marshal(client.TextChunk{Type: "text", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	user := normalizeACPUpdateEvent(clock, binding, "remote-1", "turn-1", client.ContentChunk{
		SessionUpdate: client.UpdateUserMessage,
		Content:       textRaw,
	})
	if user == nil || user.Visibility != session.VisibilityCanonical || user.Type != session.EventTypeUser {
		t.Fatalf("user event = %#v, want canonical user", user)
	}

	assistant := normalizeACPUpdateEvent(clock, binding, "remote-1", "turn-1", client.ContentChunk{
		SessionUpdate: client.UpdateAgentMessage,
		Content:       textRaw,
	})
	if assistant == nil || assistant.Visibility != session.VisibilityUIOnly || assistant.Type != session.EventTypeAssistant {
		t.Fatalf("assistant chunk = %#v, want ui-only assistant chunk", assistant)
	}

	targetTool := normalizeACPUpdateEvent(clock, binding, "remote-1", "turn-1", client.ToolCallUpdate{
		SessionUpdate: client.UpdateToolCallState,
		ToolCallID:    "tool-1",
		Kind:          testStringPtr("execute"),
		Status:        testStringPtr("completed"),
		RawOutput:     map[string]any{"stdout": "ok"},
	})
	if targetTool == nil || targetTool.Visibility != session.VisibilityUIOnly || targetTool.Protocol == nil || targetTool.Protocol.ToolCall == nil {
		t.Fatalf("tool update = %#v, want ui-only structured tool update", targetTool)
	}
}

func TestControllerRunApplyStartupStatePreservesPreSessionUpdates(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(1, 0) }, client.AvailableCommandsUpdate{
		SessionUpdate: client.UpdateAvailableCmds,
		AvailableCommands: []map[string]any{
			{"name": "/search", "description": "remote search"},
		},
	})
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(2, 0) }, client.ConfigOptionUpdate{
		SessionUpdate: client.UpdateConfigOption,
		ConfigOptions: []client.SessionConfigOption{{
			ID:           "model",
			Name:         "Model",
			CurrentValue: "live-model",
		}},
	})
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(3, 0) }, client.CurrentModeUpdate{
		SessionUpdate: client.UpdateCurrentMode,
		CurrentModeID: "review",
	})

	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []controller.ControllerConfigOption{
			{ID: "model", Name: "Model", CurrentValue: "startup-model"},
			{ID: "reasoning", Name: "Reasoning", CurrentValue: "medium"},
		},
		mode: "default",
		modeOptions: []controller.ControllerMode{
			{ID: "default", Name: "Default"},
			{ID: "review", Name: "Review"},
		},
		agentLabel: "Remote ACP",
	}, 7)

	status := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if len(status.Commands) != 1 || status.Commands[0].Name != "search" {
		t.Fatalf("commands = %#v, want preserved startup update", status.Commands)
	}
	if status.Model != "live-model" {
		t.Fatalf("model = %q, want live-model from update", status.Model)
	}
	if status.ReasoningEffort != "medium" {
		t.Fatalf("reasoning effort = %q, want missing startup config filled", status.ReasoningEffort)
	}
	if status.Mode != "review" {
		t.Fatalf("mode = %q, want review from update", status.Mode)
	}
	if len(status.ModeOptions) != 2 {
		t.Fatalf("mode options = %#v, want startup options filled", status.ModeOptions)
	}
	if run.binding.RemoteSessionID != "remote-1" || run.binding.ContextSyncSeq != 7 || run.binding.Label != "Remote ACP" {
		t.Fatalf("binding = %#v, want startup binding fields", run.binding)
	}
}

func TestControllerRunAppliesSessionInfoUpdate(t *testing.T) {
	t.Parallel()

	title := "Remote title"
	updatedAt := "2026-05-04T12:34:56Z"
	run := &controllerRun{}
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(1, 0) }, client.SessionInfoUpdate{
		SessionUpdate: client.UpdateSessionInfo,
		Title:         &title,
		UpdatedAt:     &updatedAt,
	})

	status := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if status.RemoteTitle != "Remote title" {
		t.Fatalf("RemoteTitle = %q, want Remote title", status.RemoteTitle)
	}
	if got := status.UpdatedAt.Format(time.RFC3339); got != updatedAt {
		t.Fatalf("UpdatedAt = %q, want %q", got, updatedAt)
	}
}

func TestControllerRunStatusUsesConfigOptionsForModelAndEffort(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []controller.ControllerConfigOption{
			{
				ID:           "model",
				Name:         "Model",
				CurrentValue: "gpt-5.5",
				Options: []controller.ControllerConfigChoice{
					{Value: "gpt-5.5", Name: "GPT-5.5"},
					{Value: "gpt-5.4", Name: "gpt-5.4"},
				},
			},
			{
				ID:           "reasoning_effort",
				Name:         "Reasoning Effort",
				CurrentValue: "xhigh",
				Options: []controller.ControllerConfigChoice{
					{Value: "low", Name: "Low"},
					{Value: "medium", Name: "Medium"},
					{Value: "high", Name: "High"},
					{Value: "xhigh", Name: "Xhigh"},
				},
			},
		},
	}, 0)

	status := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if status.Model != "gpt-5.5" || status.ReasoningEffort != "xhigh" {
		t.Fatalf("status model/effort = %q/%q, want gpt-5.5/xhigh", status.Model, status.ReasoningEffort)
	}
	if got := controllerChoiceValues(status.ModelOptions); !equalStrings(got, []string{"gpt-5.5", "gpt-5.4"}) {
		t.Fatalf("model options = %#v, want config model options", got)
	}
	if got := controllerChoiceValues(status.EffortOptions); !equalStrings(got, []string{"low", "medium", "high", "xhigh"}) {
		t.Fatalf("effort options = %#v, want config effort options", got)
	}
}

func TestControllerRunApplyStartupStateFillsPartialPreSessionConfigOptions(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(1, 0) }, client.ConfigOptionUpdate{
		SessionUpdate: client.UpdateConfigOption,
		ConfigOptions: []client.SessionConfigOption{
			{ID: "model", Name: "Model"},
			{ID: "reasoning_effort", Name: "Reasoning Effort"},
		},
	})

	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []controller.ControllerConfigOption{
			{
				ID:           "model",
				Name:         "Model",
				CurrentValue: "gpt-5.5",
				Options: []controller.ControllerConfigChoice{
					{Value: "gpt-5.5", Name: "GPT-5.5"},
					{Value: "gpt-5.4", Name: "gpt-5.4"},
				},
			},
			{
				ID:           "reasoning_effort",
				Name:         "Reasoning Effort",
				CurrentValue: "xhigh",
				Options: []controller.ControllerConfigChoice{
					{Value: "low", Name: "Low"},
					{Value: "medium", Name: "Medium"},
					{Value: "high", Name: "High"},
					{Value: "xhigh", Name: "Xhigh"},
				},
			},
		},
	}, 0)

	status := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if status.Model != "gpt-5.5" || status.ReasoningEffort != "xhigh" {
		t.Fatalf("status model/effort = %q/%q, want startup current values filled", status.Model, status.ReasoningEffort)
	}
	if got := controllerChoiceValues(status.ModelOptions); !equalStrings(got, []string{"gpt-5.5", "gpt-5.4"}) {
		t.Fatalf("model options = %#v, want startup model options filled", got)
	}
	if got := controllerChoiceValues(status.EffortOptions); !equalStrings(got, []string{"low", "medium", "high", "xhigh"}) {
		t.Fatalf("effort options = %#v, want startup effort options filled", got)
	}
}

func TestControllerRunStatusFillsCurrentModelEffortFromACPModelState(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []controller.ControllerConfigOption{
			{
				ID: "model",
				Options: []controller.ControllerConfigChoice{
					{Value: "gpt-5.5", Name: "GPT-5.5"},
					{Value: "gpt-5.4", Name: "gpt-5.4"},
				},
			},
			{
				ID: "reasoning_effort",
				Options: []controller.ControllerConfigChoice{
					{Value: "low", Name: "Low"},
					{Value: "high", Name: "High"},
					{Value: "xhigh", Name: "Xhigh"},
				},
			},
		},
		models: &client.SessionModelState{
			CurrentModelID: "gpt-5.5/xhigh",
		},
	}, 0)

	status := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if status.Model != "gpt-5.5" || status.ReasoningEffort != "xhigh" {
		t.Fatalf("status model/effort = %q/%q, want gpt-5.5/xhigh", status.Model, status.ReasoningEffort)
	}
}

func TestControllerRunStatusDerivesEffortOptionsFromACPModelState(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []controller.ControllerConfigOption{{
			ID:           "model",
			Name:         "Model",
			CurrentValue: "gpt-5.4",
			Options: []controller.ControllerConfigChoice{
				{Value: "gpt-5.5", Name: "GPT-5.5"},
				{Value: "gpt-5.4", Name: "gpt-5.4"},
			},
		}},
		models: &client.SessionModelState{
			CurrentModelID: "gpt-5.4/high",
			AvailableModels: []client.ModelInfo{
				{ModelID: "gpt-5.5", Name: "GPT-5.5"},
				{ModelID: "gpt-5.4/low", Name: "gpt-5.4 (low)"},
				{ModelID: "gpt-5.4/high", Name: "gpt-5.4 (high)"},
				{ModelID: "gpt-5.4/xhigh", Name: "gpt-5.4 (xhigh)"},
			},
		},
	}, 0)

	status := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if status.Model != "gpt-5.4" || status.ReasoningEffort != "high" {
		t.Fatalf("status model/effort = %q/%q, want gpt-5.4/high", status.Model, status.ReasoningEffort)
	}
	if got := controllerChoiceValues(status.EffortOptions); !equalStrings(got, []string{"low", "high", "xhigh"}) {
		t.Fatalf("effort options = %#v, want model-derived low/high/xhigh", got)
	}
}

func TestControllerRunStatusPreservesConfigChoicesAfterPartialUpdate(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []controller.ControllerConfigOption{{
			ID:           "model",
			Name:         "Model",
			CurrentValue: "gpt-5.5",
			Options: []controller.ControllerConfigChoice{
				{Value: "gpt-5.5", Name: "GPT-5.5"},
				{Value: "gpt-5.4", Name: "gpt-5.4"},
			},
		}},
	}, 0)

	run.applySessionUpdateLocked(func() time.Time { return time.Unix(2, 0) }, client.ConfigOptionUpdate{
		SessionUpdate: client.UpdateConfigOption,
		ConfigOptions: []client.SessionConfigOption{{
			ID:           "model",
			Name:         "Model",
			CurrentValue: "gpt-5.4",
			Options: []client.SessionConfigSelectOption{
				{Value: "gpt-5.4", Name: "gpt-5.4"},
			},
		}},
	})

	status := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if status.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want updated current model", status.Model)
	}
	if got := controllerChoiceValues(status.ModelOptions); !equalStrings(got, []string{"gpt-5.5", "gpt-5.4"}) {
		t.Fatalf("model options = %#v, want preserved full choices", got)
	}
}

func TestManagerLifecycleUsesSingleClientStarterSeam(t *testing.T) {
	registry, err := acp.NewRegistry([]acp.AgentConfig{{
		Name:    "helper",
		Command: "helper-acp",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	manager, err := NewManager(Config{
		Registry: registry,
		Clock:    func() time.Time { return time.Unix(100, 0) },
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	starts := 0
	manager.startClient = func(
		_ context.Context,
		_ string,
		cfg acp.AgentConfig,
		resumeRemoteSessionID string,
		onUpdate func(client.UpdateEnvelope),
		_ func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		starts++
		remoteID := "remote-session"
		if resumeRemoteSessionID != "" {
			remoteID = resumeRemoteSessionID
		}
		onUpdate(client.UpdateEnvelope{Update: client.AvailableCommandsUpdate{
			SessionUpdate: client.UpdateAvailableCmds,
			AvailableCommands: []map[string]any{{
				"name":        "/search",
				"description": "remote search",
			}},
		}})
		return nil, remoteID, controllerClientState{
			agentLabel: "Helper ACP",
			configOptions: []controller.ControllerConfigOption{{
				ID:           "model",
				Name:         "Model",
				CurrentValue: "gpt-test",
			}},
			mode: "default",
			modeOptions: []controller.ControllerMode{{
				ID:   "default",
				Name: "Default",
			}},
		}, nil
	}

	parentSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws",
		},
		CWD: "/workspace",
	}
	binding, err := manager.Activate(context.Background(), controller.HandoffRequest{
		Session: parentSession,
		Agent:   "helper",
		Source:  "test",
	})
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if binding.Kind != session.ControllerKindACP || binding.RemoteSessionID != "remote-session" || binding.Label != "Helper ACP" {
		t.Fatalf("binding = %#v, want ACP helper binding", binding)
	}
	status, ok, err := manager.ControllerStatus(context.Background(), parentSession.SessionRef)
	if err != nil || !ok {
		t.Fatalf("ControllerStatus() = %#v, %v, %v; want active status", status, ok, err)
	}
	if status.Model != "gpt-test" || status.Mode != "default" || len(status.Commands) != 1 || status.Commands[0].Name != "search" {
		t.Fatalf("status = %#v, want startup model/mode and live commands", status)
	}
	turn, err := manager.RunTurn(context.Background(), controller.TurnRequest{
		SessionRef: parentSession.SessionRef,
		Session:    parentSession,
		TurnID:     "turn-empty",
		Input:      " ",
	})
	if err != nil {
		t.Fatalf("RunTurn(empty) error = %v", err)
	}
	if turn.Handle == nil {
		t.Fatal("RunTurn(empty) handle = nil")
	}
	for range turn.Handle.Events() {
	}
	if err := manager.Deactivate(context.Background(), parentSession.SessionRef); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}
	if _, ok, err := manager.ControllerStatus(context.Background(), parentSession.SessionRef); err != nil || ok {
		t.Fatalf("ControllerStatus(after deactivate) ok/err = %v/%v, want false/nil", ok, err)
	}

	participant, err := manager.Attach(context.Background(), controller.AttachRequest{
		Session: parentSession,
		Agent:   "helper",
		Label:   "helper",
	})
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	if participant.ID == "" || participant.SessionID != "remote-session" {
		t.Fatalf("participant binding = %#v, want remote session binding", participant)
	}
	if err := manager.Detach(context.Background(), controller.DetachRequest{ParticipantID: participant.ID}); err != nil {
		t.Fatalf("Detach() error = %v", err)
	}
	manager.mu.RLock()
	_, stillAttached := manager.participants[participant.ID]
	manager.mu.RUnlock()
	if stillAttached {
		t.Fatal("participant still attached after Detach")
	}
	if starts != 2 {
		t.Fatalf("client starts = %d, want 2 (controller + participant)", starts)
	}
}

func TestManagerStartACPClientFallsBackToNewSessionWhenResumeFails(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manager := &Manager{}
	acpClient, remoteSessionID, _, err := manager.startACPClient(ctx, t.TempDir(), acp.AgentConfig{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerACPResumeFallbackHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_HELPER": "resume-fallback",
		},
	}, "stale-remote-session", nil, func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
		return client.RequestPermissionResponse{}, nil
	})
	if err != nil {
		t.Fatalf("startACPClient() error = %v", err)
	}
	defer acpClient.Close(context.Background())
	if got, want := remoteSessionID, "new-session"; got != want {
		t.Fatalf("remoteSessionID = %q, want %q", got, want)
	}
}

func TestManagerActivateResetsContextCheckpointForNewRemoteSession(t *testing.T) {
	t.Parallel()

	registry, err := acp.NewRegistry([]acp.AgentConfig{{
		Name:    "helper",
		Command: "helper-acp",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	manager, err := NewManager(Config{Registry: registry})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.startClient = func(
		_ context.Context,
		_ string,
		cfg acp.AgentConfig,
		resumeRemoteSessionID string,
		_ func(client.UpdateEnvelope),
		_ func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		if cfg.Name != "helper" {
			t.Fatalf("startClient cfg = %q, want helper", cfg.Name)
		}
		if resumeRemoteSessionID != "old-remote" {
			t.Fatalf("resumeRemoteSessionID = %q, want old-remote", resumeRemoteSessionID)
		}
		return nil, "new-remote", controllerClientState{}, nil
	}

	binding, err := manager.Activate(context.Background(), controller.HandoffRequest{
		Session: session.Session{
			SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws",
			},
			Controller: session.ControllerBinding{
				Kind:            session.ControllerKindACP,
				AgentName:       "helper",
				RemoteSessionID: "old-remote",
				ContextSyncSeq:  42,
			},
		},
		Agent:          "helper",
		ContextPrelude: "incremental context for old remote",
		ContextSyncSeq: 42,
	})
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if binding.RemoteSessionID != "new-remote" {
		t.Fatalf("RemoteSessionID = %q, want new-remote", binding.RemoteSessionID)
	}
	if binding.ContextSyncSeq != 0 {
		t.Fatalf("ContextSyncSeq = %d, want reset for fresh remote session", binding.ContextSyncSeq)
	}
}

func TestManagerACPResumeFallbackHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_HELPER") != "resume-fallback" {
		return
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case client.MethodInitialize:
			return client.InitializeResponse{
				ProtocolVersion: 1,
				AgentCapabilities: schema.AgentCapabilities{
					SessionCapabilities: map[string]json.RawMessage{
						"resume": json.RawMessage("{}"),
					},
				},
			}, nil
		case client.MethodSessionResume:
			var req client.ResumeSessionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if req.SessionID != "stale-remote-session" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/resume id"}
			}
			return nil, &jsonrpc.RPCError{Code: -32004, Message: "session not found"}
		case client.MethodSessionNew:
			var req client.NewSessionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if strings.TrimSpace(req.CWD) == "" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "session/new cwd is required"}
			}
			return client.NewSessionResponse{SessionID: "new-session"}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper Serve() error = %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestTurnHandlePublishDoesNotBlockAfterBufferFillsOrFinishes(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 128; i++ {
			handle.publishEvent(&session.Event{ID: "event", Type: session.EventTypeAssistant})
		}
		handle.finish()
		for i := 0; i < 8; i++ {
			handle.publishEvent(&session.Event{ID: "late", Type: session.EventTypeAssistant})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("turn handle publish blocked with a full or finished channel")
	}
}

func controllerChoiceValues(in []controller.ControllerConfigChoice) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		out = append(out, item.Value)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func testStringPtr(value string) *string {
	return &value
}
