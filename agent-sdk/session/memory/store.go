package inmemory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/internal/identity"
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
	sessions           map[string]*record
}

// Service is the in-memory implementation of session.Service.
type Service struct {
	store *Store
}

var _ session.ApprovalRecoveryService = (*Service)(nil)

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
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.lookupLocked(ref)
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
		if !rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
			return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
		}
		return rows[i].SessionID < rows[j].SessionID
	})
	if encoded := strings.TrimSpace(req.Cursor); encoded != "" {
		cursor, err := session.DecodeSessionListCursor(encoded)
		if err != nil {
			return session.SessionList{}, err
		}
		start := sort.Search(len(rows), func(i int) bool {
			return rows[i].UpdatedAt.Before(cursor.UpdatedAt) ||
				(rows[i].UpdatedAt.Equal(cursor.UpdatedAt) && rows[i].SessionID > cursor.SessionID)
		})
		rows = rows[start:]
	}
	hasMore := req.Limit > 0 && len(rows) > req.Limit
	if hasMore {
		rows = rows[:req.Limit]
	}
	nextCursor := ""
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		var err error
		nextCursor, err = session.EncodeSessionListCursor(session.SessionListCursor{UpdatedAt: last.UpdatedAt, SessionID: last.SessionID})
		if err != nil {
			return session.SessionList{}, err
		}
	}
	return session.SessionList{Sessions: session.CloneSessionSummaries(rows), NextCursor: nextCursor}, nil
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
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return nil, err
	}

	tx, err := s.prepareAppendTransactionForRecord(record, []*session.Event{event}, nil, nil, req.ExpectedRevision, "", "")
	if err != nil {
		return nil, err
	}
	normalized := tx.Prepared.Events[0]
	s.applyAppendTransactionToRecord(record, tx)
	return session.CloneEvent(normalized), nil
}

// SettlePendingApproval appends one approval settlement only while the exact
// request observed by recovery remains pending at the expected revision.
func (s *Store) SettlePendingApproval(
	_ context.Context,
	req session.SettlePendingApprovalRequest,
) (session.SettlePendingApprovalResult, error) {
	if err := session.ValidateSettlePendingApprovalRequest(req); err != nil {
		return session.SettlePendingApprovalResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.SettlePendingApprovalResult{}, session.ErrSessionNotFound
	}
	result := session.SettlePendingApprovalResult{}
	current := pendingApprovalForEvents(record.events, req.ApprovalRequestID)
	if !session.PendingApprovalMatches(current, req) {
		return result, nil
	}
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return result, err
	}
	if err := session.CheckExpectedRevision(record.session, req.ExpectedRevision); err != nil {
		return result, err
	}
	tx, err := s.prepareAppendTransactionForRecord(
		record,
		[]*session.Event{req.Settlement},
		nil,
		nil,
		req.ExpectedRevision,
		"",
		"",
	)
	if err != nil {
		return result, err
	}
	s.applyAppendTransactionToRecord(record, tx)
	result.Settled = tx.Changed
	if len(tx.Prepared.Events) > 0 {
		result.Event = session.CloneEvent(tx.Prepared.Events[0])
	}
	return result, nil
}

func pendingApprovalForEvents(events []*session.Event, approvalRequestID string) *session.Event {
	requestID := strings.TrimSpace(approvalRequestID)
	var pending *session.Event
	for _, event := range events {
		if event == nil || strings.TrimSpace(event.ApprovalRequestID) != requestID {
			continue
		}
		switch {
		case session.ProtocolPermissionOf(event) != nil:
			pending = event
		case event.Lifecycle != nil:
			pending = nil
		}
	}
	return pending
}

