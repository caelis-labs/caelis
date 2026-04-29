package inmemory

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

// Config defines one in-memory session store and service instance.
type Config struct {
	SessionIDGenerator func() string
	EventIDGenerator   func() string
	Clock              func() time.Time
}

// Store is the in-memory implementation of session.Store.
type Store struct {
	mu                 sync.RWMutex
	sessionIDGenerator func() string
	eventIDGenerator   func() string
	clock              func() time.Time
	idCounter          atomic.Uint64
	sessions           map[string]*record
}

// Service is the in-memory implementation of session.Service.
type Service struct {
	store *Store
}

// NewStore constructs one new in-memory session store.
func NewStore(cfg Config) *Store {
	store := &Store{
		sessionIDGenerator: cfg.SessionIDGenerator,
		eventIDGenerator:   cfg.EventIDGenerator,
		clock:              cfg.Clock,
		sessions:           map[string]*record{},
	}
	if store.clock == nil {
		store.clock = time.Now
	}
	return store
}

// NewService constructs one session service backed by one in-memory store.
func NewService(store *Store) *Service {
	if store == nil {
		store = NewStore(Config{})
	}
	return &Service{store: store}
}

func (s *Store) GetOrCreate(
	_ context.Context,
	req sdksession.StartSessionRequest,
) (sdksession.Session, error) {
	ref := sdksession.NormalizeSessionRef(sdksession.SessionRef{
		AppName:      req.AppName,
		UserID:       req.UserID,
		SessionID:    req.PreferredSessionID,
		WorkspaceKey: req.Workspace.Key,
	})
	if ref.AppName == "" || ref.UserID == "" {
		return sdksession.Session{}, sdksession.ErrInvalidSession
	}
	if ref.SessionID == "" {
		ref.SessionID = s.nextID("session", s.sessionIDGenerator)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.sessions[ref.SessionID]; ok {
		return existing.cloneSession(), nil
	}

	now := s.now()
	session := sdksession.Session{
		SessionRef:   ref,
		CWD:          strings.TrimSpace(req.Workspace.CWD),
		Title:        strings.TrimSpace(req.Title),
		Metadata:     maps.Clone(req.Metadata),
		Participants: nil,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.sessions[ref.SessionID] = &record{
		session: session,
		state:   map[string]any{},
	}
	return sdksession.CloneSession(session), nil
}

func (s *Store) Get(
	_ context.Context,
	ref sdksession.SessionRef,
) (sdksession.Session, error) {
	record, ok := s.lookup(ref)
	if !ok {
		return sdksession.Session{}, sdksession.ErrSessionNotFound
	}
	return record.cloneSession(), nil
}

func (s *Store) List(
	_ context.Context,
	req sdksession.ListSessionsRequest,
) (sdksession.SessionList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows := make([]sdksession.SessionSummary, 0, len(s.sessions))
	for _, record := range s.sessions {
		if req.AppName != "" && record.session.AppName != strings.TrimSpace(req.AppName) {
			continue
		}
		if req.UserID != "" && record.session.UserID != strings.TrimSpace(req.UserID) {
			continue
		}
		if req.WorkspaceKey != "" && record.session.WorkspaceKey != strings.TrimSpace(req.WorkspaceKey) {
			continue
		}
		rows = append(rows, sdksession.SessionSummary{
			SessionRef: record.session.SessionRef,
			CWD:        record.session.CWD,
			Title:      record.session.Title,
			UpdatedAt:  record.session.UpdatedAt,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	if req.Limit > 0 && len(rows) > req.Limit {
		rows = rows[:req.Limit]
	}
	return sdksession.SessionList{
		Sessions: sdksession.CloneSessionSummaries(rows),
	}, nil
}

func (s *Store) AppendEvent(
	_ context.Context,
	ref sdksession.SessionRef,
	event *sdksession.Event,
) (*sdksession.Event, error) {
	if event == nil {
		return nil, sdksession.ErrInvalidEvent
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return nil, sdksession.ErrSessionNotFound
	}

	normalized := sdksession.CloneEvent(event)
	if normalized.ID == "" {
		normalized.ID = s.nextID("event", s.eventIDGenerator)
	}
	normalized.SessionID = record.session.SessionID
	if normalized.Time.IsZero() {
		normalized.Time = s.now()
	}
	if normalized.Type == "" {
		normalized.Type = sdksession.EventTypeOf(normalized)
	}
	if normalized.Visibility == "" {
		normalized.Visibility = sdksession.VisibilityCanonical
	}
	record.events = append(record.events, normalized)
	record.session.UpdatedAt = normalized.Time
	if record.session.Title == "" && normalized.Text != "" {
		record.session.Title = truncateTitle(normalized.Text)
	}
	return sdksession.CloneEvent(normalized), nil
}

func (s *Store) Events(
	_ context.Context,
	req sdksession.EventsRequest,
) ([]*sdksession.Event, error) {
	record, ok := s.lookup(req.SessionRef)
	if !ok {
		return nil, sdksession.ErrSessionNotFound
	}
	return sdksession.FilterEvents(record.events, req.Limit, req.IncludeTransient), nil
}

func (s *Store) BindController(
	_ context.Context,
	ref sdksession.SessionRef,
	binding sdksession.ControllerBinding,
) (sdksession.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return sdksession.Session{}, sdksession.ErrSessionNotFound
	}
	record.session.Controller = sdksession.CloneControllerBinding(binding)
	record.session.UpdatedAt = s.now()
	return record.cloneSession(), nil
}

func (s *Store) PutParticipant(
	_ context.Context,
	ref sdksession.SessionRef,
	binding sdksession.ParticipantBinding,
) (sdksession.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return sdksession.Session{}, sdksession.ErrSessionNotFound
	}
	normalized := sdksession.CloneParticipantBinding(binding)
	for i := range record.session.Participants {
		if record.session.Participants[i].ID == normalized.ID && normalized.ID != "" {
			record.session.Participants[i] = normalized
			record.session.UpdatedAt = s.now()
			return record.cloneSession(), nil
		}
	}
	record.session.Participants = append(record.session.Participants, normalized)
	record.session.UpdatedAt = s.now()
	return record.cloneSession(), nil
}

func (s *Store) RemoveParticipant(
	_ context.Context,
	ref sdksession.SessionRef,
	participantID string,
) (sdksession.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return sdksession.Session{}, sdksession.ErrSessionNotFound
	}
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return record.cloneSession(), nil
	}
	filtered := record.session.Participants[:0]
	for _, item := range record.session.Participants {
		if strings.TrimSpace(item.ID) == participantID {
			continue
		}
		filtered = append(filtered, item)
	}
	record.session.Participants = append([]sdksession.ParticipantBinding(nil), filtered...)
	record.session.UpdatedAt = s.now()
	return record.cloneSession(), nil
}

func (s *Store) SnapshotState(
	_ context.Context,
	ref sdksession.SessionRef,
) (map[string]any, error) {
	record, ok := s.lookup(ref)
	if !ok {
		return nil, sdksession.ErrSessionNotFound
	}
	return sdksession.CloneState(record.state), nil
}

func (s *Store) ReplaceState(
	_ context.Context,
	ref sdksession.SessionRef,
	state map[string]any,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return sdksession.ErrSessionNotFound
	}
	record.state = sdksession.CloneState(state)
	record.session.UpdatedAt = s.now()
	return nil
}

