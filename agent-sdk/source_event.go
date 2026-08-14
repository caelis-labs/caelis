package agentsdk

import (
	"iter"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// PublishedContent identifies canonical content streams that already have a
// separate live incremental owner. Values may be combined.
type PublishedContent uint8

const (
	// PublishedAssistantMessage identifies visible Assistant narrative.
	PublishedAssistantMessage PublishedContent = 1 << iota
	// PublishedAssistantThought identifies visible Assistant reasoning.
	PublishedAssistantThought
	// PublishedTerminal identifies RunCommand terminal bytes.
	PublishedTerminal
)

// Has reports whether all requested content streams are present.
func (p PublishedContent) Has(content PublishedContent) bool {
	return content != 0 && p&content == content
}

// SourceEvent is one live source event emitted by a runtime run handle before
// the control layer chooses the durable or native passthrough publication path.
//
// Canonical carries durable runtime facts. Native carries opaque
// protocol-specific passthrough that the runtime does not interpret.
type SourceEvent struct {
	Canonical *session.Event
	Native    any
	// CanonicalContentAlreadyPublished identifies the exact canonical content
	// streams with separate live incremental owners. Live consumers omit only
	// those streams; durable replay still uses Canonical in full.
	CanonicalContentAlreadyPublished PublishedContent
}

// SourceHandle is an optional alternate view for handles that expose canonical
// session events plus opaque native passthrough. It and Runner.Events must not
// be consumed concurrently; one handle has one event consumer. Consumers may
// continue after EventStreamGapError to receive the newest retained suffix.
type SourceHandle interface {
	SourceEvents() iter.Seq2[SourceEvent, error]
}

// CloneSourceEvent copies one source event before queueing or fan-out. Native
// passthrough is copied by reference because the runtime does not interpret it.
func CloneSourceEvent(in SourceEvent) SourceEvent {
	return SourceEvent{
		Canonical:                        session.CloneEvent(in.Canonical),
		Native:                           in.Native,
		CanonicalContentAlreadyPublished: in.CanonicalContentAlreadyPublished,
	}
}
