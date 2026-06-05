package session

import (
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestVisibilityRules(t *testing.T) {
	t.Parallel()

	canonical := &Event{
		Type:    EventTypeAssistant,
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "ok")),
	}
	if !IsCanonicalHistoryEvent(canonical) {
		t.Fatal("expected assistant event to be canonical")
	}
	if !IsInvocationVisibleEvent(canonical) {
		t.Fatal("expected assistant event to be invocation visible")
	}

	uiOnly := MarkUIOnly(&Event{
		Type:    EventTypeNotice,
		Message: ptrMessage(model.NewTextMessage(model.RoleSystem, "warn: retrying")),
	})
	if !IsTransient(uiOnly) {
		t.Fatal("expected ui-only event to be transient")
	}
	if IsCanonicalHistoryEvent(uiOnly) {
		t.Fatal("ui-only event must not be canonical")
	}
	if IsInvocationVisibleEvent(uiOnly) {
		t.Fatal("ui-only event must not be invocation visible")
	}

	overlay := MarkOverlay(&Event{
		Type:    EventTypeSystem,
		Message: ptrMessage(model.NewTextMessage(model.RoleSystem, "overlay")),
	})
	if !IsTransient(overlay) {
		t.Fatal("overlay event must be transient")
	}
	if !IsInvocationVisibleEvent(overlay) {
		t.Fatal("overlay event should remain invocation visible")
	}

	mirror := MarkMirror(&Event{
		Type:    EventTypeSystem,
		Message: ptrMessage(model.NewTextMessage(model.RoleSystem, "mirror")),
	})
	if IsTransient(mirror) {
		t.Fatal("mirror event must not be transient")
	}
	if IsCanonicalHistoryEvent(mirror) {
		t.Fatal("mirror event must not be canonical")
	}
	if IsInvocationVisibleEvent(mirror) {
		t.Fatal("mirror event must not be invocation visible")
	}
}

func TestMainInvocationVisibleSharesSideDialogueAndExcludesDelegatedWork(t *testing.T) {
	t.Parallel()

	main := &Event{
		Type:    EventTypeAssistant,
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "main")),
	}
	if !IsMainInvocationVisibleEvent(main) {
		t.Fatal("main event should be visible to the main invocation")
	}

	sideAssistant := &Event{
		Type:    EventTypeAssistant,
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "side")),
		Scope: &EventScope{
			Participant: ParticipantRef{
				ID:   "side-agent",
				Kind: ParticipantKindSubagent,
				Role: ParticipantRoleSidecar,
			},
		},
	}
	if !IsInvocationVisibleEvent(sideAssistant) {
		t.Fatal("side assistant event should remain invocation-visible for non-main consumers")
	}
	if !IsMainInvocationVisibleEvent(sideAssistant) {
		t.Fatal("side assistant final event should be visible to the main invocation")
	}

	delegatedAssistant := &Event{
		Type:    EventTypeAssistant,
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "spawn")),
		Scope: &EventScope{
			Source: "agent_spawn",
			Participant: ParticipantRef{
				ID:   "spawned-agent",
				Kind: ParticipantKindSubagent,
				Role: ParticipantRoleDelegated,
			},
		},
	}
	if IsMainInvocationVisibleEvent(delegatedAssistant) {
		t.Fatal("delegated subagent event must not be visible to the main invocation")
	}
}

func TestFilterEvents(t *testing.T) {
	t.Parallel()

	now := time.Now()
	events := []*Event{
		{ID: "1", Type: EventTypeAssistant, Time: now.Add(-3 * time.Minute), Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "a"))},
		MarkNotice(&Event{ID: "2", Time: now.Add(-2 * time.Minute), Message: ptrMessage(model.NewTextMessage(model.RoleSystem, "warn: retrying"))}, "warn", "retrying"),
		{ID: "3", Type: EventTypeAssistant, Time: now.Add(-time.Minute), Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "b"))},
	}

	filtered := FilterEvents(events, 0, false)
	if got, want := len(filtered), 2; got != want {
		t.Fatalf("len(filtered) = %d, want %d", got, want)
	}

	withTransient := FilterEvents(events, 2, true)
	if got, want := len(withTransient), 2; got != want {
		t.Fatalf("len(withTransient) = %d, want %d", got, want)
	}
	if got := withTransient[0].ID; got != "2" {
		t.Fatalf("first limited event id = %q, want %q", got, "2")
	}
}

