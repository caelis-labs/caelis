package projector

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
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

func TestProjectGatewayEventEnvelopeProjectsGatewayToolResultTerminalOutput(t *testing.T) {
	events := ProjectGatewayEventEnvelope(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolResult,
		SessionRef: session.SessionRef{SessionID: "session-1"},
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "call-ls",
			ToolName: "RUN_COMMAND",
			Status:   gateway.ToolStatusCompleted,
			Content: []session.ProtocolToolCallContent{{
				Type:       "terminal",
				TerminalID: "runtime-term-1",
				Content:    session.ProtocolTextContent("total 0\n"),
			}},
		},
	}})
	if len(events) != 1 {
		t.Fatalf("ProjectGatewayEventEnvelope() returned %d events, want 1: %#v", len(events), events)
	}
	update, ok := events[0].Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %#v, want ToolCallUpdate", events[0].Update)
	}
	if len(update.Content) != 1 {
		t.Fatalf("content items = %d, want 1", len(update.Content))
	}
	if update.Content[0].TerminalID != "runtime-term-1" {
		t.Fatalf("content terminal_id = %q, want runtime-term-1", update.Content[0].TerminalID)
	}
	terminalOutput := metautil.RuntimeSection(update.Meta, metautil.Terminal)
	if len(terminalOutput) == 0 {
		t.Fatalf("update meta = %#v, want caelis.runtime.terminal", update.Meta)
	}
	if terminalOutput["terminal_id"] != "call-ls" {
		t.Fatalf("caelis.runtime.terminal.terminal_id = %#v, want call-ls", terminalOutput["terminal_id"])
	}
	if terminalOutput["data"] != "total 0\n" {
		t.Fatalf("caelis.runtime.terminal.data = %#v, want terminal output", terminalOutput["data"])
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

func TestGatewayAndSessionAssistantProjectionParity(t *testing.T) {
	message := model.NewMessage(
		model.RoleAssistant,
		model.NewReasoningPart("thinking", model.ReasoningVisibilityVisible),
		model.NewTextPart("done"),
	)
	base := eventstream.Envelope{
		Cursor:    "e1",
		SessionID: "session-1",
		Actor:     "codex",
		Final:     true,
	}
	sessionEvents := ProjectSessionEventEnvelope(base, &session.Event{
		ID:        "e1",
		SessionID: "session-1",
		Type:      session.EventTypeAssistant,
		Message:   &message,
	})
	gatewayEvents := ProjectGatewayEventEnvelope(gateway.EventEnvelope{
		Cursor: "e1",
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "session-1"},
			Narrative: &gateway.NarrativePayload{
				Role:          gateway.NarrativeRoleAssistant,
				Actor:         "codex",
				ReasoningText: "thinking",
				Text:          "done",
				Final:         true,
			},
		},
	})
	if len(sessionEvents) != len(gatewayEvents) {
		t.Fatalf("session events = %#v, gateway events = %#v", sessionEvents, gatewayEvents)
	}
	for i := range sessionEvents {
		if sessionEvents[i].Kind != gatewayEvents[i].Kind ||
			sessionEvents[i].Cursor != gatewayEvents[i].Cursor ||
			sessionEvents[i].SessionID != gatewayEvents[i].SessionID ||
			sessionEvents[i].Actor != gatewayEvents[i].Actor ||
			sessionEvents[i].Final != gatewayEvents[i].Final ||
			eventstream.UpdateType(sessionEvents[i].Update) != eventstream.UpdateType(gatewayEvents[i].Update) {
			t.Fatalf("event[%d] session = %#v, gateway = %#v", i, sessionEvents[i], gatewayEvents[i])
		}
		sessionChunk, ok := sessionEvents[i].Update.(schema.ContentChunk)
		if !ok {
			t.Fatalf("session update[%d] = %#v, want content chunk", i, sessionEvents[i].Update)
		}
		gatewayChunk, ok := gatewayEvents[i].Update.(schema.ContentChunk)
		if !ok {
			t.Fatalf("gateway update[%d] = %#v, want content chunk", i, gatewayEvents[i].Update)
		}
		if schema.ExtractTextValue(sessionChunk.Content) != schema.ExtractTextValue(gatewayChunk.Content) {
			t.Fatalf("content[%d] session = %#v, gateway = %#v", i, sessionChunk.Content, gatewayChunk.Content)
		}
	}
}