func (s *Store) prepareAppendTransactionForRecord(
	record *record,
	events []*session.Event,
	mutate session.AppendSessionMutation,
	updateState session.AppendStateUpdate,
	expectedRevision *uint64,
	transactionID string,
	mutationDigest string,
) (session.PreparedAppendTransaction, error) {
	if record == nil {
		return session.PreparedAppendTransaction{}, session.ErrSessionNotFound
	}
	return session.PrepareAppendTransaction(session.PrepareAppendTransactionRequest{
		Session:                  record.session,
		State:                    record.state,
		Events:                   events,
		ExistingEvents:           record.events,
		ExistingIDs:              existingEventIDSet(record.events),
		ExpectedRevision:         expectedRevision,
		TransactionID:            transactionID,
		MutationDigest:           mutationDigest,
		TransactionApplied:       record.appliedTransactions[strings.TrimSpace(transactionID)],
		AppliedTransactionDigest: record.appliedTransactionDigests[strings.TrimSpace(transactionID)],
		LastSeq:                  session.LastEventSeq(record.events),
		Now:                      s.now(),
		AllocateEventID:          s.ensureUniqueEventID,
		MutateSession:            mutate,
		UpdateState:              updateState,
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
	if tx.RecordTransaction {
		if record.appliedTransactions == nil {
			record.appliedTransactions = map[string]bool{}
		}
		record.appliedTransactions[tx.TransactionID] = true
		if record.appliedTransactionDigests == nil {
			record.appliedTransactionDigests = map[string]string{}
		}
		record.appliedTransactionDigests[tx.TransactionID] = tx.TransactionDigest
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
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return nil, err
	}
	tx, err := s.prepareAppendTransactionForRecord(record, req.Events, nil, nil, req.ExpectedRevision, "", "")
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
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return nil, err
	}
	tx, err := s.prepareAppendTransactionForRecord(record, req.Events, nil, req.UpdateState, req.ExpectedRevision, req.TransactionID, req.MutationDigest)
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	return session.FilterEvents(record.events, req.Limit, req.IncludeTransient), nil
}

// EventsPage returns one bounded forward sequence page.
func (s *Store) EventsPage(
	_ context.Context,
	req session.EventPageRequest,
) (session.EventPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.EventPage{}, session.ErrSessionNotFound
	}
	return session.PageEvents(record.events, req), nil
}

// EventCheckpoint returns one Session/event high-water cut under the Store
// read lock without cloning the complete event history.
func (s *Store) EventCheckpoint(
	_ context.Context,
	ref session.SessionRef,
) (session.EventCheckpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.lookupLocked(ref)
	if !ok {
		return session.EventCheckpoint{}, session.ErrSessionNotFound
	}
	out := session.EventCheckpoint{Session: record.cloneSession()}
	for index := len(record.events) - 1; index >= 0; index-- {
		event := record.events[index]
		if event == nil || session.IsTransient(event) {
			continue
		}
		if out.ThroughSeq == 0 {
			out.ThroughSeq = event.Seq
		}
		if session.IsClientReplayEvent(event) {
			out.LastClientReplayEvent = session.CloneEvent(event)
			break
		}
	}
	return out, nil
}

func (s *Store) BindController(
	_ context.Context,
	ref session.SessionRef,
	binding session.ControllerBinding,
) (session.Session, error) {
	return s.bindControllerRequest(session.BindControllerRequest{SessionRef: ref, Binding: binding})
}

func (s *Store) bindControllerRequest(req session.BindControllerRequest) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return session.Session{}, err
	}
	record.session.Controller = session.CloneControllerBinding(req.Binding)
	record.session.Revision++
	record.session.UpdatedAt = s.now()
	return record.cloneSession(), nil
}

func (s *Store) BindControllerWithEvent(
	_ context.Context,
	req session.BindControllerWithEventRequest,
) (session.Session, *session.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.Session{}, nil, session.ErrSessionNotFound
	}
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return session.Session{}, nil, err
	}
	tx, err := s.prepareAppendTransactionForRecord(
		record,
		[]*session.Event{req.Event},
		func(active *session.Session, _ session.PreparedAppendEvents) (bool, error) {
			active.Controller = session.CloneControllerBinding(req.Binding)
			return true, nil
		},
		nil,
		req.ExpectedRevision,
		"",
		"",
	)
	if err != nil {
		return session.Session{}, nil, err
	}
	normalized := tx.Prepared.Events[0]
	s.applyAppendTransactionToRecord(record, tx)
	return record.cloneSession(), session.CloneEvent(normalized), nil
}

func (s *Store) PutParticipant(
	_ context.Context,
	ref session.SessionRef,
	binding session.ParticipantBinding,
) (session.Session, error) {
	return s.putParticipantRequest(session.PutParticipantRequest{SessionRef: ref, Binding: binding})
}

