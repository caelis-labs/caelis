package session

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestVisibilityRules(t *testing.T) {
	t.Parallel()

	assistant := func(text string) *Event {
		return &Event{
			Type:    EventTypeAssistant,
			Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, text)),
		}
	}
	tests := []struct {
		name             string
		event            *Event
		wantTransient    bool
		wantPersist      bool
		wantReplay       bool
		wantModelVisible bool
		wantSharedLedger bool
		wantReplayTrace  bool
	}{
		{
			name:             "canonical",
			event:            assistant("canonical"),
			wantPersist:      true,
			wantReplay:       true,
			wantModelVisible: true,
			wantSharedLedger: true,
		},
		{
			name:       "mirror",
			event:      MarkMirror(assistant("mirror")),
			wantReplay: true,
		},
		{
			name:          "ui_only",
			event:         MarkUIOnly(assistant("ui-only")),
			wantTransient: true,
		},
		{
			name:          "overlay",
			event:         MarkOverlay(assistant("overlay")),
			wantTransient: true,
		},
		{
			name:          "notice",
			event:         MarkNotice(assistant("notice"), "notice", "display only"),
			wantTransient: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsTransient(tt.event); got != tt.wantTransient {
				t.Fatalf("IsTransient() = %v, want %v", got, tt.wantTransient)
			}
			if got := IsCanonicalHistoryEvent(tt.event); got != tt.wantPersist {
				t.Fatalf("IsCanonicalHistoryEvent() = %v, want %v", got, tt.wantPersist)
			}
			if got := IsReplayDialogueEvent(tt.event); got != tt.wantReplay {
				t.Fatalf("IsReplayDialogueEvent() = %v, want %v", got, tt.wantReplay)
			}
			if got := IsInvocationVisibleEvent(tt.event); got != tt.wantModelVisible {
				t.Fatalf("IsInvocationVisibleEvent() = %v, want %v", got, tt.wantModelVisible)
			}
			if got := IsMainReplayTraceEvent(tt.event); got != tt.wantReplayTrace {
				t.Fatalf("IsMainReplayTraceEvent() = %v, want %v", got, tt.wantReplayTrace)
			}
			if got := IsSharedDialogueEvent(tt.event); got != tt.wantSharedLedger {
				t.Fatalf("IsSharedDialogueEvent() = %v, want %v", got, tt.wantSharedLedger)
			}
		})
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

	scopedContextMessage := model.NewTextMessage(model.RoleUser, "plugin context")
	scopedContext := &Event{
		Type:    EventTypeContext,
		Message: &scopedContextMessage,
		Scope: &EventScope{
			Source: "plugin_hook",
			Participant: ParticipantRef{
				ID:   "context-source",
				Kind: ParticipantKindSubagent,
				Role: ParticipantRoleDelegated,
			},
		},
	}
	if !IsMainInvocationVisibleEvent(scopedContext) {
		t.Fatal("scoped context event should be visible to the main invocation")
	}
}

func TestPrepareAppendTransactionAppliesSessionMutationStateAndMetadata(t *testing.T) {
	t.Parallel()

	now := time.Unix(20, 0)
	message := model.NewTextMessage(model.RoleUser, "hello from transaction")
	tx, err := PrepareAppendTransaction(PrepareAppendTransactionRequest{
		Session: Session{SessionRef: SessionRef{SessionID: "sess-1"}},
		State:   map[string]any{"cursor": float64(1)},
		Events: []*Event{{
			Type:    EventTypeUser,
			Message: &message,
			Text:    "hello from transaction",
		}},
		ExistingIDs: map[string]struct{}{},
		Now:         now,
		AllocateEventID: func(event *Event, _ map[string]struct{}) {
			event.ID = "event-1"
		},
		MutateSession: func(activeSession *Session, _ PreparedAppendEvents) (bool, error) {
			return PutParticipantBinding(activeSession, ParticipantBinding{
				ID:        "side-1",
				Kind:      ParticipantKindACP,
				Role:      ParticipantRoleSidecar,
				AgentName: "reviewer",
			}), nil
		},
		UpdateState: func(events []*Event, state map[string]any) (map[string]any, error) {
			if len(events) != 1 || events[0].ID != "event-1" {
				t.Fatalf("prepared events = %#v, want allocated event-1", events)
			}
			state["cursor"] = float64(2)
			return state, nil
		},
	})
	if err != nil {
		t.Fatalf("PrepareAppendTransaction() error = %v", err)
	}
	if !tx.Changed {
		t.Fatal("PrepareAppendTransaction().Changed = false, want true")
	}
	if tx.Session.UpdatedAt != now {
		t.Fatalf("UpdatedAt = %s, want %s", tx.Session.UpdatedAt, now)
	}
	if tx.Session.Title != "hello from transaction" {
		t.Fatalf("Title = %q, want generated title", tx.Session.Title)
	}
	if len(tx.Session.Participants) != 1 || tx.Session.Participants[0].ID != "side-1" {
		t.Fatalf("Participants = %#v, want side-1", tx.Session.Participants)
	}
	if got := tx.State["cursor"]; got != float64(2) {
		t.Fatalf("State[cursor] = %#v, want 2", got)
	}
	if len(tx.Prepared.Persisted) != 1 || tx.Prepared.Persisted[0].ID != "event-1" {
		t.Fatalf("Persisted = %#v, want allocated event", tx.Prepared.Persisted)
	}
}

