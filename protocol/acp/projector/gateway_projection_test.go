package projector

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestProjectGatewayEventEnvelopeProjectsGatewayToolResult(t *testing.T) {
	events := ProjectGatewayEventEnvelope(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolResult,
		SessionRef: session.SessionRef{SessionID: "session-1"},
		ToolResult: &gateway.ToolResultPayload{
			CallID:    "call-1",
			ToolName:  "RUN_COMMAND",
			Status:    gateway.ToolStatusRunning,
			RawOutput: map[string]any{"running": true},
		},
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"task": map[string]any{"task_id": "task-1"},
				},
			},
		},
	}})
	if len(events) != 1 {
		t.Fatalf("ProjectGatewayEventEnvelope() returned %d events, want 1: %#v", len(events), events)
	}
	env := events[0]
	if env.Kind != eventstream.KindSessionUpdate {
		t.Fatalf("kind = %q, want session/update", env.Kind)
	}
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %#v, want ToolCallUpdate", env.Update)
	}
	if update.ToolCallID != "call-1" || stringPtrValue(update.Kind) != "RUN_COMMAND" || stringPtrValue(update.Status) != schema.ToolStatusInProgress {
		t.Fatalf("tool update = %#v, want RUN_COMMAND in_progress call-1", update)
	}
	if got := metaString(update.Meta, "caelis", "runtime", "tool", "name"); got != "RUN_COMMAND" {
		t.Fatalf("tool meta name = %q, want RUN_COMMAND", got)
	}
	if got := metaString(env.Meta, "caelis", "runtime", "task", "task_id"); got != "task-1" {
		t.Fatalf("envelope task meta = %q, want task-1", got)
	}
}

func TestProjectGatewayEventEnvelopeAddsInvocationMeta(t *testing.T) {
	events := ProjectGatewayEventEnvelope(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindAssistantMessage,
		SessionRef: session.SessionRef{SessionID: "session-1"},
		Narrative:  &gateway.NarrativePayload{Role: gateway.NarrativeRoleAssistant, Text: "done"},
		Invocation: &session.EventInvocation{Provider: "deepseek", Model: "deepseek-v4-flash"},
	}})
	if len(events) == 0 {
		t.Fatal("ProjectGatewayEventEnvelope() returned no events")
	}
	if got := metaString(events[0].Meta, "caelis", "invocation", "provider"); got != "deepseek" {
		t.Fatalf("invocation provider = %q, want deepseek", got)
	}
	if got := metaString(events[0].Meta, "caelis", "invocation", "model"); got != "deepseek-v4-flash" {
		t.Fatalf("invocation model = %q, want deepseek-v4-flash", got)
	}
}

func TestProjectGatewayEventEnvelopeProjectsGatewayAssistantNarrative(t *testing.T) {
	events := ProjectGatewayEventEnvelope(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindAssistantMessage,
		SessionRef: session.SessionRef{SessionID: "session-1"},
		Narrative: &gateway.NarrativePayload{
			Role:          gateway.NarrativeRoleAssistant,
			Actor:         "codex",
			ReasoningText: "thinking",
			Text:          "done",
			Final:         true,
		},
	}})
	if len(events) != 2 {
		t.Fatalf("ProjectGatewayEventEnvelope() returned %d events, want thought + message: %#v", len(events), events)
	}
	for _, env := range events {
		if env.Kind != eventstream.KindSessionUpdate || env.Actor != "codex" || !env.Final {
			t.Fatalf("event = %#v, want codex final session/update", env)
		}
	}
	thought, ok := events[0].Update.(schema.ContentChunk)
	if !ok || thought.SessionUpdate != schema.UpdateAgentThought {
		t.Fatalf("first update = %#v, want agent_thought_chunk", events[0].Update)
	}
	if content, ok := thought.Content.(schema.TextContent); !ok || content.Text != "thinking" {
		t.Fatalf("thought content = %#v, want thinking text", thought.Content)
	}
	message, ok := events[1].Update.(schema.ContentChunk)
	if !ok || message.SessionUpdate != schema.UpdateAgentMessage {
		t.Fatalf("second update = %#v, want agent_message_chunk", events[1].Update)
	}
	if content, ok := message.Content.(schema.TextContent); !ok || content.Text != "done" {
		t.Fatalf("message content = %#v, want done text", message.Content)
	}
}

