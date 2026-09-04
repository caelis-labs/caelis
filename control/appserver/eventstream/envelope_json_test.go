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
