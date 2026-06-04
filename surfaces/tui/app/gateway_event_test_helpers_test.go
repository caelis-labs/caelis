package tuiapp

import (
	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/kernel"
)

func gatewayEventMsg(env kernel.EventEnvelope) tea.Msg {
	projected := kernel.ProjectACPEventEnvelope(env)
	if len(projected) == 1 {
		return projected[0]
	}
	events := make([]TranscriptEvent, 0, len(projected))
	for _, item := range projected {
		events = append(events, ProjectACPEventToTranscriptEvents(item)...)
	}
	return TranscriptEventsMsg{Events: events}
}

func ProjectGatewayEventToTranscriptEvents(ev kernel.Event) []TranscriptEvent {
	out := make([]TranscriptEvent, 0, 4)
	for _, env := range kernel.ProjectACPEventEnvelope(kernel.EventEnvelope{Event: ev}) {
		out = append(out, ProjectACPEventToTranscriptEvents(env)...)
	}
	return out
}
