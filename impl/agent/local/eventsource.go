package local

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/eventsource"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func cloneSourceEvent(in eventsource.Event) eventsource.Event {
	return eventsource.CloneEvent(in)
}

func cloneACPEnvelopePtr(in *eventstream.Envelope) *eventstream.Envelope {
	return eventsource.CloneACPEnvelopePtr(in)
}

func acpEnvelopeWithNarrativeText(env *eventstream.Envelope, updateType string, text string) *eventstream.Envelope {
	if env == nil {
		return nil
	}
	out := eventstream.CloneEnvelope(*env)
	out.Kind = eventstream.KindSessionUpdate
	out.Update = schema.ContentChunk{
		SessionUpdate: strings.TrimSpace(updateType),
		Content: schema.TextContent{
			Type: "text",
			Text: text,
		},
	}
	if out.Meta == nil {
		out.Meta = eventstream.UpdateMeta(env.Update)
	}
	return &out
}
