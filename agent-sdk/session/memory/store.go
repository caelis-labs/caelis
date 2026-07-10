package inmemory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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
	req session.StartSessionRequest,
) (session.Session, error) {
	if err := session.ValidateMetadata(req.Metadata); err != nil {
		return session.Session{}, err
	}
	ref := session.NormalizeSessionRef(session.SessionRef{
		AppName:      req.AppName,
		UserID:       req.UserID,
		SessionID:    req.PreferredSessionID,
		WorkspaceKey: req.Workspace.Key,
	})
	if ref.AppName == "" || ref.UserID == "" {
		return session.Session{}, session.ErrInvalidSession
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
	createdSession := session.Session{
		SessionRef:   ref,
		CWD:          strings.TrimSpace(req.Workspace.CWD),
		Title:        strings.TrimSpace(req.Title),
		Metadata:     session.CloneState(req.Metadata),
		Participants: nil,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.sessions[ref.SessionID] = &record{
		session: createdSession,
		state:   map[string]any{},
	}
	return session.CloneSession(createdSession), nil
}

func (s *Store) Get(
	_ context.Context,
	ref session.SessionRef,
) (session.Session, error) {
	record, ok := s.lookup(ref)
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	return record.cloneSession(), nil
}

func (s *Store) List(
	_ context.Context,
	req session.ListSessionsRequest,
) (session.SessionList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows := make([]session.SessionSummary, 0, len(s.sessions))
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
		rows = append(rows, session.SessionSummary{
			SessionRef: record.session.SessionRef,
			CWD:        record.session.CWD,
			Title:      record.session.Title,
			Metadata:   session.CloneState(record.session.Metadata),
			UpdatedAt:  record.session.UpdatedAt,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	if req.Limit > 0 && len(rows) > req.Limit {
		rows = rows[:req.Limit]
	}
	return session.SessionList{
		Sessions: session.CloneSessionSummaries(rows),
	}, nil
}

func (s *Store) AppendEvent(
	_ context.Context,
	ref session.SessionRef,
	event *session.Event,
) (*session.Event, error) {
	return s.appendEventRequest(session.AppendEventRequest{SessionRef: ref, Event: event})
}

func (s *Store) appendEventRequest(req session.AppendEventRequest) (*session.Event, error) {
	event := req.Event
	if event == nil {
		return nil, session.ErrInvalidEvent
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return nil, session.ErrSessionNotFound
	}

	tx, err := s.prepareAppendTransactionForRecord(record, []*session.Event{event}, nil, nil, req.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	normalized := tx.Prepared.Events[0]
	s.applyAppendTransactionToRecord(record, tx)
	return session.CloneEvent(normalized), nil
}

func (s *Store) prepareAppendTransactionForRecord(
	record *record,
	events []*session.Event,
	mutate session.AppendSessionMutation,
	updateState session.AppendStateUpdate,
	expectedRevision *uint64,
) (session.PreparedAppendTransaction, error) {
	if record == nil {
		return session.PreparedAppendTransaction{}, session.ErrSessionNotFound
	}
	return session.PrepareAppendTransaction(session.PrepareAppendTransactionRequest{
		Session:          record.session,
		State:            record.state,
		Events:           events,
		ExistingEvents:   record.events,
		ExistingIDs:      existingEventIDSet(record.events),
		ExpectedRevision: expectedRevision,
		LastSeq:          session.LastEventSeq(record.events),
		Now:              s.now(),
		AllocateEventID:  s.ensureUniqueEventID,
		MutateSession:    mutate,
		UpdateState:      updateState,
	})
}

func (s *Store) applyAppendTransactionToRecord(record *record, tx session.PreparedAppendTransaction) {
	if record == nil || !tx.Changed {
		return
	}
	record.session = session.CloneSession(tx.Session)
	record.state = cloneState(tx.State)
	if len(tx.Prepared.Persisted) > 0 {
		record.events = append(record.events, session.CloneEvents(tx.Prepared.Persisted)...)
	}
}

func (s *Store) AppendEvents(
	_ context.Context,
	req session.AppendEventsRequest,
) ([]*session.Event, error) {
	if len(req.Events) == 0 {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	tx, err := s.prepareAppendTransactionForRecord(record, req.Events, nil, nil, req.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	s.applyAppendTransactionToRecord(record, tx)
	return session.CloneEvents(tx.Prepared.Events), nil
}

func (s *Store) AppendEventsAndUpdateState(
	_ context.Context,
	req session.AppendEventsAndUpdateStateRequest,
) ([]*session.Event, error) {
	if len(req.Events) == 0 && req.UpdateState == nil {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	tx, err := s.prepareAppendTransactionForRecord(record, req.Events, nil, req.UpdateState, req.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	s.applyAppendTransactionToRecord(record, tx)
	return session.CloneEvents(tx.Prepared.Events), nil
}

func (s *Store) Events(
	_ context.Context,
	req session.EventsRequest,
) ([]*session.Event, error) {
	record, ok := s.lookup(req.SessionRef)
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	return session.FilterEvents(record.events, req.Limit, req.IncludeTransient), nil
}

func (s *Store) BindController(
	_ context.Context,
	ref session.SessionRef,
	binding session.ControllerBinding,
) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	record.session.Controller = session.CloneControllerBinding(binding)
	record.session.Revision++
	record.session.UpdatedAt = s.now()
	return record.cloneSession(), nil
}

func (s *Store) PutParticipant(
	_ context.Context,
	ref session.SessionRef,
	binding session.ParticipantBinding,
) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	if session.PutParticipantBinding(&record.session, binding) {
		record.session.Revision++
		record.session.UpdatedAt = s.now()
	}
	return record.cloneSession(), nil
}

func (s *Store) PutParticipantWithEvent(
	_ context.Context,
	req session.PutParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.Session{}, nil, session.ErrSessionNotFound
	}
	tx, err := s.prepareAppendTransactionForRecord(
		record,
		[]*session.Event{req.Event},
		func(activeSession *session.Session, _ session.PreparedAppendEvents) (bool, error) {
			return session.PutParticipantBinding(activeSession, req.Binding), nil
		},
		nil,
		req.ExpectedRevision,
	)
	if err != nil {
		return session.Session{}, nil, err
	}
	normalizedEvent := tx.Prepared.Events[0]
	s.applyAppendTransactionToRecord(record, tx)
	return record.cloneSession(), session.CloneEvent(normalizedEvent), nil
}

func (s *Store) RemoveParticipant(
	_ context.Context,
	ref session.SessionRef,
	participantID string,
) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	if session.RemoveParticipantBinding(&record.session, participantID) {
		record.session.Revision++
		record.session.UpdatedAt = s.now()
	}
	return record.cloneSession(), nil
}

func (s *Store) RemoveParticipantWithEvent(
	_ context.Context,
	req session.RemoveParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.Session{}, nil, session.ErrSessionNotFound
	}
	tx, err := s.prepareAppendTransactionForRecord(
		record,
		[]*session.Event{req.Event},
		func(activeSession *session.Session, _ session.PreparedAppendEvents) (bool, error) {
			return session.RemoveParticipantBinding(activeSession, req.ParticipantID), nil
		},
		nil,
		req.ExpectedRevision,
	)
	if err != nil {
		return session.Session{}, nil, err
	}
	normalizedEvent := tx.Prepared.Events[0]
	s.applyAppendTransactionToRecord(record, tx)
	return record.cloneSession(), session.CloneEvent(normalizedEvent), nil
}

func (s *Store) SnapshotState(
	_ context.Context,
	ref session.SessionRef,
) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	if record.state == nil {
		record.state = map[string]any{}
		record.session.Revision++
		record.session.UpdatedAt = s.now()
	}
	return cloneState(record.state), nil
}

func (s *Store) ReplaceState(
	_ context.Context,
	ref session.SessionRef,
	state map[string]any,
) error {
	if err := session.ValidateState(state); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return session.ErrSessionNotFound
	}
	record.state = cloneState(state)
	record.session.Revision++
	record.session.UpdatedAt = s.now()
	return nil
}

func (s *Store) UpdateState(
	_ context.Context,
	ref session.SessionRef,
	update func(map[string]any) (map[string]any, error),
) error {
	if update == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return session.ErrSessionNotFound
	}
	next, err := update(cloneState(record.state))
	if err != nil {
		return err
	}
	if err := session.ValidateState(next); err != nil {
		return err
	}
	record.state = cloneState(next)
	record.session.Revision++
	record.session.UpdatedAt = s.now()
	return nil
}

func (s *Service) StartSession(
	ctx context.Context,
	req session.StartSessionRequest,
) (session.Session, error) {
	return s.store.GetOrCreate(ctx, req)
}

func (s *Service) LoadSession(
	ctx context.Context,
	req session.LoadSessionRequest,
) (session.LoadedSession, error) {
	loadedSession, err := s.store.Get(ctx, req.SessionRef)
	if err != nil {
		return session.LoadedSession{}, err
	}
	events, err := s.store.Events(ctx, session.EventsRequest(req))
	if err != nil {
		return session.LoadedSession{}, err
	}
	state, err := s.store.SnapshotState(ctx, req.SessionRef)
	if err != nil {
		return session.LoadedSession{}, err
	}
	return session.LoadedSession{
		Session: loadedSession,
		Events:  events,
		State:   state,
	}, nil
}

func (s *Service) Session(
	ctx context.Context,
	ref session.SessionRef,
) (session.Session, error) {
	return s.store.Get(ctx, ref)
}

func (s *Service) AppendEvent(
	ctx context.Context,
	req session.AppendEventRequest,
) (*session.Event, error) {
	return s.store.appendEventRequest(req)
}

func (s *Service) AppendEvents(
	ctx context.Context,
	req session.AppendEventsRequest,
) ([]*session.Event, error) {
	return s.store.AppendEvents(ctx, req)
}

func (s *Service) AppendEventsAndUpdateState(
	ctx context.Context,
	req session.AppendEventsAndUpdateStateRequest,
) ([]*session.Event, error) {
	return s.store.AppendEventsAndUpdateState(ctx, req)
}

func (s *Service) Events(
	ctx context.Context,
	req session.EventsRequest,
) ([]*session.Event, error) {
	return s.store.Events(ctx, req)
}

func (s *Service) ListSessions(
	ctx context.Context,
	req session.ListSessionsRequest,
) (session.SessionList, error) {
	return s.store.List(ctx, req)
}

func (s *Service) BindController(
	ctx context.Context,
	req session.BindControllerRequest,
) (session.Session, error) {
	return s.store.BindController(ctx, req.SessionRef, req.Binding)
}

func (s *Service) PutParticipant(
	ctx context.Context,
	req session.PutParticipantRequest,
) (session.Session, error) {
	return s.store.PutParticipant(ctx, req.SessionRef, req.Binding)
}

func (s *Service) PutParticipantWithEvent(
	ctx context.Context,
	req session.PutParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	return s.store.PutParticipantWithEvent(ctx, req)
}

func (s *Service) RemoveParticipant(
	ctx context.Context,
	req session.RemoveParticipantRequest,
) (session.Session, error) {
	return s.store.RemoveParticipant(ctx, req.SessionRef, req.ParticipantID)
}

func (s *Service) RemoveParticipantWithEvent(
	ctx context.Context,
	req session.RemoveParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	return s.store.RemoveParticipantWithEvent(ctx, req)
}

func (s *Service) SnapshotState(
	ctx context.Context,
	ref session.SessionRef,
) (map[string]any, error) {
	return s.store.SnapshotState(ctx, ref)
}

func (s *Service) ReplaceState(
	ctx context.Context,
	ref session.SessionRef,
	state map[string]any,
) error {
	return s.store.ReplaceState(ctx, ref, state)
}

func (s *Service) UpdateState(
	ctx context.Context,
	ref session.SessionRef,
	update func(map[string]any) (map[string]any, error),
) error {
	return s.store.UpdateState(ctx, ref, update)
}

type record struct {
	session session.Session
	events  []*session.Event
	state   map[string]any
}

func (r *record) cloneSession() session.Session {
	return session.CloneSession(r.session)
}

func (s *Store) lookup(ref session.SessionRef) (*record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lookupLocked(ref)
}

func (s *Store) lookupLocked(ref session.SessionRef) (*record, bool) {
	normalized := session.NormalizeSessionRef(ref)
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

func (s *Store) ensureUniqueEventID(event *session.Event, existing map[string]struct{}) {
	if event == nil {
		return
	}
	id := strings.TrimSpace(event.ID)
	if id != "" {
		if _, used := existing[id]; !used {
			event.ID = id
			return
		}
	}
	for attempt := 0; attempt < 8; attempt++ {
		id = strings.TrimSpace(s.nextID("event", s.eventIDGenerator))
		if id == "" {
			continue
		}
		if _, used := existing[id]; !used {
			event.ID = id
			return
		}
	}
	for {
		id = fmt.Sprintf("event-%d", s.idCounter.Add(1))
		if _, used := existing[id]; !used {
			event.ID = id
			return
		}
	}
}

func existingEventIDSet(events []*session.Event) map[string]struct{} {
	out := make(map[string]struct{}, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if id := strings.TrimSpace(event.ID); id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func cloneState(state map[string]any) map[string]any {
	out := session.CloneState(state)
	if out == nil {
		return map[string]any{}
	}
	return out
}

func (s *Store) now() time.Time {
	return s.clock()
}
