package session

import (
	"strings"
	"time"
)

// EventIDAllocator assigns a durable event id that is unique within existingIDs.
// The allocator may mutate existingIDs or leave that to PrepareEventsForAppend.
type EventIDAllocator func(event *Event, existingIDs map[string]struct{})

// PrepareEventsForAppendRequest describes one backend-independent event append
// preparation pass. Stores still own the commit primitive and ID allocation.
type PrepareEventsForAppendRequest struct {
	SessionID       string
	Events          []*Event
	ExistingIDs     map[string]struct{}
	Now             time.Time
	AllocateEventID EventIDAllocator
}

// PreparedAppendEvents is the normalized append plan shared by session stores.
type PreparedAppendEvents struct {
	Events    []*Event
	Persisted []*Event
	UpdatedAt time.Time
	Title     string
}

// AppendSessionMutation applies a backend-independent session mutation inside
// the same append transaction after events are prepared and before state update.
type AppendSessionMutation func(*Session, PreparedAppendEvents) (bool, error)

// AppendStateUpdate computes the next session state from prepared events and
// the current state snapshot.
type AppendStateUpdate func([]*Event, map[string]any) (map[string]any, error)

// PrepareAppendTransactionRequest describes one backend-independent append
// transaction. Stores provide existing IDs and commit the returned session,
// state, and persisted events with their own IO primitive.
type PrepareAppendTransactionRequest struct {
	Session         Session
	State           map[string]any
	Events          []*Event
	ExistingIDs     map[string]struct{}
	Now             time.Time
	AllocateEventID EventIDAllocator
	MutateSession   AppendSessionMutation
	UpdateState     AppendStateUpdate
}

// PreparedAppendTransaction is the normalized mutation plan for one store
// append transaction.
type PreparedAppendTransaction struct {
	Session  Session
	State    map[string]any
	Prepared PreparedAppendEvents
	Changed  bool
}

// PrepareEventsForAppend canonicalizes and validates one append batch before
// any backend mutates durable state.
func PrepareEventsForAppend(req PrepareEventsForAppendRequest) (PreparedAppendEvents, error) {
	if len(req.Events) == 0 {
		return PreparedAppendEvents{}, nil
	}
	existingIDs := cloneEventIDSet(req.ExistingIDs)
	prepared := make([]*Event, 0, len(req.Events))
	persisted := make([]*Event, 0, len(req.Events))
	var updatedAt time.Time
	var title string
	for _, event := range req.Events {
		normalized := CanonicalizeEvent(event)
		if normalized == nil {
			return PreparedAppendEvents{}, ErrInvalidEvent
		}
		normalized.SessionID = req.SessionID
		if normalized.Time.IsZero() {
			normalized.Time = req.Now
		}
		if normalized.Type == "" {
			normalized.Type = EventTypeOf(normalized)
		}
		if normalized.Visibility == "" {
			normalized.Visibility = VisibilityCanonical
		}
		if err := ValidateDurableCoreEvent(normalized); err != nil {
			return PreparedAppendEvents{}, err
		}
		if !IsTransient(normalized) {
			if req.AllocateEventID != nil {
				req.AllocateEventID(normalized, existingIDs)
			}
			if id := strings.TrimSpace(normalized.ID); id != "" {
				existingIDs[id] = struct{}{}
			}
			persisted = append(persisted, normalized)
			updatedAt = normalized.Time
			if title == "" {
				title = generatedSessionTitle(normalized)
			}
		}
		prepared = append(prepared, normalized)
	}
	return PreparedAppendEvents{
		Events:    CloneEvents(prepared),
		Persisted: CloneEvents(persisted),
		UpdatedAt: updatedAt,
		Title:     title,
	}, nil
}

// PrepareAppendTransaction canonicalizes events and applies shared session and
// state mutations before any backend commit occurs.
func PrepareAppendTransaction(req PrepareAppendTransactionRequest) (PreparedAppendTransaction, error) {
	if err := ValidateState(req.State); err != nil {
		return PreparedAppendTransaction{}, err
	}
	prepared, err := PrepareEventsForAppend(PrepareEventsForAppendRequest{
		SessionID:       req.Session.SessionID,
		Events:          req.Events,
		ExistingIDs:     req.ExistingIDs,
		Now:             req.Now,
		AllocateEventID: req.AllocateEventID,
	})
	if err != nil {
		return PreparedAppendTransaction{}, err
	}

	nextSession := CloneSession(req.Session)
	nextState := CloneState(req.State)
	if nextState == nil {
		nextState = map[string]any{}
	}
	var changed bool
	if req.MutateSession != nil {
		mutated, err := req.MutateSession(&nextSession, prepared)
		if err != nil {
			return PreparedAppendTransaction{}, err
		}
		changed = changed || mutated
	}
	if ApplyPreparedAppendToSession(&nextSession, prepared) {
		changed = true
	}
	if req.UpdateState != nil {
		next, err := req.UpdateState(CloneEvents(prepared.Events), CloneState(nextState))
		if err != nil {
			return PreparedAppendTransaction{}, err
		}
		if err := ValidateState(next); err != nil {
			return PreparedAppendTransaction{}, err
		}
		nextState = CloneState(next)
		if nextState == nil {
			nextState = map[string]any{}
		}
		nextSession.UpdatedAt = req.Now
		changed = true
	}
	return PreparedAppendTransaction{
		Session:  nextSession,
		State:    nextState,
		Prepared: prepared,
		Changed:  changed,
	}, nil
}

// ApplyPreparedAppendToSession applies title and UpdatedAt changes implied by
// one prepared append batch. It returns true when the session changed.
func ApplyPreparedAppendToSession(activeSession *Session, prepared PreparedAppendEvents) bool {
	if activeSession == nil || len(prepared.Persisted) == 0 {
		return false
	}
	activeSession.UpdatedAt = prepared.UpdatedAt
	if activeSession.Title == "" && prepared.Title != "" {
		activeSession.Title = prepared.Title
	}
	return true
}

func cloneEventIDSet(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for id := range in {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			out[trimmed] = struct{}{}
		}
	}
	return out
}

func generatedSessionTitle(event *Event) string {
	if event == nil || titleHiddenEvent(event) {
		return ""
	}
	return truncateGeneratedSessionTitle(EventDisplayText(event))
}

func titleHiddenEvent(event *Event) bool {
	if event == nil {
		return false
	}
	if hiddenTranscriptMeta(event.Meta["hidden_from_transcript"]) {
		return true
	}
	return event.Meta["source"] == "plugin_hook"
}

func hiddenTranscriptMeta(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func truncateGeneratedSessionTitle(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 80 {
		return text[:80]
	}
	return text
}