func (s *Store) putParticipantRequest(req session.PutParticipantRequest) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return session.Session{}, err
	}
	if err := session.CheckExpectedRevision(record.session, req.ExpectedRevision); err != nil {
		return session.Session{}, err
	}
	if strings.TrimSpace(req.Binding.ID) != "" {
		expected := strings.TrimSpace(req.Binding.DelegationID)
		if req.ExpectedDelegationID != nil {
			expected = strings.TrimSpace(*req.ExpectedDelegationID)
		}
		if err := session.CheckParticipantDelegation(&record.session, req.Binding.ID, expected); err != nil {
			return session.Session{}, err
		}
	}
	if session.PutParticipantBinding(&record.session, req.Binding) {
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
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return session.Session{}, nil, err
	}
	tx, err := s.prepareAppendTransactionForRecord(
		record,
		[]*session.Event{req.Event},
		func(activeSession *session.Session, _ session.PreparedAppendEvents) (bool, error) {
			if strings.TrimSpace(req.Binding.ID) != "" {
				expected := strings.TrimSpace(req.Binding.DelegationID)
				if req.ExpectedDelegationID != nil {
					expected = strings.TrimSpace(*req.ExpectedDelegationID)
				}
				if err := session.CheckParticipantDelegation(activeSession, req.Binding.ID, expected); err != nil {
					return false, err
				}
			}
			return session.PutParticipantBinding(activeSession, req.Binding), nil
		},
		nil,
		req.ExpectedRevision,
		"",
		"",
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
	return s.removeParticipantRequest(session.RemoveParticipantRequest{SessionRef: ref, ParticipantID: participantID})
}

func (s *Store) removeParticipantRequest(req session.RemoveParticipantRequest) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return session.Session{}, err
	}
	if err := session.CheckExpectedRevision(record.session, req.ExpectedRevision); err != nil {
		return session.Session{}, err
	}
	if req.ExpectedDelegationID != nil {
		if err := session.CheckParticipantDelegation(&record.session, req.ParticipantID, *req.ExpectedDelegationID); err != nil {
			return session.Session{}, err
		}
	}
	if session.RemoveParticipantBinding(&record.session, req.ParticipantID) {
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
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return session.Session{}, nil, err
	}
	tx, err := s.prepareAppendTransactionForRecord(
		record,
		[]*session.Event{req.Event},
		func(activeSession *session.Session, _ session.PreparedAppendEvents) (bool, error) {
			if req.ExpectedDelegationID != nil {
				if err := session.CheckParticipantDelegation(activeSession, req.ParticipantID, *req.ExpectedDelegationID); err != nil {
					return false, err
				}
			}
			return session.RemoveParticipantBinding(activeSession, req.ParticipantID), nil
		},
		nil,
		req.ExpectedRevision,
		"",
		"",
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
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.lookupLocked(ref)
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	return cloneState(record.state), nil
}

func (s *Store) ReplaceState(
	_ context.Context,
	req session.ReplaceStateRequest,
) (session.Session, error) {
	if err := session.ValidateState(req.State); err != nil {
		return session.Session{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return session.Session{}, err
	}
	if err := session.CheckExpectedRevision(record.session, req.ExpectedRevision); err != nil {
		return session.Session{}, err
	}
	record.state = cloneState(req.State)
	record.session.Revision++
	record.session.UpdatedAt = s.now()
	return record.cloneSession(), nil
}

func (s *Store) UpdateState(
	_ context.Context,
	req session.UpdateStateRequest,
) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.Session{}, session.ErrSessionNotFound
	}
	if err := validateMutationGuard(record.lease, req.MutationGuard, s.now()); err != nil {
		return session.Session{}, err
	}
	if err := session.CheckExpectedRevision(record.session, req.ExpectedRevision); err != nil {
		return session.Session{}, err
	}
	if req.Update == nil {
		return record.cloneSession(), nil
	}
	next, err := req.Update(cloneState(record.state))
	if err != nil {
		return session.Session{}, err
	}
	if err := session.ValidateState(next); err != nil {
		return session.Session{}, err
	}
	record.state = cloneState(next)
	record.session.Revision++
	record.session.UpdatedAt = s.now()
	return record.cloneSession(), nil
}

func (s *Service) StartSession(
	ctx context.Context,
	req session.StartSessionRequest,
) (session.Session, error) {
	return s.store.GetOrCreate(ctx, req)
}

