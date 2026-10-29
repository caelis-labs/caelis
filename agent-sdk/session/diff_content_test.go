package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestStructuredDiffCloneAndGenericRestore(t *testing.T) {
	t.Parallel()
	oldPath := " old.txt "
	changes := []ProtocolDiffChange{{Operation: "move", Path: " new.txt ", OldPath: &oldPath, Meta: map[string]any{"nested": map[string]any{"value": "original"}}}}
	input := []ProtocolToolCallContent{{Type: "diff", Changes: &changes, Patch: &ProtocolDiffPatch{Meta: map[string]any{"nested": map[string]any{"value": "original"}}, Format: "git_patch", Text: "-old\r\n+new\r\n"}, Meta: map[string]any{"nested": map[string]any{"value": "original"}}}}
	cloned := CloneProtocolToolCallContent(input)
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	restored := ProtocolToolCallContentOf(&ProtocolUpdate{Content: generic})
	if !reflect.DeepEqual(restored, input) {
		t.Fatalf("generic restore = %#v, want %#v", restored, input)
	}
	changes[0].Path = "mutated"
	oldPath = "mutated"
	changes[0].Meta["nested"].(map[string]any)["value"] = "mutated"
	input[0].Patch.Text = "mutated"
	input[0].Patch.Meta["nested"].(map[string]any)["value"] = "mutated"
	input[0].Meta["nested"].(map[string]any)["value"] = "mutated"
	if (*cloned[0].Changes)[0].Path != " new.txt " || *(*cloned[0].Changes)[0].OldPath != " old.txt " || cloned[0].Patch.Meta["nested"].(map[string]any)["value"] != "original" || cloned[0].Patch.Text != "-old\r\n+new\r\n" || (*cloned[0].Changes)[0].Meta["nested"].(map[string]any)["value"] != "original" || cloned[0].Meta["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("clone shares structured diff state: %#v", cloned)
	}
}

func TestDiffPatchAnnotationsSurviveNativeAndProtocolCanonicalRoundTrips(t *testing.T) {
	patch := &ProtocolDiffPatch{Format: "git_patch", Text: "exact patch", Meta: map[string]any{"large": json.Number("9007199254740993"), "huge": json.Number("1e400")}}
	for _, native := range []bool{false, true} {
		event := Event{Type: EventTypeToolResult}
		if native {
			event.Tool = &EventTool{ID: "tool", Content: []EventToolContent{{Type: "diff", Patch: patch}}}
		} else {
			event.Protocol = &EventProtocol{Update: &ProtocolUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Content: []ProtocolToolCallContent{{Type: "diff", Patch: patch}}}}
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var restored Event
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		var got *ProtocolDiffPatch
		if native {
			got = restored.Tool.Content[0].Patch
		} else {
			got = ProtocolToolCallContentOf(restored.Protocol.Update)[0].Patch
		}
		if !reflect.DeepEqual(got, patch) {
			t.Fatalf("native=%v: patch %#v, want %#v", native, got, patch)
		}
	}
}
