package kernel

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/eventsource"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (g *Gateway) forwardSourceEvents(activeSession session.Session, handle *turnHandle, source eventsource.Handle) {
	for sourceEvent, seqErr := range source.SourceEvents() {
		if seqErr != nil {
			handle.publishError(seqErr)
			return
		}
		if sourceEvent.Canonical != nil {
			handle.publishSessionEventWithACPProjection(sourceEvent.Canonical, shouldProjectSourceCanonicalToACP(sourceEvent))
			g.noteSessionCursor(activeSession.SessionID, sourceEvent.Canonical.ID)
		}
		if sourceEvent.ACP != nil {
			handle.publishACP(*sourceEvent.ACP, "acp_passthrough")
		}
	}
}

func shouldProjectSourceCanonicalToACP(sourceEvent eventsource.Event) bool {
	if sourceEvent.Canonical == nil || sourceEvent.ACP != nil {
		return false
	}
	return !isACPFinalAssistantMaterialization(sourceEvent.Canonical)
}

func isACPFinalAssistantMaterialization(event *session.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp") {
		return false
	}
	if session.EventTypeOf(event) != session.EventTypeAssistant {
		return false
	}
	return session.IsCanonicalHistoryEvent(event) && !session.IsUIOnly(event)
}
