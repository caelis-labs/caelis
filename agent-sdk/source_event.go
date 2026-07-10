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

// SourceHandle is an optional alternate view for handles that expose canonical
// session events plus opaque native passthrough. It and Runner.Events must not
// be consumed concurrently; one handle has one event consumer.
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
