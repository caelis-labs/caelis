package transcript

import (
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func ProjectReplayEvents(events []eventstream.Envelope, surface SurfaceProjector) []Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]Event, 0, len(events))
	for _, env := range events {
		out = append(out, ProjectReplayEvent(env, surface)...)
	}
	return out
}

func ProjectReplayEvent(env eventstream.Envelope, surface SurfaceProjector) []Event {
	if projected := replayableACPEvents(env, surface); len(projected) != 0 {
		return projected
	}
	if event, ok := participantUserReplayEvent(env); ok {
		return []Event{event}
	}
	return nil
}

func replayableACPEvents(env eventstream.Envelope, surface SurfaceProjector) []Event {
	if env.Kind != eventstream.KindSessionUpdate {
		return nil
	}
	update, ok := env.Update.(schema.ContentChunk)
	if !ok {
		return nil
	}
	projected := ProjectACPEventToEvents(env, surface)
	if len(projected) == 0 {
		return nil
	}
	switch strings.TrimSpace(update.SessionUpdate) {
	case schema.UpdateUserMessage:
		return projected
	case schema.UpdateAgentMessage, schema.UpdateAgentThought:
		if !env.Final {
			return nil
		}
		return projected
	default:
		return nil
	}
}

func participantUserReplayEvent(env eventstream.Envelope) (Event, bool) {
	if env.Kind != eventstream.KindSessionUpdate || env.Scope != eventstream.ScopeParticipant {
		return Event{}, false
	}
	update, ok := env.Update.(schema.ContentChunk)
	if !ok || strings.TrimSpace(update.SessionUpdate) != schema.UpdateUserMessage {
		return Event{}, false
	}
	text := strings.TrimSpace(ProtocolTextContent(update.Content))
	if text == "" {
		return Event{}, false
	}
	label := FirstNonEmpty(
		MetaString(env.Meta, "mention"),
		MetaString(env.Meta, "handle"),
	)
	if label != "" && !strings.HasPrefix(label, "@") {
		label = "@" + label
	}
	label = FirstNonEmpty(label, env.ParticipantID, env.Actor, env.ScopeID)
	label = FirstNonEmpty(label, "side ACP")
	return Event{
		Kind:          EventNarrative,
		Scope:         ScopeMain,
		NarrativeKind: NarrativeUser,
		Text:          fmt.Sprintf("User to %s: %s", label, text),
		Final:         true,
		OccurredAt:    env.OccurredAt,
	}, true
}
