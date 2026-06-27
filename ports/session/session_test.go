package session

import (
	"encoding/json"
	"reflect"
	"strconv"
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

func TestFilterReplayTranscriptEventsKeepsLatestTurnTraceOnly(t *testing.T) {
	t.Parallel()

	events := []*Event{
		{
			ID:      "turn-1-user",
			Type:    EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "first prompt")),
			Scope:   &EventScope{TurnID: "turn-1"},
		},
		{
			ID:    "turn-1-tool-call",
			Type:  EventTypeToolCall,
			Tool:  &EventTool{ID: "old-call", Name: "RUN_COMMAND", Status: "running"},
			Scope: &EventScope{TurnID: "turn-1"},
		},
		{
			ID:    "turn-1-tool-result",
			Type:  EventTypeToolResult,
			Tool:  &EventTool{ID: "old-call", Name: "RUN_COMMAND", Status: "completed", Output: map[string]any{"stdout": "old"}},
			Scope: &EventScope{TurnID: "turn-1"},
		},
		{
			ID:      "turn-1-assistant",
			Type:    EventTypeAssistant,
			Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "first reply")),
			Scope:   &EventScope{TurnID: "turn-1"},
		},
		{
			ID:      "turn-2-user",
			Type:    EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "second prompt")),
			Scope:   &EventScope{TurnID: "turn-2"},
		},
		{
			ID:   "turn-2-plan-old",
			Type: EventTypePlan,
			PlanPayload: &EventPlanPayload{Entries: []EventPlanEntry{{
				Content: "old plan",
				Status:  "completed",
			}}},
			Scope: &EventScope{TurnID: "turn-2"},
		},
		{
			ID:   "turn-2-plan",
			Type: EventTypePlan,
			PlanPayload: &EventPlanPayload{Entries: []EventPlanEntry{{
				Content: "run command",
				Status:  "in_progress",
			}}},
			Scope: &EventScope{TurnID: "turn-2"},
		},
		{
			ID:    "turn-2-tool-call",
			Type:  EventTypeToolCall,
			Tool:  &EventTool{ID: "latest-call", Name: "RUN_COMMAND", Status: "running"},
			Scope: &EventScope{TurnID: "turn-2"},
		},
		{
			ID:    "turn-2-tool-result-old",
			Type:  EventTypeToolResult,
			Tool:  &EventTool{ID: "latest-call", Name: "RUN_COMMAND", Status: "running", Output: map[string]any{"stdout": "partial"}},
			Scope: &EventScope{TurnID: "turn-2"},
		},
		{
			ID:    "turn-2-tool-result",
			Type:  EventTypeToolResult,
			Tool:  &EventTool{ID: "latest-call", Name: "RUN_COMMAND", Status: "interrupted", Output: map[string]any{"stderr": "stopped"}},
			Scope: &EventScope{TurnID: "turn-2"},
		},
		{
			ID:        "turn-2-lifecycle",
			Type:      EventTypeLifecycle,
			Lifecycle: &EventLifecycle{Status: "interrupted", Reason: "user interrupt"},
			Scope:     &EventScope{TurnID: "turn-2"},
		},
		{
			ID:    "turn-2-side-tool",
			Type:  EventTypeToolCall,
			Tool:  &EventTool{ID: "side-call", Name: "RUN_COMMAND", Status: "running"},
			Scope: &EventScope{TurnID: "turn-2", Source: "acp_participant", Participant: ParticipantRef{ID: "participant-1", Kind: ParticipantKindACP}},
		},
	}

	got := eventIDs(FilterReplayTranscriptEvents(events, false))
	want := []string{
		"turn-1-user",
		"turn-1-assistant",
		"turn-2-user",
		"turn-2-plan",
		"turn-2-tool-call",
		"turn-2-tool-result",
		"turn-2-lifecycle",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replay event ids = %#v, want %#v", got, want)
	}
}

