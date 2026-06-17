package kernel

import (
	"github.com/OnslaughtSnail/caelis/ports/eventsource"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (g *Gateway) forwardSourceEvents(activeSession session.Session, handle *turnHandle, source eventsource.Handle) {
	for sourceEvent, seqErr := range source.SourceEvents() {
		if seqErr != nil {
			handle.publish(turnLifecycleError(handle, seqErr))
			return
		}
		if sourceEvent.Canonical != nil {
			handle.publishSessionEventWithACPProjection(sourceEvent.Canonical, sourceEvent.ACP == nil)
			g.noteSessionCursor(activeSession.SessionID, sourceEvent.Canonical.ID)
		}
		if sourceEvent.ACP != nil {
			handle.publishACP(*sourceEvent.ACP, "acp_passthrough")
		}
	}
}

func turnLifecycleError(handle *turnHandle, err error) EventEnvelope {
	return EventEnvelope{
		Event: Event{
			Kind:       EventKindLifecycle,
			HandleID:   handle.handleID,
			RunID:      handle.runID,
			TurnID:     handle.turnID,
			SessionRef: handle.sessionRef,
		},
		Err: EventError(err),
	}
}
