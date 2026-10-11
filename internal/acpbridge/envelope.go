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
		SessionUpdate:  strings.TrimSpace(updateType),
		MessageID:      strings.TrimSpace(messageID),
		Replacement:    narrativeEnvelopeReplacement(env.Update),
		Meta:           eventstream.UpdateMeta(env.Update),
		ReplacedFields: narrativeEnvelopeReplacedFields(env.Update),
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

func narrativeEnvelopeReplacedFields(update eventstream.Update) []string {
	switch typed := update.(type) {
	case eventstream.ContentChunk:
		return append([]string(nil), typed.ReplacedFields...)
	case *eventstream.ContentChunk:
		if typed != nil {
			return append([]string(nil), typed.ReplacedFields...)
		}
	}
	return nil
}

func narrativeEnvelopeReplacement(update eventstream.Update) bool {
	switch typed := update.(type) {
	case eventstream.ContentChunk:
		return typed.Replacement
	case *eventstream.ContentChunk:
		return typed != nil && typed.Replacement
	}
	return false
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
