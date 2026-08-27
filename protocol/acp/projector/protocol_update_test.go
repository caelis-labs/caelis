package projector

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestProtocolToolUpdateProjectionClonesAndPreservesWireFields(t *testing.T) {
	t.Parallel()

	line := 17
	protocolUpdate := &session.ProtocolUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    "call-1",
		Title:         " Tool result ",
		Kind:          ToolKindRead,
		Status:        ToolStatusCompleted,
		RawInput:      map[string]any{"nested": map[string]any{"value": "before"}},
		Content: []session.ProtocolToolCallContent{{
			Type: "content", Content: map[string]any{"nested": map[string]any{"value": "before"}},
		}},
		Locations: []session.ProtocolToolCallLocation{{Path: " README.md ", Line: &line}},
		Meta:      map[string]any{"nested": map[string]any{"value": "before"}},
	}
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		Type:     session.EventTypeToolResult,
		Protocol: &session.EventProtocol{Update: protocolUpdate},
	})
	if err != nil || len(updates) != 1 {
		t.Fatalf("ProjectEvent() = %#v, %v; want one update", updates, err)
	}
	wire, ok := updates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("ProjectEvent() update = %T, want ToolCallUpdate", updates[0])
	}
	protocolUpdate.RawInput["nested"].(map[string]any)["value"] = "after"
	protocolUpdate.Meta["nested"].(map[string]any)["value"] = "after"
	protocolUpdate.Content.([]session.ProtocolToolCallContent)[0].Content.(map[string]any)["nested"].(map[string]any)["value"] = "after"
	line = 18

	if wire.Title == nil || *wire.Title != "Tool result" || wire.Locations[0].Path != " README.md " || wire.Locations[0].Line == nil || *wire.Locations[0].Line != 17 {
		t.Fatalf("wire scalar fields = %#v, want normalized title and exact location", wire)
	}
	if got := wire.RawInput.(map[string]any)["nested"].(map[string]any)["value"]; got != "before" {
		t.Fatalf("wire raw input mutated to %v", got)
	}
	if got := wire.Content[0].Content.(map[string]any)["nested"].(map[string]any)["value"]; got != "before" {
		t.Fatalf("wire content mutated to %v", got)
	}
	if got := wire.Meta["nested"].(map[string]any)["value"]; got != "before" {
		t.Fatalf("wire meta mutated to %v", got)
	}
}
