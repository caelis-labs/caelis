package agentsdk

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// EventNormalizer normalizes one external controller event into durable runtime
// shape before persistence or live publication.
type EventNormalizer func(activeSession session.Session, turnID string, event *session.Event) *session.Event

// SourceEventPublisher publishes live events from one running handle.
type SourceEventPublisher interface {
	PublishEvent(event *session.Event)
	PublishSourceEvent(event SourceEvent)
}

// ControllerEventForwardRequest configures one external controller turn
// forwarding session before the remote producer starts.
type ControllerEventForwardRequest struct {
	ActiveSession session.Session
	SessionRef    session.SessionRef
	// MutationGuard is the execution authority for every durable event emitted
	// by this forwarding job. Forwarders must preserve it on every store write.
	MutationGuard session.MutationGuard
	TurnID        string
	Publisher     SourceEventPublisher
	Normalize     EventNormalizer
	IsUserEcho    func(*session.Event) bool
}

// ControllerEventSession synchronously normalizes and persists remote source
// events. Complete flushes any final canonical event after the producer is
// quiescent.
type ControllerEventSession interface {
	SourceEventObserver
	Complete(context.Context) error
}

// ControllerEventForwarder creates one per-turn forwarding session before the
// controller starts. It does not own a pull stream or payload queue.
type ControllerEventForwarder interface {
	BeginControllerEvents(context.Context, ControllerEventForwardRequest) (ControllerEventSession, error)
}
