package acp

import (
	"encoding/json"
	"testing"
	"time"

	sdkacpclient "github.com/OnslaughtSnail/caelis/acp/client"
	sdkcontroller "github.com/OnslaughtSnail/caelis/sdk/controller"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestContentChunkTextPreservesStreamWhitespace(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(sdkacpclient.TextChunk{Type: "text", Text: "hello "})
	if err != nil {
		t.Fatal(err)
	}

	got := contentChunkText(sdkacpclient.ContentChunk{
		SessionUpdate: sdkacpclient.UpdateAgentMessage,
		Content:       raw,
	})
	if got != "hello " {
		t.Fatalf("contentChunkText() = %q, want trailing space preserved", got)
	}
}

func TestNormalizeACPUpdateEventKeepsCodexWebSearchToolIdentity(t *testing.T) {
	t.Parallel()

	event := normalizeACPUpdateEvent(func() time.Time { return time.Unix(0, 0) }, sdksession.ControllerBinding{
		Kind:         sdksession.ControllerKindACP,
		ControllerID: "codex",
		Label:        "codex",
	}, "remote-1", "turn-1", sdkacpclient.ToolCallUpdate{
		SessionUpdate: sdkacpclient.UpdateToolCallState,
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

func TestControllerRunApplyStartupStatePreservesPreSessionUpdates(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(1, 0) }, sdkacpclient.AvailableCommandsUpdate{
		SessionUpdate: sdkacpclient.UpdateAvailableCmds,
		AvailableCommands: []map[string]any{
			{"name": "/search", "description": "remote search"},
		},
	})
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(2, 0) }, sdkacpclient.ConfigOptionUpdate{
		SessionUpdate: sdkacpclient.UpdateConfigOption,
		ConfigOptions: []sdkacpclient.SessionConfigOption{{
			ID:           "model",
			Name:         "Model",
			CurrentValue: "live-model",
		}},
	})
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(3, 0) }, sdkacpclient.CurrentModeUpdate{
		SessionUpdate: sdkacpclient.UpdateCurrentMode,
		CurrentModeID: "review",
	})

	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []sdkcontroller.ControllerConfigOption{
			{ID: "model", Name: "Model", CurrentValue: "startup-model"},
			{ID: "reasoning", Name: "Reasoning", CurrentValue: "medium"},
		},
		mode: "default",
		modeOptions: []sdkcontroller.ControllerMode{
			{ID: "default", Name: "Default"},
			{ID: "review", Name: "Review"},
		},
		agentLabel: "Remote ACP",
	}, 7)

	status := run.controllerStatusLocked(sdksession.SessionRef{SessionID: "parent"})
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

func TestControllerRunStatusUsesConfigOptionsForModelAndEffort(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []sdkcontroller.ControllerConfigOption{
			{
				ID:           "model",
				Name:         "Model",
				CurrentValue: "gpt-5.5",
				Options: []sdkcontroller.ControllerConfigChoice{
					{Value: "gpt-5.5", Name: "GPT-5.5"},
					{Value: "gpt-5.4", Name: "gpt-5.4"},
				},
			},
			{
				ID:           "reasoning_effort",
				Name:         "Reasoning Effort",
				CurrentValue: "xhigh",
				Options: []sdkcontroller.ControllerConfigChoice{
					{Value: "low", Name: "Low"},
					{Value: "medium", Name: "Medium"},
					{Value: "high", Name: "High"},
					{Value: "xhigh", Name: "Xhigh"},
				},
			},
		},
	}, 0)

	status := run.controllerStatusLocked(sdksession.SessionRef{SessionID: "parent"})
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
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(1, 0) }, sdkacpclient.ConfigOptionUpdate{
		SessionUpdate: sdkacpclient.UpdateConfigOption,
		ConfigOptions: []sdkacpclient.SessionConfigOption{
			{ID: "model", Name: "Model"},
			{ID: "reasoning_effort", Name: "Reasoning Effort"},
		},
	})

	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []sdkcontroller.ControllerConfigOption{
			{
				ID:           "model",
				Name:         "Model",
				CurrentValue: "gpt-5.5",
				Options: []sdkcontroller.ControllerConfigChoice{
					{Value: "gpt-5.5", Name: "GPT-5.5"},
					{Value: "gpt-5.4", Name: "gpt-5.4"},
				},
			},
			{
				ID:           "reasoning_effort",
				Name:         "Reasoning Effort",
				CurrentValue: "xhigh",
				Options: []sdkcontroller.ControllerConfigChoice{
					{Value: "low", Name: "Low"},
					{Value: "medium", Name: "Medium"},
					{Value: "high", Name: "High"},
					{Value: "xhigh", Name: "Xhigh"},
				},
			},
		},
	}, 0)

	status := run.controllerStatusLocked(sdksession.SessionRef{SessionID: "parent"})
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
		configOptions: []sdkcontroller.ControllerConfigOption{
			{
				ID: "model",
				Options: []sdkcontroller.ControllerConfigChoice{
					{Value: "gpt-5.5", Name: "GPT-5.5"},
					{Value: "gpt-5.4", Name: "gpt-5.4"},
				},
			},
			{
				ID: "reasoning_effort",
				Options: []sdkcontroller.ControllerConfigChoice{
					{Value: "low", Name: "Low"},
					{Value: "high", Name: "High"},
					{Value: "xhigh", Name: "Xhigh"},
				},
			},
		},
		models: &sdkacpclient.SessionModelState{
			CurrentModelID: "gpt-5.5/xhigh",
		},
	}, 0)

	status := run.controllerStatusLocked(sdksession.SessionRef{SessionID: "parent"})
	if status.Model != "gpt-5.5" || status.ReasoningEffort != "xhigh" {
		t.Fatalf("status model/effort = %q/%q, want gpt-5.5/xhigh", status.Model, status.ReasoningEffort)
	}
}