func TestCloneEventPreservesCompactEnvelope(t *testing.T) {
	t.Parallel()

	event := &Event{
		ID:   "evt-1",
		Type: EventTypeAssistant,
		Actor: ActorRef{
			Kind: ActorKindController,
			Name: "kernel",
		},
		Scope: &EventScope{
			TurnID: "turn-1",
			Controller: ControllerRef{
				Kind:    ControllerKindKernel,
				EpochID: "ep-1",
			},
			Participant: ParticipantRef{
				ID:   "part-1",
				Kind: ParticipantKindSubagent,
				Role: ParticipantRoleDelegated,
			},
		},
		Notice: &EventNotice{
			Level: "warn",
			Text:  "retrying",
		},
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "hello")),
		Meta:    map[string]any{"raw": "ok"},
	}

	cloned := CloneEvent(event)
	if cloned == nil || cloned.Scope == nil || cloned.Notice == nil {
		t.Fatal("CloneEvent() must preserve nested envelope payloads")
	}
	cloned.Actor.Name = "mutated"
	cloned.Scope.TurnID = "turn-2"
	cloned.Notice.Text = "changed"
	cloned.Meta["raw"] = "changed"
	if event.Actor.Name != "kernel" {
		t.Fatalf("source actor mutated = %q", event.Actor.Name)
	}
	if event.Scope.TurnID != "turn-1" {
		t.Fatalf("source scope turn = %q, want %q", event.Scope.TurnID, "turn-1")
	}
	if event.Notice.Text != "retrying" {
		t.Fatalf("source notice text = %q, want %q", event.Notice.Text, "retrying")
	}
	if got := event.Meta["raw"]; got != "ok" {
		t.Fatalf("source meta raw = %v, want %q", got, "ok")
	}
}

func TestCloneEventPreservesTextWhitespace(t *testing.T) {
	t.Parallel()

	event := &Event{
		Type:       EventTypeAssistant,
		Text:       " thought boundary ",
		Visibility: VisibilityUIOnly,
		Protocol: &EventProtocol{
			UpdateType: string(ProtocolUpdateTypeAgentThought),
		},
	}

	cloned := CloneEvent(event)
	if cloned == nil {
		t.Fatal("CloneEvent() = nil")
		return
	}
	if got := cloned.Text; got != event.Text {
		t.Fatalf("cloned.Text = %q, want exact source text %q", got, event.Text)
	}
}

func TestCloneEventProtocolPreservesRuntimeToolNameWithDurableUpdate(t *testing.T) {
	t.Parallel()

	protocol := CloneEventProtocol(EventProtocol{
		Update: &ProtocolUpdate{
			SessionUpdate: string(ProtocolUpdateTypeToolCall),
			ToolCallID:    "call-1",
			Title:         "RUN_COMMAND echo hi",
			Kind:          "execute",
			Status:        "pending",
			RawInput:      map[string]any{"command": "echo hi"},
		},
		ToolCall: &ProtocolToolCall{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Kind:   "execute",
			Title:  "RUN_COMMAND echo hi",
			Status: "pending",
			RawInput: map[string]any{
				"command": "echo hi",
			},
		},
	})

	if protocol.ToolCall == nil {
		t.Fatal("ToolCall = nil")
	}
	if protocol.ToolCall.Name != "RUN_COMMAND" {
		t.Fatalf("ToolCall.Name = %q, want original RUN_COMMAND", protocol.ToolCall.Name)
	}
	if protocol.ToolCall.Kind != "execute" {
		t.Fatalf("ToolCall.Kind = %q, want execute", protocol.ToolCall.Kind)
	}
}

