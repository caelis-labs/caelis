package turningress

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// ingressDeliveryError identifies a producer contract violation before the
// invalid envelope can enter the Control-owned Session feed.
type ingressDeliveryError struct {
	kind     eventstream.Kind
	scope    eventstream.Scope
	delivery eventstream.DeliveryMode
	eventID  string
	cause    error
}

func (e *ingressDeliveryError) Error() string {
	if e == nil {
		return "turningress: invalid envelope delivery"
	}
	return fmt.Sprintf(
		"turningress: invalid %s envelope delivery kind=%q scope=%q event_id=%q: %v",
		e.delivery, e.kind, e.scope, e.eventID, e.cause,
	)
}

func (e *ingressDeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func validateIngressEnvelope(envelope eventstream.Envelope) error {
	if err := eventstream.ValidateEnvelopeDelivery(envelope); err != nil {
		return newIngressDeliveryError(envelope, err)
	}
	return nil
}

func newIngressDeliveryError(envelope eventstream.Envelope, cause error) error {
	mode := eventstream.DeliveryMode("")
	if envelope.Delivery != nil {
		mode = envelope.Delivery.Mode
	}
	return &ingressDeliveryError{
		kind:     envelope.Kind,
		scope:    envelope.Scope,
		delivery: mode,
		eventID:  strings.TrimSpace(envelope.EventID),
		cause:    cause,
	}
}
