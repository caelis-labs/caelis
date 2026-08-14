package kernel

import (
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func (g *Gateway) forwardHandleSourceEvents(activeSession session.Session, handle *turnHandle, source acpbridge.EventHandle) {
	g.forwardSourceEvents(activeSession, handle, acpbridge.SourceStreamFrom(source))
}

func (g *Gateway) forwardSourceEvents(activeSession session.Session, handle *turnHandle, source acpbridge.SourceStream) {
	for sourceEvent, seqErr := range source.Events {
		if seqErr != nil {
			if gap, ok := agent.AsEventStreamGap(seqErr); ok {
				handle.publishACP(acpprojector.ProjectRuntimeObservationGap(gap.Dropped), "runtime_observation")
				continue
			}
			handle.publishError(seqErr)
			return
		}
		if sourceEvent.ACP != nil {
			// Native live content/state is the primary projection for a paired
			// source event. Publish it before the canonical accounting sibling.
			handle.publishACP(*sourceEvent.ACP, "acp_passthrough")
		}
		if sourceEvent.Canonical != nil {
			project := acpprojector.ProjectSessionEventEnvelope
			switch {
			case sourceEvent.ACP != nil && eventstream.UpdateType(sourceEvent.ACP.Update) == schema.UpdateUsage:
				project = nil
			case sourceEvent.ACP != nil:
				// The paired native envelope owns live content and state. Usage is
				// still canonical accounting and must not disappear with the
				// duplicate content projection.
				project = acpprojector.ProjectSessionEventUsageEnvelope
			case sourceEvent.CanonicalContentAlreadyPublished != 0:
				published := sourceEvent.CanonicalContentAlreadyPublished
				project = func(base eventstream.Envelope, event *session.Event) []eventstream.Envelope {
					return acpprojector.ProjectSessionEventLiveSupplementEnvelope(base, event, published)
				}
			}
			var projected []eventstream.Envelope
			if project != nil {
				projected = projectSessionACPEventWith(
					handle.sessionRef,
					sourceEvent.Canonical,
					handle.handleID,
					handle.runID,
					handle.turnID,
					project,
				)
			}
			handle.publishEnvelopes(projected, "")
			g.noteSessionCursor(activeSession.SessionID, sourceEvent.Canonical.ID)
		}
	}
}
