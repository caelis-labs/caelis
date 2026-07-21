package projector

import (
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

// RuntimeObservationGapNotice is stable presentation copy for a skipped suffix
// in a Runtime observer stream. Consumers must use the structured metadata,
// not this text, for classification.
const RuntimeObservationGapNotice = "Some live runtime updates were skipped; durable Session history remains available."

// ProjectRuntimeObservationGap returns the transient product projection for an
// SDK observer gap. It is diagnostic live state, never an execution failure or
// a durable replay fact.
func ProjectRuntimeObservationGap(dropped uint64) eventstream.Envelope {
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
