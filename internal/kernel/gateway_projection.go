package kernel

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

func projectSessionACPEvent(ref session.SessionRef, event *session.Event, handleID string, runID string, turnID string) []eventstream.Envelope {
	base := acpprojector.EnvelopeBaseFromSessionEvent(ref, event, acpprojector.SessionEventTransport{
		HandleID: handleID,
		RunID:    runID,
		TurnID:   turnID,
	})
	base.Meta = sessionACPEventMeta(event)
	out := acpprojector.ProjectSessionEventEnvelope(base, event)
	return stampSessionACPProjectionIDs(strings.TrimSpace(event.ID), out)
}

func sessionACPEventMeta(event *session.Event) map[string]any {
	var meta map[string]any
	if event != nil {
		meta = event.Meta
	}
	if event == nil || event.Invocation == nil {
		return meta
	}
	invocation := session.CloneEventInvocation(*event.Invocation)
	if strings.TrimSpace(invocation.Provider) == "" && strings.TrimSpace(invocation.Model) == "" {
		return meta
	}
	return metautil.Merge(meta, map[string]any{
		metautil.Root: map[string]any{
			metautil.Version: 1,
			"invocation": map[string]any{
				"provider": strings.TrimSpace(invocation.Provider),
				"model":    strings.TrimSpace(invocation.Model),
			},
		},
	})
}

func stampSessionACPProjectionIDs(eventID string, events []eventstream.Envelope) []eventstream.Envelope {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" || len(events) == 0 {
		return events
	}
	out := make([]eventstream.Envelope, len(events))
	for i, env := range events {
		env.EventID = eventID
		if strings.TrimSpace(env.ProjectionID) == "" {
			env.ProjectionID = formatACPProjectionCursor(eventID, i)
		}
		out[i] = env
	}
	return out
}

func formatACPProjectionCursor(eventID string, index int) string {
	return eventstream.FormatProjectionID(eventID, index)
}
