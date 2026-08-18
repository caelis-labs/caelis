package acpingress

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestNormalizeControllerUserMessageIsCanonicalDurableMessage(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(client.TextContent{Type: "text", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	event := NormalizeUpdate(client.ContentChunk{
		SessionUpdate: client.UpdateUserMessage,
		Content:       raw,
		MessageID:     "msg-1",
	}, controllerTestOptions())
	if event == nil {
		t.Fatal("NormalizeUpdate() = nil, want user event")
	}
	if event.Visibility != session.VisibilityCanonical || event.Type != session.EventTypeUser {
		t.Fatalf("event = %#v, want canonical user event", event)
	}
	if event.Message == nil {
		t.Fatalf("event.Message = nil, want durable model message")
	}
	if err := session.ValidateDurableCoreEvent(event); err != nil {
		t.Fatalf("ValidateDurableCoreEvent() error = %v", err)
	}
	update := session.ProtocolUpdateOf(event)
	if update == nil || update.MessageID != "msg-1" {
		t.Fatalf("protocol update = %#v, want message id", update)
	}
}

func TestNormalizeControllerAssistantAndPlanAreUIOnlyTrace(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(client.TextContent{Type: "text", Text: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	assistant := NormalizeUpdate(client.ContentChunk{
		SessionUpdate: client.UpdateAgentMessage,
		Content:       raw,
	}, controllerTestOptions())
	if assistant == nil || assistant.Visibility != session.VisibilityUIOnly || session.IsCanonicalHistoryEvent(assistant) {
		t.Fatalf("assistant = %#v, want ui-only trace", assistant)
	}

	plan := NormalizeUpdate(client.PlanUpdate{
		SessionUpdate: client.UpdatePlan,
		Entries:       []client.PlanEntry{{Content: "Run tests", Status: "pending", Priority: "high"}},
	}, controllerTestOptions())
	if plan == nil || plan.Visibility != session.VisibilityUIOnly || plan.PlanPayload != nil || session.IsCanonicalHistoryEvent(plan) {
		t.Fatalf("plan = %#v, want ui-only protocol trace", plan)
	}
}

func TestNormalizeSubagentToolStreamIsAlwaysUIOnly(t *testing.T) {
	t.Parallel()

	status := "completed"
	event := NormalizeUpdate(client.ToolCallUpdate{
		SessionUpdate: client.UpdateToolCallState,
		ToolCallID:    "call-1",
		Kind:          testStringPtr("execute"),
		Status:        &status,
		RawOutput:     map[string]any{"stdout": "ok\n"},
	}, Options{
		At: time.Unix(1, 0),
		Scope: session.EventScope{
			Source: "acp_subagent",
			Participant: session.ParticipantRef{
				ID:   "agent-1",
				Kind: session.ParticipantKindSubagent,
				Role: session.ParticipantRoleDelegated,
			},
		},
		Actor:      session.ActorRef{Kind: session.ActorKindParticipant, ID: "agent-1", Name: "codex"},
		Visibility: UIOnlyVisibility,
	})
	if event == nil || event.Visibility != session.VisibilityUIOnly || event.Type != session.EventTypeToolResult {
		t.Fatalf("event = %#v, want ui-only tool result trace", event)
	}
	if event.Tool != nil || session.IsCanonicalHistoryEvent(event) {
		t.Fatalf("event = %#v, want no durable tool payload in subagent trace", event)
	}
	if update := session.ProtocolUpdateOf(event); update == nil || update.RawOutput["stdout"] != "ok\n" {
		t.Fatalf("protocol update = %#v, want raw output", update)
	}
}

func TestNormalizeSparseToolUpdateDoesNotInventDisplayContent(t *testing.T) {
	t.Parallel()

	status := "completed"
	event := NormalizeUpdate(client.ToolCallUpdate{
		SessionUpdate: client.UpdateToolCallState,
		ToolCallID:    "call-1",
		Status:        &status,
		RawOutput:     map[string]any{"formatted_output": "ok\n", "exit_code": 0},
		Meta: map[string]any{
			metautil.TerminalOutputDeltaKey: map[string]any{"terminal_id": "call-1", "data": "ok\n"},
		},
	}, Options{
		At:         time.Unix(1, 0),
		Scope:      session.EventScope{Source: "acp_subagent"},
		Actor:      session.ActorRef{Kind: session.ActorKindParticipant, ID: "agent-1", Name: "codex"},
		Visibility: UIOnlyVisibility,
	})
	if event == nil {
		t.Fatal("NormalizeUpdate() = nil, want sparse tool update")
	}
	if event.Text != "" {
		t.Fatalf("event.Text = %q, want no invented tool-update content", event.Text)
	}
	update := session.ProtocolUpdateOf(event)
	if update == nil || update.ToolCallID != "call-1" || update.Status != status || len(session.ProtocolToolCallContentOf(update)) != 0 {
		t.Fatalf("protocol update = %#v, want exact sparse ACP update", update)
	}
	if output, ok := metautil.TerminalOutput(update.Meta); !ok || output.Data != "ok\n" {
		t.Fatalf("provider terminal output = %#v, want compatibility delta normalized", update.Meta)
	}
	if _, ok := update.Meta[metautil.TerminalOutputDeltaKey]; ok {
		t.Fatalf("provider terminal alias escaped ingress normalization: %#v", update.Meta)
	}
}

func TestNormalizeToolCallDefaultsMissingStatusToPending(t *testing.T) {
	t.Parallel()

	event := NormalizeUpdate(client.ToolCall{
		SessionUpdate: client.UpdateToolCall,
		ToolCallID:    "call-1",
		Kind:          "execute",
	}, Options{
		At:         time.Unix(1, 0),
		Scope:      session.EventScope{Source: "acp", TurnID: "turn-1"},
		Actor:      session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
		Visibility: UIOnlyVisibility,
	})
	update := session.ProtocolUpdateOf(event)
	if update == nil || update.Status != "pending" {
		t.Fatalf("protocol update = %#v, want pending status default", update)
	}
}

func TestNormalizeExternalToolCallCannotDeclareRuntimeToolName(t *testing.T) {
	t.Parallel()

	meta := metautil.WithRuntimeSection(
		map[string]any{"vendor": map[string]any{"trace": "keep"}},
		metautil.RuntimeTool,
		map[string]any{metautil.RuntimeToolName: "RunCommand"},
	)
	meta = metautil.WithRuntimeSection(meta, "binding", map[string]any{"task_result": true})
	event := NormalizeUpdate(client.ToolCall{
		SessionUpdate: client.UpdateToolCall,
		ToolCallID:    "call-1",
		Title:         "External command",
		Kind:          "execute",
		Meta:          meta,
	}, controllerTestOptions())
	update := session.ProtocolUpdateOf(event)
	if update == nil {
		t.Fatal("NormalizeUpdate() = nil, want UI-only tool event")
	}
	if got := metautil.String(update.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName); got != "" {
		t.Fatalf("external runtime tool name = %q, want stripped", got)
	}
	if got := metautil.RuntimeSection(update.Meta, "binding"); len(got) != 0 {
		t.Fatalf("external runtime binding = %#v, want stripped", got)
	}
	vendor, _ := update.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "keep" {
		t.Fatalf("unrelated provider metadata was not preserved: %#v", update.Meta)
	}
}

func TestNormalizeRejectsProtocolOnlyCanonicalToolAndPlan(t *testing.T) {
	t.Parallel()

	tool := NormalizeUpdate(client.ToolCall{
		SessionUpdate: client.UpdateToolCall,
		ToolCallID:    "call-1",
		Kind:          "execute",
		RawInput:      map[string]any{"command": "pwd"},
	}, Options{
		At:         time.Unix(1, 0),
		Scope:      session.EventScope{Source: "acp", TurnID: "turn-1"},
		Actor:      session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
		Visibility: canonicalVisibility,
	})
	if tool != nil {
		t.Fatalf("canonical tool event = %#v, want rejected until durable ACP tool storage is wired", tool)
	}

	plan := NormalizeUpdate(client.PlanUpdate{
		SessionUpdate: client.UpdatePlan,
		Entries:       []client.PlanEntry{{Content: "Ship", Status: "pending", Priority: "medium"}},
	}, Options{
		At:         time.Unix(1, 0),
		Scope:      session.EventScope{Source: "acp", TurnID: "turn-1"},
		Actor:      session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
		Visibility: canonicalVisibility,
	})
	if plan != nil {
		t.Fatalf("canonical plan event = %#v, want rejected until durable ACP plan storage is wired", plan)
	}
}

func TestNormalizeUsageUpdateIsEventstreamOnly(t *testing.T) {
	t.Parallel()

	event := NormalizeUpdate(client.UsageUpdate{
		SessionUpdate: client.UpdateUsage,
		Size:          200000,
		Used:          42000,
	}, controllerTestOptions())
	if event != nil {
		t.Fatalf("usage_update event = %#v, want no canonical ingress event", event)
	}
}

func controllerTestOptions() Options {
	return Options{
		At: time.Unix(1, 0),
		Scope: session.EventScope{
			Source: "acp",
			TurnID: "turn-1",
			Controller: session.ControllerRef{
				Kind: session.ControllerKindACP,
				ID:   "codex",
			},
			ACP: session.ACPRef{SessionID: "remote-1"},
		},
		Actor:      session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
		Visibility: ControllerVisibility,
	}
}

func canonicalVisibility(string, session.EventType) session.Visibility {
	return session.VisibilityCanonical
}

func testStringPtr(value string) *string {
	return &value
}
