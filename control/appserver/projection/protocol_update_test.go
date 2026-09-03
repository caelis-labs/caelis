package projection

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestProtocolToolUpdateProjectionPreservesExplicitEmptyContent(t *testing.T) {
	t.Parallel()

	updates, err := ProjectEvent(&session.Event{
		Type: session.EventTypeToolResult,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Content:       []session.ProtocolToolCallContent{},
		}},
	})
	if err != nil || len(updates) != 1 {
		t.Fatalf("ProjectEvent() = %#v, %v; want one update", updates, err)
	}
	wire, ok := updates[0].(eventstream.ToolCallUpdate)
	if !ok {
		t.Fatalf("ProjectEvent() update = %T, want ToolCallUpdate", updates[0])
	}
	if wire.Content == nil || len(wire.Content) != 0 {
		t.Fatalf("wire content = %#v, want present empty collection", wire.Content)
	}
	if wire.Title != nil || wire.Kind != nil || wire.Status != nil {
		t.Fatalf("wire sparse scalar fields = %#v, want omitted title/kind/status", wire)
	}
}

func TestProtocolRunCommandUpdatePreservesExplicitEmptyContent(t *testing.T) {
	t.Parallel()

	updates, err := ProjectEvent(&session.Event{
		Type: session.EventTypeToolResult,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          "RunCommand",
			Content:       []session.ProtocolToolCallContent{},
		}},
	})
	if err != nil || len(updates) != 1 {
		t.Fatalf("ProjectEvent() = %#v, %v; want one update", updates, err)
	}
	wire, ok := updates[0].(eventstream.ToolCallUpdate)
	if !ok {
		t.Fatalf("ProjectEvent() update = %T, want ToolCallUpdate", updates[0])
	}
	if wire.Content == nil || len(wire.Content) != 0 {
		t.Fatalf("wire content = %#v, want present empty replacement collection", wire.Content)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("json.Marshal(update) error = %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("json.Unmarshal(update) error = %v", err)
	}
	if content, present := object["content"]; !present || string(content) != "[]" {
		t.Fatalf("wire update = %s, want explicit empty content array", raw)
	}
	assertTerminalInfo(t, wire.Meta, "call-1")
}

func TestProtocolToolUpdateProjectionClonesAndPreservesWireFields(t *testing.T) {
	t.Parallel()

	line := 17
	protocolUpdate := &session.ProtocolUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo,
		ToolCallID:    "call-1",
		Title:         " Tool result ",
		Kind:          eventstream.ToolKindRead,
		Status:        eventstream.ToolStatusCompleted,
		RawInput:      map[string]any{"nested": map[string]any{"value": "before"}},
		Content: []session.ProtocolToolCallContent{{
			Type: "content", Content: map[string]any{"nested": map[string]any{"value": "before"}},
		}},
		Locations: []session.ProtocolToolCallLocation{{Path: " README.md ", Line: &line}},
		Meta:      map[string]any{"nested": map[string]any{"value": "before"}},
	}
	updates, err := ProjectEvent(&session.Event{
		Type:     session.EventTypeToolResult,
		Protocol: &session.EventProtocol{Update: protocolUpdate},
	})
	if err != nil || len(updates) != 1 {
		t.Fatalf("ProjectEvent() = %#v, %v; want one update", updates, err)
	}
	wire, ok := updates[0].(eventstream.ToolCallUpdate)
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
