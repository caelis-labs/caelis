package eventsource

import (
	"iter"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
)

// Event is one runtime-source event before the kernel chooses the durable or
// ACP-native publication path.
type Event struct {
	Canonical *session.Event
	ACP       *eventstream.Envelope
}

// Handle is an optional live-event source for runtimes that can provide both
// durable canonical session events and ACP-native passthrough events.
type Handle interface {
	SourceEvents() iter.Seq2[Event, error]
}

func CloneEvent(in Event) Event {
	return Event{
		Canonical: session.CloneEvent(in.Canonical),
		ACP:       CloneACPEnvelopePtr(in.ACP),
	}
}

func CloneACPEnvelopePtr(in *eventstream.Envelope) *eventstream.Envelope {
	if in == nil {
		return nil
	}
	out := eventstream.CloneEnvelope(*in)
	return &out
}