func TestEventTypeOfProtocolPlan(t *testing.T) {
	t.Parallel()

	event := &Event{
		Protocol: &EventProtocol{
			UpdateType: "plan",
			Plan: &ProtocolPlan{
				Entries: []ProtocolPlanEntry{{Content: "step 1", Status: "pending"}},
			},
			Approval: &ProtocolApproval{
				ToolCall: ProtocolToolCall{
					ID:     "call-1",
					Name:   "RUN_COMMAND",
					Status: "pending",
				},
				Options: []ProtocolApprovalOption{
					{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
				},
			},
		},
	}

	if got := EventTypeOf(event); got != EventTypePlan {
		t.Fatalf("EventTypeOf(plan) = %q, want %q", got, EventTypePlan)
	}
	cloned := CloneEvent(event)
	if cloned == nil || cloned.Protocol == nil || cloned.Protocol.Plan == nil || cloned.Protocol.Approval == nil {
		t.Fatal("CloneEvent() must preserve protocol payloads")
	}
	cloned.Protocol.Plan.Entries[0].Content = "changed"
	cloned.Protocol.Approval.Options[0].ID = "reject_once"
	if got := event.Protocol.Plan.Entries[0].Content; got != "step 1" {
		t.Fatalf("source plan content = %q, want %q", got, "step 1")
	}
	if got := event.Protocol.Approval.Options[0].ID; got != "allow_once" {
		t.Fatalf("source approval option = %q, want %q", got, "allow_once")
	}
}

func TestCanonicalizeEventBuildsCoreMessageFromProtocolText(t *testing.T) {
	t.Parallel()

	event := CanonicalizeEvent(&Event{
		Type: EventTypeAssistant,
		Protocol: &EventProtocol{Update: &ProtocolUpdate{
			SessionUpdate: string(ProtocolUpdateTypeAgentMessage),
			Content:       ProtocolTextContent("final answer"),
		}},
	})
	if event == nil || event.AssistantMessage == nil {
		t.Fatal("CanonicalizeEvent() did not build durable assistant semantic payload")
	}
	if event.Schema != EventSchemaSemanticV2 {
		t.Fatalf("schema = %d, want %d", event.Schema, EventSchemaSemanticV2)
	}
	if event.Message != nil || event.Protocol != nil || event.Meta != nil {
		t.Fatalf("legacy projection fields persisted after canonicalization: message=%#v protocol=%#v meta=%#v", event.Message, event.Protocol, event.Meta)
	}
	message, ok := ModelMessageOf(event)
	if !ok {
		t.Fatal("ModelMessageOf() did not project assistant semantic payload")
	}
	if message.Role != model.RoleAssistant || message.TextContent() != "final answer" {
		t.Fatalf("projected message = %#v, want assistant final answer", message)
	}
}

func TestCanonicalizeEventPreservesProtocolTextWhitespaceInSemanticPayload(t *testing.T) {
	t.Parallel()

	const text = "  final answer\n"
	event := CanonicalizeEvent(&Event{
		Type: EventTypeAssistant,
		Protocol: &EventProtocol{Update: &ProtocolUpdate{
			SessionUpdate: string(ProtocolUpdateTypeAgentMessage),
			Content:       ProtocolTextContent(text),
		}},
	})
	if event == nil || event.AssistantMessage == nil {
		t.Fatal("CanonicalizeEvent() did not build durable assistant semantic payload")
	}
	message, ok := ModelMessageOf(event)
	if !ok {
		t.Fatal("ModelMessageOf() did not project assistant semantic payload")
	}
	if got := message.TextContent(); got != text {
		t.Fatalf("projected message text = %q, want raw protocol text %q", got, text)
	}
}

func TestCanonicalizeEventMigratesProtocolOnlyToolResult(t *testing.T) {
	t.Parallel()

	event := CanonicalizeEvent(&Event{
		Type:       EventTypeToolResult,
		Visibility: VisibilityCanonical,
		Protocol: &EventProtocol{Update: &ProtocolUpdate{
			SessionUpdate: string(ProtocolUpdateTypeToolUpdate),
			ToolCallID:    "call-1",
			Kind:          "RUN_COMMAND",
			RawOutput:     map[string]any{"stdout": "ok"},
		}},
	})
	if event == nil || event.ToolResultPayload == nil {
		t.Fatal("CanonicalizeEvent() did not build durable tool_result semantic payload")
	}
	if event.Message != nil || event.Tool != nil || event.Protocol != nil || event.Meta != nil {
		t.Fatalf("legacy projection fields persisted after canonicalization: message=%#v tool=%#v protocol=%#v meta=%#v", event.Message, event.Tool, event.Protocol, event.Meta)
	}
	if err := ValidateDurableCoreEvent(event); err != nil {
		t.Fatalf("ValidateDurableCoreEvent() error = %v, want semantic tool result accepted", err)
	}
	if event.ToolResultPayload.ToolCallID != "call-1" || event.ToolResultPayload.Name != "RUN_COMMAND" {
		t.Fatalf("tool_result payload = %#v, want call-1/RUN_COMMAND", event.ToolResultPayload)
	}
	message, ok := ModelMessageOf(event)
	if !ok || len(message.ToolResults()) != 1 {
		t.Fatalf("ModelMessageOf() = %#v, %v; want one tool result", message, ok)
	}
	result := message.ToolResults()[0]
	if result.ToolUseID != "call-1" || result.Name != "RUN_COMMAND" {
		t.Fatalf("projected tool result = %#v, want call-1/RUN_COMMAND", result)
	}
}

func TestValidateDurableCoreEventAllowsUsageOnlyProtocolToolEvent(t *testing.T) {
	t.Parallel()

	err := ValidateDurableCoreEvent(&Event{
		Type:       EventTypeToolCall,
		Visibility: VisibilityCanonical,
		Protocol: &EventProtocol{Update: &ProtocolUpdate{
			SessionUpdate: string(ProtocolUpdateTypeToolCall),
			ToolCallID:    "call-1",
			Kind:          "RUN_COMMAND",
			RawInput:      map[string]any{"command": "pwd"},
		}},
		Meta: map[string]any{
			"caelis": map[string]any{
				"sdk": map[string]any{
					"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 2},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ValidateDurableCoreEvent() error = %v, want usage-only protocol tool event accepted", err)
	}
}

func TestValidateDurableCoreEventAllowsTextOnlyToolPlaceholder(t *testing.T) {
	t.Parallel()

	err := ValidateDurableCoreEvent(&Event{
		Type:       EventTypeToolResult,
		Visibility: VisibilityCanonical,
		Text:       "tool output shown only in transcript",
	})
	if err != nil {
		t.Fatalf("ValidateDurableCoreEvent() error = %v, want text-only placeholder accepted", err)
	}
}

func TestValidateDurableCoreEventAllowsMatchingToolMessageOutput(t *testing.T) {
	t.Parallel()

	message := model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{{
			Kind: model.PartKindToolResult,
			ToolResult: &model.ToolResultPart{
				ToolUseID: "call-1",
				Name:      "RUN_COMMAND",
				Content:   []model.Part{model.NewTextPart("ok")},
			},
		}},
	}
	err := ValidateDurableCoreEvent(&Event{
		Type:       EventTypeToolResult,
		Visibility: VisibilityCanonical,
		Tool: &EventTool{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Output: map[string]any{"result": "ok"},
		},
		Message: &message,
	})
	if err != nil {
		t.Fatalf("ValidateDurableCoreEvent() error = %v, want matching tool output accepted", err)
	}
}

func TestCanonicalizeEventMigratesToolResultNameCaseMismatch(t *testing.T) {
	t.Parallel()

	message := model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{{
			Kind: model.PartKindToolResult,
			ToolResult: &model.ToolResultPart{
				ToolUseID: "call-1",
				Name:      "WRITE",
				Content:   []model.Part{model.NewJSONPart([]byte(`{"result":"ok"}`))},
			},
		}},
	}
	event := CanonicalizeEvent(&Event{
		Type:       EventTypeToolResult,
		Visibility: VisibilityCanonical,
		Tool: &EventTool{
			ID:     "call-1",
			Name:   "Write",
			Status: "completed",
			Output: map[string]any{"result": "ok"},
		},
		Message: &message,
		Meta:    toolResultTestMeta("WRITE"),
	})
	if event == nil || event.ToolResultPayload == nil {
		t.Fatal("CanonicalizeEvent() did not build durable tool_result semantic payload")
	}
	if event.Message != nil || event.Tool != nil || event.Protocol != nil || event.Meta != nil {
		t.Fatalf("legacy projection fields persisted after canonicalization: message=%#v tool=%#v protocol=%#v meta=%#v", event.Message, event.Tool, event.Protocol, event.Meta)
	}
	if err := ValidateDurableCoreEvent(event); err != nil {
		t.Fatalf("ValidateDurableCoreEvent() error = %v, want semantic mismatch accepted", err)
	}
	if got := event.ToolResultPayload.Name; got != "Write" {
		t.Fatalf("tool_result name = %q, want canonical Event.Tool name %q", got, "Write")
	}
	projected, ok := ModelMessageOf(event)
	if !ok || len(projected.ToolResults()) != 1 {
		t.Fatalf("ModelMessageOf() = %#v, %v; want one tool result", projected, ok)
	}
	if got := projected.ToolResults()[0].Name; got != "Write" {
		t.Fatalf("projected tool result name = %q, want %q", got, "Write")
	}
}

func TestCanonicalizeToolResultPreservesRuntimeTaskMetadata(t *testing.T) {
	t.Parallel()

	event := CanonicalizeEvent(&Event{
		Type:       EventTypeToolResult,
		Visibility: VisibilityCanonical,
		Tool: &EventTool{
			ID:     "spawn-1",
			Name:   "SPAWN",
			Status: "running",
			Output: map[string]any{"task_id": "reya", "state": "running"},
		},
		Meta: map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{
					"tool": map[string]any{
						"name": "SPAWN",
					},
					"task": map[string]any{
						"task_id":          "reya",
						"internal_task_id": "task-1",
						"terminal_id":      "subagent-task-1",
						"output_cursor":    int64(7),
						"running":          true,
						"state":            "running",
					},
				},
			},
		},
	})
	if event == nil || event.ToolResultPayload == nil {
		t.Fatal("CanonicalizeEvent() did not build durable tool_result semantic payload")
	}
	if event.Meta != nil {
		t.Fatalf("event.Meta = %#v, want stripped projection", event.Meta)
	}
	taskMeta, _ := nestedAnyFromMap(event.ToolResultPayload.Metadata, "caelis", "runtime", "task").(map[string]any)
	for _, key := range []string{"task_id", "internal_task_id", "terminal_id", "output_cursor", "running", "state"} {
		if _, ok := taskMeta[key]; !ok {
			t.Fatalf("semantic task metadata missing %q: %#v", key, event.ToolResultPayload.Metadata)
		}
	}
}

