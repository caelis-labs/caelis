package acpbridge

import (
	"iter"

	agentsdk "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// SourceEventFromAgent adapts one SDK-owned source event into the control-layer
// ACP bridge shape.
func SourceEventFromAgent(in agentsdk.SourceEvent) SourceEvent {
	return SourceEvent{
		Canonical: session.CloneEvent(in.Canonical),
		ACP:       nativeACPEnvelope(in.Native),
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

func adaptedAgentSourceEvents(seq iter.Seq2[agentsdk.SourceEvent, error]) iter.Seq2[SourceEvent, error] {
	return func(yield func(SourceEvent, error) bool) {
		for event, err := range seq {
			if !yield(SourceEventFromAgent(event), err) {
				return
			}
		}
	}
}
