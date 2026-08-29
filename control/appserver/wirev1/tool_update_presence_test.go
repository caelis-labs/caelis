package wirev1

import (
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestEnvelopeRoundTripPreservesToolUpdateContentPresence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		content     []eventstream.ToolCallContent
		wantPresent bool
	}{
		{name: "omitted"},
		{name: "explicit empty", content: []eventstream.ToolCallContent{}, wantPresent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw, err := MarshalEnvelope(eventstream.Envelope{
				Kind:      eventstream.KindSessionUpdate,
				SessionID: "session-1",
				Scope:     eventstream.ScopeMain,
				Update: eventstream.ToolCallUpdate{
					SessionUpdate: eventstream.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Content:       test.content,
				},
			})
			if err != nil {
				t.Fatalf("MarshalEnvelope() error = %v", err)
			}
			decoded, err := UnmarshalEnvelope(raw)
			if err != nil {
				t.Fatalf("UnmarshalEnvelope() error = %v", err)
			}
			update, ok := decoded.Update.(eventstream.ToolCallUpdate)
			if !ok {
				t.Fatalf("round-trip update = %T, want ToolCallUpdate", decoded.Update)
			}
			if present := update.Content != nil; present != test.wantPresent {
				t.Fatalf("round-trip content = %#v, presence = %t, want %t (wire %s)", update.Content, present, test.wantPresent, raw)
			}
		})
	}
}