func TestValidateDurableCoreEventRejectsToolMessageOutputDivergence(t *testing.T) {
	t.Parallel()

	message := model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{model.NewToolResultJSONPart("call-1", "RUN_COMMAND", map[string]any{
			"result":    "raw",
			"exit_code": 1,
		}, true)},
	}
	err := ValidateDurableCoreEvent(&Event{
		Type:       EventTypeToolResult,
		Visibility: VisibilityCanonical,
		Tool: &EventTool{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Output: map[string]any{"result": "canonical", "exit_code": 1},
		},
		Message: &message,
	})
	if err == nil {
		t.Fatal("ValidateDurableCoreEvent() error = nil, want divergence rejection")
	}
	if detail := EventValidationDetail(err); !strings.Contains(detail, "diverges") {
		t.Fatalf("validation detail = %q, want divergence detail", detail)
	}
}

func TestValidateDurableCoreEventRejectsSemanticToolOutputDivergence(t *testing.T) {
	t.Parallel()

	err := ValidateDurableCoreEvent(&Event{
		Type:       EventTypeToolResult,
		Visibility: VisibilityCanonical,
		ToolResultPayload: &EventToolResultPayload{
			ToolCallID: "call-1",
			Name:       "RUN_COMMAND",
			Output:     map[string]any{"result": "canonical", "exit_code": 1},
			Content: []EventPart{{
				Kind: string(model.PartKindJSON),
				JSON: []byte(`{"result":"raw","exit_code":1}`),
			}},
		},
	})
	if err == nil {
		t.Fatal("ValidateDurableCoreEvent() error = nil, want semantic divergence rejection")
	}
	if detail := EventValidationDetail(err); !strings.Contains(detail, "diverges") {
		t.Fatalf("validation detail = %q, want divergence detail", detail)
	}
}

func TestValidateDurableCoreEventRejectsUntruncatedSemanticToolOutput(t *testing.T) {
	t.Parallel()

	err := ValidateDurableCoreEvent(&Event{
		Type:       EventTypeToolResult,
		Visibility: VisibilityCanonical,
		ToolResultPayload: &EventToolResultPayload{
			ToolCallID: "call-1",
			Name:       "RUN_COMMAND",
			Output: map[string]any{
				"result": strings.Repeat("x", tool.DefaultTruncationPolicy().ByteBudget()*2),
			},
		},
	})
	if err == nil {
		t.Fatal("ValidateDurableCoreEvent() error = nil, want truncation rejection")
	}
	if detail := EventValidationDetail(err); !strings.Contains(detail, "not canonical-truncated") {
		t.Fatalf("validation detail = %q, want truncation detail", detail)
	}
}

func ptrMessage(message model.Message) *model.Message {
	return &message
}

func toolResultTestMeta(name string) map[string]any {
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": map[string]any{
				"tool": map[string]any{
					"name": name,
				},
			},
		},
	}
}
