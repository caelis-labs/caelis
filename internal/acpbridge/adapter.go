package acpbridge

import (
	agentsdk "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// SourceEventFromAgent adapts one SDK-owned source event into the control-layer
// ACP bridge shape.
func SourceEventFromAgent(in agentsdk.SourceEvent) SourceEvent {
	return SourceEvent{
		Canonical:                        session.CloneEvent(in.Canonical),
		ACP:                              nativeACPEnvelope(in.Native),
		CanonicalContentAlreadyPublished: in.CanonicalContentAlreadyPublished,
	}
}

func nativeACPEnvelope(native any) *eventstream.Envelope {
	if native == nil {
		return nil
	}
	envelope, ok := native.(*eventstream.Envelope)
	if !ok {
		return nil
	}
	return CloneEnvelopePtr(envelope)
}
