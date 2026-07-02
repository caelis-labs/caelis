package kernel

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/internal/acpbridge"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (g *Gateway) forwardHandleSourceEvents(activeSession session.Session, handle *turnHandle, source acpbridge.EventHandle) {
	g.forwardSourceEvents(activeSession, handle, acpbridge.SourceStreamFrom(source))
}

func (g *Gateway) forwardSourceEvents(activeSession session.Session, handle *turnHandle, source acpbridge.SourceStream) {
	for sourceEvent, seqErr := range source.Events {
		if seqErr != nil {
			handle.publishError(seqErr)
			return
		}
		if sourceEvent.Canonical != nil {
			handle.publishSessionEventWithACPProjection(sourceEvent.Canonical, shouldProjectSourceCanonicalToACP(sourceEvent, source.NativeACP))
			g.noteSessionCursor(activeSession.SessionID, sourceEvent.Canonical.ID)
		}
		if sourceEvent.ACP != nil {
			handle.publishACP(*sourceEvent.ACP, "acp_passthrough")
		}
	}
}

func shouldProjectSourceCanonicalToACP(sourceEvent acpbridge.SourceEvent, nativeACP bool) bool {
	if sourceEvent.Canonical == nil || sourceEvent.ACP != nil {
		return false
	}
	if !nativeACP {
		return true
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
