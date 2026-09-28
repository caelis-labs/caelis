package projection

import (
	"encoding/json"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestTextAndStructuredReceiptProjectAsSeparateOrderedContent(t *testing.T) {
	t.Parallel()
	message := model.NewMessage(model.RoleTool, model.Part{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{
		ToolUseID: "call-1", Name: "ApplicationLookup", Content: []model.Part{
			model.NewTextPart("Evidence"),
			model.NewJSONPart(json.RawMessage(`{"outcome":"failed","structuredContent":{"reason":"not found"}}`)),
		},
	}})
	updates, err := ProjectEvent(&session.Event{Type: session.EventTypeToolResult, Message: &message, Tool: &session.EventTool{
		ID: "call-1", Name: "ApplicationLookup", Status: "failed", Output: map[string]any{"outcome": "failed", "structuredContent": map[string]any{"reason": "not found"}},
	}})
	if err != nil || len(updates) != 1 {
		t.Fatalf("updates = %#v, err = %v", updates, err)
	}
	projected, ok := updates[0].(eventstream.ToolCallUpdate)
	if !ok || len(projected.Content) != 2 {
		t.Fatalf("tool update = %#v", updates[0])
	}
	first, ok := projected.Content[0].Content.(eventstream.TextContent)
	if !ok || first.Text != "Evidence" {
		t.Fatalf("text content = %#v", projected.Content[0])
	}
	last, ok := projected.Content[1].Content.(eventstream.TextContent)
	if !ok || last.Text != `{"outcome":"failed","structuredContent":{"reason":"not found"}}` {
		t.Fatalf("receipt content = %#v", projected.Content[1])
	}
}

func TestCanonicalMixedToolResultProjectsOrderedACPContent(t *testing.T) {
	t.Parallel()
	receipt := json.RawMessage(`{"outcome":"succeeded","structuredContent":{"score":1},"resources":["resource-1"]}`)
	message := model.NewMessage(model.RoleTool, model.Part{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{
		ToolUseID: "call-1", Name: "ApplicationLookup", Content: []model.Part{
			model.NewTextPart("Evidence"),
			model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: "aW1n"}, "image/png", "evidence.png"),
			model.NewJSONPart(receipt),
		},
	}})
	event := &session.Event{
		Type: session.EventTypeToolResult, Message: &message,
		Tool: &session.EventTool{ID: "call-1", Name: "ApplicationLookup", Status: "completed", Output: map[string]any{
			"outcome": "succeeded", "structuredContent": map[string]any{"score": 1}, "resources": []any{"resource-1"},
		}},
	}
	original, err := ProjectEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded session.Event
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatal(err)
	}
	replayed, err := ProjectEvent(&reloaded)
	if err != nil {
		t.Fatal(err)
	}
	liveWire, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	replayWire, err := json.Marshal(replayed)
	if err != nil || string(liveWire) != string(replayWire) {
		t.Fatalf("live/replay projection differ: %s / %s (%v)", liveWire, replayWire, err)
	}
	if len(replayed) != 1 {
		t.Fatalf("updates = %#v", replayed)
	}
	update, ok := replayed[0].(eventstream.ToolCallUpdate)
	if !ok || len(update.Content) != 3 {
		t.Fatalf("tool update = %#v, want text/image/receipt", replayed[0])
	}
	for index, want := range []string{"text", "image", "text"} {
		data, err := json.Marshal(update.Content[index].Content)
		if err != nil {
			t.Fatal(err)
		}
		var part map[string]any
		if err := json.Unmarshal(data, &part); err != nil || part["type"] != want {
			t.Fatalf("content[%d] = %s (%v), want %s", index, data, err, want)
		}
		if index == 1 && (part["mimeType"] != "image/png" || part["data"] != "aW1n") {
			t.Fatalf("image content = %#v", part)
		}
	}
	if update.RawOutput.(map[string]any)["outcome"] != "succeeded" {
		t.Fatalf("rawOutput = %#v, want true receipt", update.RawOutput)
	}
	wire, err := json.Marshal(eventstream.SessionNotification{SessionID: "sess-1", Update: update})
	if err != nil {
		t.Fatal(err)
	}
	var notification acpsdk.SessionNotification
	if err := json.Unmarshal(wire, &notification); err != nil {
		t.Fatalf("ACP decode image tool result: %v", err)
	}
	if err := notification.Validate(); err != nil {
		t.Fatalf("ACP validate image tool result: %v", err)
	}
}