func TestFilterReplayTranscriptEventsLatestTurnWithoutFinalAssistantIncludesDurableTrace(t *testing.T) {
	t.Parallel()

	events := []*Event{
		{
			ID:      "turn-1-user",
			Type:    EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "prompt")),
			Scope:   &EventScope{TurnID: "turn-1"},
		},
		{
			ID:    "turn-1-tool-call",
			Type:  EventTypeToolCall,
			Tool:  &EventTool{ID: "call-1", Name: "RUN_COMMAND", Status: "running", Input: map[string]any{"command": "sleep 10"}},
			Scope: &EventScope{TurnID: "turn-1"},
		},
		{
			ID:    "turn-1-tool-result",
			Type:  EventTypeToolResult,
			Tool:  &EventTool{ID: "call-1", Name: "RUN_COMMAND", Status: "running", Output: map[string]any{"running": true}},
			Scope: &EventScope{TurnID: "turn-1"},
		},
	}

	got := eventIDs(FilterReplayTranscriptEvents(events, false))
	want := []string{"turn-1-user", "turn-1-tool-call", "turn-1-tool-result"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replay event ids = %#v, want %#v", got, want)
	}
}

func TestFilterReplayTranscriptEventsIncludeTransientUnchanged(t *testing.T) {
	t.Parallel()

	events := []*Event{
		{ID: "user-1", Type: EventTypeUser, Message: ptrMessage(model.NewTextMessage(model.RoleUser, "prompt"))},
		MarkUIOnly(&Event{ID: "ui-1", Type: EventTypeAssistant, Text: "partial"}),
		{ID: "tool-1", Type: EventTypeToolCall, Tool: &EventTool{ID: "call-1", Name: "READ"}},
	}

	got := FilterReplayTranscriptEvents(events, true)
	if len(got) != len(events) {
		t.Fatalf("replay len = %d, want %d", len(got), len(events))
	}
	for i := range events {
		if got[i] != events[i] {
			t.Fatalf("replay[%d] = %#v, want original pointer %#v", i, got[i], events[i])
		}
	}
}

func TestFilterReplayTranscriptEventsBoundsLatestTurnTraceAndKeepsToolPairs(t *testing.T) {
	t.Parallel()

	events := []*Event{{
		ID:      "turn-1-user",
		Type:    EventTypeUser,
		Message: ptrMessage(model.NewTextMessage(model.RoleUser, "prompt")),
		Scope:   &EventScope{TurnID: "turn-1"},
	}}
	for i := 0; i < maxReplayTraceEvents; i++ {
		callID := "call-" + strconv.Itoa(i)
		events = append(events,
			&Event{
				ID:    "call-" + strconv.Itoa(i),
				Type:  EventTypeToolCall,
				Tool:  &EventTool{ID: callID, Name: "RUN_COMMAND", Status: "running"},
				Scope: &EventScope{TurnID: "turn-1"},
			},
			&Event{
				ID:    "result-" + strconv.Itoa(i),
				Type:  EventTypeToolResult,
				Tool:  &EventTool{ID: callID, Name: "RUN_COMMAND", Status: "completed", Output: map[string]any{"stdout": "ok"}},
				Scope: &EventScope{TurnID: "turn-1"},
			},
		)
	}
	events = append(events, &Event{
		ID:        "latest-lifecycle",
		Type:      EventTypeLifecycle,
		Lifecycle: &EventLifecycle{Status: "failed", Reason: "boom"},
		Scope:     &EventScope{TurnID: "turn-1"},
	})

	got := FilterReplayTranscriptEvents(events, false)
	traceCount := 0
	sawLatest := false
	selectedCalls := map[string]bool{}
	selectedResults := map[string]bool{}
	for _, event := range got {
		if IsMainReplayTraceEvent(event) {
			traceCount++
		}
		if event.ID == "latest-lifecycle" {
			sawLatest = true
		}
		if event.Tool == nil {
			continue
		}
		switch EventTypeOf(event) {
		case EventTypeToolCall:
			selectedCalls[event.Tool.ID] = true
		case EventTypeToolResult:
			selectedResults[event.Tool.ID] = true
		}
	}
	if traceCount > maxReplayTraceEvents {
		t.Fatalf("trace event count = %d, want <= %d", traceCount, maxReplayTraceEvents)
	}
	if !sawLatest {
		t.Fatalf("replay ids = %#v, want latest durable lifecycle retained", eventIDs(got))
	}
	for callID := range selectedResults {
		if !selectedCalls[callID] {
			t.Fatalf("selected tool result %q without its tool call; replay ids = %#v", callID, eventIDs(got))
		}
	}
}

