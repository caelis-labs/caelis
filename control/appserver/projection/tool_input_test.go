package projection_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

func TestModelToolInputProjectionPreservesNumberUntilWireAdmission(t *testing.T) {
	for _, number := range []string{"9007199254740991", "9007199254740993", "9007199254740991.1"} {
		message := model.MessageFromAssistantParts("", "", []model.ToolCall{{ID: "call-1", Name: "Callback", Args: `{"id":` + number + `}`}})
		event := &session.Event{ID: "event-1", Seq: 1, Type: session.EventTypeToolCall, Message: &message}
		base := projection.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "s1"}, event, projection.SessionEventTransport{})
		events := projection.ProjectSessionEventEnvelope(base, event)
		if len(events) != 1 {
			t.Fatalf("projection = %+v", events)
		}
		call, ok := events[0].Update.(eventstream.ToolCall)
		if !ok {
			t.Fatalf("update = %T", events[0].Update)
		}
		input, ok := call.RawInput.(map[string]any)
		if !ok || input["id"] != json.Number(number) {
			t.Fatalf("projected input rounded before wire validation: %+v", call.RawInput)
		}
		raw, err := wirev1.MarshalEnvelope(events[0])
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(`"rawInput"`)) != (number == "9007199254740991") {
			t.Fatalf("wire input = %s", raw)
		}
		if message.ToolCalls()[0].Args != `{"id":`+number+`}` {
			t.Fatal("projection changed canonical model arguments")
		}
	}
}
