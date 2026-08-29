package wirev1

import (
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestEnvelopeRoundTripPreservesControlOwnedUsageSemantics(t *testing.T) {
	t.Parallel()

	want := eventstream.UsageSemanticsContextGauge
	raw, err := MarshalEnvelope(eventstream.Envelope{
		Kind:           eventstream.KindSessionUpdate,
		UsageSemantics: want,
		Update: eventstream.UsageUpdate{
			SessionUpdate: eventstream.UpdateUsage,
			Size:          200000,
			Used:          42000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.UsageSemantics != want {
		t.Fatalf("usage semantics = %q, want %q", decoded.UsageSemantics, want)
	}
}