func TestFilterReplayTranscriptEventsIgnoresTrailingNonMainTurnForTrace(t *testing.T) {
	t.Parallel()

	events := []*Event{
		{
			ID:      "turn-1-user",
			Type:    EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "prompt")),
			Scope:   &EventScope{TurnID: "turn-1"},
		},
		{
			ID:    "turn-1-tool-call",
			Type:  EventTypeToolCall,
			Tool:  &EventTool{ID: "call-1", Name: "RUN_COMMAND", Status: "running"},
			Scope: &EventScope{TurnID: "turn-1"},
		},
		{
			ID:    "turn-2-compact",
			Type:  EventTypeCompact,
			Scope: &EventScope{TurnID: "turn-2"},
		},
		{
			ID:      "turn-2-side-assistant",
			Type:    EventTypeAssistant,
			Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "side final")),
			Scope: &EventScope{
				TurnID: "turn-2",
				Participant: ParticipantRef{
					ID:   "side-agent",
					Kind: ParticipantKindACP,
					Role: ParticipantRoleSidecar,
				},
			},
		},
	}

	got := eventIDs(FilterReplayTranscriptEvents(events, false))
	want := []string{"turn-1-user", "turn-1-tool-call", "turn-2-side-assistant"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replay event ids = %#v, want %#v", got, want)
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
		Invocation: &EventInvocation{
			Provider: "deepseek",
			Model:    "deepseek-v4-flash",
		},
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "hello")),
		Meta:    map[string]any{"raw": "ok"},
	}

	cloned := CloneEvent(event)
	if cloned == nil || cloned.Scope == nil || cloned.Notice == nil || cloned.Invocation == nil {
		t.Fatal("CloneEvent() must preserve nested envelope payloads")
	}
	cloned.Actor.Name = "mutated"
	cloned.Scope.TurnID = "turn-2"
	cloned.Notice.Text = "changed"
	cloned.Invocation.Model = "changed"
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
	if event.Invocation.Model != "deepseek-v4-flash" {
		t.Fatalf("source invocation model = %q, want deepseek-v4-flash", event.Invocation.Model)
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

func TestEventProtocolRoundTripPreservesUpdateMessageID(t *testing.T) {
	t.Parallel()

	source := EventProtocol{
		Update: &ProtocolUpdate{
			SessionUpdate: string(ProtocolUpdateTypeAgentMessage),
			MessageID:     "msg-1",
			Content:       ProtocolTextContent("hello"),
			Meta:          map[string]any{"vendor": map[string]any{"trace": "abc"}},
		},
	}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("json.Marshal(EventProtocol) error = %v", err)
	}
	var decoded EventProtocol
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(EventProtocol) error = %v", err)
	}
	update := ProtocolUpdateOf(&Event{Protocol: &decoded})
	if update == nil {
		t.Fatal("ProtocolUpdateOf() = nil")
	}
	if update.MessageID != "msg-1" {
		t.Fatalf("MessageID = %q, want msg-1", update.MessageID)
	}
	vendor, _ := update.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "abc" {
		t.Fatalf("Meta = %#v, want vendor trace", update.Meta)
	}
}

