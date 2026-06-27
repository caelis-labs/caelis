package tuiapp

import (
	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	acpprojector "github.com/OnslaughtSnail/caelis/protocol/acp/projector"
)

func acpEventMsg(env eventstream.Envelope) tea.Msg {
	return env
}

func gatewayEventMsg(env gateway.EventEnvelope) tea.Msg {
	projected := acpprojector.ProjectGatewayEventEnvelope(env)
	if len(projected) == 1 {
		return projected[0]
	}
	events := make([]TranscriptEvent, 0, len(projected))
	for _, item := range projected {
		events = append(events, ProjectACPEventToTranscriptEvents(item)...)
	}
	return TranscriptEventsMsg{Events: events}
}

func ProjectGatewayEventToTranscriptEvents(ev gateway.Event) []TranscriptEvent {
	out := make([]TranscriptEvent, 0, 4)
	for _, env := range acpprojector.ProjectGatewayEventEnvelope(gateway.EventEnvelope{Event: ev}) {
		out = append(out, ProjectACPEventToTranscriptEvents(env)...)
	}
	return out
}
