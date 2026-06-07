package orchestrator

import (
	"github.com/OnslaughtSnail/caelis/session"
)

// MainContext returns model-visible events for the main invocation:
//   - canonical main events (user, assistant, tool_call, tool_result, plan)
//   - sidecar participant final events marked shareable
//   - SPAWN prompt/anchor and final result/summary
//   - EXCLUDES delegated child transcript
func MainContext(events []session.Event) []session.Event {
	var out []session.Event
	for _, e := range events {
		if !e.Visibility.IsModelVisible() {
			continue
		}
		scope := eventScopeKind(&e)
		switch scope {
		case EventScopeMain:
			out = append(out, e)
		case EventScopeParticipant:
			// Include sidecar participant events that are marked shareable.
			if isShareableParticipant(&e) {
				out = append(out, e)
			}
		case EventScopeSubagent:
			// Include only the SPAWN anchor (tool_call) and final result (tool_result).
			if isSpawnAnchor(&e) || isSpawnResult(&e) {
				out = append(out, e)
			}
		}
	}
	return out
}

// SidecarContext returns events visible to a sidecar participant.
// Includes main events plus the participant's own events.
func SidecarContext(events []session.Event, participantID string) []session.Event {
	var out []session.Event
	for _, e := range events {
		if !e.Visibility.IsModelVisible() {
			continue
		}
		scope := eventScopeKind(&e)
		switch scope {
		case EventScopeMain:
			out = append(out, e)
		case EventScopeParticipant:
			if e.Actor.ParticipantID == participantID {
				out = append(out, e)
			}
		case EventScopeSubagent:
			// Sidecar sees anchor + result from subagents.
			if isSpawnAnchor(&e) || isSpawnResult(&e) {
				out = append(out, e)
			}
		}
	}
	return out
}

// DelegatedChildContext returns events for a delegated child session.
// This is the child's own transcript.
func DelegatedChildContext(events []session.Event, delegationID string) []session.Event {
	var out []session.Event
	for _, e := range events {
		if e.Actor.Source == "acp_subagent" && e.Actor.ParticipantID == delegationID {
			out = append(out, e)
		}
	}
	return out
}

// ParentVisibleSummary returns the parent-visible subset of delegated child events:
// just the SPAWN anchor + final result/summary.
func ParentVisibleSummary(events []session.Event, delegationID string) []session.Event {
	var out []session.Event
	for _, e := range events {
		if e.Actor.Source != "acp_subagent" {
			continue
		}
		if e.Actor.ParticipantID != delegationID {
			continue
		}
		if isSpawnAnchor(&e) || isSpawnResult(&e) {
			out = append(out, e)
		}
	}
	return out
}

// EventScopeKind represents the scope of an event.
type EventScopeKind string

const (
	EventScopeMain        EventScopeKind = "main"
	EventScopeParticipant EventScopeKind = "participant"
	EventScopeSubagent    EventScopeKind = "subagent"
)

// eventScopeKind determines the scope of an event based on its actor metadata.
func eventScopeKind(e *session.Event) EventScopeKind {
	if e.Actor.ParticipantID != "" {
		if e.Actor.Source == "acp_subagent" {
			return EventScopeSubagent
		}
		return EventScopeParticipant
	}
	return EventScopeMain
}

// isShareableParticipant returns true if a participant event should be
// shared into the main model context. Currently, final canonical events
// from sidecar participants are shareable.
func isShareableParticipant(e *session.Event) bool {
	// Final canonical assistant events from participants are shareable.
	if e.Kind == session.EventKindAssistant && e.Visibility == session.VisibilityCanonical {
		return true
	}
	return false
}

// isSpawnAnchor returns true if the event is a SPAWN tool call (the anchor).
func isSpawnAnchor(e *session.Event) bool {
	return e.Kind == session.EventKindToolCall &&
		e.ToolCallPayload != nil &&
		e.ToolCallPayload.Name == "SPAWN"
}

// isSpawnResult returns true if the event is a SPAWN tool result (the summary).
func isSpawnResult(e *session.Event) bool {
	return e.Kind == session.EventKindToolResult &&
		e.ToolResultPayload != nil &&
		e.ToolResultPayload.Name == "SPAWN"
}
