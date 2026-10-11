package session

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestOpaqueCanonicalIDsDoNotAliasDuringRoundTripOrNameResolution(t *testing.T) {
	seen := map[string]bool{}
	for _, id := range []string{" identity ", "identity", " "} {
		message := model.NewTextMessage(model.RoleAssistant, "answer")
		event := CanonicalizeEvent(&Event{Type: EventTypeAssistant, MessageID: id, Message: &message, Protocol: &EventProtocol{Method: ProtocolMethodSessionUpdate, Update: &ProtocolUpdate{SessionUpdate: string(ProtocolUpdateTypeAgentMessage), MessageID: id, Content: ProtocolTextContent("answer")}}})
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Event
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		got := EventMessageID(CloneEvent(&decoded))
		if got != id || seen[got] || decoded.Protocol != nil {
			t.Fatalf("canonical IDs collided or retained redundant projection: got %q want %q; %s", got, id, raw)
		}
		seen[got] = true
		tool := CloneEventTool(EventTool{ID: id, Name: "Read"})
		projected := EventToolProjection(&Event{Tool: &tool})
		if tool.ID != id || projected.ID != id {
			t.Fatalf("typed tool identity changed: %#v / %#v", tool, projected)
		}
	}
	message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: " call ", Name: "Historical", Args: "{}"}, {ID: "call", Name: "Current", Args: "{}"}}, "")
	for id, want := range map[string]string{" call ": "Historical", "call": "Current"} {
		event := CloneEvent(&Event{Message: &message, Protocol: &EventProtocol{Update: &ProtocolUpdate{SessionUpdate: string(ProtocolUpdateTypeToolUpdate), ToolCallID: id}}})
		if got := CanonicalToolName(event, nil); got != want {
			t.Fatalf("opaque tool identity selected another call: %q got %q, want %q", id, got, want)
		}
	}
}