func TestProjectGatewayEventEnvelopeProjectsGatewayToolCall(t *testing.T) {
	events := ProjectGatewayEventEnvelope(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolCall,
		SessionRef: session.SessionRef{SessionID: "session-1"},
		ToolCall: &gateway.ToolCallPayload{
			CallID:   "call-1",
			ToolName: "RUN_COMMAND",
			Status:   gateway.ToolStatusStarted,
			RawInput: map[string]any{"cmd": "go test ./..."},
			Actor:    "codex",
		},
	}})
	if len(events) != 1 {
		t.Fatalf("ProjectGatewayEventEnvelope() returned %d events, want 1: %#v", len(events), events)
	}
	env := events[0]
	if env.Kind != eventstream.KindSessionUpdate || env.Actor != "codex" {
		t.Fatalf("event = %#v, want codex session/update", env)
	}
	update, ok := env.Update.(schema.ToolCall)
	if !ok {
		t.Fatalf("update = %#v, want ToolCall", env.Update)
	}
	if update.ToolCallID != "call-1" || update.Kind != "RUN_COMMAND" || update.Status != schema.ToolStatusInProgress {
		t.Fatalf("tool call = %#v, want RUN_COMMAND in_progress call-1", update)
	}
	if got := metaString(update.Meta, "caelis", "runtime", "tool", "name"); got != "RUN_COMMAND" {
		t.Fatalf("tool meta name = %q, want RUN_COMMAND", got)
	}
}

func TestProjectGatewayEventEnvelopeProjectsGatewayPlan(t *testing.T) {
	events := ProjectGatewayEventEnvelope(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindPlanUpdate,
		SessionRef: session.SessionRef{SessionID: "session-1"},
		Plan: &gateway.PlanPayload{Entries: []gateway.PlanEntryPayload{{
			Content: "inspect",
			Status:  "in_progress",
		}}},
	}})
	if len(events) != 1 {
		t.Fatalf("ProjectGatewayEventEnvelope() returned %d events, want 1: %#v", len(events), events)
	}
	update, ok := events[0].Update.(schema.PlanUpdate)
	if !ok {
		t.Fatalf("update = %#v, want PlanUpdate", events[0].Update)
	}
	if len(update.Entries) != 1 || update.Entries[0].Content != "inspect" || update.Entries[0].Status != "in_progress" {
		t.Fatalf("plan update = %#v, want inspect in_progress", update)
	}
}

func TestProjectGatewayEventEnvelopeProjectsProtocolPermission(t *testing.T) {
	events := ProjectGatewayEventEnvelope(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindApprovalRequested,
		SessionRef: session.SessionRef{SessionID: "session-1"},
		Protocol: &session.EventProtocol{Permission: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID:       "call-1",
				Name:     "RUN_COMMAND",
				Kind:     "execute",
				Status:   "pending",
				RawInput: map[string]any{"command": "go test ./..."},
			},
			Options: []session.ProtocolApprovalOption{{
				ID:   "allow_once",
				Name: "Allow once",
				Kind: "allow_once",
			}},
		}},
	}})
	if len(events) != 1 {
		t.Fatalf("ProjectGatewayEventEnvelope() returned %d events, want 1: %#v", len(events), events)
	}
	env := events[0]
	if env.Kind != eventstream.KindRequestPermission || env.Permission == nil {
		t.Fatalf("event = %#v, want request_permission", env)
	}
	if env.Permission.SessionID != "session-1" || env.Permission.ToolCall.ToolCallID != "call-1" {
		t.Fatalf("permission = %#v, want session/call ids", env.Permission)
	}
	if len(env.Permission.Options) != 1 || env.Permission.Options[0].OptionID != "allow_once" {
		t.Fatalf("options = %#v, want allow_once", env.Permission.Options)
	}
}