func (s *Service) LoadSession(
	_ context.Context,
	req session.LoadSessionRequest,
) (session.LoadedSession, error) {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	record, ok := s.store.lookupLocked(req.SessionRef)
	if !ok {
		return session.LoadedSession{}, session.ErrSessionNotFound
	}
	return session.LoadedSession{
		Session: record.cloneSession(),
		Events:  session.FilterEvents(record.events, req.Limit, req.IncludeTransient),
		State:   cloneState(record.state),
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

// SettlePendingApproval atomically settles one still-pending recovery
// candidate.
func (s *Service) SettlePendingApproval(
	ctx context.Context,
	req session.SettlePendingApprovalRequest,
) (session.SettlePendingApprovalResult, error) {
	return s.store.SettlePendingApproval(ctx, req)
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

// EventsPage returns one bounded forward sequence page.
func (s *Service) EventsPage(
	ctx context.Context,
	req session.EventPageRequest,
) (session.EventPage, error) {
	return s.store.EventsPage(ctx, req)
}

// EventCheckpoint returns one atomic Session/event high-water cut.
func (s *Service) EventCheckpoint(
	ctx context.Context,
	ref session.SessionRef,
) (session.EventCheckpoint, error) {
	return s.store.EventCheckpoint(ctx, ref)
}

func (s *Service) ListSessions(
	ctx context.Context,
	req session.ListSessionsRequest,
) (session.SessionList, error) {
	return s.store.List(ctx, req)
}

// PendingApprovals returns durable permission requests without a later
// settlement across the in-memory Store.
func (s *Service) PendingApprovals(ctx context.Context) ([]session.PendingApproval, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	type pendingKey struct {
		sessionID string
		requestID string
		ref       session.SessionRef
		revision  uint64
		request   *session.Event
	}
	keys := make([]pendingKey, 0)
	for _, record := range s.store.sessions {
		pending := map[string]*session.Event{}
		for _, event := range record.events {
			requestID := ""
			if event != nil {
				requestID = strings.TrimSpace(event.ApprovalRequestID)
			}
			if requestID == "" {
				continue
			}
			switch {
			case session.ProtocolPermissionOf(event) != nil:
				pending[requestID] = event
			case event.Lifecycle != nil:
				delete(pending, requestID)
			}
		}
		for requestID, request := range pending {
			keys = append(keys, pendingKey{
				sessionID: record.session.SessionID, requestID: requestID,
				ref: record.session.SessionRef, request: request, revision: record.session.Revision,
			})
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].sessionID != keys[j].sessionID {
			return keys[i].sessionID < keys[j].sessionID
		}
		return keys[i].requestID < keys[j].requestID
	})
	out := make([]session.PendingApproval, 0, len(keys))
	for _, key := range keys {
		out = append(out, session.PendingApproval{
			SessionRef: key.ref, Revision: key.revision, Request: session.CloneEvent(key.request),
		})
	}
	return out, nil
}

func (s *Service) BindController(
	ctx context.Context,
	req session.BindControllerRequest,
) (session.Session, error) {
	return s.store.bindControllerRequest(req)
}

func (s *Service) BindControllerWithEvent(
	ctx context.Context,
	req session.BindControllerWithEventRequest,
) (session.Session, *session.Event, error) {
	return s.store.BindControllerWithEvent(ctx, req)
}

func (s *Service) PutParticipant(
	ctx context.Context,
	req session.PutParticipantRequest,
) (session.Session, error) {
	return s.store.putParticipantRequest(req)
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
	return s.store.removeParticipantRequest(req)
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
	req session.ReplaceStateRequest,
) (session.Session, error) {
	return s.store.ReplaceState(ctx, req)
}

func (s *Service) UpdateState(
	ctx context.Context,
	req session.UpdateStateRequest,
) (session.Session, error) {
	return s.store.UpdateState(ctx, req)
}

type record struct {
	session                   session.Session
	events                    []*session.Event
	state                     map[string]any
	appliedTransactions       map[string]bool
	appliedTransactionDigests map[string]string
	lease                     session.SessionLease
	leaseEpoch                uint64
}

func (r *record) cloneSession() session.Session {
	return session.CloneSession(r.session)
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
	return identity.New(prefix)
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
		id = identity.New("event")
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
