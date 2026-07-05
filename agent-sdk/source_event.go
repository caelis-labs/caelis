package agentsdk

import (
	"iter"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// SourceEvent is one live source event emitted by a runtime run handle before
// the control layer chooses the durable or native passthrough publication path.
//
// Canonical carries durable runtime facts. Native carries opaque
// protocol-specific passthrough that the runtime does not interpret.
type SourceEvent struct {
	Canonical *session.Event
	Native    any
}

// SourceHandle is an optional extension for handles that expose both canonical
// session events and opaque native passthrough events.
type SourceHandle interface {
	SourceEvents() iter.Seq2[SourceEvent, error]
}

// CloneSourceEvent copies one source event before queueing or fan-out. Native
// passthrough is copied by reference because the runtime does not interpret it.
func CloneSourceEvent(in SourceEvent) SourceEvent {
	return SourceEvent{
		Canonical: session.CloneEvent(in.Canonical),
		Native:    in.Native,
	}
}
