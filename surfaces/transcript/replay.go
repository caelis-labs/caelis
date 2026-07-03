package transcript

import (
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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
	return nil
}

// replayableACPEvents is a defensive projector boundary for replay envelopes
// supplied directly as ACP events. Canonical session replay filtering lives in
// ports/session.
func replayableACPEvents(env eventstream.Envelope, surface SurfaceProjector) []Event {
	switch env.Kind {
	case eventstream.KindSessionUpdate:
		return replayableACPSessionUpdate(env, surface)
	case eventstream.KindLifecycle:
		return replayableACPTraceEvent(env, surface)
	default:
		return nil
	}
}

func replayableACPSessionUpdate(env eventstream.Envelope, surface SurfaceProjector) []Event {
	update, ok := env.Update.(schema.ContentChunk)
	if !ok {
		switch env.Update.(type) {
		case schema.ToolCall, schema.ToolCallUpdate, schema.PlanUpdate, schema.UsageUpdate:
			return replayableACPTraceEvent(env, surface)
		default:
			return nil
		}
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

func replayableACPTraceEvent(env eventstream.Envelope, surface SurfaceProjector) []Event {
	if !replayableTraceScope(env) {
		return nil
	}
	return ProjectACPEventToEvents(env, surface)
}

func replayableTraceScope(env eventstream.Envelope) bool {
	return ACPEventScope(env.Scope) == ScopeMain
}
