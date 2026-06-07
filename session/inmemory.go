package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// memService is an in-memory session.Service for testing.
type memService struct {
	mu         sync.RWMutex
	sessions   map[string]Session
	events     map[string][]Event // keyed by session ID
	nextID     int
	structured map[string]map[string]any // structured JSON state per session
}

// InMemoryService returns an in-memory session Service suitable for
// testing. It does not persist across process restarts.
func InMemoryService() Service {
	return &memService{
		sessions:   make(map[string]Session),
		events:     make(map[string][]Event),
		structured: make(map[string]map[string]any),
	}
}

func (s *memService) key(r Ref) string { return r.String() }

func (s *memService) nextIDStr(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s-%d", prefix, s.nextID)
}

func (s *memService) Create(_ context.Context, req CreateRequest) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := Session{
		Ref: Ref{
			AppName:      req.AppName,
			UserID:       req.UserID,
			WorkspaceKey: req.WorkspaceKey,
			SessionID:    s.nextIDStr("sess"),
		},
		Workspace:    req.Workspace.Clone(),
		Title:        req.Title,
		State:        req.State.Clone(),
		Controller:   req.Controller,
		Participants: make([]ParticipantBinding, len(req.Participants)),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	for i, p := range req.Participants {
		sess.Participants[i] = p.Clone()
	}
	k := s.key(sess.Ref)
	s.sessions[k] = sess
	s.events[k] = nil
	s.structured[k] = make(map[string]any)
	return sess, nil
}

func (s *memService) Get(_ context.Context, ref Ref) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := s.key(ref)
	sess, ok := s.sessions[k]
	if !ok {
		return Session{}, fmt.Errorf("session not found: %s", ref)
	}
	return sess.Clone(), nil
}

func (s *memService) List(_ context.Context, req ListRequest) (ListResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Session
	for _, sess := range s.sessions {
		if req.AppName != "" && sess.Ref.AppName != req.AppName {
			continue
		}
		if req.UserID != "" && sess.Ref.UserID != req.UserID {
			continue
		}
		if req.WorkspaceKey != "" && sess.Ref.WorkspaceKey != req.WorkspaceKey {
			continue
		}
		out = append(out, sess.Clone())
	}
	if req.Limit > 0 && len(out) > req.Limit {
		out = out[:req.Limit]
	}
	return ListResponse{Sessions: out}, nil
}

func (s *memService) Fork(_ context.Context, req ForkRequest) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(req.Source)
	orig, ok := s.sessions[k]
	if !ok {
		return Session{}, fmt.Errorf("session not found: %s", req.Source)
	}
	forked := orig.Clone()
	forked.Ref.SessionID = s.nextIDStr("sess")
	if req.Title != "" {
		forked.Title = req.Title
	}
	forked.CreatedAt = time.Now()
	forked.UpdatedAt = time.Now()
	fk := s.key(forked.Ref)
	s.sessions[fk] = forked
	if evts, ok := s.events[k]; ok {
		cloned := make([]Event, len(evts))
		for i, e := range evts {
			cloned[i] = e.Clone()
			cloned[i].ID = s.nextIDStr("evt")
			cloned[i].SessionRef = forked.Ref
		}
		s.events[fk] = cloned
	}
	// Clone structured state.
	if st, ok := s.structured[k]; ok {
		cp := make(map[string]any, len(st))
		for sk, sv := range st {
			cp[sk] = sv
		}
		s.structured[fk] = cp
	}
	return forked, nil
}

func (s *memService) Delete(_ context.Context, ref Ref) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(ref)
	if _, ok := s.sessions[k]; !ok {
		return fmt.Errorf("session not found: %s", ref)
	}
	delete(s.sessions, k)
	delete(s.events, k)
	delete(s.structured, k)
	return nil
}

