package session

import (
	"context"
	"testing"
)

func TestValidateEvent(t *testing.T) {
	tests := []struct {
		name    string
		event   *Event
		wantErr bool
	}{
		{"nil", nil, true},
		{"empty kind", &Event{}, true},
		{"user without payload", &Event{Kind: EventKindUser, Visibility: VisibilityCanonical}, true},
		{"user valid", &Event{
			Kind: EventKindUser, Visibility: VisibilityCanonical,
			UserPayload: &UserPayload{Parts: []EventPart{{Kind: PartKindText, Text: "hi"}}},
		}, false},
		{"tool_call no callid", &Event{
			Kind: EventKindToolCall, Visibility: VisibilityCanonical,
			ToolCallPayload: &ToolCallPayload{Name: "TOOL"},
		}, true},
		{"tool_call valid", &Event{
			Kind: EventKindToolCall, Visibility: VisibilityCanonical,
			ToolCallPayload: &ToolCallPayload{CallID: "c1", Name: "TOOL"},
		}, false},
		{"tool_result no callid", &Event{
			Kind: EventKindToolResult, Visibility: VisibilityCanonical,
			ToolResultPayload: &ToolResultPayload{Name: "TOOL"},
		}, true},
		{"plan no payload", &Event{Kind: EventKindPlan, Visibility: VisibilityCanonical}, true},
		{"compaction no payload", &Event{Kind: EventKindCompaction, Visibility: VisibilityCanonical}, true},
		{"system no payload", &Event{Kind: EventKindSystem, Visibility: VisibilityCanonical}, true},
		{"lifecycle no payload", &Event{Kind: EventKindLifecycle, Visibility: VisibilityCanonical}, true},
		{"notice no payload", &Event{Kind: EventKindNotice, Visibility: VisibilityCanonical}, true},
		{"assistant valid", &Event{
			Kind: EventKindAssistant, Visibility: VisibilityCanonical,
			AssistantPayload: &AssistantPayload{Parts: []EventPart{{Kind: PartKindText, Text: "ok"}}},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEvent(tt.event)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEvent() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCanonicalizeEvent(t *testing.T) {
	e := Event{Kind: EventKindUser}
	CanonicalizeEvent(&e)
	if e.Visibility != VisibilityCanonical {
		t.Errorf("visibility: got %q, want %q", e.Visibility, VisibilityCanonical)
	}
	if e.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
}

func TestCanonicalizeEventNilMeta(t *testing.T) {
	e := Event{
		Kind: EventKindAssistant,
		AssistantPayload: &AssistantPayload{
			Parts: []EventPart{{Kind: PartKindText, Text: "ok"}},
		},
		ProviderMeta: map[string]any{},
	}
	CanonicalizeEvent(&e)
	if e.ProviderMeta != nil {
		t.Error("expected empty ProviderMeta to be nilled")
	}
}

func TestAppendEventValidationRejectsInvalid(t *testing.T) {
	svc := InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
	})

	// Invalid event: tool_call without CallID.
	_, err := svc.AppendEvent(ctx, sess.Ref, Event{
		Kind:            EventKindToolCall,
		Visibility:      VisibilityCanonical,
		ToolCallPayload: &ToolCallPayload{Name: "TOOL"},
	})
	if err == nil {
		t.Error("expected validation error for tool_call without CallID")
	}
}

func TestControllerBinding(t *testing.T) {
	svc := InMemoryService().(*memService)
	ctx := context.Background()
	sess, _ := svc.Create(ctx, CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
	})

	err := svc.BindController(ctx, sess.Ref, ControllerBinding{AgentName: "agent-a"})
	if err != nil {
		t.Fatalf("BindController: %v", err)
	}

	got, _ := svc.Get(ctx, sess.Ref)
	if got.Controller.AgentName != "agent-a" {
		t.Errorf("got %q, want %q", got.Controller.AgentName, "agent-a")
	}
}

func TestParticipantLifecycle(t *testing.T) {
	svc := InMemoryService().(*memService)
	ctx := context.Background()
	sess, _ := svc.Create(ctx, CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
	})

	// Add participant.
	err := svc.PutParticipant(ctx, sess.Ref, ParticipantBinding{ID: "p1", Role: "observer"})
	if err != nil {
		t.Fatalf("PutParticipant: %v", err)
	}

	got, _ := svc.Get(ctx, sess.Ref)
	if len(got.Participants) != 1 {
		t.Fatalf("got %d participants, want 1", len(got.Participants))
	}

	// Update participant.
	err = svc.PutParticipant(ctx, sess.Ref, ParticipantBinding{ID: "p1", Role: "editor"})
	if err != nil {
		t.Fatalf("PutParticipant update: %v", err)
	}
	got, _ = svc.Get(ctx, sess.Ref)
	if len(got.Participants) != 1 || got.Participants[0].Role != "editor" {
		t.Errorf("expected updated role")
	}

	// Remove participant.
	err = svc.RemoveParticipant(ctx, sess.Ref, "p1")
	if err != nil {
		t.Fatalf("RemoveParticipant: %v", err)
	}
	got, _ = svc.Get(ctx, sess.Ref)
	if len(got.Participants) != 0 {
		t.Errorf("got %d participants, want 0", len(got.Participants))
	}

	// Remove non-existent.
	err = svc.RemoveParticipant(ctx, sess.Ref, "missing")
	if err == nil {
		t.Error("expected error for missing participant")
	}
}

