package appserver

import (
	"fmt"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// ValidateEnvelopeDelivery checks that an Envelope's declared replay guarantee
// has the typed position required by the Control-owned delivery lane. Transient
// ingress may omit a position because the Session Feed Broker assigns it at
// publication.
func ValidateEnvelopeDelivery(envelope eventstream.Envelope) error {
	mode := eventstream.DeliveryMode("")
	if envelope.Delivery != nil {
		mode = envelope.Delivery.Mode
	}
	switch mode {
	case "", eventstream.DeliveryTransient:
		return nil
	case eventstream.DeliveryCanonical, eventstream.DeliveryMirror:
		if envelope.Position == nil || envelope.Position.Durable == nil {
			return fmt.Errorf("appserver: durable envelope requires a durable position")
		}
		if err := envelope.Position.Validate(); err != nil {
			return fmt.Errorf("appserver: invalid durable position: %w", err)
		}
		if envelope.Position.Durable.Seq == 0 {
			return fmt.Errorf("appserver: durable envelope position sequence must be greater than zero")
		}
		return nil
	default:
		return fmt.Errorf("appserver: unsupported delivery mode %q", mode)
	}
}
