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

// ControllerEventForwardRequest carries one external controller turn stream
// forwarding job.
type ControllerEventForwardRequest struct {
	ActiveSession session.Session
	SessionRef    session.SessionRef
	TurnID        string
	Source        EventSource
	Publisher     SourceEventPublisher
	Normalize     EventNormalizer
	IsUserEcho    func(*session.Event) bool
}

// ControllerEventForwarder forwards one external controller source stream into
// canonical persistence and live publication paths.
type ControllerEventForwarder interface {
	ForwardControllerEvents(ctx context.Context, req ControllerEventForwardRequest) error
}
