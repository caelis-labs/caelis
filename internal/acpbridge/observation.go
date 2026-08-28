package acpbridge

import (
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

// RuntimeObservationGapNotice is stable presentation copy for a skipped suffix
// in a Runtime observer stream. Consumers must use the structured metadata,
// not this text, for classification.
const RuntimeObservationGapNotice = "Some live runtime updates were skipped; durable Session history remains available."

// RuntimeObservationGapEnvelope adapts one SDK observer gap into transient ACP
// bridge output. It is diagnostic live state, never an execution failure or a
// durable replay fact.
func RuntimeObservationGapEnvelope(dropped uint64) eventstream.Envelope {
	return eventstream.Envelope{
		Kind:     eventstream.KindNotice,
		Notice:   RuntimeObservationGapNotice,
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Meta: metautil.WithRuntimeSection(nil, metautil.RuntimeObservation, map[string]any{
			metautil.RuntimeObservationCode:    metautil.RuntimeObservationGap,
			metautil.RuntimeObservationDropped: dropped,
		}),
	}
}