func TestControllerRunStatusDerivesEffortOptionsFromACPModelState(t *testing.T) {
	t.Parallel()

	run := &controllerRun{}
	run.applyStartupStateLocked(nil, "remote-1", controllerClientState{
		configOptions: []sdkcontroller.ControllerConfigOption{{
			ID:           "model",
			Name:         "Model",
			CurrentValue: "gpt-5.4",
			Options: []sdkcontroller.ControllerConfigChoice{
				{Value: "gpt-5.5", Name: "GPT-5.5"},
				{Value: "gpt-5.4", Name: "gpt-5.4"},
			},
		}},
		models: &sdkacpclient.SessionModelState{
			CurrentModelID: "gpt-5.4/high",
			AvailableModels: []sdkacpclient.ModelInfo{
				{ModelID: "gpt-5.5", Name: "GPT-5.5"},
				{ModelID: "gpt-5.4/low", Name: "gpt-5.4 (low)"},
				{ModelID: "gpt-5.4/high", Name: "gpt-5.4 (high)"},
				{ModelID: "gpt-5.4/xhigh", Name: "gpt-5.4 (xhigh)"},
			},
		},
	}, 0)

	status := run.controllerStatusLocked(sdksession.SessionRef{SessionID: "parent"})
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
		configOptions: []sdkcontroller.ControllerConfigOption{{
			ID:           "model",
			Name:         "Model",
			CurrentValue: "gpt-5.5",
			Options: []sdkcontroller.ControllerConfigChoice{
				{Value: "gpt-5.5", Name: "GPT-5.5"},
				{Value: "gpt-5.4", Name: "gpt-5.4"},
			},
		}},
	}, 0)

	run.applySessionUpdateLocked(func() time.Time { return time.Unix(2, 0) }, sdkacpclient.ConfigOptionUpdate{
		SessionUpdate: sdkacpclient.UpdateConfigOption,
		ConfigOptions: []sdkacpclient.SessionConfigOption{{
			ID:           "model",
			Name:         "Model",
			CurrentValue: "gpt-5.4",
			Options: []sdkacpclient.SessionConfigSelectOption{
				{Value: "gpt-5.4", Name: "gpt-5.4"},
			},
		}},
	})

	status := run.controllerStatusLocked(sdksession.SessionRef{SessionID: "parent"})
	if status.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want updated current model", status.Model)
	}
	if got := controllerChoiceValues(status.ModelOptions); !equalStrings(got, []string{"gpt-5.5", "gpt-5.4"}) {
		t.Fatalf("model options = %#v, want preserved full choices", got)
	}
}

func controllerChoiceValues(in []sdkcontroller.ControllerConfigChoice) []string {
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