func TestParticipantBindingHelpersPreserveStoreSemantics(t *testing.T) {
	t.Parallel()

	activeSession := Session{Participants: []ParticipantBinding{{ID: "p1", Label: "@old"}}}
	if !PutParticipantBinding(&activeSession, ParticipantBinding{ID: "p1", Label: "@new"}) {
		t.Fatal("PutParticipantBinding() = false, want true")
	}
	if len(activeSession.Participants) != 1 || activeSession.Participants[0].Label != "@new" {
		t.Fatalf("Participants after replace = %#v, want one @new binding", activeSession.Participants)
	}
	if !PutParticipantBinding(&activeSession, ParticipantBinding{Label: "@empty"}) ||
		!PutParticipantBinding(&activeSession, ParticipantBinding{Label: "@empty-again"}) {
		t.Fatal("PutParticipantBinding(empty ID) = false, want append")
	}
	if len(activeSession.Participants) != 3 {
		t.Fatalf("Participants after empty IDs = %#v, want appended empty-ID bindings", activeSession.Participants)
	}
	if RemoveParticipantBinding(&activeSession, " ") {
		t.Fatal("RemoveParticipantBinding(empty ID) = true, want false")
	}
	if !RemoveParticipantBinding(&activeSession, "missing") {
		t.Fatal("RemoveParticipantBinding(missing) = false, want detach-request semantics")
	}
	if len(activeSession.Participants) != 3 {
		t.Fatalf("Participants after missing remove = %#v, want unchanged bindings", activeSession.Participants)
	}
	if !RemoveParticipantBinding(&activeSession, "p1") {
		t.Fatal("RemoveParticipantBinding(p1) = false, want true")
	}
	if len(activeSession.Participants) != 2 || activeSession.Participants[0].ID == "p1" {
		t.Fatalf("Participants after remove = %#v, want p1 removed", activeSession.Participants)
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

func TestCloneContractsIsolateNestedJSONValues(t *testing.T) {
	t.Parallel()

	sess := Session{Metadata: map[string]any{
		"nested": map[string]any{"items": []any{"original"}},
	}}
	clonedSession := CloneSession(sess)
	clonedSession.Metadata["nested"].(map[string]any)["items"].([]any)[0] = "mutated"
	if got := sess.Metadata["nested"].(map[string]any)["items"].([]any)[0]; got != "original" {
		t.Fatalf("CloneSession() leaked nested metadata mutation: %v", got)
	}

	event := &Event{
		Tool: &EventTool{
			Input:  map[string]any{"nested": map[string]any{"value": "input"}},
			Output: map[string]any{"nested": []any{map[string]any{"value": "output"}}},
		},
		Protocol: &EventProtocol{Update: &ProtocolUpdate{
			RawInput:  map[string]any{"nested": map[string]any{"value": "raw-input"}},
			RawOutput: map[string]any{"nested": map[string]any{"value": "raw-output"}},
		}},
		Meta: map[string]any{"nested": map[string]any{"value": "meta"}},
	}
	clonedEvent := CloneEvent(event)
	clonedEvent.Tool.Input["nested"].(map[string]any)["value"] = "mutated"
	clonedEvent.Tool.Output["nested"].([]any)[0].(map[string]any)["value"] = "mutated"
	clonedEvent.Protocol.Update.RawInput["nested"].(map[string]any)["value"] = "mutated"
	clonedEvent.Protocol.Update.RawOutput["nested"].(map[string]any)["value"] = "mutated"
	clonedEvent.Meta["nested"].(map[string]any)["value"] = "mutated"

	want := map[string]any{
		"input":      event.Tool.Input["nested"].(map[string]any)["value"],
		"output":     event.Tool.Output["nested"].([]any)[0].(map[string]any)["value"],
		"raw_input":  event.Protocol.Update.RawInput["nested"].(map[string]any)["value"],
		"raw_output": event.Protocol.Update.RawOutput["nested"].(map[string]any)["value"],
		"meta":       event.Meta["nested"].(map[string]any)["value"],
	}
	if got := want; !reflect.DeepEqual(got, map[string]any{
		"input": "input", "output": "output", "raw_input": "raw-input", "raw_output": "raw-output", "meta": "meta",
	}) {
		t.Fatalf("original event after clone mutation = %#v", got)
	}
}

func TestPrepareEventsRejectsInvalidJSONCompatibleValue(t *testing.T) {
	t.Parallel()

	_, err := PrepareEventsForAppend(PrepareEventsForAppendRequest{
		SessionID: "sess-1",
		Events: []*Event{{
			Type: EventTypeToolCall,
			Tool: &EventTool{
				ID:    "call-1",
				Name:  "READ",
				Input: map[string]any{"limit": math.Inf(1)},
			},
		}},
	})
	if err == nil {
		t.Fatal("PrepareEventsForAppend() error = nil, want invalid JSON value rejection")
	}
}

func TestPrepareAppendTransactionAssignsSeqRevisionAndEnforcesCAS(t *testing.T) {
	t.Parallel()

	expected := uint64(4)
	first := model.NewTextMessage(model.RoleUser, "first")
	second := model.NewTextMessage(model.RoleAssistant, "second")
	tx, err := PrepareAppendTransaction(PrepareAppendTransactionRequest{
		Session:          Session{SessionRef: SessionRef{SessionID: "sess-1"}, Revision: 4},
		ExpectedRevision: &expected,
		LastSeq:          8,
		Events: []*Event{
			{ID: "event-9", Type: EventTypeUser, Message: &first},
			{ID: "event-10", Type: EventTypeAssistant, Message: &second},
		},
	})
	if err != nil {
		t.Fatalf("PrepareAppendTransaction() error = %v", err)
	}
	if tx.Session.Revision != 5 {
		t.Fatalf("Session.Revision = %d, want 5", tx.Session.Revision)
	}
	if got := []uint64{tx.Prepared.Events[0].Seq, tx.Prepared.Events[1].Seq}; !reflect.DeepEqual(got, []uint64{9, 10}) {
		t.Fatalf("event seqs = %v, want [9 10]", got)
	}

	stale := uint64(3)
	_, err = PrepareAppendTransaction(PrepareAppendTransactionRequest{
		Session:          Session{SessionRef: SessionRef{SessionID: "sess-1"}, Revision: 4},
		ExpectedRevision: &stale,
	})
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale transaction error = %v, want *RevisionConflictError", err)
	}
}

func TestPrepareEventsDedupesStableEventIDOrReturnsConflict(t *testing.T) {
	t.Parallel()

	message := model.NewTextMessage(model.RoleUser, "retry me")
	existing := &Event{
		ID:         "event-stable",
		SessionID:  "sess-1",
		Seq:        1,
		Type:       EventTypeUser,
		Visibility: VisibilityCanonical,
		Message:    &message,
	}
	prepared, err := PrepareEventsForAppend(PrepareEventsForAppendRequest{
		SessionID:      "sess-1",
		Events:         []*Event{{ID: "event-stable", Type: EventTypeUser, Message: &message}},
		ExistingEvents: []*Event{existing},
		LastSeq:        1,
	})
	if err != nil {
		t.Fatalf("PrepareEventsForAppend(retry) error = %v", err)
	}
	if len(prepared.Events) != 1 || len(prepared.Persisted) != 0 || prepared.Events[0].Seq != 1 {
		t.Fatalf("retry prepared = %#v, want existing event and no new persistence", prepared)
	}

	different := model.NewTextMessage(model.RoleUser, "different payload")
	_, err = PrepareEventsForAppend(PrepareEventsForAppendRequest{
		SessionID:      "sess-1",
		Events:         []*Event{{ID: "event-stable", Type: EventTypeUser, Message: &different}},
		ExistingEvents: []*Event{existing},
		LastSeq:        1,
	})
	var conflict *EventConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflicting event error = %v, want *EventConflictError", err)
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
			Update: &ProtocolUpdate{SessionUpdate: string(ProtocolUpdateTypeAgentThought)},
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

func TestCloneEventProtocolDeepClonesDurableToolUpdate(t *testing.T) {
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
	})

	update := ProtocolUpdateOfProtocol(&protocol)
	if update == nil {
		t.Fatal("ProtocolUpdateOfProtocol() = nil")
	}
	if update.ToolCallID != "call-1" || update.Kind != "execute" || update.Title != "RUN_COMMAND echo hi" {
		t.Fatalf("update = %#v, want durable tool call fields", update)
	}
	update.RawInput["command"] = "mutated"
	if got := protocol.Update.RawInput["command"]; got != "echo hi" {
		t.Fatalf("source update raw input = %#v, want clone isolation", got)
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
		Method: ProtocolMethodParticipantUpdate,
		Update: &ProtocolUpdate{SessionUpdate: " attached "},
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

func TestEventProtocolRoundTripPreservesHandoffPayload(t *testing.T) {
	t.Parallel()

	source := EventProtocol{
		Method: ProtocolMethodControllerHandoff,
		Update: &ProtocolUpdate{SessionUpdate: " activation "},
	}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("json.Marshal(EventProtocol) error = %v", err)
	}
	var decoded EventProtocol
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(EventProtocol) error = %v", err)
	}
	handoff := ProtocolHandoffOf(&Event{
		Type:     EventTypeHandoff,
		Protocol: &decoded,
	})
	if handoff == nil {
		t.Fatal("ProtocolHandoffOf() = nil")
	}
	if handoff.Phase != "activation" {
		t.Fatalf("handoff.Phase = %q, want activation", handoff.Phase)
	}
	if decoded.Method != ProtocolMethodControllerHandoff {
		t.Fatalf("decoded.Method = %q, want %q", decoded.Method, ProtocolMethodControllerHandoff)
	}
	if got := ProtocolSessionUpdateTypeOfProtocol(&decoded); got != "activation" {
		t.Fatalf("ProtocolSessionUpdateTypeOfProtocol() = %q, want activation", got)
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
			Update: &ProtocolUpdate{
				SessionUpdate: "plan",
				Entries:       []ProtocolPlanEntry{{Content: "step 1", Status: "pending"}},
			},
			Permission: &ProtocolApproval{
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
	if cloned == nil || cloned.Protocol == nil || cloned.Protocol.Update == nil || cloned.Protocol.Permission == nil {
		t.Fatal("CloneEvent() must preserve protocol payloads")
	}
	cloned.Protocol.Update.Entries[0].Content = "changed"
	cloned.Protocol.Permission.Options[0].ID = "reject_once"
	if got := event.Protocol.Update.Entries[0].Content; got != "step 1" {
		t.Fatalf("source plan content = %q, want %q", got, "step 1")
	}
	if got := event.Protocol.Permission.Options[0].ID; got != "allow_once" {
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

func TestCanonicalToolNamePrefersDurablePayloadAndMatchesMessageToolCallID(t *testing.T) {
	t.Parallel()

	message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{
		{ID: "call-read", Name: "READ_FILE", Args: `{"path":"a.go"}`},
		{ID: "call-run", Name: "RUN_COMMAND", Args: `{"command":"go test ./..."}`},
	}, "")
	event := &Event{
		Type:    EventTypeToolCall,
		Message: &message,
		Tool:    &EventTool{ID: "call-run"},
		Meta:    map[string]any{"caelis": map[string]any{"runtime": map[string]any{"tool": map[string]any{"name": "META_ONLY"}}}},
	}

	if got := CanonicalToolName(event, nil); got != "RUN_COMMAND" {
		t.Fatalf("CanonicalToolName() = %q, want Event.Tool.ID matched message tool call name", got)
	}
	event.Protocol = &EventProtocol{
		Update: &ProtocolUpdate{
			SessionUpdate: string(ProtocolUpdateTypeToolCall),
			ToolCallID:    "call-run",
			Title:         "EXECUTE",
			Kind:          "execute",
		},
	}
	if got := CanonicalToolName(event, nil); got != "RUN_COMMAND" {
		t.Fatalf("CanonicalToolName() = %q, want message tool call name", got)
	}
	event.Tool.Name = "CANONICAL_TOOL"
	if got := CanonicalToolName(event, nil); got != "CANONICAL_TOOL" {
		t.Fatalf("CanonicalToolName() = %q, want Event.Tool name", got)
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

func TestValidateDurableCoreEventAllowsExplicitContextMessage(t *testing.T) {
	t.Parallel()

	message := model.NewTextMessage(model.RoleUser, "plugin-provided context")
	err := ValidateDurableCoreEvent(CanonicalizeEvent(&Event{
		Type:       EventTypeContext,
		Visibility: VisibilityCanonical,
		Message:    &message,
		Text:       "plugin-provided context",
	}))
	if err != nil {
		t.Fatalf("ValidateDurableCoreEvent(context) error = %v", err)
	}
}

func TestValidateDurableCoreEventRejectsCustomMessage(t *testing.T) {
	t.Parallel()

	message := model.NewTextMessage(model.RoleUser, "implicit custom context")
	err := ValidateDurableCoreEvent(CanonicalizeEvent(&Event{
		Type:       EventTypeCustom,
		Visibility: VisibilityCanonical,
		Message:    &message,
		Text:       "implicit custom context",
	}))
	if err == nil {
		t.Fatal("ValidateDurableCoreEvent(custom message) error = nil, want explicit context type")
	}
	if detail := EventValidationDetail(err); !strings.Contains(detail, "explicit model-context event type") {
		t.Fatalf("validation detail = %q, want explicit context type", detail)
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
