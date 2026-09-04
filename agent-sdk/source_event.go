package agentsdk

import (
	"context"

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
	// Err reports a producer failure at its exact position in the source
	// sequence. It is delivered through the same synchronous observer so SDK
	// implementations do not need a second error queue.
	Err error
	// CanonicalContentAlreadyPublished identifies the exact canonical content
	// streams with separate live incremental owners. Live consumers omit only
	// those streams; durable replay still uses Canonical in full.
	CanonicalContentAlreadyPublished PublishedContent
}

// SourceEventObserver receives raw producer events synchronously. The caller
// installs it on the request before execution starts. Implementations must not
// retain an unbounded payload queue; product replay and cursor semantics belong
// to Control rather than the SDK.
type SourceEventObserver interface {
	ObserveSourceEvent(context.Context, SourceEvent) error
}

// SourceEventObserverFunc adapts a function to SourceEventObserver.
type SourceEventObserverFunc func(context.Context, SourceEvent) error

func (f SourceEventObserverFunc) ObserveSourceEvent(ctx context.Context, event SourceEvent) error {
	if f == nil {
		return nil
	}
	return f(ctx, event)
}

// CloneSourceEvent copies one source event before synchronous handoff. Native
// passthrough is copied by reference because the runtime does not interpret it.
func CloneSourceEvent(in SourceEvent) SourceEvent {
	return SourceEvent{
		Canonical:                        session.CloneEvent(in.Canonical),
		Native:                           in.Native,
		Err:                              in.Err,
		CanonicalContentAlreadyPublished: in.CanonicalContentAlreadyPublished,
	}
}
