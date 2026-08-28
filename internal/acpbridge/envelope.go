package acpbridge

import (
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func envelopeWithNarrativeText(env *eventstream.Envelope, updateType string, text string) *eventstream.Envelope {
	if env == nil {
		return nil
	}
	out := eventstream.CloneEnvelope(*env)
	out.Kind = eventstream.KindSessionUpdate
	out.Update = eventstream.ContentChunk{
		SessionUpdate: strings.TrimSpace(updateType),
		Content: eventstream.TextContent{
			Type: "text",
			Text: text,
		},
	}
	if out.Meta == nil {
		out.Meta = eventstream.UpdateMeta(env.Update)
	}
	return &out
}
