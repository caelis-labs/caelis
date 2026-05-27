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
	"github.com/OnslaughtSnail/caelis/ports/model"
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

func TestBuildPromptPartsPreservesContentPartWhitespace(t *testing.T) {
	t.Parallel()

	parts := buildPromptParts("ignored fallback", []model.ContentPart{
		{Type: model.ContentPartText, Text: "first "},
		{Type: model.ContentPartImage, MimeType: "image/png", Data: "iVBORw0KGgo=", FileName: "shot.png"},
		{Type: model.ContentPartText, Text: " second"},
	})
	if len(parts) != 3 {
		t.Fatalf("len(parts) = %d, want 3", len(parts))
	}
	var first client.TextContent
	if err := json.Unmarshal(parts[0], &first); err != nil {
		t.Fatal(err)
	}
	var image client.ImageContent
	if err := json.Unmarshal(parts[1], &image); err != nil {
		t.Fatal(err)
	}
	var second client.TextContent
	if err := json.Unmarshal(parts[2], &second); err != nil {
		t.Fatal(err)
	}
	if first.Text != "first " || image.Type != "image" || image.Name != "shot.png" || second.Text != " second" {
		t.Fatalf("prompt parts = %#v %#v %#v, want whitespace-preserving text/image/text", first, image, second)
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

func TestControllerRunStatusUsesConfigCategoriesForModeAndThoughtLevel(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []controller.ControllerConfigOption{
			{
				ID:           "session-mode",
				Name:         "Session Mode",
				Category:     "mode",
				CurrentValue: "code",
				Options: []controller.ControllerConfigChoice{
					{Value: "ask", Name: "Ask"},
					{Value: "code", Name: "Code"},
				},
			},
			{
				ID:           "thinking",
				Name:         "Thinking",
				Category:     "thought_level",
				CurrentValue: "high",
				Options: []controller.ControllerConfigChoice{
					{Value: "low", Name: "Low"},
					{Value: "high", Name: "High"},
				},
			},
		},
		mode: "ask",
		modeOptions: []controller.ControllerMode{
			{ID: "ask", Name: "Ask"},
		},
	}, 0)

	status := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if status.Mode != "code" || status.ReasoningEffort != "high" {
		t.Fatalf("status mode/effort = %q/%q, want code/high", status.Mode, status.ReasoningEffort)
	}
	if got := controllerModeIDs(status.ModeOptions); !equalStrings(got, []string{"ask", "code"}) {
		t.Fatalf("mode options = %#v, want config mode options", got)
	}
	if got := controllerChoiceValues(status.EffortOptions); !equalStrings(got, []string{"low", "high"}) {
		t.Fatalf("effort options = %#v, want thought_level options", got)
	}
}

func TestControllerRunStatusDoesNotTreatModelCategoryEffortAsModel(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []controller.ControllerConfigOption{
			{
				ID:           "effort",
				Name:         "Effort",
				Category:     "model",
				CurrentValue: "high",
				Options: []controller.ControllerConfigChoice{
					{Value: "low", Name: "Low"},
					{Value: "high", Name: "High"},
				},
			},
			{
				ID:           "model",
				Name:         "Model",
				Category:     "model",
				CurrentValue: "gpt-next",
				Options: []controller.ControllerConfigChoice{
					{Value: "gpt-old", Name: "GPT Old"},
					{Value: "gpt-next", Name: "GPT Next"},
				},
			},
		},
	}, 0)

	status := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if status.Model != "gpt-next" || status.ReasoningEffort != "high" {
		t.Fatalf("status model/effort = %q/%q, want gpt-next/high", status.Model, status.ReasoningEffort)
	}
	if got := controllerChoiceValues(status.ModelOptions); !equalStrings(got, []string{"gpt-old", "gpt-next"}) {
		t.Fatalf("model options = %#v, want actual model options", got)
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
		CWD: t.TempDir(),
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

func TestManagerRejectsImagePromptWithoutACPImageCapability(t *testing.T) {
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
		_ acp.AgentConfig,
		_ string,
		_ func(client.UpdateEnvelope),
		_ func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		return nil, "remote-session", controllerClientState{}, nil
	}

	parentSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	if _, err := manager.Activate(context.Background(), controller.HandoffRequest{
		Session: parentSession,
		Agent:   "helper",
		Source:  "test",
	}); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	image := []model.ContentPart{{
		Type:     model.ContentPartImage,
		MimeType: "image/png",
		Data:     "iVBORw0KGgo=",
		FileName: "shot.png",
	}}
	if _, err := manager.RunTurn(context.Background(), controller.TurnRequest{
		SessionRef:   parentSession.SessionRef,
		Session:      parentSession,
		TurnID:       "turn-image",
		Input:        "look",
		ContentParts: image,
	}); err == nil || !strings.Contains(err.Error(), "does not support image prompts") {
		t.Fatalf("RunTurn(image) error = %v, want unsupported image prompt", err)
	}

	participant, err := manager.Attach(context.Background(), controller.AttachRequest{
		Session: parentSession,
		Agent:   "helper",
		Label:   "helper",
	})
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	if _, err := manager.PromptParticipant(context.Background(), controller.ParticipantPromptRequest{
		SessionRef:    parentSession.SessionRef,
		ParticipantID: participant.ID,
		Input:         "look",
		ContentParts:  image,
	}); err == nil || !strings.Contains(err.Error(), "does not support image prompts") {
		t.Fatalf("PromptParticipant(image) error = %v, want unsupported image prompt", err)
	}
}