func TestEventProtocolRoundTripPreservesParticipantPayload(t *testing.T) {
	t.Parallel()

	source := EventProtocol{
		Participant: &ProtocolParticipant{Action: " attached "},
	}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("json.Marshal(EventProtocol) error = %v", err)
	}
	var decoded EventProtocol
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(EventProtocol) error = %v", err)
	}
	participant := ProtocolParticipantOf(&Event{
		Type:     EventTypeParticipant,
		Protocol: &decoded,
	})
	if participant == nil {
		t.Fatal("ProtocolParticipantOf() = nil")
	}
	if participant.Action != "attached" {
		t.Fatalf("participant.Action = %q, want attached", participant.Action)
	}
	if decoded.Method != ProtocolMethodParticipantUpdate {
		t.Fatalf("decoded.Method = %q, want %q", decoded.Method, ProtocolMethodParticipantUpdate)
	}
	if got := ProtocolSessionUpdateTypeOfProtocol(&decoded); got != "attached" {
		t.Fatalf("ProtocolSessionUpdateTypeOfProtocol() = %q, want attached", got)
	}
}

func TestCloneEventDeepClonesNestedMeta(t *testing.T) {
	t.Parallel()

	event := &Event{
		Type: EventTypeAssistant,
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"terminal": map[string]any{"terminal_id": "term-1"},
				},
			},
		},
		Protocol: &EventProtocol{
			Update: &ProtocolUpdate{
				SessionUpdate: string(ProtocolUpdateTypeAgentMessage),
				Meta: map[string]any{
					"vendor": map[string]any{"trace": "abc"},
				},
			},
		},
	}

	cloned := CloneEvent(event)
	if cloned == nil || cloned.Protocol == nil || cloned.Protocol.Update == nil {
		t.Fatalf("CloneEvent() = %#v, want protocol update", cloned)
	}
	event.Meta["caelis"].(map[string]any)["runtime"].(map[string]any)["terminal"].(map[string]any)["terminal_id"] = "mutated"
	event.Protocol.Update.Meta["vendor"].(map[string]any)["trace"] = "mutated"

	terminal := cloned.Meta["caelis"].(map[string]any)["runtime"].(map[string]any)["terminal"].(map[string]any)
	if terminal["terminal_id"] != "term-1" {
		t.Fatalf("cloned event meta aliased source = %#v", cloned.Meta)
	}
	vendor := cloned.Protocol.Update.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "abc" {
		t.Fatalf("cloned protocol update meta aliased source = %#v", cloned.Protocol.Update.Meta)
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

func TestCanonicalizeEventDoesNotBuildCoreMessageFromProtocolText(t *testing.T) {
	t.Parallel()

	event := CanonicalizeEvent(&Event{
		Type: EventTypeAssistant,
		Protocol: &EventProtocol{Update: &ProtocolUpdate{
			SessionUpdate: string(ProtocolUpdateTypeAgentMessage),
			Content:       ProtocolTextContent("final answer"),
		}},
	})
	if event == nil {
		t.Fatal("CanonicalizeEvent() = nil")
	}
	if event.Message != nil {
		t.Fatalf("event.Message = %#v, want no protocol-to-message migration", event.Message)
	}
	if event.Protocol == nil {
		t.Fatal("event.Protocol = nil, want ACP projection preserved for protocol event")
	}
	if _, ok := ModelMessageOf(event); ok {
		t.Fatal("ModelMessageOf() projected protocol-only event, want false")
	}
	if got := EventText(event); got != "final answer" {
		t.Fatalf("EventText() = %q, want protocol display text", got)
	}
}

func TestValidateDurableCoreEventRejectsProtocolOnlyMessage(t *testing.T) {
	t.Parallel()

	const text = "  final answer\n"
	event := CanonicalizeEvent(&Event{
		Type: EventTypeAssistant,
		Protocol: &EventProtocol{Update: &ProtocolUpdate{
			SessionUpdate: string(ProtocolUpdateTypeAgentMessage),
			Content:       ProtocolTextContent(text),
		}},
	})
	err := ValidateDurableCoreEvent(event)
	if err == nil {
		t.Fatal("ValidateDurableCoreEvent() error = nil, want protocol-only message rejected")
	}
	if detail := EventValidationDetail(err); !strings.Contains(detail, "Event.Message") {
		t.Fatalf("validation detail = %q, want missing Event.Message", detail)
	}
}

func TestValidateDurableCoreEventRejectsProtocolOnlyToolResult(t *testing.T) {
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
	err := ValidateDurableCoreEvent(event)
	if err == nil {
		t.Fatal("ValidateDurableCoreEvent() error = nil, want protocol-only tool result rejected")
	}
	if detail := EventValidationDetail(err); !strings.Contains(detail, "Event.Tool") {
		t.Fatalf("validation detail = %q, want missing Event.Tool", detail)
	}
}

func TestValidateDurableCoreEventRejectsUsageOnlyProtocolToolEvent(t *testing.T) {
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
	if err == nil {
		t.Fatal("ValidateDurableCoreEvent() error = nil, want protocol-only tool event rejected")
	}
	if detail := EventValidationDetail(err); !strings.Contains(detail, "Event.Tool") {
		t.Fatalf("validation detail = %q, want missing Event.Tool", detail)
	}
}

func TestValidateDurableCoreEventRejectsTextOnlyToolPlaceholder(t *testing.T) {
	t.Parallel()

	err := ValidateDurableCoreEvent(&Event{
		Type:       EventTypeToolResult,
		Visibility: VisibilityCanonical,
		Text:       "tool output shown only in transcript",
	})
	if err == nil {
		t.Fatal("ValidateDurableCoreEvent() error = nil, want text-only tool placeholder rejected")
	}
	if detail := EventValidationDetail(err); !strings.Contains(detail, "Event.Tool") {
		t.Fatalf("validation detail = %q, want missing Event.Tool", detail)
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

func TestValidateDurableCoreEventRejectsAmbiguousToolResultMessageMatch(t *testing.T) {
	t.Parallel()

	message := model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{
			model.NewToolResultJSONPart("call-1", "RUN_COMMAND", map[string]any{"result": "one"}, false),
			model.NewToolResultJSONPart("call-2", "RUN_COMMAND", map[string]any{"result": "two"}, false),
		},
	}
	err := ValidateDurableCoreEvent(&Event{
		Type:       EventTypeToolResult,
		Visibility: VisibilityCanonical,
		Tool: &EventTool{
			Name:   "RUN_COMMAND",
			Output: map[string]any{"result": "one"},
		},
		Message: &message,
	})
	if err == nil {
		t.Fatal("ValidateDurableCoreEvent() error = nil, want missing Event.Tool id rejected")
	}
	if detail := EventValidationDetail(err); !strings.Contains(detail, "Event.Tool id") {
		t.Fatalf("validation detail = %q, want Event.Tool id detail", detail)
	}
}

func TestValidateDurableCoreEventRejectsToolResultNameCaseMismatch(t *testing.T) {
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
	if err := ValidateDurableCoreEvent(event); err == nil {
		t.Fatal("ValidateDurableCoreEvent() error = nil, want tool result name mismatch rejected")
	} else if detail := EventValidationDetail(err); !strings.Contains(detail, "name") {
		t.Fatalf("validation detail = %q, want name mismatch detail", detail)
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
	if event == nil || event.Tool == nil {
		t.Fatal("CanonicalizeEvent() did not preserve durable Event.Tool")
	}
	taskMeta, _ := nestedAnyFromMap(event.Meta, "caelis", "runtime", "task").(map[string]any)
	for _, key := range []string{"task_id", "internal_task_id", "terminal_id", "output_cursor", "running", "state"} {
		if _, ok := taskMeta[key]; !ok {
			t.Fatalf("runtime task metadata missing %q: %#v", key, event.Meta)
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

func TestValidateDurableCoreEventRejectsUntruncatedToolOutput(t *testing.T) {
	t.Parallel()

	err := ValidateDurableCoreEvent(&Event{
		Type:       EventTypeToolResult,
		Visibility: VisibilityCanonical,
		Tool: &EventTool{
			ID:   "call-1",
			Name: "RUN_COMMAND",
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

func eventIDs(events []*Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		out = append(out, event.ID)
	}
	return out
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

func nestedAnyFromMap(values map[string]any, path ...string) any {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	return current
}