func (s *Store) UpdateState(
	_ context.Context,
	ref sdksession.SessionRef,
	update func(map[string]any) (map[string]any, error),
) error {
	if update == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return sdksession.ErrSessionNotFound
	}
	next, err := update(sdksession.CloneState(record.state))
	if err != nil {
		return err
	}
	record.state = sdksession.CloneState(next)
	record.session.UpdatedAt = s.now()
	return nil
}

func (s *Service) StartSession(
	ctx context.Context,
	req sdksession.StartSessionRequest,
) (sdksession.Session, error) {
	return s.store.GetOrCreate(ctx, req)
}

func (s *Service) LoadSession(
	ctx context.Context,
	req sdksession.LoadSessionRequest,
) (sdksession.LoadedSession, error) {
	session, err := s.store.Get(ctx, req.SessionRef)
	if err != nil {
		return sdksession.LoadedSession{}, err
	}
	events, err := s.store.Events(ctx, sdksession.EventsRequest(req))
	if err != nil {
		return sdksession.LoadedSession{}, err
	}
	state, err := s.store.SnapshotState(ctx, req.SessionRef)
	if err != nil {
		return sdksession.LoadedSession{}, err
	}
	return sdksession.LoadedSession{
		Session: session,
		Events:  events,
		State:   state,
	}, nil
}