func TestManagerSetControllerModelReconnectsAfterBrokenPipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := acp.NewRegistry([]acp.AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerACPControllerReconnectHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_HELPER": "controller-reconnect",
		},
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
	baseStart := manager.startClient
	starts := 0
	manager.startClient = func(
		ctx context.Context,
		cwd string,
		cfg acp.AgentConfig,
		resumeRemoteSessionID string,
		onUpdate func(client.UpdateEnvelope),
		onPermission func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		starts++
		return baseStart(ctx, cwd, cfg, resumeRemoteSessionID, onUpdate, onPermission)
	}
	parentSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	t.Cleanup(func() {
		_ = manager.Deactivate(context.Background(), parentSession.SessionRef)
	})
	if _, err := manager.Activate(ctx, controller.HandoffRequest{
		Session: parentSession,
		Agent:   "helper",
		Source:  "test",
	}); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	status, err := manager.SetControllerModel(ctx, controller.SetControllerModelRequest{
		SessionRef: parentSession.SessionRef,
		Model:      "gpt-next",
	})
	if err != nil {
		t.Fatalf("SetControllerModel() error = %v", err)
	}
	if starts != 2 {
		t.Fatalf("client starts = %d, want activate plus reconnect", starts)
	}
	if status.Model != "gpt-next" {
		t.Fatalf("status.Model = %q, want gpt-next", status.Model)
	}
}

