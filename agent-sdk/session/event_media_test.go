package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func sharedMediaEvent(t testing.TB, size int) Event {
	image := tool.Image{Data: []byte(strings.Repeat("abc", size/4)), MIMEType: "image/png", Name: "screen"}
	message := model.Message{Role: model.RoleTool, Parts: []model.Part{{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{ToolUseID: "call", Name: "screen", Content: tool.ModelParts([]tool.ContentBlock{image})}}}}
	return Event{Schema: EventSchemaVersion, Type: EventTypeToolResult, Message: &message, Tool: &EventTool{ID: "call", Name: "screen", Result: &tool.Result{Content: []tool.ContentBlock{image}}}}
}
func TestEventMediaSharesPayloadAndCompleteResult(t *testing.T) {
	original := sharedMediaEvent(t, 64<<10)
	before, _ := json.Marshal(original)
	raw, err := MarshalEventJSON(&original)
	if err != nil {
		t.Fatal(err)
	}
	data := original.Message.ToolResults()[0].Content[0].Media.Source.Data
	if bytes.Count(raw, []byte(data)) != 1 {
		t.Fatal("media not shared")
	}
	var got Event
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, got) {
		t.Fatal("full result or model view changed")
	}
	after, _ := json.Marshal(original)
	if !bytes.Equal(before, after) {
		t.Fatal("encoding mutated source")
	}
	for _, name := range []string{"missing", "digest", "unused", "future"} {
		t.Run(name, func(t *testing.T) {
			var object map[string]json.RawMessage
			_ = json.Unmarshal(raw, &object)
			switch name {
			case "missing":
				delete(object, "media")
			case "digest":
				object["media"] = json.RawMessage(`{"wrong":"payload"}`)
			case "unused":
				object["media"] = json.RawMessage(fmt.Sprintf(`{"%s":"unused"}`, eventMediaID("unused")))
			case "future":
				object["schema"] = json.RawMessage(`999`)
			}
			bad, _ := json.Marshal(object)
			if _, err := MigrateEventJSON(bad); err != nil {
				return
			}
			var event Event
			if err := json.Unmarshal(bad, &event); err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
}
func TestRetiredJournalMediaIsConsumedBeforeIgnoringExecution(t *testing.T) {
	event := sharedMediaEvent(t, 8192)
	raw, err := MarshalEventJSON(&event)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal(raw, &object)
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(object["tool"], &payload)
	object["journal"] = json.RawMessage(fmt.Sprintf(`{"schema":1,"kind":"tool_execution","tool_execution":{"schema":1,"result":%s}}`, payload["result"]))
	delete(object, "tool")
	object["schema"] = json.RawMessage(`2`)
	raw, _ = json.Marshal(object)
	migrated, err := MigrateEventJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got Event
	if err := json.Unmarshal(migrated, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Message, event.Message) {
		t.Fatal("legacy message media changed")
	}
	again, _ := json.Marshal(got)
	if bytes.Contains(again, []byte(`"journal"`)) {
		t.Fatal("retired execution was rewritten")
	}
	// A reference only in the retired receipt must also be consumed.
	delete(object, "message")
	raw, _ = json.Marshal(object)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyToolReceiptRestoresEventFields(t *testing.T) {
	var event EventTool
	const old = `{"id":"original","name":"lookup","result":{"id":"other","turn_complete":true,"is_error":true,"metadata":{"caelis.mcp.discovery.v1":true,"application_call":{"result_format":"content-v1"}},"content":[{"type":"text","text":"old"}]}}`
	if err := json.Unmarshal([]byte(old), &event); err != nil {
		t.Fatal(err)
	}
	if event.ID != "original" || event.Outcome != ToolOutcomeFailed || !event.Discovery || !event.ResultIncludesReceipt || event.Result.Content[0] != tool.Text("old") {
		t.Fatalf("restored event = %+v", event)
	}
	raw, err := json.Marshal(event.Result)
	if err != nil || string(raw) != `{"content":[{"type":"text","text":"old"}]}` {
		t.Fatalf("restored result = %s, %v", raw, err)
	}
}
