package session_test

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestProtocolMetadataNumbersSurviveEventRoundTrip(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent_message_chunk", "agent_thought_chunk", "user_message_chunk", "tool_call_update"} {
		t.Run(kind, func(t *testing.T) {
			var event session.Event
			raw := []byte(`{"protocol":{"method":"session/update","update":{"sessionUpdate":"` + kind + `","_meta":{"large":9007199254740993,"nested":{"decimal":1.2500}}}}}`)
			for range 2 {
				if err := json.Unmarshal(raw, &event); err != nil {
					t.Fatal(err)
				}
				meta := session.ProtocolUpdateOf(session.CloneEvent(&event)).Meta
				if meta["large"] != json.Number("9007199254740993") || meta["nested"].(map[string]any)["decimal"] != json.Number("1.2500") {
					t.Fatalf("protocol metadata lost numeric tokens: %#v", meta)
				}
				var err error
				raw, err = json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
