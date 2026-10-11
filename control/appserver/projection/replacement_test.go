package projection

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/internal/eventmeta"
)

func TestToolDisplayPatchPresenceAndEmptyTerminalSnapshotSurviveReplay(t *testing.T) {
	e := &session.Event{Type: session.EventTypeToolResult, Visibility: session.VisibilityUIOnly, Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "call", ReplacedFields: []string{"title", "kind", "rawInput", "rawOutput", "content"}, Meta: eventmeta.WithTerminalOutput(nil, "terminal", "", true)}}}
	updates, err := ProjectEvent(session.CloneEvent(e))
	if err != nil || len(updates) != 1 {
		t.Fatalf("project clear: %v %#v", err, updates)
	}
	u, ok := updates[0].(eventstream.ToolCallUpdate)
	if !ok || !reflect.DeepEqual(u.ReplacedFields, e.Protocol.Update.ReplacedFields) {
		t.Fatalf("presence dropped: %#v", updates)
	}
	output, present := eventmeta.TerminalOutput(u.Meta)
	if !present || !output.Replacement || output.Data != "" || output.TerminalID != "terminal" {
		t.Fatalf("terminal clear lost: %#v", u.Meta)
	}
	if u.RawInput != nil || u.RawOutput != nil || u.Title != nil || u.Kind != nil {
		t.Fatalf("clear fabricated values: %#v", u)
	}
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := eventstream.DecodeUpdateJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	copy := decoded.(eventstream.ToolCallUpdate)
	if !reflect.DeepEqual(copy.ReplacedFields, u.ReplacedFields) {
		t.Fatalf("wire lost presence: %s", raw)
	}
}

func TestNarrativeMetadataOnlyPatchSurvivesNativeWireRoundTrip(t *testing.T) {
	for _, kind := range []string{eventstream.UpdateUserMessage, eventstream.UpdateAgentMessage, eventstream.UpdateAgentThought} {
		e := &session.Event{Visibility: session.VisibilityUIOnly, Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{SessionUpdate: kind, MessageID: "message", Content: map[string]any{"type": "text", "text": ""}, ReplacedFields: []string{"_meta"}}}}
		updates, err := ProjectEvent(session.CloneEvent(e))
		if err != nil || len(updates) != 1 {
			t.Fatalf("%s metadata clear projection: %v %#v", kind, err, updates)
		}
		original := updates[0].(eventstream.ContentChunk)
		cloned := eventstream.CloneUpdate(original).(eventstream.ContentChunk)
		cloned.ReplacedFields[0] = "changed"
		if original.ReplacedFields[0] != "_meta" {
			t.Fatal("native clone aliases metadata presence")
		}
		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := eventstream.DecodeUpdateJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		chunk := decoded.(eventstream.ContentChunk)
		if chunk.Replacement || chunk.Meta != nil || !reflect.DeepEqual(chunk.ReplacedFields, []string{"_meta"}) || session.ExtractProtocolText(chunk.Content) != "" {
			t.Fatalf("%s metadata clear lost or changed content: %s", kind, raw)
		}
	}
}
