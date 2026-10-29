package client

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestDraftOutboundFileDiffPreservesStructuredChangesAndPatch(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	patch := "diff --git a/changed.txt b/changed.txt\r\n--- a/changed.txt\r\n+++ b/changed.txt\r\n@@ -1 +1 @@\r\n-old\r\n+new\r\n"
	wire := json.RawMessage(`{"type":"diff","_meta":{"display":{"label":"exact"}},"changes":[` +
		`{"operation":"add","path":"/tmp/new file.txt ","fileType":"text","mimeType":"text/plain","_meta":{"source":{"ordinal":1}}},` +
		`{"operation":"delete","path":"/tmp/deleted.bin","fileType":"binary","mimeType":null},` +
		`{"operation":"modify","path":"/tmp/changed.txt","fileType":null},` +
		`{"operation":"move","path":"/tmp/新目录","oldPath":"/tmp/旧目录","fileType":"directory"},` +
		`{"operation":"copy","path":"/tmp/copied-link","oldPath":"/tmp/original-link","fileType":"symlink"}` +
		`],"patch":{"_meta":{"large":9007199254740993,"huge":1e400},"format":"git_patch","text":` + string(mustDiffJSON(t, patch)) + `}}`)
	if err := agent.update(ctx, "session", map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "edit", "status": "completed", "content": []any{wire},
	}); err != nil {
		t.Fatal(err)
	}
	update := requireDraftUpdate(t, ctx, updates).Update.(ToolCallUpdate)
	if len(update.Content) != 1 {
		t.Fatalf("tool content = %#v", update.Content)
	}
	var want ToolCallContent
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.UseNumber()
	if err := decoder.Decode(&want); err != nil {
		t.Fatal(err)
	}
	got := update.Content[0]
	if !reflect.DeepEqual(got, want) || got.Changes == nil || len(*got.Changes) != 5 || got.Patch == nil || got.Patch.Text != patch {
		t.Fatalf("structured diff = %#v, want %#v", got, want)
	}
	if got.Path != "" || got.OldText != nil || got.NewText != "" {
		t.Fatalf("git patch was reinterpreted as legacy file content: %#v", got)
	}
	var restored ToolCallUpdate
	if err := json.Unmarshal(mustDiffJSON(t, update), &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Content[0].Patch, update.Content[0].Patch) {
		t.Fatalf("typed diff changed after JSON round trip: %#v", restored.Content)
	}
}

func TestDraftOutboundFileDiffEmptyAndNullableContent(t *testing.T) {
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		content json.RawMessage
		diff    bool
	}{
		{name: "omitted patch", content: json.RawMessage(`[{"type":"diff","changes":[]}]`), diff: true},
		{name: "null patch and metadata", content: json.RawMessage(`[{"type":"diff","changes":[],"patch":null,"_meta":null}]`), diff: true},
		{name: "null content", content: json.RawMessage(`null`)},
		{name: "empty content", content: json.RawMessage(`[]`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := agent.update(ctx, "session", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "edit", "content": test.content}); err != nil {
				t.Fatal(err)
			}
			got := requireDraftUpdate(t, ctx, updates).Update.(ToolCallUpdate)
			if _, present := got.RawPatch["content"]; !present {
				t.Fatal("content replacement presence lost")
			}
			if test.diff {
				if len(got.Content) != 1 || got.Content[0].Changes == nil || len(*got.Content[0].Changes) != 0 || got.Content[0].Patch != nil || got.Content[0].Meta != nil {
					t.Fatalf("empty diff facts changed: %#v", got.Content)
				}
				var restored ToolCallContent
				if err := json.Unmarshal(mustDiffJSON(t, got.Content[0]), &restored); err != nil || restored.Changes == nil || len(*restored.Changes) != 0 {
					t.Fatalf("explicit empty changes lost: %#v, %v", restored, err)
				}
			} else if len(got.Content) != 0 {
				t.Fatalf("clear retained file diff: %#v", got.Content)
			}
		})
	}
}

func TestStableOutboundLegacyFileDiffRetainsOldAndNewText(t *testing.T) {
	update, err := decodeUpdate(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"edit","content":[{"type":"diff","path":"/tmp/legacy.txt","oldText":"before\n","newText":"after\n"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := update.(ToolCallUpdate).Content[0]
	if got.Path != "/tmp/legacy.txt" || got.OldText == nil || *got.OldText != "before\n" || got.NewText != "after\n" || got.Changes != nil || got.Patch != nil {
		t.Fatalf("legacy diff changed: %#v", got)
	}
}

func mustDiffJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
