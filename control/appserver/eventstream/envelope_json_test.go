package eventstream

import (
	"encoding/json"
	"testing"
)

func TestEnvelopePlainJSONRoundTripRestoresTypedUpdate(t *testing.T) {
	t.Parallel()

	want := Envelope{
		Kind: KindSessionUpdate,
		Update: ContentChunk{
			SessionUpdate: UpdateAgentMessage,
			Content: TextContent{
				Type: "text",
				Text: "hello",
				Meta: map[string]json.RawMessage{"vendor": json.RawMessage(`{"trace":"abc"}`)},
			},
		},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	update, ok := got.Update.(ContentChunk)
	if !ok || update.SessionUpdate != UpdateAgentMessage {
		t.Fatalf("Update = %#v", got.Update)
	}
	content, ok := update.Content.(TextContent)
	if !ok || content.Text != "hello" || string(content.Meta["vendor"]) != `{"trace":"abc"}` {
		t.Fatalf("Content = %#v, want typed text content", update.Content)
	}
}

func TestEnvelopePlainJSONRoundTripRestoresNestedToolTextContent(t *testing.T) {
	t.Parallel()

	want := Envelope{
		Kind: KindSessionUpdate,
		Update: ToolCallUpdate{
			SessionUpdate: UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Content: []ToolCallContent{{
				Type:    "content",
				Content: TextContent{Type: "text", Text: "done"},
			}},
		},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	update, ok := got.Update.(ToolCallUpdate)
	if !ok || len(update.Content) != 1 {
		t.Fatalf("Update = %#v", got.Update)
	}
	content, ok := update.Content[0].Content.(TextContent)
	if !ok || content.Text != "done" {
		t.Fatalf("Content = %#v, want typed text content", update.Content[0].Content)
	}
}

func TestEnvelopeMetadataNumbersSurvivePlainJSONRoundTrip(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{UpdateAgentMessage, UpdateAgentThought, UpdateUserMessage, UpdateToolCall, UpdateToolCallInfo} {
		t.Run(kind, func(t *testing.T) {
			raw := json.RawMessage(`{"sessionUpdate":"` + kind + `","messageId":"message","toolCallId":"tool","_meta":{"large":9007199254740993,"nested":{"decimal":1.2500}}}`)
			update, err := DecodeUpdateJSON(raw)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(Envelope{Kind: KindSessionUpdate, Update: update, Meta: UpdateMeta(update)})
			if err != nil {
				t.Fatal(err)
			}
			var restored Envelope
			if err := json.Unmarshal(wire, &restored); err != nil {
				t.Fatal(err)
			}
			for _, meta := range []map[string]any{UpdateMeta(update), UpdateMeta(restored.Update), restored.Meta} {
				if meta["large"] != json.Number("9007199254740993") || meta["nested"].(map[string]any)["decimal"] != json.Number("1.2500") {
					t.Fatalf("metadata tokens changed: %#v", meta)
				}
			}
		})
	}
}
