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

func testStringPtr(value string) *string {
	return &value
}
