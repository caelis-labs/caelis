package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
	"github.com/caelis-labs/caelis/internal/jsonvalue"
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

func mustParticipantPlacement(t *testing.T, agentName string) placement.Placement {
	t.Helper()
	frozen, err := placement.Seal(placement.Placement{
		Kind: placement.KindAgent, ProfileID: "acp:" + agentName + ":model",
		Agent: agentName, Model: "model", ReasoningEffort: "none",
		ConfigFingerprint: "sha256:test-config",
	})
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func closeActiveControllerClient(t *testing.T, manager *Manager, sessionID string) {
	t.Helper()
	manager.mu.RLock()
	run := manager.controllers[sessionID]
	manager.mu.RUnlock()
	if run == nil {
		t.Fatalf("controller run for %q is unavailable", sessionID)
	}
	run.mu.Lock()
	acpClient := run.client
	run.mu.Unlock()
	if acpClient == nil {
		t.Fatalf("controller client for %q is unavailable", sessionID)
	}
	if err := acpClient.Close(context.Background()); err != nil {
		t.Fatalf("close controller client for %q: %v", sessionID, err)
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
	var image struct {
		Type     string `json:"type"`
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
		Name     string `json:"name"`
	}
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

func TestComposeACPContextPromptLeavesEmptyOffsetUnchanged(t *testing.T) {
	t.Parallel()

	prompt := buildPromptParts("hi", nil)
	got := composeACPContextPrompt(prompt, agent.ContextTransfer{})
	if len(got) != 1 || string(got[0]) != string(prompt[0]) {
		t.Fatalf("composeACPContextPrompt() = %s, want unchanged prompt %s", got, prompt)
	}
}

func TestComposeACPContextPromptSeparatesBackgroundFromCurrentContentParts(t *testing.T) {
	t.Parallel()

	prompt := buildPromptParts("ignored", []model.ContentPart{
		{Type: model.ContentPartText, Text: "hi"},
		{Type: model.ContentPartImage, MimeType: "image/png", Data: "iVBORw0KGgo="},
	})
	got := composeACPContextPrompt(prompt, agent.ContextTransfer{Turns: []agent.ContextTurn{{
		Executor:     session.ActorRef{Kind: session.ActorKindParticipant, Name: "claude(aria)"},
		UserMessages: []string{"earlier question"}, AssistantSummary: "earlier answer",
	}}})
	if len(got) != 4 {
		t.Fatalf("len(composeACPContextPrompt()) = %d, want background, marker, and two current parts", len(got))
	}
	var background, marker, current client.TextContent
	if err := json.Unmarshal(got[0], &background); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got[1], &marker); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got[2], &current); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(background.Text, `<caelis_background version="1">`) ||
		!strings.Contains(background.Text, `"executor":"claude(aria)"`) ||
		strings.Contains(background.Text, "session_id") || strings.Contains(background.Text, "workspace") ||
		strings.Contains(background.Text, "target_agent") {
		t.Fatalf("background block = %q", background.Text)
	}
	if marker.Text != "<caelis_current_request>" || current.Text != "hi" {
		t.Fatalf("marker/current = %q/%q, want isolated current request", marker.Text, current.Text)
	}
	if string(got[3]) != string(prompt[1]) {
		t.Fatalf("current image part changed: got %s want %s", got[3], prompt[1])
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

func TestNormalizeACPUpdateEventPreservesGrokSerializedToolInput(t *testing.T) {
	t.Parallel()

	const input = `{"query":"CAELIS_ACP_QUERY_PROBE_7F31","limit":"3","mode":"Latest"}`
	event := normalizeACPUpdateEvent(func() time.Time { return time.Unix(0, 0) }, session.ControllerBinding{
		Kind:         session.ControllerKindACP,
		ControllerID: "grok",
		Label:        "@ivy",
	}, "remote-1", "turn-1", client.ToolCallUpdate{
		SessionUpdate: client.UpdateToolCallState,
		ToolCallID:    "x-search-1",
		Title:         testStringPtr("X search:"),
		Status:        testStringPtr("completed"),
		RawOutput: map[string]any{
			"name":  "x_keyword_search",
			"input": input,
		},
	})
	update := session.ProtocolUpdateOf(event)
	if event == nil || event.Protocol == nil || update == nil {
		t.Fatalf("event = %#v, want structured Grok tool update", event)
	}
	if got := update.RawOutput["input"]; got != input {
		t.Fatalf("raw output input = %#v, want serialized invocation %q", got, input)
	}
	if got := update.RawOutput["name"]; got != "x_keyword_search" {
		t.Fatalf("raw output name = %#v, want x_keyword_search", got)
	}
	displayMeta := testDisplayMeta(update.Meta)
	displayInput, _ := displayMeta["tool_input"].(map[string]any)
	if got := displayInput["query"]; got != "CAELIS_ACP_QUERY_PROBE_7F31" {
		t.Fatalf("display tool input = %#v, want normalized query", displayInput)
	}
}

func TestNormalizeACPUpdateEventDoesNotTreatArbitraryRawOutputInputAsInvocation(t *testing.T) {
	t.Parallel()

	event := normalizeACPUpdateEvent(func() time.Time { return time.Unix(0, 0) }, session.ControllerBinding{
		Kind:         session.ControllerKindACP,
		ControllerID: "external",
		Label:        "@ivy",
	}, "remote-1", "turn-1", client.ToolCallUpdate{
		SessionUpdate: client.UpdateToolCallState,
		ToolCallID:    "read-1",
		Title:         testStringPtr("Read"),
		Status:        testStringPtr("completed"),
		RawOutput: map[string]any{
			"name":  "read",
			"input": `{"query":"returned-result-or-sensitive-text"}`,
		},
		Meta: withTestDisplayMeta(map[string]any{
			"vendor": map[string]any{"trace": "keep"},
		}, map[string]any{
			"tool_input": map[string]any{"query": "forged"},
			"theme":      "keep",
		}),
	})
	update := session.ProtocolUpdateOf(event)
	if event == nil || update == nil {
		t.Fatalf("event = %#v, want structured tool update", event)
	}
	displayMeta := testDisplayMeta(update.Meta)
	if _, exists := displayMeta["tool_input"]; exists {
		t.Fatalf("display meta = %#v, want external tool_input removed", displayMeta)
	}
	if displayMeta["theme"] != "keep" {
		t.Fatalf("display meta = %#v, want unrelated display metadata preserved", displayMeta)
	}
	if vendor, _ := update.Meta["vendor"].(map[string]any); vendor["trace"] != "keep" {
		t.Fatalf("meta = %#v, want unrelated provider metadata preserved", update.Meta)
	}
}

func TestNormalizeACPUpdateEventRequiresCompletedXSearchForDisplayInput(t *testing.T) {
	t.Parallel()

	event := normalizeACPUpdateEvent(func() time.Time { return time.Unix(0, 0) }, session.ControllerBinding{
		Kind:         session.ControllerKindACP,
		ControllerID: "grok",
		Label:        "@ivy",
	}, "remote-1", "turn-1", client.ToolCallUpdate{
		SessionUpdate: client.UpdateToolCallState,
		ToolCallID:    "x-search-1",
		Title:         testStringPtr("X search:"),
		Status:        testStringPtr(eventstream.ToolStatusInProgress),
		RawOutput: map[string]any{
			"name":  "x_keyword_search",
			"input": `{"query":"not-completed"}`,
		},
	})
	update := session.ProtocolUpdateOf(event)
	if event == nil || update == nil {
		t.Fatalf("event = %#v, want structured tool update", event)
	}
	if displayMeta := testDisplayMeta(update.Meta); len(displayMeta) != 0 {
		t.Fatalf("display meta = %#v, want incomplete x-search to fail closed", displayMeta)
	}
}

func TestNormalizeACPUpdateEventStripsExternalDisplayInputFromToolStart(t *testing.T) {
	t.Parallel()

	event := normalizeACPUpdateEvent(func() time.Time { return time.Unix(0, 0) }, session.ControllerBinding{
		Kind:         session.ControllerKindACP,
		ControllerID: "external",
		Label:        "@ivy",
	}, "remote-1", "turn-1", client.ToolCall{
		SessionUpdate: client.UpdateToolCall,
		ToolCallID:    "read-1",
		Title:         "Read",
		Status:        eventstream.ToolStatusInProgress,
		Meta: withTestDisplayMeta(map[string]any{
			"vendor": map[string]any{"trace": "keep"},
		}, map[string]any{
			"tool_input": map[string]any{"query": "forged"},
			"theme":      "keep",
		}),
	})
	update := session.ProtocolUpdateOf(event)
	if event == nil || update == nil {
		t.Fatalf("event = %#v, want structured tool call", event)
	}
	displayMeta := testDisplayMeta(update.Meta)
	if _, exists := displayMeta["tool_input"]; exists {
		t.Fatalf("display meta = %#v, want external start tool_input removed", displayMeta)
	}
	if displayMeta["theme"] != "keep" {
		t.Fatalf("display meta = %#v, want unrelated display metadata preserved", displayMeta)
	}
	if vendor, _ := update.Meta["vendor"].(map[string]any); vendor["trace"] != "keep" {
		t.Fatalf("meta = %#v, want unrelated provider metadata preserved", update.Meta)
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
	if len(content) != 1 || content[0].TerminalID != "call-1" || session.ExtractProtocolText(content[0].Content) != "line\n" {
		t.Fatalf("Protocol.Update.Content = %#v, want terminal content", event.Protocol.Update.Content)
	}
}

func TestTranslateApprovalRequestPreservesToolRawInput(t *testing.T) {
	t.Parallel()

	kind := acpsdk.ToolKindExecute
	status := acpsdk.ToolCallStatusPending
	req, err := translateApprovalRequest(session.Session{
		SessionRef: session.SessionRef{SessionID: "sess-1"},
	}, "codex", "default", client.RequestPermissionRequest{
		SessionId: "remote-1",
		ToolCall: acpsdk.ToolCallUpdate{
			ToolCallId: "call-1",
			Kind:       &kind,
			Title:      testStringPtr("Run command"),
			Status:     &status,
			RawInput: map[string]any{
				"command": "pwd",
				"workdir": "/tmp/project",
			},
			RawOutput: map[string]any{"preview": "ok"},
			Content:   []acpsdk.ToolCallContent{acpsdk.ToolContent(acpsdk.TextBlock("approval detail"))},
		},
		Options: []acpsdk.PermissionOption{{OptionId: "allow_once", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.ToolCall.RawInput["command"] != "pwd" {
		t.Fatalf("ToolCall.RawInput[command] = %#v", req.ToolCall.RawInput["command"])
	}
	if req.ToolCall.Name != "execute" {
		t.Fatalf("ToolCall.Name = %q, want exact generic ACP kind", req.ToolCall.Name)
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
		AvailableCommands: []acpsdk.AvailableCommand{
			{Name: "/search", Description: "remote search"},
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
		CurrentModeId: "review",
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
		SessionUpdate:    client.UpdateSessionInfo,
		Title:            &title,
		TitlePresent:     true,
		UpdatedAt:        &updatedAt,
		UpdatedAtPresent: true,
	})

	status := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if status.RemoteTitle != "Remote title" {
		t.Fatalf("RemoteTitle = %q, want Remote title", status.RemoteTitle)
	}
	if got := status.UpdatedAt.Format(time.RFC3339); got != updatedAt {
		t.Fatalf("UpdatedAt = %q, want %q", got, updatedAt)
	}

	run.applySessionUpdateLocked(func() time.Time { return time.Unix(2, 0) }, client.SessionInfoUpdate{
		SessionUpdate: client.UpdateSessionInfo,
	})
	status = run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if got := status.RemoteTitle; got != "Remote title" {
		t.Fatalf("RemoteTitle after absent title = %q, want retained value", got)
	}
	if got := status.UpdatedAt.Format(time.RFC3339); got != updatedAt {
		t.Fatalf("UpdatedAt after absent field = %q, want retained %q", got, updatedAt)
	}

	run.applySessionUpdateLocked(func() time.Time { return time.Unix(3, 0) }, client.SessionInfoUpdate{
		SessionUpdate: client.UpdateSessionInfo,
		TitlePresent:  true,
	})
	status = run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if got := status.RemoteTitle; got != "" {
		t.Fatalf("RemoteTitle after null title = %q, want cleared value", got)
	}
	if got := status.UpdatedAt.Format(time.RFC3339); got != updatedAt {
		t.Fatalf("UpdatedAt after title-only update = %q, want retained %q", got, updatedAt)
	}

	run.applySessionUpdateLocked(func() time.Time { return time.Unix(4, 0) }, client.SessionInfoUpdate{
		SessionUpdate:    client.UpdateSessionInfo,
		UpdatedAtPresent: true,
	})
	if got := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"}).UpdatedAt; !got.IsZero() {
		t.Fatalf("UpdatedAt after null = %v, want zero", got)
	}

	newUpdatedAt := "2026-05-05T12:34:56.123Z"
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(5, 0) }, client.SessionInfoUpdate{
		SessionUpdate:    client.UpdateSessionInfo,
		UpdatedAt:        &newUpdatedAt,
		UpdatedAtPresent: true,
	})
	status = run.controllerStatusLocked(session.SessionRef{SessionID: "parent"})
	if got := status.UpdatedAt.Format(time.RFC3339Nano); got != newUpdatedAt {
		t.Fatalf("UpdatedAt after new value = %q, want %q", got, newUpdatedAt)
	}

	invalidUpdatedAt := "not-a-timestamp"
	run.applySessionUpdateLocked(func() time.Time { return time.Unix(6, 0) }, client.SessionInfoUpdate{
		SessionUpdate:    client.UpdateSessionInfo,
		UpdatedAt:        &invalidUpdatedAt,
		UpdatedAtPresent: true,
	})
	if got := run.controllerStatusLocked(session.SessionRef{SessionID: "parent"}).UpdatedAt.Format(time.RFC3339Nano); got != newUpdatedAt {
		t.Fatalf("UpdatedAt after invalid value = %q, want retained %q", got, newUpdatedAt)
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
			AvailableCommands: []acpsdk.AvailableCommand{{
				Name:        "/search",
				Description: "remote search",
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
		Session:   parentSession,
		Agent:     "helper",
		Label:     "helper",
		Placement: mustParticipantPlacement(t, "helper"),
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
		Session:   parentSession,
		Agent:     "helper",
		Label:     "helper",
		Placement: mustParticipantPlacement(t, "helper"),
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

func TestManagerSetControllerModelDoesNotRetryAfterBrokenPipe(t *testing.T) {
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
	binding, err := manager.Activate(ctx, controller.HandoffRequest{
		Session: parentSession,
		Agent:   "helper",
		Source:  "test",
	})
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	_, err = manager.SetControllerModel(ctx, SetControllerModelRequest{
		SessionRef: parentSession.SessionRef, ExpectedControllerEpoch: "stale-epoch", Model: "gpt-next",
	})
	if err == nil || ConfigurationEffectStarted(err) {
		t.Fatalf("SetControllerModel(stale epoch) error = %v, want pre-effect rejection", err)
	}
	if starts != 1 {
		t.Fatalf("client starts after stale epoch = %d, want activate only", starts)
	}
	_, err = manager.SetControllerModel(ctx, SetControllerModelRequest{
		SessionRef: parentSession.SessionRef, ExpectedControllerEpoch: binding.EpochID, Model: "gpt-next",
	})
	if !ConfigurationEffectStarted(err) {
		t.Fatalf("SetControllerModel() error = %v, want unknown remote effect", err)
	}
	if starts != 1 {
		t.Fatalf("client starts = %d, want activate only", starts)
	}
}

func TestManagerSetControllerModelReportsUnknownAfterPartialRemoteEffect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerACPControllerReapplyHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_HELPER":      "controller-reapply",
			"CAELIS_ACP_FAIL_EFFORT": "1",
		},
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	manager, err := NewManager(Config{Registry: registry})
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
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "partial", WorkspaceKey: "ws"},
		CWD:        t.TempDir(),
	}
	t.Cleanup(func() { _ = manager.Deactivate(context.Background(), parentSession.SessionRef) })
	binding, err := manager.Activate(ctx, controller.HandoffRequest{Session: parentSession, Agent: "helper", Source: "test"})
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	_, err = manager.SetControllerModel(ctx, SetControllerModelRequest{
		SessionRef: parentSession.SessionRef, ExpectedControllerEpoch: binding.EpochID,
		Model: "gpt-next", ReasoningEffort: "high",
	})
	if !ConfigurationEffectStarted(err) {
		t.Fatalf("SetControllerModel(partial) error = %v, want unknown remote effect", err)
	}
	if starts != 1 {
		t.Fatalf("client starts = %d, want no retry after partial effect", starts)
	}
}

func TestManagerRunTurnReportsUnknownAfterPossiblePromptSubmissionAndRecoversNextTurn(t *testing.T) {
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
	var turnErr error
	for _, eventErr := range turn.Handle.Events() {
		if eventErr != nil {
			turnErr = eventErr
		}
	}
	if turnErr == nil {
		t.Fatal("turn error = nil, want ambiguous submitted prompt failure")
	}
	if !errorcode.Is(turnErr, errorcode.UnknownOutcome) {
		t.Fatalf("turn error = %v, want unknown_outcome", turnErr)
	}
	if starts != 1 {
		t.Fatalf("client starts = %d, want no reconnect after possible submission", starts)
	}

	recovery, err := manager.RunTurn(ctx, controller.TurnRequest{
		SessionRef: parentSession.SessionRef,
		Session:    parentSession,
		TurnID:     "turn-recovery",
		Input:      "continue with new input",
	})
	if err != nil {
		t.Fatalf("RunTurn(recovery) error = %v", err)
	}
	for _, eventErr := range recovery.Handle.Events() {
		if eventErr != nil {
			t.Fatalf("recovery turn event error = %v", eventErr)
		}
	}
	if starts != 2 {
		t.Fatalf("client starts after recovery = %d, want one reconnect for the new Turn", starts)
	}
}

func TestManagerRunTurnCancelAfterPromptSubmissionReportsUnknownAndKeepsAdmissionOpen(t *testing.T) {
	clientSide, peerSide := net.Pipe()
	firstPromptReceived := make(chan struct{})
	releaseFirstPrompt := make(chan struct{})
	var releaseOnce sync.Once
	releaseFirst := func() {
		releaseOnce.Do(func() { close(releaseFirstPrompt) })
	}
	var promptCalls atomic.Int32
	peer := jsonrpc.New(peerSide, peerSide)
	peerCtx, cancelPeer := context.WithCancel(context.Background())
	peerDone := make(chan error, 1)
	go func() {
		peerDone <- peer.Serve(peerCtx, func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
			if msg.Method != client.MethodSessionPrompt {
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
			if promptCalls.Add(1) == 1 {
				close(firstPromptReceived)
				<-releaseFirstPrompt
			}
			return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
		}, nil)
	}()
	acpClient, err := client.NewStreamClient(clientSide, clientSide, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseFirst()
		cancelPeer()
		_ = acpClient.Close(context.Background())
		_ = peer.Close()
		_ = clientSide.Close()
		_ = peerSide.Close()
		select {
		case <-peerDone:
		case <-time.After(time.Second):
			t.Error("ACP test peer did not stop")
		}
	})

	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{Name: "helper", Command: "helper-acp"}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	manager, err := NewManager(Config{Registry: registry})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.startClient = func(
		context.Context,
		string,
		subagent.AgentConfig,
		string,
		func(client.UpdateEnvelope),
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		return acpClient, "remote-1", controllerClientState{}, nil
	}
	parentSession := session.Session{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "cancel-after-submit", WorkspaceKey: "ws"},
		CWD:        t.TempDir(),
	}
	if _, err := manager.Activate(context.Background(), controller.HandoffRequest{
		Session: parentSession,
		Agent:   "helper",
		Source:  "test",
	}); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}

	first, err := manager.RunTurn(context.Background(), controller.TurnRequest{
		SessionRef: parentSession.SessionRef,
		Session:    parentSession,
		TurnID:     "turn-cancelled-after-submit",
		Input:      "first input",
	})
	if err != nil {
		t.Fatalf("RunTurn(first) error = %v", err)
	}
	select {
	case <-firstPromptReceived:
	case <-time.After(time.Second):
		t.Fatal("first prompt was not submitted")
	}
	if got := first.Handle.Cancel().Status; got != controller.CancelStatusCancelled {
		t.Fatalf("Cancel() status = %q, want %q", got, controller.CancelStatusCancelled)
	}
	var firstErr error
	for _, eventErr := range first.Handle.Events() {
		if eventErr != nil {
			firstErr = eventErr
		}
	}
	if !errorcode.Is(firstErr, errorcode.UnknownOutcome) {
		t.Fatalf("first Turn error = %v, want unknown_outcome", firstErr)
	}

	releaseFirst()
	secondCtx, cancelSecond := context.WithTimeout(context.Background(), time.Second)
	defer cancelSecond()
	second, err := manager.RunTurn(secondCtx, controller.TurnRequest{
		SessionRef: parentSession.SessionRef,
		Session:    parentSession,
		TurnID:     "turn-after-unknown",
		Input:      "new input",
	})
	if err != nil {
		t.Fatalf("RunTurn(second) error = %v", err)
	}
	for _, eventErr := range second.Handle.Events() {
		if eventErr != nil {
			t.Fatalf("second Turn event error = %v", eventErr)
		}
	}
	if got := promptCalls.Load(); got != 2 {
		t.Fatalf("prompt calls = %d, want two independently admitted Turns", got)
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
	closeActiveControllerClient(t, manager, parentSession.SessionID)
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
	closeActiveControllerClient(t, manager, parentSession.SessionID)
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
		if cfg.SessionOptions.ModelID != "opus" || cfg.SessionOptions.ConfigValues["thought_level"] != "very-high" {
			t.Fatalf("startClient placement options = %#v", cfg.SessionOptions)
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
	frozen, err := placement.Seal(placement.Placement{
		Kind: placement.KindAgent, ProfileID: "acp:tova:opus", Agent: "tova", Model: "opus",
		ReasoningEffort: "xhigh", SessionConfigValues: map[string]string{"thought_level": "very-high"},
		ConfigFingerprint: "sha256:config",
	})
	if err != nil {
		t.Fatal(err)
	}
	persisted := session.ParticipantBinding{
		ID:             "codex-3",
		Kind:           session.ParticipantKindACP,
		Role:           session.ParticipantRoleSidecar,
		AgentName:      "tova",
		Label:          "@tova",
		Placement:      frozen,
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
	if binding.ID != "codex-3" || binding.SessionID != "remote-tova" || binding.ContextSyncSeq != 7 || binding.Label != "@tova" || binding.Placement.Fingerprint != frozen.Fingerprint {
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

func TestApplyACPParticipantEventScopeIncludesPlacementAuditIdentity(t *testing.T) {
	t.Parallel()

	event := &session.Event{}
	applyACPParticipantEventScope(event, session.ParticipantBinding{
		ID: "participant-1", Label: "@tova",
		Placement: placement.Placement{ProfileID: "acp:tova:opus", ReasoningEffort: "xhigh"},
	}, "tova")
	if event.Meta["profile_id"] != "acp:tova:opus" || event.Meta["reasoning_effort"] != "xhigh" {
		t.Fatalf("participant audit meta = %#v", event.Meta)
	}
}

func TestParticipantRunRejectsOverlappingPrompts(t *testing.T) {
	t.Parallel()
	run := &participantRun{id: "participant-busy"}
	first := newTurnHandle(nil)
	if err := run.beginPrompt(controller.ParticipantPromptRequest{TurnID: "turn-1", ParticipantID: run.id}, first); err != nil {
		t.Fatal(err)
	}
	second := newTurnHandle(nil)
	if err := run.beginPrompt(controller.ParticipantPromptRequest{TurnID: "turn-2", ParticipantID: run.id}, second); err == nil {
		t.Fatal("overlapping participant prompt was allowed to overwrite active turn state")
	}
	if _, _ = run.finishPrompt(second); run.handle != first {
		t.Fatal("stale participant prompt owner cleared the active prompt")
	}
	run.finishPrompt(first)
	first.finish()
	second.finish()

	third := newTurnHandle(nil)
	if err := run.beginPrompt(controller.ParticipantPromptRequest{TurnID: "turn-3", ParticipantID: run.id}, third); err != nil {
		t.Fatalf("prompt after completion remained busy: %v", err)
	}
	run.closePromptAdmission()
	if got := third.Cancel().Status; got != controller.CancelStatusAlreadyCancelled {
		t.Fatalf("Cancel() after closePromptAdmission status = %q, want %q", got, controller.CancelStatusAlreadyCancelled)
	}
	fourth := newTurnHandle(nil)
	if err := run.beginPrompt(controller.ParticipantPromptRequest{TurnID: "turn-4", ParticipantID: run.id}, fourth); !errors.Is(err, controller.ErrNotActive) {
		t.Fatalf("beginPrompt() after close error = %v, want ErrNotActive", err)
	}
	run.finishPrompt(third)
	third.finish()
	fourth.finish()
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
	binding := session.ParticipantBinding{
		ID: "shared", DelegationID: "delegation-shared", AttachmentGeneration: "durable-generation",
		Placement: mustParticipantPlacement(t, "helper"),
	}
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
	persisted := session.ParticipantBinding{
		ID: "shared", DelegationID: "delegation", AttachmentGeneration: "old-generation",
		Placement: mustParticipantPlacement(t, "helper"),
	}
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
			Placement:      mustParticipantPlacement(t, "tova"),
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
		Context:        agent.ContextTransfer{Summary: "incremental context for old remote"},
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
				AgentCapabilities: client.AgentCapabilities{
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
			if req.SessionId != "stale-remote-session" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/resume id"}
			}
			return nil, &jsonrpc.RPCError{Code: -32004, Message: "session not found"}
		case client.MethodSessionNew:
			var req client.NewSessionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if strings.TrimSpace(req.Cwd) == "" {
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
				AgentCapabilities: client.AgentCapabilities{
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
			if req.SessionId != "remote-reconnect" {
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
			if req.ValueId == nil || req.ValueId.SessionId != "remote-reconnect" || req.ValueId.ConfigId != "model" || req.ValueId.Value != "gpt-next" {
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
			return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
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
			return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
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
				AgentCapabilities: client.AgentCapabilities{
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
			if req.SessionId != "remote-reapply" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/resume id"}
			}
			sawResume = true
			return client.ResumeSessionResponse{ConfigOptions: modelConfig("gpt-old")}, nil
		case client.MethodSessionSetConfig:
			var req client.SetSessionConfigOptionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if req.ValueId == nil || req.ValueId.SessionId != "remote-reapply" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/set_config_option session"}
			}
			switch req.ValueId.ConfigId {
			case "model":
				if req.ValueId.Value != "gpt-next" {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected model value"}
				}
				modelApplied = true
				return client.SetSessionConfigOptionResponse{ConfigOptions: configWithEffort("gpt-next", "low")}, nil
			case "effort":
				if req.ValueId.Value != "high" || !modelApplied {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected effort value"}
				}
				if os.Getenv("CAELIS_ACP_FAIL_EFFORT") == "1" {
					return nil, &jsonrpc.RPCError{Code: -32000, Message: "effort failed after model commit"}
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
			return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
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
				AgentCapabilities: client.AgentCapabilities{
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
			if req.SessionId != "remote-mode-reapply" {
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
			if req.ValueId == nil || req.ValueId.SessionId != "remote-mode-reapply" || req.ValueId.ConfigId != "mode" || req.ValueId.Value != "code" {
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
			return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
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
	title := "Read"
	status := eventstream.ToolStatusInProgress
	line := 7
	raw := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","title":"Read","status":"in_progress","locations":[{"path":"main.go","line":7}],"_meta":{"vendor":{"trace":"abc"}}}`)
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
	update, ok := events[0].ACP.Update.(eventstream.ToolCallUpdate)
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
			Status:        testStringPtr(eventstream.ToolStatusCompleted),
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
	if got := session.ExtractProtocolText(canonicalContent[0].Content); got != want {
		t.Fatalf("canonical terminal content = %q, want %q", got, want)
	}
	acpUpdate, ok := events[0].ACP.Update.(eventstream.ToolCallUpdate)
	if !ok {
		t.Fatalf("ACP update = %T, want ToolCallUpdate", events[0].ACP.Update)
	}
	rawOutput, _ := acpUpdate.RawOutput.(map[string]any)
	if got := rawOutput["stdout"]; got != fenced {
		t.Fatalf("ACP raw output stdout = %#v, want original %q", got, fenced)
	}
	if got := session.ExtractProtocolText(acpUpdate.Content[0].Content); got != want {
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
	kind := eventstream.ToolKindExecute
	run.handleUpdate(func() time.Time { return time.Unix(10, 0) }, client.UpdateEnvelope{
		SessionID: "remote-1",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "call-1",
			Kind:          &kind,
			Status:        testStringPtr(eventstream.ToolStatusCompleted),
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
	if got := session.ExtractProtocolText(canonicalContent[0].Content); got != want {
		t.Fatalf("canonical execute content = %q, want %q", got, want)
	}
	acpUpdate, ok := events[0].ACP.Update.(eventstream.ToolCallUpdate)
	if !ok {
		t.Fatalf("ACP update = %T, want ToolCallUpdate", events[0].ACP.Update)
	}
	if got := session.ExtractProtocolText(acpUpdate.Content[0].Content); got != want {
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
			Status:        testStringPtr(eventstream.ToolStatusCompleted),
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
	if got := session.ExtractProtocolText(canonicalContent[0].Content); got != want {
		t.Fatalf("canonical claude bash content = %q, want %q", got, want)
	}
	acpUpdate, ok := events[0].ACP.Update.(eventstream.ToolCallUpdate)
	if !ok {
		t.Fatalf("ACP update = %T, want ToolCallUpdate", events[0].ACP.Update)
	}
	if got := session.ExtractProtocolText(acpUpdate.Content[0].Content); got != want {
		t.Fatalf("ACP claude bash content = %q, want %q", got, want)
	}
}

func TestParticipantRunNormalizesDelayedXSearchDisplayInput(t *testing.T) {
	t.Parallel()

	const serializedInput = `{"query":"CAELIS_ACP_QUERY_PROBE_7F31","limit":"3","mode":"Latest"}`
	handle := newTurnHandle(nil)
	run := &participantRun{
		remoteSessionID: "remote-participant",
		agent:           "grok",
		binding: session.ParticipantBinding{
			ID:            "participant-1",
			Kind:          session.ParticipantKindACP,
			Role:          "worker",
			Label:         "@ivy",
			ControllerRef: "epoch-1",
			SessionID:     "remote-participant",
		},
		turnID:     "turn-1",
		turnStream: true,
		handle:     handle,
	}
	title := "X search:"
	status := eventstream.ToolStatusCompleted
	run.handleUpdate(func() time.Time { return time.Unix(10, 0) }, client.UpdateEnvelope{
		SessionID: "remote-participant",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "x-search-1",
			Title:         &title,
			Status:        &status,
			RawOutput: map[string]any{
				"name":  "x_keyword_search",
				"input": serializedInput,
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
	if len(events) != 1 || events[0].ACP == nil {
		t.Fatalf("source events = %#v, want one ACP envelope", events)
	}
	update, ok := events[0].ACP.Update.(eventstream.ToolCallUpdate)
	if !ok {
		t.Fatalf("ACP update = %T, want ToolCallUpdate", events[0].ACP.Update)
	}
	rawOutput, _ := update.RawOutput.(map[string]any)
	if rawOutput["name"] != "x_keyword_search" || rawOutput["input"] != serializedInput {
		t.Fatalf("raw output = %#v, want provider result preserved", rawOutput)
	}
	displayMeta := testDisplayMeta(update.Meta)
	displayInput, _ := displayMeta["tool_input"].(map[string]any)
	if got := displayInput["query"]; got != "CAELIS_ACP_QUERY_PROBE_7F31" {
		t.Fatalf("display tool input = %#v, want normalized query on live participant envelope", displayInput)
	}
}

func TestParticipantRunSharesCanonicalTerminalCompatibilityAcrossSourceViews(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	run := &participantRun{
		remoteSessionID: "remote-participant",
		agent:           "codex",
		binding: session.ParticipantBinding{
			ID:            "participant-1",
			Kind:          session.ParticipantKindACP,
			Role:          "reviewer",
			Label:         "@reviewer",
			ControllerRef: "epoch-1",
			SessionID:     "remote-participant",
		},
		turnID:     "turn-1",
		turnStream: true,
		handle:     handle,
	}
	run.handleUpdate(func() time.Time { return time.Unix(10, 0) }, client.UpdateEnvelope{
		SessionID: "remote-participant",
		Update: client.ToolCallUpdate{
			SessionUpdate: client.UpdateToolCallState,
			ToolCallID:    "command-1",
			Kind:          testStringPtr(eventstream.ToolKindExecute),
			Status:        testStringPtr(eventstream.ToolStatusInProgress),
			Meta: map[string]any{
				acpmeta.TerminalOutputDeltaKey: map[string]any{
					"terminal_id": "command-1",
					"data":        "participant output\n",
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
	if len(events) != 1 || events[0].Canonical == nil || events[0].ACP == nil {
		t.Fatalf("source events = %#v, want one paired canonical/native event", events)
	}
	canonicalUpdate := session.ProtocolUpdateOf(events[0].Canonical)
	if canonicalUpdate == nil {
		t.Fatal("canonical protocol update is nil")
	}
	nativeUpdate, ok := events[0].ACP.Update.(eventstream.ToolCallUpdate)
	if !ok {
		t.Fatalf("native ACP update = %T, want ToolCallUpdate", events[0].ACP.Update)
	}
	for name, meta := range map[string]map[string]any{
		"canonical": canonicalUpdate.Meta,
		"native":    nativeUpdate.Meta,
	} {
		output, ok := acpmeta.ReadTerminalOutput(meta)
		if !ok || output.TerminalID != "command-1" || output.Data != "participant output\n" {
			t.Fatalf("%s terminal output = %#v, %v; want shared canonical output", name, output, ok)
		}
		if _, ok := meta[acpmeta.TerminalOutputDeltaKey]; ok {
			t.Fatalf("%s metadata retained provider alias: %#v", name, meta)
		}
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
	if env.Scope != eventstream.ScopeParticipant || env.ScopeID != "turn-1" || env.ParticipantID != "participant-1" {
		t.Fatalf("ACP scope = scope:%q scopeID:%q participantID:%q, want participant turn-1/participant-1", env.Scope, env.ScopeID, env.ParticipantID)
	}
	if env.Actor != "@otto" || env.TurnID != "turn-1" {
		t.Fatalf("ACP actor/turn = %q/%q, want @otto/turn-1", env.Actor, env.TurnID)
	}
	update, ok := env.Update.(eventstream.RawUpdate)
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

func testDisplayMeta(meta map[string]any) map[string]any {
	caelisMeta, _ := meta["caelis"].(map[string]any)
	displayMeta, _ := caelisMeta["display"].(map[string]any)
	return jsonvalue.CloneMap(displayMeta)
}

func withTestDisplayMeta(meta map[string]any, values map[string]any) map[string]any {
	return jsonvalue.MergeMap(meta, map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"display": values,
		},
	})
}

func testStringPtr(value string) *string {
	return &value
}