func (s *memService) AppendEvent(_ context.Context, ref Ref, evt Event) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(ref)
	if _, ok := s.sessions[k]; !ok {
		return Event{}, fmt.Errorf("session not found: %s", ref)
	}

	// Validate before persisting.
	CanonicalizeEvent(&evt)
	if err := ValidateEvent(&evt); err != nil {
		return Event{}, fmt.Errorf("event validation: %w", err)
	}

	// Reject transient events — they should not be persisted.
	if evt.Visibility.IsTransient() {
		return Event{}, fmt.Errorf("transient events (overlay/ui_only) cannot be persisted")
	}

	persisted := evt.Clone()
	persisted.ID = s.nextIDStr("evt")
	persisted.SessionRef = ref
	if persisted.CreatedAt.IsZero() {
		persisted.CreatedAt = time.Now()
	}
	s.events[k] = append(s.events[k], persisted)
	sess := s.sessions[k]
	sess.UpdatedAt = persisted.CreatedAt
	s.sessions[k] = sess
	return persisted, nil
}

func (s *memService) Events(_ context.Context, req EventsRequest) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := s.key(req.SessionRef)
	evts, ok := s.events[k]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", req.SessionRef)
	}
	var out []Event
	found := req.AfterID == ""
	for _, e := range evts {
		if !found {
			if e.ID == req.AfterID {
				found = true
			}
			continue
		}
		if len(req.Kinds) > 0 {
			match := false
			for _, k := range req.Kinds {
				if e.Kind == k {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, e.Clone())
		if req.Limit > 0 && len(out) >= req.Limit {
			break
		}
	}
	return out, nil
}

func (s *memService) UpdateState(_ context.Context, ref Ref, fn func(State) (State, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(ref)
	sess, ok := s.sessions[k]
	if !ok {
		return fmt.Errorf("session not found: %s", ref)
	}
	newState, err := fn(sess.State.Clone())
	if err != nil {
		return err
	}
	sess.State = newState
	sess.UpdatedAt = time.Now()
	s.sessions[k] = sess
	return nil
}

// ─── Optional interface implementations ──────────────────────────────

func (s *memService) BindController(_ context.Context, ref Ref, binding ControllerBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(ref)
	sess, ok := s.sessions[k]
	if !ok {
		return fmt.Errorf("session not found: %s", ref)
	}
	sess.Controller = binding
	sess.UpdatedAt = time.Now()
	s.sessions[k] = sess
	return nil
}

func (s *memService) PutParticipant(_ context.Context, ref Ref, p ParticipantBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(ref)
	sess, ok := s.sessions[k]
	if !ok {
		return fmt.Errorf("session not found: %s", ref)
	}
	// Update existing or append.
	for i, existing := range sess.Participants {
		if existing.ID == p.ID {
			sess.Participants[i] = p.Clone()
			s.sessions[k] = sess
			return nil
		}
	}
	sess.Participants = append(sess.Participants, p.Clone())
	sess.UpdatedAt = time.Now()
	s.sessions[k] = sess
	return nil
}

func (s *memService) RemoveParticipant(_ context.Context, ref Ref, participantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(ref)
	sess, ok := s.sessions[k]
	if !ok {
		return fmt.Errorf("session not found: %s", ref)
	}
	for i, p := range sess.Participants {
		if p.ID == participantID {
			sess.Participants = append(sess.Participants[:i], sess.Participants[i+1:]...)
			sess.UpdatedAt = time.Now()
			s.sessions[k] = sess
			return nil
		}
	}
	return fmt.Errorf("participant not found: %s", participantID)
}

func (s *memService) SnapshotState(_ context.Context, ref Ref) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := s.key(ref)
	if _, ok := s.sessions[k]; !ok {
		return nil, fmt.Errorf("session not found: %s", ref)
	}
	st, ok := s.structured[k]
	if !ok {
		return make(map[string]any), nil
	}
	// Deep copy via JSON round-trip.
	data, _ := json.Marshal(st)
	var cp map[string]any
	json.Unmarshal(data, &cp)
	return cp, nil
}

func (s *memService) ReplaceState(_ context.Context, ref Ref, state map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(ref)
	if _, ok := s.sessions[k]; !ok {
		return fmt.Errorf("session not found: %s", ref)
	}
	// Deep copy via JSON round-trip.
	data, _ := json.Marshal(state)
	var cp map[string]any
	json.Unmarshal(data, &cp)
	s.structured[k] = cp
	return nil
}

// Compile-time interface checks.
var (
	_ Service            = (*memService)(nil)
	_ ControllerManager  = (*memService)(nil)
	_ ParticipantManager = (*memService)(nil)
	_ StructuredState    = (*memService)(nil)
)
