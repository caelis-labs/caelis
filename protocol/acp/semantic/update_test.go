package semantic_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/semantic"
)

func TestUpdateWireRoundTripPreservesSDKSemantics(t *testing.T) {
	t.Parallel()

	line := 17
	oldText := "before"
	tests := []session.ProtocolUpdate{
		{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       map[string]any{"type": "text", "text": "hello"},
			MessageID:     "message-1",
			Meta:          map[string]any{"provider": map[string]any{"sequence": float64(2)}},
		},
		{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "Read file",
			Kind:          schema.ToolKindRead,
			Status:        schema.ToolStatusPending,
			RawInput:      map[string]any{"path": "README.md", "nested": map[string]any{"line": float64(1)}},
			Content: []session.ProtocolToolCallContent{{
				Type:    "diff",
				Path:    "README.md",
				OldText: &oldText,
				NewText: "after",
			}},
			Locations: []session.ProtocolToolCallLocation{{Path: "README.md", Line: &line}},
		},
		{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        schema.ToolStatusCompleted,
			RawOutput:     map[string]any{"stdout": "ok\n"},
			Content: []session.ProtocolToolCallContent{{
				Type:       "terminal",
				TerminalID: "terminal-1",
				Content:    map[string]any{"type": "text", "text": "ok\n"},
			}},
		},
		{
			SessionUpdate: schema.UpdatePlan,
			Entries: []session.ProtocolPlanEntry{{
				Content: "Run tests", Status: "in_progress", Priority: "high",
			}},
		},
	}

	for _, want := range tests {
		want := want
		t.Run(want.SessionUpdate, func(t *testing.T) {
			t.Parallel()
			wire, err := semantic.EncodeUpdate(&want)
			if err != nil {
				t.Fatalf("EncodeUpdate() error = %v", err)
			}
			got, err := semantic.DecodeUpdate(wire)
			if err != nil {
				t.Fatalf("DecodeUpdate() error = %v", err)
			}
			if !reflect.DeepEqual(got, normalizedUpdate(want)) {
				t.Fatalf("round trip = %#v, want %#v", got, normalizedUpdate(want))
			}
		})
	}
}

func TestRawAndDecodedContentUseSameSDKSemantics(t *testing.T) {
	t.Parallel()

	content := map[string]any{"type": "text", "text": "external"}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	external, err := semantic.DecodeRawContentUpdate(schema.UpdateAgentMessage, raw, "message-1", map[string]any{"sequence": float64(3)})
	if err != nil {
		t.Fatalf("DecodeRawContentUpdate() error = %v", err)
	}
	builtIn, err := semantic.DecodeUpdate(schema.ContentChunk{
		SessionUpdate: schema.UpdateAgentMessage,
		Content:       schema.TextContent{Type: "text", Text: "external"},
		MessageID:     "message-1",
		Meta:          map[string]any{"sequence": float64(3)},
	})
	if err != nil {
		t.Fatalf("DecodeUpdate() error = %v", err)
	}
	if !reflect.DeepEqual(external, builtIn) {
		t.Fatalf("external semantics = %#v, built-in semantics = %#v", external, builtIn)
	}
}

func TestDecodeUpdateRecursivelyIsolatesWireValues(t *testing.T) {
	t.Parallel()

	wire := schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    "call-1",
		RawInput:      map[string]any{"nested": map[string]any{"value": "before"}},
		Meta:          map[string]any{"nested": map[string]any{"value": "before"}},
	}
	decoded, err := semantic.DecodeUpdate(wire)
	if err != nil {
		t.Fatalf("DecodeUpdate() error = %v", err)
	}
	wire.RawInput.(map[string]any)["nested"].(map[string]any)["value"] = "after"
	wire.Meta["nested"].(map[string]any)["value"] = "after"
	if got := decoded.RawInput["nested"].(map[string]any)["value"]; got != "before" {
		t.Fatalf("decoded raw input mutated to %v", got)
	}
	if got := decoded.Meta["nested"].(map[string]any)["value"]; got != "before" {
		t.Fatalf("decoded meta mutated to %v", got)
	}
}

func normalizedUpdate(in session.ProtocolUpdate) *session.ProtocolUpdate {
	protocol := session.CloneEventProtocol(session.EventProtocol{Update: &in})
	return protocol.Update
}