func (s *Service) Session(
	ctx context.Context,
	ref sdksession.SessionRef,
) (sdksession.Session, error) {
	return s.store.Get(ctx, ref)
}

func (s *Service) AppendEvent(
	ctx context.Context,
	req sdksession.AppendEventRequest,
) (*sdksession.Event, error) {
	return s.store.AppendEvent(ctx, req.SessionRef, req.Event)
}

func (s *Service) Events(
	ctx context.Context,
	req sdksession.EventsRequest,
) ([]*sdksession.Event, error) {
	return s.store.Events(ctx, req)
}

func (s *Service) ListSessions(
	ctx context.Context,
	req sdksession.ListSessionsRequest,
) (sdksession.SessionList, error) {
	return s.store.List(ctx, req)
}

func (s *Service) BindController(
	ctx context.Context,
	req sdksession.BindControllerRequest,
) (sdksession.Session, error) {
	return s.store.BindController(ctx, req.SessionRef, req.Binding)
}

func (s *Service) PutParticipant(
	ctx context.Context,
	req sdksession.PutParticipantRequest,
) (sdksession.Session, error) {
	return s.store.PutParticipant(ctx, req.SessionRef, req.Binding)
}

func (s *Service) RemoveParticipant(
	ctx context.Context,
	req sdksession.RemoveParticipantRequest,
) (sdksession.Session, error) {
	return s.store.RemoveParticipant(ctx, req.SessionRef, req.ParticipantID)
}

func (s *Service) SnapshotState(
	ctx context.Context,
	ref sdksession.SessionRef,
) (map[string]any, error) {
	return s.store.SnapshotState(ctx, ref)
}

func (s *Service) ReplaceState(
	ctx context.Context,
	ref sdksession.SessionRef,
	state map[string]any,
) error {
	return s.store.ReplaceState(ctx, ref, state)
}

func (s *Service) UpdateState(
	ctx context.Context,
	ref sdksession.SessionRef,
	update func(map[string]any) (map[string]any, error),
) error {
	return s.store.UpdateState(ctx, ref, update)
}

type record struct {
	session sdksession.Session
	events  []*sdksession.Event
	state   map[string]any
}

func (r *record) cloneSession() sdksession.Session {
	return sdksession.CloneSession(r.session)
}

func (s *Store) lookup(ref sdksession.SessionRef) (*record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lookupLocked(ref)
}

func (s *Store) lookupLocked(ref sdksession.SessionRef) (*record, bool) {
	normalized := sdksession.NormalizeSessionRef(ref)
	record, ok := s.sessions[normalized.SessionID]
	if !ok {
		return nil, false
	}
	if normalized.AppName != "" && record.session.AppName != normalized.AppName {
		return nil, false
	}
	if normalized.UserID != "" && record.session.UserID != normalized.UserID {
		return nil, false
	}
	if normalized.WorkspaceKey != "" && record.session.WorkspaceKey != normalized.WorkspaceKey {
		return nil, false
	}
	return record, true
}

func (s *Store) nextID(prefix string, custom func() string) string {
	if custom != nil {
		if id := strings.TrimSpace(custom()); id != "" {
			return id
		}
	}
	n := s.idCounter.Add(1)
	return fmt.Sprintf("%s-%d", prefix, n)
}

func (s *Store) now() time.Time {
	return s.clock()
}

func truncateTitle(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 80 {
		return text[:80]
	}
	return text
}
