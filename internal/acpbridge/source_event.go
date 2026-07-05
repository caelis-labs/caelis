// Package acpbridge contains internal control-layer bridges for live ACP
// passthrough events.
package acpbridge

import (
	"iter"

	agentsdk "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// SourceEvent is one live source event before the kernel chooses the durable or
// ACP-native publication path.
//
// It is intentionally internal because ACP passthrough is a client-protocol
// bridge, not a reusable runtime port contract.
type SourceEvent struct {
	Canonical *session.Event
	ACP       *eventstream.Envelope
}

// SourceHandle is an optional live-event source for handles that can provide
// both durable canonical session events and ACP-native passthrough events.
type SourceHandle interface {
	SourceEvents() iter.Seq2[SourceEvent, error]
}

// EventHandle is the minimal legacy canonical event stream shape that can be
// adapted into SourceEvent values.
type EventHandle interface {
	Events() iter.Seq2[*session.Event, error]
}

// SourceStream is the selected live source stream for one running handle.
type SourceStream struct {
	Events    iter.Seq2[SourceEvent, error]
	NativeACP bool
}

// SourceStreamFrom selects a native source stream when the handle provides one,
// otherwise it wraps the canonical event stream. It recognizes both the legacy
// acpbridge.SourceHandle and the SDK-owned agentsdk.SourceHandle shape.
func SourceStreamFrom(handle EventHandle) SourceStream {
	if sourceHandle, ok := handle.(SourceHandle); ok && sourceHandle != nil {
		return SourceStream{Events: sourceHandle.SourceEvents(), NativeACP: true}
	}
	if sourceHandle, ok := handle.(agentsdk.SourceHandle); ok && sourceHandle != nil {
		return SourceStream{Events: adaptedAgentSourceEvents(sourceHandle.SourceEvents()), NativeACP: true}
	}
	return SourceStream{Events: canonicalSourceEvents(handle)}
}

// SourceEventsFrom adapts a handle into SourceEvent values.
func SourceEventsFrom(handle EventHandle) iter.Seq2[SourceEvent, error] {
	return SourceStreamFrom(handle).Events
}

func canonicalSourceEvents(handle EventHandle) iter.Seq2[SourceEvent, error] {
	return func(yield func(SourceEvent, error) bool) {
		if handle == nil {
			return
		}
		for event, err := range handle.Events() {
			if !yield(SourceEvent{Canonical: event}, err) {
				return
			}
		}
	}
}

// CloneSourceEvent copies a source event before it is queued or fanned out.
func CloneSourceEvent(in SourceEvent) SourceEvent {
	return SourceEvent{
		Canonical: session.CloneEvent(in.Canonical),
		ACP:       CloneEnvelopePtr(in.ACP),
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
