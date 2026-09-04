// Package acpbridge contains internal control-layer bridges for live ACP
// passthrough events.
package acpbridge

import (
	agentsdk "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// SourceEvent is one live source event before the kernel chooses the durable or
// ACP-native publication path.
//
// It is intentionally internal because ACP passthrough is a client-protocol
// bridge, not a reusable runtime port contract.
type SourceEvent struct {
	Canonical *session.Event
	ACP       *eventstream.Envelope
	// CanonicalContentAlreadyPublished is the control-layer copy of the SDK
	// live-source contract. It affects live projection only, never persistence.
	CanonicalContentAlreadyPublished agentsdk.PublishedContent
}

// CloneSourceEvent copies a source event before a short semantic barrier.
func CloneSourceEvent(in SourceEvent) SourceEvent {
	return SourceEvent{
		Canonical:                        session.CloneEvent(in.Canonical),
		ACP:                              CloneEnvelopePtr(in.ACP),
		CanonicalContentAlreadyPublished: in.CanonicalContentAlreadyPublished,
	}
}

// CloneEnvelopePtr copies an optional ACP envelope.
func CloneEnvelopePtr(in *eventstream.Envelope) *eventstream.Envelope {
	if in == nil {
		return nil
	}
	out := eventstream.CloneEnvelope(*in)
	return &out
}
