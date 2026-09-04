package kernel

import (
	"context"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/internal/acpbridge"
)

// observeSourceEvent is the synchronous Control boundary between an SDK
// producer and the Session spool. It projects one raw source value and retains
// no replayable payload state.
func (g *Gateway) observeSourceEvent(_ context.Context, activeSession session.Session, handle *turnHandle, event agent.SourceEvent) error {
	if handle == nil {
		return nil
	}
	if event.Err == nil {
		return g.observeSourceValue(activeSession, handle, event)
	}
	handle.publishError(event.Err)
	return nil
}

func (g *Gateway) observeSourceValue(activeSession session.Session, handle *turnHandle, event agent.SourceEvent) error {
	sourceEvent := acpbridge.SourceEventFromAgent(event)
	if sourceEvent.ACP != nil {
		// Native live content/state owns a paired source event and is published
		// before its canonical accounting sibling.
		handle.publishACP(*sourceEvent.ACP, "acp_passthrough")
	}
	if sourceEvent.Canonical == nil {
		return nil
	}
	project := acpprojector.ProjectSessionEventEnvelope
	switch {
	case sourceEvent.ACP != nil && eventstream.UpdateType(sourceEvent.ACP.Update) == eventstream.UpdateUsage:
		project = nil
	case sourceEvent.ACP != nil:
		project = projectSessionEventUsageEnvelope
	case sourceEvent.CanonicalContentAlreadyPublished != 0:
		published := sourceEvent.CanonicalContentAlreadyPublished
		project = func(base eventstream.Envelope, event *session.Event) []eventstream.Envelope {
			return acpprojector.ProjectSessionEventLiveSupplementEnvelope(base, event, published)
		}
	}
	if project != nil {
		handle.publishEnvelopes(projectSessionACPEventWith(
			handle.sessionRef,
			sourceEvent.Canonical,
			handle.handleID,
			handle.runID,
			handle.turnID,
			project,
		), "")
	}
	g.noteSessionCursor(activeSession.SessionID, sourceEvent.Canonical.ID)
	return nil
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
