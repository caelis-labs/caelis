package local

import (
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

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
