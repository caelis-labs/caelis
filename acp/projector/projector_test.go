package projector

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/session"
)

func TestEventProjectorProjectsCanonicalToolCall(t *testing.T) {
	line := 9
	updates := (EventProjector{}).ProjectEvent(&session.Event{
		Kind:         session.EventKindToolCall,
		RunID:        "run-1",
		ProviderMeta: map[string]any{"acp_locations": []acp.ToolCallLocation{{Path: "main.go", Line: &line}}},
		ToolCallPayload: &session.ToolCallPayload{
			CallID: "call-1",
			Name:   "RUN_COMMAND",
			Status: "pending",
			Args:   map[string]any{"command": "go test ./..."},
			Display: []session.EventPart{{
				Kind: session.PartKindText,
				Text: "display only",
			}},
		},
	})
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(acp.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want acp.ToolCallUpdate", updates[0])
	}
	if update.ToolCallID != "call-1" || update.Kind != "execute" || update.RawInput.(map[string]any)["command"] != "go test ./..." {
		t.Fatalf("update = %#v, want canonical tool identity and input", update)
	}
	if len(update.Locations) != 1 || update.Locations[0].Path != "main.go" || update.Locations[0].Line == nil || *update.Locations[0].Line != line {
		t.Fatalf("locations = %#v, want standard ACP location", update.Locations)
	}
	if _, ok := update.Meta["run_id"]; ok {
		t.Fatalf("run_id leaked at _meta top level: %#v", update.Meta)
	}
	caelis, ok := update.Meta["caelis"].(map[string]any)
	if !ok || caelis["run_id"] != "run-1" || caelis["display"] == nil {
		t.Fatalf("_meta.caelis = %#v, want run_id and display hints", update.Meta)
	}
}

func TestEventProjectorProjectsProviderMetadataOutsideCaelisNamespace(t *testing.T) {
	updates := (EventProjector{}).ProjectEvent(&session.Event{
		Kind:  session.EventKindToolCall,
		RunID: "run-1",
		ProviderMeta: map[string]any{
			"acp_meta": map[string]any{
				"provider":      "openai",
				"finish_reason": "tool_calls",
			},
		},
		ToolCallPayload: &session.ToolCallPayload{
			CallID: "call-1",
			Name:   "RUN_COMMAND",
			Status: "pending",
			Args:   map[string]any{"command": "go test ./..."},
		},
	})
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(acp.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want acp.ToolCallUpdate", updates[0])
	}
	provider, ok := update.Meta["provider"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.provider = %#v, want provider metadata", update.Meta)
	}
	if provider["provider"] != "openai" || provider["finish_reason"] != "tool_calls" {
		t.Fatalf("_meta.provider = %#v", provider)
	}
	caelis, ok := update.Meta["caelis"].(map[string]any)
	if !ok || caelis["run_id"] != "run-1" {
		t.Fatalf("_meta.caelis = %#v, want Caelis display hints only", update.Meta)
	}
	if _, ok := caelis["provider"]; ok {
		t.Fatalf("provider metadata leaked into _meta.caelis: %#v", caelis)
	}
}

func TestEventProjectorProjectsCanonicalHandoff(t *testing.T) {
	updates := (EventProjector{}).ProjectEvent(&session.Event{
		Kind:  session.EventKindHandoff,
		RunID: "run-1",
		HandoffPayload: &session.HandoffPayload{
			FromAgent: "planner",
			ToAgent:   "implementer",
			Reason:    "implementation is ready",
		},
	})
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(acp.SessionInfoUpdate)
	if !ok {
		t.Fatalf("update = %T, want acp.SessionInfoUpdate", updates[0])
	}
	if update.Handoff == nil || update.Handoff.FromAgent != "planner" || update.Handoff.ToAgent != "implementer" || update.Handoff.Reason != "implementation is ready" {
		t.Fatalf("handoff = %#v, want canonical handoff fields", update.Handoff)
	}
	if update.Meta != nil {
		if _, ok := update.Meta["handoff"]; ok {
			t.Fatalf("handoff semantics leaked into _meta: %#v", update.Meta)
		}
	}
}

func TestEventProjectorProjectsCanonicalParticipant(t *testing.T) {
	updates := (EventProjector{}).ProjectEvent(&session.Event{
		Kind:  session.EventKindParticipant,
		RunID: "run-1",
		Actor: session.ActorRef{
			ParticipantID: "participant-1",
		},
		ParticipantPayload: &session.ParticipantPayload{
			ParticipantID: "participant-1",
			Role:          "reviewer",
			State:         "joined",
			Metadata:      map[string]string{"agent": "codex"},
		},
	})
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(acp.SessionInfoUpdate)
	if !ok {
		t.Fatalf("update = %T, want acp.SessionInfoUpdate", updates[0])
	}
	if update.Participant == nil || update.Participant.ParticipantID != "participant-1" || update.Participant.Role != "reviewer" || update.Participant.State != "joined" {
		t.Fatalf("participant = %#v, want canonical participant fields", update.Participant)
	}
	if update.Participant.Metadata["agent"] != "codex" {
		t.Fatalf("participant metadata = %#v, want agent codex", update.Participant.Metadata)
	}
	if update.Meta != nil {
		if _, ok := update.Meta["participant"]; ok {
			t.Fatalf("participant semantics leaked into _meta: %#v", update.Meta)
		}
	}
}
