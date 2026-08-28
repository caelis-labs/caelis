package kernel

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
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
	return mergeKernelMeta(meta, map[string]any{
		"version": 1,
		"invocation": map[string]any{
			"provider": strings.TrimSpace(invocation.Provider),
			"model":    strings.TrimSpace(invocation.Model),
		},
	})
}
