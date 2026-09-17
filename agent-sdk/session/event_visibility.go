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

// IsCanonicalHistoryEvent reports whether one event belongs to durable history.
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

// IsInvocationVisibleEvent reports whether one event may participate in the
// current invocation context. Overlay events are transient display overlays, so
// they are not model-visible even when they mirror otherwise canonical shapes.
func IsInvocationVisibleEvent(event *Event) bool {
	if event == nil || IsUIOnly(event) || IsOverlay(event) || IsNotice(event) || IsMirror(event) || IsJournal(event) {
		return false
	}
	return true
}

// IsSharedDialogueEvent reports whether one event belongs to the public
// user/final-assistant ledger shared by all agents in the session.
func IsSharedDialogueEvent(event *Event) bool {
	if event == nil || !IsCanonicalHistoryEvent(event) {
		return false
	}
	switch EventTypeOf(event) {
	case EventTypeUser, EventTypeAssistant:
		return true
	default:
		return false
	}
}

// IsMainInvocationVisibleEvent reports whether one event belongs to the main
// controller context. Delegated subagent tool work remains private to its owner,
// while public user/final assistant dialogue is visible across participants.
func IsMainInvocationVisibleEvent(event *Event) bool {
	if !IsInvocationVisibleEvent(event) {
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
	return IsSharedDialogueEvent(event)
}