func TestManagerRunTurnReconnectsAfterBrokenPipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := acp.NewRegistry([]acp.AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerACPControllerReconnectHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_HELPER": "controller-reconnect",
		},
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
	baseStart := manager.startClient
	starts := 0
	manager.startClient = func(
		ctx context.Context,
		cwd string,
		cfg acp.AgentConfig,
		resumeRemoteSessionID string,
		onUpdate func(client.UpdateEnvelope),
		onPermission func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		starts++
		return baseStart(ctx, cwd, cfg, resumeRemoteSessionID, onUpdate, onPermission)
	}
	parentSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	t.Cleanup(func() {
		_ = manager.Deactivate(context.Background(), parentSession.SessionRef)
	})
	if _, err := manager.Activate(ctx, controller.HandoffRequest{
		Session: parentSession,
		Agent:   "helper",
		Source:  "test",
	}); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	turn, err := manager.RunTurn(ctx, controller.TurnRequest{
		SessionRef: parentSession.SessionRef,
		Session:    parentSession,
		TurnID:     "turn-prompt",
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	for _, eventErr := range turn.Handle.Events() {
		if eventErr != nil {
			t.Fatalf("turn event error = %v", eventErr)
		}
	}
	if starts != 2 {
		t.Fatalf("client starts = %d, want activate plus reconnect", starts)
	}
}

func TestManagerActivateKeepsControllerProcessAfterHandoffContextCancel(t *testing.T) {
	registry, err := acp.NewRegistry([]acp.AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerACPDurableHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_HELPER": "controller-durable",
		},
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
	parentSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	handoffCtx, cancelHandoff := context.WithCancel(context.Background())
	if _, err := manager.Activate(handoffCtx, controller.HandoffRequest{
		Session: parentSession,
		Agent:   "helper",
		Source:  "test",
	}); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	cancelHandoff()

	turnCtx, cancelTurn := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelTurn()
	turn, err := manager.RunTurn(turnCtx, controller.TurnRequest{
		SessionRef: parentSession.SessionRef,
		Session:    parentSession,
		TurnID:     "turn-after-cancel",
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	for _, eventErr := range turn.Handle.Events() {
		if eventErr != nil {
			t.Fatalf("turn event error = %v", eventErr)
		}
	}
	if err := manager.Deactivate(context.Background(), parentSession.SessionRef); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}
}

func TestManagerRunTurnReconnectReappliesSelectedModelAndEffort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := acp.NewRegistry([]acp.AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerACPControllerReapplyHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_HELPER": "controller-reapply",
		},
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
	baseStart := manager.startClient
	starts := 0
	manager.startClient = func(
		ctx context.Context,
		cwd string,
		cfg acp.AgentConfig,
		resumeRemoteSessionID string,
		onUpdate func(client.UpdateEnvelope),
		onPermission func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		starts++
		return baseStart(ctx, cwd, cfg, resumeRemoteSessionID, onUpdate, onPermission)
	}
	parentSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	t.Cleanup(func() {
		_ = manager.Deactivate(context.Background(), parentSession.SessionRef)
	})
	if _, err := manager.Activate(ctx, controller.HandoffRequest{
		Session: parentSession,
		Agent:   "helper",
		Source:  "test",
	}); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	status, err := manager.SetControllerModel(ctx, controller.SetControllerModelRequest{
		SessionRef:      parentSession.SessionRef,
		Model:           "gpt-next",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("SetControllerModel() error = %v", err)
	}
	if status.Model != "gpt-next" || status.ReasoningEffort != "high" {
		t.Fatalf("status model/effort = %q/%q, want gpt-next/high", status.Model, status.ReasoningEffort)
	}
	turn, err := manager.RunTurn(ctx, controller.TurnRequest{
		SessionRef: parentSession.SessionRef,
		Session:    parentSession,
		TurnID:     "turn-reapply",
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	for _, eventErr := range turn.Handle.Events() {
		if eventErr != nil {
			t.Fatalf("turn event error = %v", eventErr)
		}
	}
	if starts != 2 {
		t.Fatalf("client starts = %d, want activate plus reconnect", starts)
	}
}

func TestManagerRunTurnReconnectReappliesModeWhenResumeReportsEmptyCurrentMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := acp.NewRegistry([]acp.AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerACPControllerModeReapplyHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_HELPER": "controller-mode-reapply",
		},
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
	baseStart := manager.startClient
	starts := 0
	manager.startClient = func(
		ctx context.Context,
		cwd string,
		cfg acp.AgentConfig,
		resumeRemoteSessionID string,
		onUpdate func(client.UpdateEnvelope),
		onPermission func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		starts++
		return baseStart(ctx, cwd, cfg, resumeRemoteSessionID, onUpdate, onPermission)
	}
	parentSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	t.Cleanup(func() {
		_ = manager.Deactivate(context.Background(), parentSession.SessionRef)
	})
	if _, err := manager.Activate(ctx, controller.HandoffRequest{
		Session: parentSession,
		Agent:   "helper",
		Source:  "test",
	}); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	status, err := manager.SetControllerMode(ctx, controller.SetControllerModeRequest{
		SessionRef: parentSession.SessionRef,
		Mode:       "code",
	})
	if err != nil {
		t.Fatalf("SetControllerMode() error = %v", err)
	}
	if status.Mode != "code" {
		t.Fatalf("status.Mode = %q, want code", status.Mode)
	}
	turn, err := manager.RunTurn(ctx, controller.TurnRequest{
		SessionRef: parentSession.SessionRef,
		Session:    parentSession,
		TurnID:     "turn-mode-reapply",
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	for _, eventErr := range turn.Handle.Events() {
		if eventErr != nil {
			t.Fatalf("turn event error = %v", eventErr)
		}
	}
	if starts != 2 {
		t.Fatalf("client starts = %d, want activate plus reconnect", starts)
	}
}

func TestManagerReconnectDoesNotRestartInactiveControllerRun(t *testing.T) {
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
	starts := 0
	manager.startClient = func(
		context.Context,
		string,
		acp.AgentConfig,
		string,
		func(client.UpdateEnvelope),
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		starts++
		return nil, "", controllerClientState{}, nil
	}
	run := &controllerRun{
		parentSessionID: "parent",
		cfg:             acp.AgentConfig{Name: "helper", Command: "helper-acp"},
		cwd:             t.TempDir(),
	}

	err = manager.reconnectControllerRun(context.Background(), run)
	if err == nil || !strings.Contains(err.Error(), "no longer active") {
		t.Fatalf("reconnectControllerRun() error = %v, want inactive run error", err)
	}
	if starts != 0 {
		t.Fatalf("client starts = %d, want no restart for inactive run", starts)
	}
}

func TestManagerAttachRehydratesPersistedParticipant(t *testing.T) {
	t.Parallel()

	registry, err := acp.NewRegistry([]acp.AgentConfig{{
		Name:    "tova",
		Command: "tova-acp",
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
	var resumed string
	manager.startClient = func(
		_ context.Context,
		_ string,
		cfg acp.AgentConfig,
		resumeRemoteSessionID string,
		_ func(client.UpdateEnvelope),
		_ func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		starts++
		resumed = resumeRemoteSessionID
		if cfg.Name != "tova" {
			t.Fatalf("startClient cfg = %q, want tova", cfg.Name)
		}
		return nil, resumeRemoteSessionID, controllerClientState{}, nil
	}

	parentSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws",
		},
		CWD: "/workspace",
		Controller: session.ControllerBinding{
			Kind:    session.ControllerKindKernel,
			EpochID: "epoch-1",
		},
	}
	persisted := session.ParticipantBinding{
		ID:             "codex-3",
		Kind:           session.ParticipantKindACP,
		Role:           session.ParticipantRoleSidecar,
		AgentName:      "tova",
		Label:          "@tova",
		SessionID:      "remote-tova",
		Source:         "tui_agent_add",
		ContextSyncSeq: 7,
		AttachedAt:     time.Unix(50, 0),
		ControllerRef:  "epoch-1",
	}
	binding, err := manager.Attach(context.Background(), controller.AttachRequest{
		Session: parentSession,
		Agent:   "tova",
		Binding: persisted,
	})
	if err != nil {
		t.Fatalf("Attach(rehydrate) error = %v", err)
	}
	if resumed != "remote-tova" {
		t.Fatalf("resumeRemoteSessionID = %q, want remote-tova", resumed)
	}
	if binding.ID != "codex-3" || binding.SessionID != "remote-tova" || binding.ContextSyncSeq != 7 || binding.Label != "@tova" {
		t.Fatalf("binding = %#v, want persisted participant binding", binding)
	}

	updated := persisted
	updated.ContextSyncSeq = 9
	binding, err = manager.Attach(context.Background(), controller.AttachRequest{
		Session: parentSession,
		Agent:   "tova",
		Binding: updated,
	})
	if err != nil {
		t.Fatalf("Attach(existing) error = %v", err)
	}
	if starts != 1 {
		t.Fatalf("client starts = %d, want one rehydration", starts)
	}
	if binding.ContextSyncSeq != 9 {
		t.Fatalf("ContextSyncSeq = %d, want refreshed checkpoint 9", binding.ContextSyncSeq)
	}
}

func TestManagerAttachResetsParticipantCheckpointForFreshRemoteSession(t *testing.T) {
	t.Parallel()

	registry, err := acp.NewRegistry([]acp.AgentConfig{{
		Name:    "tova",
		Command: "tova-acp",
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
		_ acp.AgentConfig,
		resumeRemoteSessionID string,
		_ func(client.UpdateEnvelope),
		_ func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		if resumeRemoteSessionID != "stale-remote" {
			t.Fatalf("resumeRemoteSessionID = %q, want stale-remote", resumeRemoteSessionID)
		}
		return nil, "fresh-remote", controllerClientState{}, nil
	}

	binding, err := manager.Attach(context.Background(), controller.AttachRequest{
		Session: session.Session{
			SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws",
			},
			CWD: "/workspace",
		},
		Agent: "tova",
		Binding: session.ParticipantBinding{
			ID:             "codex-3",
			Kind:           session.ParticipantKindACP,
			Role:           session.ParticipantRoleSidecar,
			AgentName:      "tova",
			Label:          "@tova",
			SessionID:      "stale-remote",
			ContextSyncSeq: 42,
		},
	})
	if err != nil {
		t.Fatalf("Attach(rehydrate fallback) error = %v", err)
	}
	if binding.SessionID != "fresh-remote" {
		t.Fatalf("SessionID = %q, want fresh-remote", binding.SessionID)
	}
	if binding.ContextSyncSeq != 0 {
		t.Fatalf("ContextSyncSeq = %d, want reset for fresh remote session", binding.ContextSyncSeq)
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

func TestManagerACPControllerReconnectHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_HELPER") != "controller-reconnect" {
		return
	}
	modelConfig := func(current string) []client.SessionConfigOption {
		return []client.SessionConfigOption{{
			ID:           "model",
			Name:         "Model",
			Category:     "model",
			Type:         "select",
			CurrentValue: current,
			Options: []client.SessionConfigSelectOption{
				{Value: "gpt-old", Name: "GPT Old"},
				{Value: "gpt-next", Name: "GPT Next"},
			},
		}}
	}
	var sawResume bool
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
		case client.MethodSessionNew:
			return client.NewSessionResponse{
				SessionID:     "remote-reconnect",
				ConfigOptions: modelConfig("gpt-old"),
			}, nil
		case client.MethodSessionResume:
			var req client.ResumeSessionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if req.SessionID != "remote-reconnect" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/resume id"}
			}
			sawResume = true
			return client.ResumeSessionResponse{ConfigOptions: modelConfig("gpt-old")}, nil
		case client.MethodSessionSetConfig:
			var req client.SetSessionConfigOptionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if !sawResume {
				os.Exit(0)
			}
			if req.SessionID != "remote-reconnect" || req.ConfigID != "model" || fmt.Sprint(req.Value) != "gpt-next" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/set_config_option request"}
			}
			return client.SetSessionConfigOptionResponse{ConfigOptions: modelConfig("gpt-next")}, nil
		case client.MethodSessionPrompt:
			var req client.PromptRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if !sawResume {
				os.Exit(0)
			}
			if req.SessionID != "remote-reconnect" || len(req.Prompt) == 0 {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/prompt request"}
			}
			return client.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
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

func TestManagerACPDurableHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_HELPER") != "controller-durable" {
		return
	}
	modelConfig := func(current string) []client.SessionConfigOption {
		return []client.SessionConfigOption{{
			ID:           "model",
			Name:         "Model",
			Category:     "model",
			Type:         "select",
			CurrentValue: current,
			Options: []client.SessionConfigSelectOption{
				{Value: "gpt-old", Name: "GPT Old"},
			},
		}}
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case client.MethodInitialize:
			return client.InitializeResponse{ProtocolVersion: 1}, nil
		case client.MethodSessionNew:
			return client.NewSessionResponse{
				SessionID:     "remote-durable",
				ConfigOptions: modelConfig("gpt-old"),
			}, nil
		case client.MethodSessionPrompt:
			var req client.PromptRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if req.SessionID != "remote-durable" || len(req.Prompt) == 0 {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/prompt request"}
			}
			return client.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
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

func TestManagerACPControllerReapplyHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_HELPER") != "controller-reapply" {
		return
	}
	modelConfig := func(current string) []client.SessionConfigOption {
		return []client.SessionConfigOption{{
			ID:           "model",
			Name:         "Model",
			Category:     "model",
			Type:         "select",
			CurrentValue: current,
			Options: []client.SessionConfigSelectOption{
				{Value: "gpt-old", Name: "GPT Old"},
				{Value: "gpt-next", Name: "GPT Next"},
			},
		}}
	}
	configWithEffort := func(model string, effort string) []client.SessionConfigOption {
		return append(modelConfig(model), client.SessionConfigOption{
			ID:           "effort",
			Name:         "Effort",
			Category:     "model",
			Type:         "select",
			CurrentValue: effort,
			Options: []client.SessionConfigSelectOption{
				{Value: "low", Name: "Low"},
				{Value: "high", Name: "High"},
			},
		})
	}
	var sawResume bool
	var modelApplied bool
	var effortApplied bool
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
		case client.MethodSessionNew:
			return client.NewSessionResponse{
				SessionID:     "remote-reapply",
				ConfigOptions: modelConfig("gpt-old"),
			}, nil
		case client.MethodSessionResume:
			var req client.ResumeSessionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if req.SessionID != "remote-reapply" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/resume id"}
			}
			sawResume = true
			return client.ResumeSessionResponse{ConfigOptions: modelConfig("gpt-old")}, nil
		case client.MethodSessionSetConfig:
			var req client.SetSessionConfigOptionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if req.SessionID != "remote-reapply" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/set_config_option session"}
			}
			switch req.ConfigID {
			case "model":
				if fmt.Sprint(req.Value) != "gpt-next" {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected model value"}
				}
				modelApplied = true
				return client.SetSessionConfigOptionResponse{ConfigOptions: configWithEffort("gpt-next", "low")}, nil
			case "effort":
				if fmt.Sprint(req.Value) != "high" || !modelApplied {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected effort value"}
				}
				effortApplied = true
				return client.SetSessionConfigOptionResponse{ConfigOptions: configWithEffort("gpt-next", "high")}, nil
			default:
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected config id"}
			}
		case client.MethodSessionPrompt:
			var req client.PromptRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if !sawResume {
				os.Exit(0)
			}
			if req.SessionID != "remote-reapply" || len(req.Prompt) == 0 || !modelApplied || !effortApplied {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "prompt before reapplying model/effort"}
			}
			return client.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
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

func TestManagerACPControllerModeReapplyHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_HELPER") != "controller-mode-reapply" {
		return
	}
	modeConfig := func(current string) []client.SessionConfigOption {
		return []client.SessionConfigOption{{
			ID:           "mode",
			Name:         "Mode",
			Category:     "mode",
			Type:         "select",
			CurrentValue: current,
			Options: []client.SessionConfigSelectOption{
				{Value: "ask", Name: "Ask"},
				{Value: "code", Name: "Code"},
			},
		}}
	}
	modeState := func(current string) *client.SessionModeState {
		return &client.SessionModeState{
			CurrentModeID: current,
			AvailableModes: []client.SessionMode{
				{ID: "ask", Name: "Ask"},
				{ID: "code", Name: "Code"},
			},
		}
	}
	var sawResume bool
	var modeApplied bool
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
		case client.MethodSessionNew:
			return client.NewSessionResponse{
				SessionID:     "remote-mode-reapply",
				ConfigOptions: modeConfig("ask"),
				Modes:         modeState("ask"),
			}, nil
		case client.MethodSessionResume:
			var req client.ResumeSessionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if req.SessionID != "remote-mode-reapply" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/resume id"}
			}
			sawResume = true
			return client.ResumeSessionResponse{
				ConfigOptions: modeConfig(""),
				Modes:         modeState(""),
			}, nil
		case client.MethodSessionSetConfig:
			var req client.SetSessionConfigOptionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if req.SessionID != "remote-mode-reapply" || req.ConfigID != "mode" || fmt.Sprint(req.Value) != "code" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/set_config_option request"}
			}
			modeApplied = true
			return client.SetSessionConfigOptionResponse{ConfigOptions: modeConfig("code")}, nil
		case client.MethodSessionSetMode:
			return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected legacy session/set_mode request"}
		case client.MethodSessionPrompt:
			var req client.PromptRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if !sawResume {
				os.Exit(0)
			}
			if req.SessionID != "remote-mode-reapply" || len(req.Prompt) == 0 || !sawResume || !modeApplied {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "prompt before reapplying mode"}
			}
			return client.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
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

func controllerModeIDs(in []controller.ControllerMode) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		out = append(out, item.ID)
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
