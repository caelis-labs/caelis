package kernel

import (
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/internal/acpbridge"
)

func (g *Gateway) forwardHandleSourceEvents(activeSession session.Session, handle *turnHandle, source acpbridge.EventHandle) {
	g.forwardSourceEvents(activeSession, handle, acpbridge.SourceStreamFrom(source))
}

func (g *Gateway) forwardSourceEvents(activeSession session.Session, handle *turnHandle, source acpbridge.SourceStream) {
	for sourceEvent, seqErr := range source.Events {
		if seqErr != nil {
			if gap, ok := agent.AsEventStreamGap(seqErr); ok {
				handle.publishACP(acpbridge.RuntimeObservationGapEnvelope(gap.Dropped), "runtime_observation")
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
			case sourceEvent.ACP != nil && eventstream.UpdateType(sourceEvent.ACP.Update) == eventstream.UpdateUsage:
				project = nil
			case sourceEvent.ACP != nil:
				// The paired native envelope owns live content and state. Usage is
				// still canonical accounting and must not disappear with the
				// duplicate content projection.
				project = projectSessionEventUsageEnvelope
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

// projectSessionEventUsageEnvelope keeps canonical accounting when the paired
// native ACP envelope already owns the event's live content and state.
func projectSessionEventUsageEnvelope(base eventstream.Envelope, event *session.Event) []eventstream.Envelope {
	projected := acpprojector.ProjectSessionEventEnvelope(base, event)
	out := make([]eventstream.Envelope, 0, 1)
	for _, env := range projected {
		if eventstream.UpdateType(env.Update) == eventstream.UpdateUsage {
			out = append(out, env)
		}
	}
	return out
}
