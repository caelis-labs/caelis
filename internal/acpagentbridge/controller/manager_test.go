package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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

func TestNormalizeACPUpdateEventPreservesContentChunkMessageIDAndMeta(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(client.TextContent{Type: "text", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"vendor": map[string]any{"trace": "abc"}}
	event := normalizeACPUpdateEvent(func() time.Time { return time.Unix(0, 0) }, session.ControllerBinding{
		Kind:         session.ControllerKindACP,
		ControllerID: "codex",
		Label:        "codex",
	}, "remote-1", "turn-1", client.ContentChunk{
		SessionUpdate: client.UpdateAgentMessage,
		Content:       raw,
		MessageID:     "msg-1",
		Meta:          meta,
	})
	if event == nil || event.Protocol == nil || event.Protocol.Update == nil {
		t.Fatalf("event = %#v, want protocol update", event)
	}
	if event.Protocol.Update.MessageID != "msg-1" {
		t.Fatalf("Protocol.Update.MessageID = %q, want msg-1", event.Protocol.Update.MessageID)
	}
	vendor, _ := event.Protocol.Update.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "abc" {
		t.Fatalf("Protocol.Update.Meta = %#v, want vendor trace", event.Protocol.Update.Meta)
	}
	meta["vendor"].(map[string]any)["trace"] = "mutated"
	if vendor["trace"] != "abc" {
		t.Fatalf("Protocol.Update.Meta aliased input meta = %#v", event.Protocol.Update.Meta)
	}
	if event.Meta != nil {
		t.Fatalf("Event.Meta = %#v, want no message id side channel", event.Meta)
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
	update := session.ProtocolUpdateOf(event)
	if event == nil || event.Protocol == nil || update == nil {
		t.Fatalf("event = %#v, want structured tool update", event)
	}
	if got := update.Kind; got != "fetch" {
		t.Fatalf("tool name = %q, want ACP kind", got)
	}
	if got := update.Title; got != "Searching for: weather: Shanghai, China" {
		t.Fatalf("tool title = %q, want ACP title", got)
	}
	if got := update.Kind; got != "fetch" {
		t.Fatalf("tool kind = %q, want fetch", got)
	}
	if got := update.RawInput["query"]; got != "weather: Shanghai, China" {
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
		Content: []client.ToolCallContent{{
			Type:       "terminal",
			TerminalID: "call-1",
			Content:    client.TextContent{Type: "text", Text: "line\n"},
		}},
	})

	if event == nil || event.Protocol == nil || event.Protocol.Update == nil {
		t.Fatalf("event = %#v, want protocol update", event)
	}
	content := session.ProtocolToolCallContentOf(event.Protocol.Update)
	if len(content) != 1 || content[0].TerminalID != "call-1" || schema.ExtractTextValue(content[0].Content) != "line\n" {
		t.Fatalf("Protocol.Update.Content = %#v, want terminal content", event.Protocol.Update.Content)
	}
}

func TestTranslateApprovalRequestPreservesToolRawInput(t *testing.T) {
	t.Parallel()

	req, err := translateApprovalRequest(session.Session{
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
			RawOutput: map[string]any{"preview": "ok"},
			Content: []client.ToolCallContent{{
				Type: "content", Content: client.TextContent{Type: "text", Text: "approval detail"},
			}},
		},
		Options: []client.PermissionOption{{OptionID: "allow_once", Name: "Allow", Kind: "allow_once"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.ToolCall.RawInput["command"] != "pwd" {
		t.Fatalf("ToolCall.RawInput[command] = %#v", req.ToolCall.RawInput["command"])
	}
	if req.ToolCall.Name != "RunCommand" {
		t.Fatalf("ToolCall.Name = %q, want RunCommand", req.ToolCall.Name)
	}
	if req.ToolCall.RawInput["workdir"] != "/tmp/project" {
		t.Fatalf("ToolCall.RawInput[workdir] = %#v", req.ToolCall.RawInput["workdir"])
	}
	if req.ToolCall.RawOutput["preview"] != "ok" || len(req.ToolCall.Content) != 1 {
		t.Fatalf("ToolCall raw output/content = %#v/%#v, want preserved", req.ToolCall.RawOutput, req.ToolCall.Content)
	}
	if len(req.Options) != 1 || req.Options[0].ID != "allow_once" || req.Options[0].Kind != "allow_once" {
		t.Fatalf("Options = %#v, want exact allow option", req.Options)
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
	if targetTool == nil || targetTool.Visibility != session.VisibilityUIOnly || targetTool.Protocol == nil || session.ProtocolUpdateOf(targetTool) == nil {
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
		configOptions: []ControllerConfigOption{
			{ID: "model", Name: "Model", CurrentValue: "startup-model"},
			{ID: "reasoning", Name: "Reasoning", CurrentValue: "medium"},
		},
		mode: "default",
		modeOptions: []ControllerMode{
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
		configOptions: []ControllerConfigOption{
			{
				ID:           "model",
				Name:         "Model",
				CurrentValue: "gpt-5.5",
				Options: []ControllerConfigChoice{
					{Value: "gpt-5.5", Name: "GPT-5.5"},
					{Value: "gpt-5.4", Name: "gpt-5.4"},
				},
			},
			{
				ID:           "reasoning_effort",
				Name:         "Reasoning Effort",
				CurrentValue: "xhigh",
				Options: []ControllerConfigChoice{
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
		configOptions: []ControllerConfigOption{
			{
				ID:           "session-mode",
				Name:         "Session Mode",
				Category:     "mode",
				CurrentValue: "code",
				Options: []ControllerConfigChoice{
					{Value: "ask", Name: "Ask"},
					{Value: "code", Name: "Code"},
				},
			},
			{
				ID:           "thinking",
				Name:         "Thinking",
				Category:     "thought_level",
				CurrentValue: "high",
				Options: []ControllerConfigChoice{
					{Value: "low", Name: "Low"},
					{Value: "high", Name: "High"},
				},
			},
		},
		mode: "ask",
		modeOptions: []ControllerMode{
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
		configOptions: []ControllerConfigOption{
			{
				ID:           "effort",
				Name:         "Effort",
				Category:     "model",
				CurrentValue: "high",
				Options: []ControllerConfigChoice{
					{Value: "low", Name: "Low"},
					{Value: "high", Name: "High"},
				},
			},
			{
				ID:           "model",
				Name:         "Model",
				Category:     "model",
				CurrentValue: "gpt-next",
				Options: []ControllerConfigChoice{
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
		configOptions: []ControllerConfigOption{
			{
				ID:           "model",
				Name:         "Model",
				CurrentValue: "gpt-5.5",
				Options: []ControllerConfigChoice{
					{Value: "gpt-5.5", Name: "GPT-5.5"},
					{Value: "gpt-5.4", Name: "gpt-5.4"},
				},
			},
			{
				ID:           "reasoning_effort",
				Name:         "Reasoning Effort",
				CurrentValue: "xhigh",
				Options: []ControllerConfigChoice{
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
		configOptions: []ControllerConfigOption{
			{
				ID: "model",
				Options: []ControllerConfigChoice{
					{Value: "gpt-5.5", Name: "GPT-5.5"},
					{Value: "gpt-5.4", Name: "gpt-5.4"},
				},
			},
			{
				ID: "reasoning_effort",
				Options: []ControllerConfigChoice{
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
		configOptions: []ControllerConfigOption{{
			ID:           "model",
			Name:         "Model",
			CurrentValue: "gpt-5.4",
			Options: []ControllerConfigChoice{
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

func TestControllerRunStatusReplacesConfigChoicesAfterFullUpdate(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []ControllerConfigOption{{
			ID:           "model",
			Name:         "Model",
			CurrentValue: "gpt-5.5",
			Options: []ControllerConfigChoice{
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
	if got := controllerChoiceValues(status.ModelOptions); !equalStrings(got, []string{"gpt-5.4"}) {
		t.Fatalf("model options = %#v, want full replacement choices", got)
	}
}

func TestControllerRunFullConfigUpdateRemovesDerivedModes(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{configOptions: []ControllerConfigOption{{
		ID: "mode", Name: "Mode", Type: "select", Category: "mode", CurrentValue: "review",
		Options: []ControllerConfigChoice{{Value: "review", Name: "Review"}, {Value: "code", Name: "Code"}},
	}}}, 0)
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(2, 0) }, client.ConfigOptionUpdate{
		SessionUpdate: client.UpdateConfigOption,
		ConfigOptions: []client.SessionConfigOption{{
			ID: "tone", Name: "Tone", Type: "select", CurrentValue: "concise",
			Options: []client.SessionConfigSelectOption{{Value: "concise"}, {Value: "detailed"}},
		}},
	})

	status := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if len(status.ModeOptions) != 0 {
		t.Fatalf("mode options = %#v, want removed config-derived modes cleared", status.ModeOptions)
	}
	if len(status.ConfigOptions) != 1 || status.ConfigOptions[0].ID != "tone" {
		t.Fatalf("config options = %#v, want full replacement tone selector", status.ConfigOptions)
	}
}

func TestManagerLifecycleUsesSingleClientStarterSeam(t *testing.T) {
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
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
		cfg subagent.AgentConfig,
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
			configOptions: []ControllerConfigOption{{
				ID:           "model",
				Name:         "Model",
				CurrentValue: "gpt-test",
			}},
			mode: "default",
			modeOptions: []ControllerMode{{
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
	if err := manager.Detach(context.Background(), controller.DetachRequest{
		Session: parentSession, ParticipantID: participant.ID, DelegationID: participant.DelegationID,
		AttachmentGeneration: participant.AttachmentGeneration,
	}); err != nil {
		t.Fatalf("Detach() error = %v", err)
	}
	manager.mu.RLock()
	_, stillAttached := manager.participants[participantKey(parentSession.SessionID, participant.ID)]
	manager.mu.RUnlock()
	if stillAttached {
		t.Fatal("participant still attached after Detach")
	}
	if starts != 2 {
		t.Fatalf("client starts = %d, want 2 (controller + participant)", starts)
	}
}

func TestManagerRejectsImagePromptWithoutACPImageCapability(t *testing.T) {
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
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
		_ subagent.AgentConfig,
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
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
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
		cfg subagent.AgentConfig,
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
	status, err := manager.SetControllerModel(ctx, SetControllerModelRequest{
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
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
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
		cfg subagent.AgentConfig,
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
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
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
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
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
		cfg subagent.AgentConfig,
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
	status, err := manager.SetControllerModel(ctx, SetControllerModelRequest{
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
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
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
		cfg subagent.AgentConfig,
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
	status, err := manager.SetControllerMode(ctx, SetControllerModeRequest{
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
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
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
		subagent.AgentConfig,
		string,
		func(client.UpdateEnvelope),
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		starts++
		return nil, "", controllerClientState{}, nil
	}
	run := &controllerRun{
		parentSessionID: "parent",
		cfg:             subagent.AgentConfig{Name: "helper", Command: "helper-acp"},
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

	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
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
		cfg subagent.AgentConfig,
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

func TestManagerDetachMatchesDelegationAndAttachmentGeneration(t *testing.T) {
	t.Parallel()
	manager := &Manager{participants: map[participantRunKey]*participantRun{}}
	key := participantKey("session-a", "shared")
	manager.participants[key] = &participantRun{parentSessionID: "session-a", binding: session.ParticipantBinding{
		ID: "shared", DelegationID: "delegation-new", AttachmentGeneration: "generation-new",
	}}
	if err := manager.Detach(context.Background(), controller.DetachRequest{
		SessionRef: session.SessionRef{SessionID: "session-a"}, ParticipantID: "shared", DelegationID: "delegation-old", AttachmentGeneration: "generation-old",
	}); err != nil {
		t.Fatal(err)
	}
	if manager.participants[key] == nil {
		t.Fatal("stale detach removed the winning live endpoint")
	}
	if err := manager.Detach(context.Background(), controller.DetachRequest{
		SessionRef: session.SessionRef{SessionID: "session-a"}, ParticipantID: "shared",
	}); err != nil {
		t.Fatal(err)
	}
	if manager.participants[key] == nil {
		t.Fatal("empty conditional identity acted as a wildcard and removed the live endpoint")
	}
	if err := manager.Detach(context.Background(), controller.DetachRequest{
		SessionRef: session.SessionRef{SessionID: "session-a"}, ParticipantID: "shared", DelegationID: "delegation-new", AttachmentGeneration: "generation-new",
	}); err != nil {
		t.Fatal(err)
	}
	if manager.participants[key] != nil {
		t.Fatal("matching detach left the endpoint attached")
	}
}

func TestParticipantRunRejectsOverlappingPrompts(t *testing.T) {
	t.Parallel()
	run := &participantRun{id: "participant-busy"}
	first := newTurnHandle(nil)
	if err := run.beginPrompt(controller.ParticipantPromptRequest{TurnID: "turn-1", ParticipantID: run.id}, first); err != nil {
		t.Fatal(err)
	}
	if err := run.beginPrompt(controller.ParticipantPromptRequest{TurnID: "turn-2", ParticipantID: run.id}, newTurnHandle(nil)); err == nil {
		t.Fatal("overlapping participant prompt was allowed to overwrite active turn state")
	}
	run.finishPrompt()
	if err := run.beginPrompt(controller.ParticipantPromptRequest{TurnID: "turn-3", ParticipantID: run.id}, newTurnHandle(nil)); err != nil {
		t.Fatalf("prompt after completion remained busy: %v", err)
	}
}

func TestManagerRejectsContradictoryParticipantSessionIdentity(t *testing.T) {
	t.Parallel()
	manager := &Manager{participants: map[participantRunKey]*participantRun{}}
	ref := session.SessionRef{SessionID: "session-ref"}
	active := session.Session{SessionRef: session.SessionRef{SessionID: "session-body"}}
	if _, err := manager.Attach(context.Background(), controller.AttachRequest{SessionRef: ref, Session: active}); err == nil {
		t.Fatal("Attach accepted contradictory session ids")
	}
	if _, err := manager.PromptParticipant(context.Background(), controller.ParticipantPromptRequest{
		SessionRef: ref, Session: active, ParticipantID: "p", Input: "hello",
	}); err == nil {
		t.Fatal("PromptParticipant accepted contradictory session ids")
	}
	if err := manager.Detach(context.Background(), controller.DetachRequest{
		SessionRef: ref, Session: active, ParticipantID: "p",
	}); err == nil {
		t.Fatal("Detach accepted contradictory session ids")
	}
}

func TestManagerScopesParticipantIdentityByParentSession(t *testing.T) {
	t.Parallel()
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{Name: "helper", Command: "helper-acp"}})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Config{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	manager.startClient = func(
		context.Context, string, subagent.AgentConfig, string,
		func(client.UpdateEnvelope),
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		starts++
		return nil, fmt.Sprintf("remote-%d", starts), controllerClientState{}, nil
	}
	binding := session.ParticipantBinding{ID: "shared", DelegationID: "delegation-shared", AttachmentGeneration: "durable-generation"}
	sessionA := session.Session{SessionRef: session.SessionRef{SessionID: "session-a"}}
	sessionB := session.Session{SessionRef: session.SessionRef{SessionID: "session-b"}}
	attachedA, err := manager.Attach(context.Background(), controller.AttachRequest{Session: sessionA, Agent: "helper", Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	attachedB, err := manager.Attach(context.Background(), controller.AttachRequest{Session: sessionB, Agent: "helper", Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	if starts != 2 || attachedA.SessionID == attachedB.SessionID || attachedA.AttachmentGeneration == attachedB.AttachmentGeneration {
		t.Fatalf("starts/bindings = %d/%#v/%#v, want isolated live endpoints", starts, attachedA, attachedB)
	}
	if err := manager.Detach(context.Background(), controller.DetachRequest{
		Session: sessionB, ParticipantID: binding.ID, DelegationID: binding.DelegationID,
		AttachmentGeneration: attachedB.AttachmentGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	manager.mu.RLock()
	runA := manager.participants[participantKey(sessionA.SessionID, binding.ID)]
	runB := manager.participants[participantKey(sessionB.SessionID, binding.ID)]
	manager.mu.RUnlock()
	if runA == nil || runB != nil {
		t.Fatalf("session-scoped participants after detach = a:%v b:%v", runA != nil, runB != nil)
	}
}

func TestManagerNewClientAlwaysRotatesAttachmentGeneration(t *testing.T) {
	t.Parallel()
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{Name: "helper", Command: "helper-acp"}})
	if err != nil {
		t.Fatal(err)
	}
	newManager := func(remote string) *Manager {
		manager, managerErr := NewManager(Config{Registry: registry})
		if managerErr != nil {
			t.Fatal(managerErr)
		}
		manager.startClient = func(
			context.Context, string, subagent.AgentConfig, string,
			func(client.UpdateEnvelope),
			func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
		) (*client.Client, string, controllerClientState, error) {
			return nil, remote, controllerClientState{}, nil
		}
		return manager
	}
	parent := session.Session{SessionRef: session.SessionRef{SessionID: "generation-session"}}
	persisted := session.ParticipantBinding{ID: "shared", DelegationID: "delegation", AttachmentGeneration: "old-generation"}
	firstManager := newManager("remote-first")
	first, err := firstManager.Attach(context.Background(), controller.AttachRequest{Session: parent, Agent: "helper", Binding: persisted})
	if err != nil {
		t.Fatal(err)
	}
	secondManager := newManager("remote-second")
	second, err := secondManager.Attach(context.Background(), controller.AttachRequest{Session: parent, Agent: "helper", Binding: first})
	if err != nil {
		t.Fatal(err)
	}
	if first.AttachmentGeneration == persisted.AttachmentGeneration || second.AttachmentGeneration == first.AttachmentGeneration {
		t.Fatalf("attachment generations were reused: persisted=%q first=%q second=%q", persisted.AttachmentGeneration, first.AttachmentGeneration, second.AttachmentGeneration)
	}
	if err := secondManager.Detach(context.Background(), controller.DetachRequest{
		Session: parent, ParticipantID: persisted.ID, DelegationID: persisted.DelegationID,
		AttachmentGeneration: first.AttachmentGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	secondManager.mu.RLock()
	stillAttached := secondManager.participants[participantKey(parent.SessionID, persisted.ID)] != nil
	secondManager.mu.RUnlock()
	if !stillAttached {
		t.Fatal("stale generation detached the restarted participant endpoint")
	}
}

func TestManagerAttachResetsParticipantCheckpointForFreshRemoteSession(t *testing.T) {
	t.Parallel()

	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
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
		_ subagent.AgentConfig,
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
	acpClient, remoteSessionID, _, err := manager.startACPClient(ctx, t.TempDir(), subagent.AgentConfig{
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

	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
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
		cfg subagent.AgentConfig,
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

func TestControllerRunPublishesACPSourceEvent(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	run := &controllerRun{
		remoteSessionID: "remote-1",
		binding: session.ControllerBinding{
			Kind:         session.ControllerKindACP,
			ControllerID: "ctrl-1",
			Label:        "Remote",
			EpochID:      "epoch-1",
		},
		turnID:     "turn-1",
		turnStream: true,
		handle:     handle,
	}
	title := "READ"
	status := schema.ToolStatusInProgress
	line := 7
	raw := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","title":"READ","status":"in_progress","locations":[{"path":"main.go","line":7}],"_meta":{"vendor":{"trace":"abc"}}}`)
	run.handleUpdate(func() time.Time { return time.Unix(10, 0) }, client.UpdateEnvelope{
		SessionID: "remote-1",
		Raw:       raw,
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "call-1",
			Title:         &title,
			Status:        &status,
			Locations:     []client.ToolCallLocation{{Path: "main.go", Line: &line}},
			Meta:          map[string]any{"vendor": map[string]any{"trace": "abc"}},
		},
	})

	handle.finish()

	var events []acpbridge.SourceEvent
	for event, err := range handle.SourceEvents() {
		if err != nil {
			t.Fatalf("source error = %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("source events len = %d, want 1", len(events))
	}
	if events[0].Canonical == nil {
		t.Fatal("source canonical event is nil")
	}
	if events[0].ACP == nil {
		t.Fatal("source ACP envelope is nil")
	}
	update, ok := events[0].ACP.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("ACP update = %T, want ToolCallUpdate", events[0].ACP.Update)
	}
	if update.ToolCallID != "call-1" || len(update.Locations) != 1 || update.Locations[0].Path != "main.go" {
		t.Fatalf("ACP tool update = %#v, want preserved call and location", update)
	}
	if vendor, ok := update.Meta["vendor"].(map[string]any); !ok || vendor["trace"] != "abc" {
		t.Fatalf("ACP tool meta = %#v, want vendor trace", update.Meta)
	}
}

func TestControllerRunStripsConsoleFenceAtUpdateIngress(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	run := &controllerRun{
		remoteSessionID: "remote-1",
		binding: session.ControllerBinding{
			Kind:         session.ControllerKindACP,
			ControllerID: "ctrl-1",
			Label:        "Remote",
		},
		turnID:     "turn-1",
		turnStream: true,
		handle:     handle,
	}
	fenced := "```console\ndiff --git a/file b/file\n```\n"
	want := "diff --git a/file b/file\n"
	run.handleUpdate(func() time.Time { return time.Unix(10, 0) }, client.UpdateEnvelope{
		SessionID: "remote-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "call-1",
			Kind:          testStringPtr("execute"),
			Status:        testStringPtr(schema.ToolStatusCompleted),
			RawOutput:     map[string]any{"stdout": fenced},
			Content: []client.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "call-1",
				Content:    client.TextContent{Type: "text", Text: fenced},
			}},
		},
	})
	handle.finish()

	var events []acpbridge.SourceEvent
	for event, err := range handle.SourceEvents() {
		if err != nil {
			t.Fatalf("source error = %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("source events len = %d, want 1", len(events))
	}
	canonical := events[0].Canonical
	canonicalUpdate := session.ProtocolUpdateOf(canonical)
	if canonical == nil || canonicalUpdate == nil {
		t.Fatalf("canonical event = %#v, want protocol tool update", canonical)
	}
	if got := canonicalUpdate.RawOutput["stdout"]; got != fenced {
		t.Fatalf("canonical raw output stdout = %#v, want original %q", got, fenced)
	}
	canonicalContent := session.ProtocolToolCallContentOf(canonicalUpdate)
	if got := schema.ExtractTextValue(canonicalContent[0].Content); got != want {
		t.Fatalf("canonical terminal content = %q, want %q", got, want)
	}
	acpUpdate, ok := events[0].ACP.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("ACP update = %T, want ToolCallUpdate", events[0].ACP.Update)
	}
	rawOutput, _ := acpUpdate.RawOutput.(map[string]any)
	if got := rawOutput["stdout"]; got != fenced {
		t.Fatalf("ACP raw output stdout = %#v, want original %q", got, fenced)
	}
	if got := schema.ExtractTextValue(acpUpdate.Content[0].Content); got != want {
		t.Fatalf("ACP terminal content = %q, want %q", got, want)
	}
}

func TestControllerRunStripsConsoleFenceFromExecuteContentAtUpdateIngress(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	run := &controllerRun{
		remoteSessionID: "remote-1",
		binding: session.ControllerBinding{
			Kind:         session.ControllerKindACP,
			ControllerID: "ctrl-1",
			Label:        "Remote",
		},
		turnID:     "turn-1",
		turnStream: true,
		handle:     handle,
	}
	fenced := "```console\nclean\n```\n"
	want := "clean\n"
	kind := schema.ToolKindExecute
	run.handleUpdate(func() time.Time { return time.Unix(10, 0) }, client.UpdateEnvelope{
		SessionID: "remote-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "call-1",
			Kind:          &kind,
			Status:        testStringPtr(schema.ToolStatusCompleted),
			Content: []client.ToolCallContent{{
				Type:    "content",
				Content: client.TextContent{Type: "text", Text: fenced},
			}},
		},
	})
	handle.finish()

	var events []acpbridge.SourceEvent
	for event, err := range handle.SourceEvents() {
		if err != nil {
			t.Fatalf("source error = %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("source events len = %d, want 1", len(events))
	}
	canonical := events[0].Canonical
	canonicalUpdate := session.ProtocolUpdateOf(canonical)
	if canonical == nil || canonicalUpdate == nil {
		t.Fatalf("canonical event = %#v, want protocol tool update", canonical)
	}
	canonicalContent := session.ProtocolToolCallContentOf(canonicalUpdate)
	if got := schema.ExtractTextValue(canonicalContent[0].Content); got != want {
		t.Fatalf("canonical execute content = %q, want %q", got, want)
	}
	acpUpdate, ok := events[0].ACP.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("ACP update = %T, want ToolCallUpdate", events[0].ACP.Update)
	}
	if got := schema.ExtractTextValue(acpUpdate.Content[0].Content); got != want {
		t.Fatalf("ACP execute content = %q, want %q", got, want)
	}
}

func TestControllerRunStripsConsoleFenceFromClaudeBashContentAtUpdateIngress(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	run := &controllerRun{
		remoteSessionID: "remote-1",
		binding: session.ControllerBinding{
			Kind:         session.ControllerKindACP,
			ControllerID: "ctrl-1",
			Label:        "Claude",
		},
		turnID:     "turn-1",
		turnStream: true,
		handle:     handle,
	}
	fenced := "```console\nFri Jun 26 14:35:27 CST 2026\n```\n"
	want := "Fri Jun 26 14:35:27 CST 2026\n"
	run.handleUpdate(func() time.Time { return time.Unix(10, 0) }, client.UpdateEnvelope{
		SessionID: "remote-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "call-1",
			Status:        testStringPtr(schema.ToolStatusCompleted),
			RawOutput:     "Fri Jun 26 14:35:27 CST 2026",
			Content: []client.ToolCallContent{{
				Type:    "content",
				Content: client.TextContent{Type: "text", Text: fenced},
			}},
			Meta: map[string]any{
				"claudeCode": map[string]any{
					"toolName": "Bash",
				},
			},
		},
	})
	handle.finish()

	var events []acpbridge.SourceEvent
	for event, err := range handle.SourceEvents() {
		if err != nil {
			t.Fatalf("source error = %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("source events len = %d, want 1", len(events))
	}
	canonical := events[0].Canonical
	canonicalUpdate := session.ProtocolUpdateOf(canonical)
	if canonical == nil || canonicalUpdate == nil {
		t.Fatalf("canonical event = %#v, want protocol tool update", canonical)
	}
	canonicalContent := session.ProtocolToolCallContentOf(canonicalUpdate)
	if got := schema.ExtractTextValue(canonicalContent[0].Content); got != want {
		t.Fatalf("canonical claude bash content = %q, want %q", got, want)
	}
	acpUpdate, ok := events[0].ACP.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("ACP update = %T, want ToolCallUpdate", events[0].ACP.Update)
	}
	if got := schema.ExtractTextValue(acpUpdate.Content[0].Content); got != want {
		t.Fatalf("ACP claude bash content = %q, want %q", got, want)
	}
}

func TestParticipantPassthroughOnlyACPUpdatePreservesScope(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	run := &participantRun{
		remoteSessionID: "remote-participant",
		agent:           "otto",
		binding: session.ParticipantBinding{
			ID:            "participant-1",
			Kind:          session.ParticipantKindACP,
			Role:          "reviewer",
			Label:         "@otto",
			ControllerRef: "epoch-1",
			SessionID:     "remote-participant",
		},
		turnID:     "turn-1",
		turnStream: true,
		handle:     handle,
	}
	raw := json.RawMessage(`{"sessionUpdate":"vendor/current_mode_update","mode":"review"}`)
	run.handleUpdate(func() time.Time { return time.Unix(10, 0) }, client.UpdateEnvelope{
		SessionID: "remote-participant",
		Raw:       raw,
		Update: client.RawUpdate{
			SessionUpdate: "vendor/current_mode_update",
			Raw:           raw,
		},
	})

	handle.finish()

	var events []acpbridge.SourceEvent
	for event, err := range handle.SourceEvents() {
		if err != nil {
			t.Fatalf("source error = %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("source events len = %d, want 1", len(events))
	}
	if events[0].Canonical != nil {
		t.Fatalf("canonical event = %#v, want nil for passthrough-only update", events[0].Canonical)
	}
	if events[0].ACP == nil {
		t.Fatal("source ACP envelope is nil")
	}
	env := events[0].ACP
	if env.Scope != eventstream.ScopeParticipant || env.ScopeID != "participant-1" || env.ParticipantID != "participant-1" {
		t.Fatalf("ACP scope = scope:%q scopeID:%q participantID:%q, want participant participant-1", env.Scope, env.ScopeID, env.ParticipantID)
	}
	if env.Actor != "@otto" || env.TurnID != "turn-1" {
		t.Fatalf("ACP actor/turn = %q/%q, want @otto/turn-1", env.Actor, env.TurnID)
	}
	update, ok := env.Update.(schema.RawUpdate)
	if !ok {
		t.Fatalf("ACP update = %T, want RawUpdate", env.Update)
	}
	if update.SessionUpdate != "vendor/current_mode_update" {
		t.Fatalf("SessionUpdate = %q, want vendor/current_mode_update", update.SessionUpdate)
	}
}

func TestTurnHandleSourceEventsDoNotDropBurst(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	for i := 0; i < 128; i++ {
		handle.publishEvent(&session.Event{ID: fmt.Sprintf("event-%d", i), Type: session.EventTypeAssistant})
	}
	handle.finish()

	count := 0
	for event, err := range handle.SourceEvents() {
		if err != nil {
			t.Fatalf("source error = %v", err)
		}
		if event.Canonical != nil {
			count++
		}
	}
	if count != 128 {
		t.Fatalf("SourceEvents received %d canonical events, want 128", count)
	}
}

func controllerChoiceValues(in []ControllerConfigChoice) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		out = append(out, item.Value)
	}
	return out
}

func controllerModeIDs(in []ControllerMode) []string {
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
