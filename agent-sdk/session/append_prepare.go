package session

import (
	"reflect"
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
	ExistingEvents  []*Event
	ExistingIDs     map[string]struct{}
	LastSeq         uint64
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
	Session          Session
	State            map[string]any
	Events           []*Event
	ExistingEvents   []*Event
	ExistingIDs      map[string]struct{}
	ExpectedRevision *uint64
	LastSeq          uint64
	Now              time.Time
	AllocateEventID  EventIDAllocator
	MutateSession    AppendSessionMutation
	UpdateState      AppendStateUpdate
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
	existingEvents := eventIndexByID(req.ExistingEvents)
	existingKeys := eventIndexByIdempotencyKey(req.ExistingEvents)
	for id := range existingEvents {
		existingIDs[id] = struct{}{}
	}
	nextSeq := req.LastSeq
	prepared := make([]*Event, 0, len(req.Events))
	persisted := make([]*Event, 0, len(req.Events))
	var updatedAt time.Time
	var title string
	for _, event := range req.Events {
		normalized := CanonicalizeEvent(event)
		if normalized == nil {
			return PreparedAppendEvents{}, ErrInvalidEvent
		}
		stampCurrentEventSchemas(normalized)
		migrated, err := MigrateEvent(*normalized)
		if err != nil {
			return PreparedAppendEvents{}, err
		}
		normalized = &migrated
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
			id := strings.TrimSpace(normalized.ID)
			key := strings.TrimSpace(normalized.IdempotencyKey)
			prior := existingEvents[id]
			if prior == nil && key != "" {
				prior = existingKeys[key]
			}
			if prior != nil {
				if !sameIdempotentEvent(prior, normalized) {
					return PreparedAppendEvents{}, &EventConflictError{SessionID: req.SessionID, EventID: id, IdempotencyKey: key}
				}
				prepared = append(prepared, CloneEvent(prior))
				continue
			}
			if req.AllocateEventID != nil {
				req.AllocateEventID(normalized, existingIDs)
			}
			nextSeq++
			normalized.Seq = nextSeq
			if id := strings.TrimSpace(normalized.ID); id != "" {
				existingIDs[id] = struct{}{}
				existingEvents[id] = CloneEvent(normalized)
			}
			if key := strings.TrimSpace(normalized.IdempotencyKey); key != "" {
				existingKeys[key] = CloneEvent(normalized)
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
	if err := CheckExpectedRevision(req.Session, req.ExpectedRevision); err != nil {
		return PreparedAppendTransaction{}, err
	}
	if err := ValidateState(req.State); err != nil {
		return PreparedAppendTransaction{}, err
	}
	prepared, err := PrepareEventsForAppend(PrepareEventsForAppendRequest{
		SessionID:       req.Session.SessionID,
		Events:          req.Events,
		ExistingEvents:  req.ExistingEvents,
		ExistingIDs:     req.ExistingIDs,
		LastSeq:         req.LastSeq,
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
	if changed {
		nextSession.Revision = req.Session.Revision + 1
	}
	return PreparedAppendTransaction{
		Session:  nextSession,
		State:    nextState,
		Prepared: prepared,
		Changed:  changed,
	}, nil
}

// LastEventSeq returns the highest durable sequence in one event set.
func LastEventSeq(events []*Event) uint64 {
	var out uint64
	for index, event := range events {
		if event == nil {
			continue
		}
		seq := event.Seq
		if seq == 0 {
			seq = uint64(index + 1)
		}
		if seq > out {
			out = seq
		}
	}
	return out
}

func eventIndexByID(events []*Event) map[string]*Event {
	out := make(map[string]*Event, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if id := strings.TrimSpace(event.ID); id != "" {
			out[id] = migratedEventForIndex(event)
		}
	}
	return out
}

func eventIndexByIdempotencyKey(events []*Event) map[string]*Event {
	out := make(map[string]*Event, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if key := strings.TrimSpace(event.IdempotencyKey); key != "" {
			out[key] = migratedEventForIndex(event)
		}
	}
	return out
}

func migratedEventForIndex(event *Event) *Event {
	if event == nil {
		return nil
	}
	migrated, err := MigrateEvent(*event)
	if err != nil {
		return CloneEvent(event)
	}
	return &migrated
}

func sameIdempotentEvent(existing *Event, retry *Event) bool {
	left := CanonicalizeEvent(existing)
	right := CanonicalizeEvent(retry)
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	left.SessionID, right.SessionID = "", ""
	left.ID, right.ID = "", ""
	left.Seq, right.Seq = 0, 0
	left.Time, right.Time = time.Time{}, time.Time{}
	left.Text, right.Text = "", ""
	return reflect.DeepEqual(left, right)
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
