package acpbridge

import (
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestRuntimeObservationGapEnvelope(t *testing.T) {
	t.Parallel()

	envelope := RuntimeObservationGapEnvelope(7)
	if envelope.Kind != eventstream.KindNotice || envelope.Notice != RuntimeObservationGapNotice {
		t.Fatalf("RuntimeObservationGapEnvelope() = %#v, want stable notice", envelope)
	}
	if envelope.Delivery == nil || envelope.Delivery.Mode != eventstream.DeliveryTransient || envelope.Position != nil {
		t.Fatalf("delivery = %#v position = %#v, want unstamped transient", envelope.Delivery, envelope.Position)
	}
	observation := metautil.RuntimeSection(envelope.Meta, metautil.RuntimeObservation)
	if observation[metautil.RuntimeObservationCode] != metautil.RuntimeObservationGap ||
		observation[metautil.RuntimeObservationDropped] != uint64(7) {
		t.Fatalf("observation meta = %#v", observation)
	}
}
