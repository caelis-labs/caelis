package turningress

import (
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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

func validateIngressEnvelopes(envelopes []eventstream.Envelope) error {
	for _, envelope := range envelopes {
		if err := validateIngressEnvelope(envelope); err != nil {
			return err
		}
	}
	return nil
}

func validateStoredChildEvents(events []*session.Event) error {
	for index, event := range events {
		if event == nil {
			return &ingressDeliveryError{
				delivery: eventstream.DeliveryMirror,
				cause:    fmt.Errorf("child recorder returned no stored event at index %d", index),
			}
		}
		if strings.TrimSpace(event.ID) == "" || event.Seq == 0 {
			return &ingressDeliveryError{
				delivery: eventstream.DeliveryMirror,
				eventID:  strings.TrimSpace(event.ID),
				cause:    fmt.Errorf("child recorder returned an event without a durable position at index %d", index),
			}
		}
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

func isIngressDeliveryError(err error) bool {
	var target *ingressDeliveryError
	return errors.As(err, &target)
}