func TestParticipantEventRequiresPayloadAndClonesMetadata(t *testing.T) {
	event := Event{
		Kind:       EventKindParticipant,
		Visibility: VisibilityCanonical,
	}
	if err := ValidateEvent(&event); err == nil {
		t.Fatal("ValidateEvent() error = nil, want missing participant payload error")
	}

	event.ParticipantPayload = &ParticipantPayload{
		ParticipantID: "participant-1",
		Role:          "reviewer",
		State:         "joined",
		Metadata:      map[string]string{"agent": "codex"},
	}
	if err := ValidateEvent(&event); err != nil {
		t.Fatalf("ValidateEvent() error = %v", err)
	}

	cloned := event.Clone()
	cloned.ParticipantPayload.Metadata["agent"] = "changed"
	if event.ParticipantPayload.Metadata["agent"] != "codex" {
		t.Fatalf("clone mutated original metadata: %#v", event.ParticipantPayload.Metadata)
	}
}

func TestStructuredState(t *testing.T) {
	svc := InMemoryService().(*memService)
	ctx := context.Background()
	sess, _ := svc.Create(ctx, CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
	})

	// Replace state.
	err := svc.ReplaceState(ctx, sess.Ref, map[string]any{
		"model": "claude-sonnet",
		"usage": map[string]any{"tokens": 1000},
	})
	if err != nil {
		t.Fatalf("ReplaceState: %v", err)
	}

	// Snapshot state.
	st, err := svc.SnapshotState(ctx, sess.Ref)
	if err != nil {
		t.Fatalf("SnapshotState: %v", err)
	}
	if st["model"] != "claude-sonnet" {
		t.Errorf("got %v, want %q", st["model"], "claude-sonnet")
	}

	// Verify deep copy.
	usage, ok := st["usage"].(map[string]any)
	if !ok {
		t.Fatal("expected usage to be map")
	}
	usage["tokens"] = 9999
	st2, _ := svc.SnapshotState(ctx, sess.Ref)
	usage2 := st2["usage"].(map[string]any)
	if usage2["tokens"].(float64) == 9999 {
		t.Error("SnapshotState should return deep copy")
	}
}

func TestMirrorExcludedFromModelContext(t *testing.T) {
	events := []Event{
		{
			Kind: EventKindUser, Visibility: VisibilityCanonical,
			UserPayload: &UserPayload{Parts: []EventPart{{Kind: PartKindText, Text: "visible"}}},
		},
		{
			Kind: EventKindAssistant, Visibility: VisibilityMirror,
			AssistantPayload: &AssistantPayload{Parts: []EventPart{{Kind: PartKindText, Text: "mirror"}}},
		},
		{
			Kind: EventKindUser, Visibility: VisibilityCanonical,
			UserPayload: &UserPayload{Parts: []EventPart{{Kind: PartKindText, Text: "after"}}},
		},
	}
	msgs := ModelContextFromEvents(events)
	if len(msgs) != 2 {
		t.Fatalf("got %d, want 2 (mirror excluded)", len(msgs))
	}
	if msgs[0].Content[0].Text != "visible" || msgs[1].Content[0].Text != "after" {
		t.Errorf("unexpected messages: %v, %v", msgs[0].Content[0].Text, msgs[1].Content[0].Text)
	}
}
