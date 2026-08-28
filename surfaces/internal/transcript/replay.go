package transcript

import (
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
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
// agent-sdk/session.
func replayableACPEvents(env eventstream.Envelope, surface SurfaceProjector) []Event {
	if env.Delivery != nil && env.Delivery.Mode == eventstream.DeliveryTransient {
		return nil
	}
	if env.Delivery != nil && env.Delivery.Mode == eventstream.DeliveryMirror {
		return replayableACPMirrorEvent(env, surface)
	}
	switch env.Kind {
	case eventstream.KindSessionUpdate:
		return replayableACPSessionUpdate(env, surface)
	case eventstream.KindAgentCommunication:
		return ProjectACPEventToEvents(env, surface)
	case eventstream.KindLifecycle:
		return replayableACPTraceEvent(env, surface)
	default:
		return nil
	}
}

func replayableACPMirrorEvent(env eventstream.Envelope, surface SurfaceProjector) []Event {
	switch env.Kind {
	case eventstream.KindLifecycle:
		return ProjectACPEventToEvents(env, surface)
	case eventstream.KindSessionUpdate:
		switch env.Update.(type) {
		case eventstream.ContentChunk, eventstream.ToolCall, eventstream.ToolCallUpdate, eventstream.PlanUpdate, eventstream.UsageUpdate:
			return ProjectACPEventToEvents(env, surface)
		}
	}
	return nil
}

func replayableACPSessionUpdate(env eventstream.Envelope, surface SurfaceProjector) []Event {
	update, ok := env.Update.(eventstream.ContentChunk)
	if !ok {
		switch env.Update.(type) {
		case eventstream.ToolCall, eventstream.ToolCallUpdate, eventstream.PlanUpdate, eventstream.UsageUpdate:
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
	case eventstream.UpdateUserMessage:
		return projected
	case eventstream.UpdateAgentMessage, eventstream.UpdateAgentThought:
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
	switch ACPEventScope(env.Scope) {
	case ScopeMain, ScopeParticipant:
		return true
	default:
		return false
	}
}