func TestProjectSessionEventEnvelopeProjectsParticipantAndLifecycleExtensions(t *testing.T) {
	participant := ProjectSessionEventEnvelope(eventstream.Envelope{
		Cursor:        "participant-1",
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "agent-1",
		ParticipantID: "agent-1",
	}, &session.Event{
		ID:    "participant-1",
		Type:  session.EventTypeParticipant,
		Actor: session.ActorRef{Kind: session.ActorKindParticipant, Name: "@agent"},
		Scope: &session.EventScope{Participant: session.ParticipantRef{ID: "agent-1"}},
		Protocol: &session.EventProtocol{
			Participant: &session.ProtocolParticipant{Action: "attached"},
		},
	})
	if len(participant) != 1 || participant[0].Kind != eventstream.KindParticipant || participant[0].Participant == nil || participant[0].Participant.State != "attached" {
		t.Fatalf("participant projection = %#v, want participant attached", participant)
	}
	if participant[0].Actor != "@agent" || participant[0].ParticipantID != "agent-1" {
		t.Fatalf("participant envelope = %#v, want actor and participant id", participant[0])
	}

	lifecycle := ProjectSessionEventEnvelope(eventstream.Envelope{
		Cursor:    "lifecycle-1",
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Actor:     "codex",
	}, &session.Event{
		ID:        "lifecycle-1",
		Type:      session.EventTypeLifecycle,
		Actor:     session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
		Lifecycle: &session.EventLifecycle{Status: "COMPLETED", Reason: "done"},
	})
	if len(lifecycle) != 1 || lifecycle[0].Kind != eventstream.KindLifecycle || lifecycle[0].Lifecycle == nil || lifecycle[0].Lifecycle.State != "completed" || lifecycle[0].Lifecycle.Reason != "done" {
		t.Fatalf("lifecycle projection = %#v, want lifecycle completed", lifecycle)
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

func TestGatewayCanonicalPayloadNormalizesBeforeACPProjection(t *testing.T) {
	base := eventstream.Envelope{SessionID: "session-1"}
	event := gateway.Event{
		Kind:       gateway.EventKindToolResult,
		SessionRef: session.SessionRef{SessionID: "session-1"},
		ToolResult: &gateway.ToolResultPayload{
			CallID:    "call-1",
			ToolName:  "RUN_COMMAND",
			Status:    gateway.ToolStatusRunning,
			RawInput:  map[string]any{"cmd": "go test ./..."},
			RawOutput: map[string]any{"running": true},
		},
	}
	sessionEvent, ok := sessionEventFromGatewayEvent(base, event)
	if !ok {
		t.Fatal("sessionEventFromGatewayEvent() ok = false, want true")
	}
	if sessionEvent.Protocol == nil || session.ProtocolUpdateOf(sessionEvent) == nil {
		t.Fatalf("session event protocol = %#v, want normalized protocol update", sessionEvent.Protocol)
	}
	updates, err := (EventProjector{}).ProjectEvent(sessionEvent)
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1: %#v", len(updates), updates)
	}
	update, ok := updates[0].(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %#v, want ToolCallUpdate", updates[0])
	}
	if update.ToolCallID != "call-1" || stringPtrValue(update.Kind) != "RUN_COMMAND" || stringPtrValue(update.Status) != schema.ToolStatusInProgress {
		t.Fatalf("tool update = %#v, want canonical gateway payload routed through EventProjector", update)
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
		Meta: map[string]any{
			"caelis": map[string]any{
				"approval": map[string]any{"mode": "manual"},
			},
		},
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
	if got := metaString(env.Permission.Meta, "caelis", "approval", "mode"); got != "manual" {
		t.Fatalf("permission meta approval mode = %q, want manual", got)
	}
	if got := metaString(env.Permission.Meta, "caelis", "bridge", "source"); got != "gateway_projection" {
		t.Fatalf("permission meta bridge source = %q, want gateway_projection", got)
	}
}

func TestProjectGatewayEventEnvelopePreservesCanonicalProtocolToolFields(t *testing.T) {
	line := 42
	events := ProjectGatewayEventEnvelope(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolResult,
		SessionRef: session.SessionRef{SessionID: "session-1"},
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
			ToolCallID:    "call-1",
			Kind:          "edit",
			Title:         "Edit file",
			Status:        "completed",
			Locations: []session.ProtocolToolCallLocation{{
				Path: "main.go",
				Line: &line,
			}},
			Meta: map[string]any{
				"vendor": map[string]any{"trace_id": "trace-1"},
			},
		}},
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "call-1",
			ToolName: "EDIT",
			Status:   gateway.ToolStatusCompleted,
		},
	}})
	if len(events) != 1 {
		t.Fatalf("ProjectGatewayEventEnvelope() returned %d events, want 1: %#v", len(events), events)
	}
	update, ok := events[0].Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %#v, want ToolCallUpdate", events[0].Update)
	}
	if len(update.Locations) != 1 || update.Locations[0].Path != "main.go" || update.Locations[0].Line == nil || *update.Locations[0].Line != 42 {
		t.Fatalf("locations = %#v, want main.go:42", update.Locations)
	}
	if got := metaString(update.Meta, "vendor", "trace_id"); got != "trace-1" {
		t.Fatalf("meta vendor.trace_id = %q, want trace-1", got)
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
