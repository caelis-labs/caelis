package acpbridge

import (
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
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
	caelisMeta, _ := envelope.Meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelisMeta["runtime"].(map[string]any)
	observation, _ := runtimeMeta[runtimeObservationSection].(map[string]any)
	if observation[runtimeObservationCode] != runtimeObservationGap ||
		observation[runtimeObservationDropped] != uint64(7) {
		t.Fatalf("observation meta = %#v", observation)
	}
}