func TestProjectGatewayEventEnvelopeProjectsManualApprovalPayloadPermission(t *testing.T) {
	events := ProjectGatewayEventEnvelope(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindApprovalRequested,
		SessionRef: session.SessionRef{SessionID: "session-1"},
		ApprovalPayload: &gateway.ApprovalPayload{
			ToolCallID:         "call-1",
			ToolName:           "RUN_COMMAND",
			RawInput:           map[string]any{"command": "go test ./..."},
			Reason:             "needs execution",
			Justification:      "requested by user",
			SandboxPermissions: "host",
			Status:             gateway.ApprovalStatusPending,
			Options: []gateway.ApprovalOption{{
				ID:   "allow_once",
				Name: "Allow once",
				Kind: "allow_once",
			}, {
				ID:   "reject_once",
				Name: "Reject once",
				Kind: "reject_once",
			}},
		},
	}})
	if len(events) != 1 {
		t.Fatalf("ProjectGatewayEventEnvelope() returned %d events, want 1: %#v", len(events), events)
	}
	env := events[0]
	if env.Kind != eventstream.KindRequestPermission || env.Permission == nil {
		t.Fatalf("event = %#v, want request_permission", env)
	}
	if env.Permission.SessionID != "session-1" {
		t.Fatalf("session id = %q, want session-1", env.Permission.SessionID)
	}
	tool := env.Permission.ToolCall
	if tool.ToolCallID != "call-1" || stringPtrValue(tool.Kind) != "RUN_COMMAND" || stringPtrValue(tool.Status) != schema.ToolStatusPending {
		t.Fatalf("tool call = %#v, want pending RUN_COMMAND call-1", tool)
	}
	rawInput, ok := tool.RawInput.(map[string]any)
	if !ok {
		t.Fatalf("raw input = %#v, want map", tool.RawInput)
	}
	if got := rawInput["command"]; got != "go test ./..." {
		t.Fatalf("raw command = %#v, want command", got)
	}
	if got := rawInput["approval_reason"]; got != "needs execution" {
		t.Fatalf("approval reason = %#v, want needs execution", got)
	}
	if got := rawInput["justification"]; got != "requested by user" {
		t.Fatalf("justification = %#v, want requested by user", got)
	}
	if got := rawInput["sandbox_permissions"]; got != "host" {
		t.Fatalf("sandbox permissions = %#v, want host", got)
	}
	if len(env.Permission.Options) != 2 || env.Permission.Options[0].OptionID != "allow_once" || env.Permission.Options[1].OptionID != "reject_once" {
		t.Fatalf("options = %#v, want allow/reject", env.Permission.Options)
	}
}

func TestProjectGatewayEventEnvelopeLeavesEmptyToolUpdateStatusUnset(t *testing.T) {
	events := ProjectGatewayEventEnvelope(gateway.EventEnvelope{Event: gateway.Event{
		Kind: gateway.EventKindToolResult,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
			ToolCallID:    "call-1",
			Kind:          "execute",
			RawOutput:     map[string]any{"exit_code": 0},
		}},
	}})
	if len(events) != 1 {
		t.Fatalf("ProjectGatewayEventEnvelope() returned %d events, want 1: %#v", len(events), events)
	}
	update, ok := events[0].Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %#v, want ToolCallUpdate", events[0].Update)
	}
	if update.Status != nil {
		t.Fatalf("status = %q, want nil so downstream can infer final status", *update.Status)
	}
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func metaString(values map[string]any, path ...string) string {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapped[key]
	}
	text, _ := current.(string)
	return text
}
