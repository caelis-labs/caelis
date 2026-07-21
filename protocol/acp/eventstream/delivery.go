package eventstream

import "fmt"

// ValidateEnvelopeDelivery checks that an Envelope's declared replay guarantee
// has the typed position required by that delivery lane. Transient ingress may
// omit a position because the Session Feed Broker assigns it at publication.
func ValidateEnvelopeDelivery(envelope Envelope) error {
	mode := DeliveryMode("")
	if envelope.Delivery != nil {
		mode = envelope.Delivery.Mode
	}
	switch mode {
	case "", DeliveryTransient:
		return nil
	case DeliveryCanonical, DeliveryMirror:
		if envelope.Position == nil || envelope.Position.Durable == nil {
			return fmt.Errorf("eventstream: durable envelope requires a durable position")
		}
		if err := envelope.Position.Validate(); err != nil {
			return fmt.Errorf("eventstream: invalid durable position: %w", err)
		}
		if envelope.Position.Durable.Seq == 0 {
			return fmt.Errorf("eventstream: durable envelope position sequence must be greater than zero")
		}
		return nil
	default:
		return fmt.Errorf("eventstream: unsupported delivery mode %q", mode)
	}
}
