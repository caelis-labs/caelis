package kernel

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func projectSessionACPEvent(ref session.SessionRef, event *session.Event, handleID string, runID string, turnID string) []eventstream.Envelope {
	return projectSessionACPEventWith(ref, event, handleID, runID, turnID, acpprojector.ProjectSessionEventEnvelope)
}

func projectSessionACPEventWith(
	ref session.SessionRef,
	event *session.Event,
	handleID string,
	runID string,
	turnID string,
	project func(eventstream.Envelope, *session.Event) []eventstream.Envelope,
) []eventstream.Envelope {
	base := acpprojector.EnvelopeBaseFromSessionEvent(ref, event, acpprojector.SessionEventTransport{
		HandleID: handleID,
		RunID:    runID,
		TurnID:   turnID,
	})
	base.Meta = sessionACPEventMeta(event)
	return project(base, event)
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
