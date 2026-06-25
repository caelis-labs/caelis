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
	if IsTransient(event) || IsMirror(event) {
		return false
	}
	return true
}

// IsInvocationVisibleEvent reports whether one event may participate in the
// current invocation context.
func IsInvocationVisibleEvent(event *Event) bool {
	if event == nil || IsUIOnly(event) || IsNotice(event) || IsMirror(event) {
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
	if event.Scope == nil {
		return true
	}
	if strings.TrimSpace(event.Scope.Participant.ID) == "" {
		return true
	}
	if event.Scope.Participant.Role == ParticipantRoleDelegated {
		return false
	}
	source := strings.ToLower(strings.TrimSpace(event.Scope.Source))
	if source == "agent_spawn" || strings.Contains(source, "spawn") {
		return false
	}
	return IsSharedDialogueEvent(event)
}
