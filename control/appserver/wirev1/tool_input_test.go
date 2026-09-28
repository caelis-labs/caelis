package wirev1

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestToolObservationOmitsUnrepresentableInputWithoutMutatingSource(t *testing.T) {
	for _, number := range []string{"9007199254740991", "-9007199254740991", "9007199254740993", "-9007199254740993", "9007199254740991.1", "1e1000"} {
		t.Run(number, func(t *testing.T) {
			input := map[string]any{"nested": []any{json.Number(number)}}
			inputJSON, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			safe := ValidateJSONNumbers(inputJSON) == nil
			status := eventstream.ToolStatusFailed
			for _, update := range []eventstream.Update{
				eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "call-1", Status: eventstream.ToolStatusPending, RawInput: input},
				eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "call-1", Status: &status, RawInput: input},
			} {
				envelope := baseEnvelope(eventstream.KindSessionUpdate)
				envelope.Update = update
				before, _ := json.Marshal(envelope)
				// Live feeds read Envelopes back from the local JSON spool before
				// the public wire boundary; that hop must not round unsafe tokens.
				var spooled eventstream.Envelope
				if err := json.Unmarshal(before, &spooled); err != nil {
					t.Fatal(err)
				}
				raw := mustMarshalEnvelope(t, spooled)
				if direct := mustMarshalEnvelope(t, envelope); !bytes.Equal(raw, direct) {
					t.Fatalf("spool changed wire input: %s; direct %s", raw, direct)
				}
				if err := openAPIValidator(t, "Envelope").Validate(decodeJSONWithNumbers(t, raw)); err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(raw, []byte(`"rawInput"`)) != safe {
					t.Fatalf("wrong optional input projection: %s", raw)
				}
				if safe && !bytes.Contains(raw, inputJSON) {
					t.Fatalf("safe input changed: %s", raw)
				}
				decoded, err := UnmarshalEnvelope(raw)
				if err != nil {
					t.Fatal(err)
				}
				if roundTrip := mustMarshalEnvelope(t, decoded); !bytes.Equal(raw, roundTrip) {
					t.Fatalf("round trip = %s; want %s", roundTrip, raw)
				}
				after, _ := json.Marshal(envelope)
				if !bytes.Equal(before, after) {
					t.Fatalf("wire encoding mutated source: %s -> %s", before, after)
				}
			}
		})
	}
}

func TestApprovalPayloadsRetainStrictNumericValidation(t *testing.T) {
	input := map[string]any{"id": json.Number("9007199254740993")}
	review := baseEnvelope(eventstream.KindApprovalReview)
	review.ApprovalReview = &eventstream.ApprovalReview{ToolCallID: "call-1", Status: "approved", RawInput: input}
	permission := baseEnvelope(eventstream.KindRequestPermission)
	permission.Permission = &eventstream.RequestPermissionRequest{ToolCall: eventstream.ToolCallUpdate{ToolCallID: "call-1", RawInput: input}}
	for _, envelope := range []eventstream.Envelope{review, permission} {
		if _, err := MarshalEnvelope(envelope); err == nil {
			t.Fatalf("unsafe approval input emitted: %+v", envelope)
		}
	}
}
