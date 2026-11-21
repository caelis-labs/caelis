package session

import (
	"strings"
)

// IsTransient reports whether one event is runtime-transient only.
func IsTransient(event *Event) bool {
	if event == nil {
		return true
	}
	return IsUIOnly(event) || IsOverlay(event) || IsNotice(event)
}

// IsCanonicalHistoryEvent reports whether an event belongs to canonical history.
func IsCanonicalHistoryEvent(event *Event) bool {
	if event == nil {
		return false
	}
	if IsTransient(event) || IsMirror(event) || IsJournal(event) {
		return false
	}
	return true
}

// IsClientReplayEvent reports whether one durable event belongs to the client
// semantic replay lane. Mirrors and completed reviewed approval decisions are
// included without becoming model context; other journal state stays internal.
func IsClientReplayEvent(event *Event) bool {
	if event == nil || IsTransient(event) {
		return false
	}
	if IsJournal(event) {
		return ResolvedApprovalReview(event) != nil
	}
	// Agent-authored Context is durable model input, not a client-feed fact.
	// Unless it carries an explicit protocol or usage projection, treating it
	// as the replay tail produces a boundary that the ACP projector cannot
	// materialize.
	if EventTypeOf(event) == EventTypeContext && event.Protocol == nil && UsageSnapshotFromSessionEvent(event) == nil {
		return false
	}
	if IsAgentCommunicationProtocol(event) {
		if ProtocolAgentCommunicationOf(event) == nil || ValidateAgentCommunicationActor(event.Actor) != nil {
			return false
		}
	}
	return IsCanonicalHistoryEvent(event) || IsMirror(event)
}

// IsMainInvocationVisibleEvent reports whether one event belongs to the main
// controller context. Delegated subagent tool work remains private to its owner,
// while public user/final assistant dialogue is visible across participants.
func IsMainInvocationVisibleEvent(event *Event) bool {
	if !IsCanonicalHistoryEvent(event) {
		return false
	}
	if EventTypeOf(event) == EventTypeContext {
		return true
	}
	if event.Scope == nil {
		return true
	}
	if strings.TrimSpace(event.Scope.Participant.ID) == "" {
		return true
	}
	if event.Scope.Participant.Role == ParticipantRoleDelegated {
		return false
	}
	switch EventTypeOf(event) {
	case EventTypeUser, EventTypeAssistant:
		return true
	default:
		return false
	}
}
