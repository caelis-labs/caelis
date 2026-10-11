package acpingress

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestNormalizeNarrativeClearAndToolPatchPresence(t *testing.T) {
	for _, kind := range []string{client.UpdateUserMessage, client.UpdateAgentMessage, client.UpdateAgentThought} {
		e := NormalizeUpdate(client.ContentChunk{SessionUpdate: kind, Content: json.RawMessage(`{"type":"text","text":""}`), MessageID: "same-message", Replacement: true}, controllerTestOptions())
		if e == nil || session.EventText(e) != "" {
			t.Fatalf("%s clear lost: %#v", kind, e)
		}
		u := session.ProtocolUpdateOf(e)
		if u == nil || !u.Replacement || u.MessageID != "same-message" {
			t.Fatalf("%s presence lost: %#v", kind, u)
		}
		clone := session.CloneEvent(e)
		if !clone.Protocol.Update.Replacement {
			t.Fatal("clone lost replacement")
		}
	}
	e := NormalizeUpdate(client.ToolCallUpdate{SessionUpdate: client.UpdateToolCallState, ToolCallID: "tool", RawPatch: map[string]json.RawMessage{"title": json.RawMessage(`null`), "rawInput": json.RawMessage(`{}`), "content": json.RawMessage(`[]`), "vendor": json.RawMessage(`null`)}}, controllerTestOptions())
	if e == nil || !reflect.DeepEqual(e.Protocol.Update.ReplacedFields, []string{"title", "rawInput", "content"}) {
		t.Fatalf("recognized patch fields = %#v", e)
	}
	copy := session.CloneEvent(e)
	copy.Protocol.Update.ReplacedFields[0] = "kind"
	if e.Protocol.Update.ReplacedFields[0] != "title" {
		t.Fatal("clone aliases patch presence")
	}
}

func TestNormalizeMetadataOnlyNarrativePatchRetainsExplicitClear(t *testing.T) {
	for _, kind := range []string{client.UpdateUserMessage, client.UpdateAgentMessage, client.UpdateAgentThought} {
		e := NormalizeUpdate(client.ContentChunk{SessionUpdate: kind, Content: json.RawMessage(`{"type":"text","text":""}`), MessageID: "message", MetaPresent: true}, controllerTestOptions())
		if e == nil || session.EventText(e) != "" {
			t.Fatalf("%s metadata clear dropped: %#v", kind, e)
		}
		u := session.ProtocolUpdateOf(session.CloneEvent(e))
		if u == nil || u.Replacement || u.Meta != nil || !reflect.DeepEqual(u.ReplacedFields, []string{"_meta"}) {
			t.Fatalf("%s metadata clear changed content/presence: %#v", kind, u)
		}
	}
}
