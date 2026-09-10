package codex

import (
	"encoding/json"
	"reflect"
	"testing"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func TestMCPToolUsesStandardInputAndContent(t *testing.T) {
	var item threadItem
	if err := json.Unmarshal([]byte(`{"id":"send-1","type":"mcpToolCall","server":"caelis-collaboration","tool":"SendMessage","arguments":{"to":"parent","message":"Found cause","reply_to":"mail-1"},"result":{"content":[{"type":"text","text":"Message queued"},{"type":"future_block","value":"retained"}],"structuredContent":{"id":"mail-2"}}}`), &item); err != nil {
		t.Fatal(err)
	}
	start := toolStart("child", item).ToolCall
	if start == nil || !reflect.DeepEqual(start.RawInput, item.Arguments) || start.Title != "caelis-collaboration / SendMessage" {
		t.Fatalf("start = %#v", start)
	}
	for _, started := range []bool{false, true} {
		update := toolComplete("child", item, started, false, terminalOutputCanonical)
		var content []acp.ToolCallContent
		var rawInput, rawOutput any
		var meta map[string]json.RawMessage
		if started {
			content, rawInput, rawOutput, meta = update.ToolCallUpdate.Content, update.ToolCallUpdate.RawInput, update.ToolCallUpdate.RawOutput, update.ToolCallUpdate.Meta
		} else {
			content, rawInput, rawOutput, meta = update.ToolCall.Content, update.ToolCall.RawInput, update.ToolCall.RawOutput, update.ToolCall.Meta
		}
		if len(content) != 1 || content[0].Content == nil || content[0].Content.Content.Text == nil || content[0].Content.Content.Text.Text != "Message queued" {
			t.Fatalf("content = %#v", content)
		}
		if !reflect.DeepEqual(rawInput, item.Arguments) || !reflect.DeepEqual(rawOutput, item.Result) || !reflect.DeepEqual(meta, start.Meta) {
			t.Fatalf("lost MCP evidence: %#v", update)
		}
	}
}

func TestMCPCompletionPreservesSparseAndArbitraryArguments(t *testing.T) {
	for _, args := range []string{`{"to":"parent"}`, `[1,"two"]`, `"value"`, `42`, `null`} {
		updates, _, err := liveItemCompleted("child", json.RawMessage(`{"id":"mcp-1","type":"mcpToolCall","arguments":`+args+`,"result":{"content":[]}}`), false, true, false, terminalOutputCanonical)
		if err != nil || len(updates) != 1 {
			t.Fatalf("args %s: %#v, %v", args, updates, err)
		}
		wire, err := json.Marshal(updates[0])
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(wire, &fields); err != nil {
			t.Fatal(err)
		}
		if args != "null" && string(fields["rawInput"]) != args {
			t.Fatalf("rawInput = %s, want %s", fields["rawInput"], args)
		}
		if string(fields["rawOutput"]) != `{"content":[]}` {
			t.Fatalf("empty result lost: %s", wire)
		}
	}
	update := toolComplete("child", threadItem{ID: "mcp-1", Type: "mcpToolCall"}, true, false, terminalOutputCanonical)
	wire, _ := json.Marshal(update)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(wire, &fields)
	if _, exists := fields["rawInput"]; exists {
		t.Fatalf("sparse completion erased start input: %s", wire)
	}
	if _, exists := fields["content"]; exists {
		t.Fatalf("sparse completion erased content: %s", wire)
	}
}
