package acpbridge

import (
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func envelopeWithNarrativeText(env *eventstream.Envelope, updateType string, text string, messageIDs ...string) *eventstream.Envelope {
	if env == nil {
		return nil
	}
	messageID := narrativeEnvelopeMessageID(env.Update)
	if len(messageIDs) > 0 {
		messageID = messageIDs[0]
	}
	out := eventstream.CloneEnvelope(*env)
	out.Kind = eventstream.KindSessionUpdate
	out.Update = eventstream.ContentChunk{
		SessionUpdate: strings.TrimSpace(updateType),
		MessageID:     strings.TrimSpace(messageID),
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

func narrativeEnvelopeMessageID(update eventstream.Update) string {
	switch typed := update.(type) {
	case eventstream.ContentChunk:
		return strings.TrimSpace(typed.MessageID)
	case *eventstream.ContentChunk:
		if typed != nil {
			return strings.TrimSpace(typed.MessageID)
		}
	}
	return ""
}
