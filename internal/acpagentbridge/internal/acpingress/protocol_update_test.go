package acpingress

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestProtocolUpdateFromContentChunkNormalizesRawJSON(t *testing.T) {
	t.Parallel()

	meta := map[string]any{"provider": map[string]any{"sequence": float64(3)}}
	update, err := protocolUpdateFromContentChunk(client.ContentChunk{
		SessionUpdate: client.UpdateAgentMessage,
		Content:       json.RawMessage(`{"type":"text","text":"external"}`),
		MessageID:     " message-1 ",
		Meta:          meta,
	})
	if err != nil {
		t.Fatalf("protocolUpdateFromContentChunk() error = %v", err)
	}
	want := &session.ProtocolUpdate{
		SessionUpdate: client.UpdateAgentMessage,
		Content:       map[string]any{"type": "text", "text": "external"},
		MessageID:     "message-1",
		Meta:          map[string]any{"provider": map[string]any{"sequence": float64(3)}},
	}
	if !reflect.DeepEqual(update, want) {
		t.Fatalf("protocolUpdateFromContentChunk() = %#v, want %#v", update, want)
	}
	meta["provider"].(map[string]any)["sequence"] = float64(4)
	if update.Meta["provider"].(map[string]any)["sequence"] != float64(3) {
		t.Fatalf("protocol update aliased input metadata: %#v", update.Meta)
	}
}

func TestProtocolUpdateFromToolCallDeepCopiesWireValues(t *testing.T) {
	t.Parallel()

	line := 17
	oldText := "before"
	wire := client.ToolCall{
		SessionUpdate: client.UpdateToolCall,
		ToolCallID:    "call-1",
		RawInput:      map[string]any{"nested": map[string]any{"value": "before"}},
		Content: []client.ToolCallContent{{
			Type: "diff", Content: map[string]any{"nested": map[string]any{"value": "before"}},
			OldText: &oldText,
		}},
		Locations: []client.ToolCallLocation{{Path: "README.md", Line: &line}},
		Meta:      map[string]any{"nested": map[string]any{"value": "before"}},
	}
	update := protocolUpdateFromToolCall(wire)
	wire.RawInput.(map[string]any)["nested"].(map[string]any)["value"] = "after"
	wire.Content[0].Content.(map[string]any)["nested"].(map[string]any)["value"] = "after"
	wire.Meta["nested"].(map[string]any)["value"] = "after"
	line = 18
	oldText = "after"
	content := session.ProtocolToolCallContentOf(update)

	if got := update.RawInput["nested"].(map[string]any)["value"]; got != "before" {
		t.Fatalf("decoded raw input mutated to %v", got)
	}
	if got := content[0].Content.(map[string]any)["nested"].(map[string]any)["value"]; got != "before" {
		t.Fatalf("decoded tool content mutated to %v", got)
	}
	if got := update.Meta["nested"].(map[string]any)["value"]; got != "before" {
		t.Fatalf("decoded meta mutated to %v", got)
	}
	if update.Locations[0].Line == nil || *update.Locations[0].Line != 17 || content[0].OldText == nil || *content[0].OldText != "before" {
		t.Fatalf("decoded pointer fields mutated: %#v", update)
	}
}
