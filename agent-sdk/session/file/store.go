package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// NewStore constructs one new file-backed session store.
func NewStore(cfg Config) *Store {
	store := &Store{
		rootDir:            strings.TrimSpace(cfg.RootDir),
		sessionIDGenerator: cfg.SessionIDGenerator,
		eventIDGenerator:   cfg.EventIDGenerator,
		clock:              cfg.Clock,
		pathCache:          map[string]string{},
		eventPageIndexes:   map[string]*eventPageIndex{},
		eventLogCaches:     map[string]*eventLogCache{},
	}
	if store.rootDir == "" {
		store.rootDir = filepath.Join(os.TempDir(), "caelis-sdk-sessions")
	}
	if store.clock == nil {
		store.clock = time.Now
	}
	return store
}

// NewService constructs one session service backed by one file store.
func NewService(store *Store) *Service {
	if store == nil {
		store = NewStore(Config{})
	}
	return &Service{store: store}
}

func (s *Store) withRootWriteLock(fn func() error) error {
	return s.withRootLockContext(context.Background(), storeRootLockExclusive, fn)
}

func (s *Store) withRootWriteLockContext(ctx context.Context, fn func() error) error {
	return s.withRootLockContext(ctx, storeRootLockExclusive, fn)
}

func (s *Store) withRootReadLock(fn func() error) error {
	// Reads may need to finish a committed WAL transaction before exposing
	// document/event state, so they take the exclusive root lock as well.
	return s.withRootLockContext(context.Background(), storeRootLockExclusive, fn)
}

func (s *Store) withRootReadLockContext(ctx context.Context, fn func() error) error {
	// Reads may need to finish a committed WAL transaction before exposing
	// document/event state, so cancellation changes acquisition only, not the
	// exclusive recovery barrier.
	return s.withRootLockContext(ctx, storeRootLockExclusive, fn)
}

func (s *Store) withRootLock(mode storeRootLockMode, fn func() error) error {
	return s.withRootLockContext(context.Background(), mode, fn)
}

func (s *Store) withRootLockContext(ctx context.Context, mode storeRootLockMode, fn func() error) error {
	if s == nil || fn == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root := s.normalizedRootDir()
	lockValue, _ := storeRootLocks.LoadOrStore(root, &storeRootLock{})
	rootLock := lockValue.(*storeRootLock)
	if err := rootLock.mu.LockContext(ctx); err != nil {
		return err
	}
	defer rootLock.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := lockSessionStoreRoot(ctx, root, mode)
	if err != nil {
		return err
	}
	defer func() {
		_ = unlockSessionStoreRoot(file)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if mode == storeRootLockExclusive {
		pending, err := s.transactionRecoveryPending()
		if err != nil {
			return err
		}
		// A full legacy scan is required once per process/root. Current writers
		// maintain a durable root marker, so subsequent operations pay only one
		// Stat unless a committed WAL was abandoned by a crashed process.
		if !rootLock.recoveryInitialized || pending {
			if err := s.recoverTransactions(); err != nil {
				return err
			}
			rootLock.recoveryInitialized = true
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func (s *Store) normalizedRootDir() string {
	root := strings.TrimSpace(s.rootDir)
	if root == "" {
		root = filepath.Join(os.TempDir(), "caelis-sdk-sessions")
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return filepath.Clean(root)
}

func (s *Store) GetOrCreate(
	ctx context.Context,
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

	if err := s.mu.LockContext(ctx); err != nil {
		return session.Session{}, err
	}
	defer s.mu.Unlock()

	var out session.Session
	if err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocument(ref.SessionID)
		switch {
		case err == nil:
			if !matchesRef(doc.Session, ref) {
				return fmt.Errorf(
					"agent-sdk/session/file: session %q already exists with different identity: %w",
					ref.SessionID,
					session.ErrInvalidSession,
				)
			}
			out = session.CloneSession(doc.Session)
			return nil
		case !errors.Is(err, session.ErrSessionNotFound):
			return err
		}

		now := s.now()
		createdSession := session.Session{
			SessionRef:   ref,
			CWD:          strings.TrimSpace(req.Workspace.CWD),
			Title:        strings.TrimSpace(req.Title),
			Metadata:     cloneMap(req.Metadata),
			CreatedAt:    now,
			UpdatedAt:    now,
			Participants: nil,
		}
		doc = persistedDocument{
			Kind:             documentKind,
			Version:          documentVersion,
			Session:          createdSession,
			State:            map[string]any{},
			PendingApprovals: map[string]*session.Event{},
		}
		out = session.CloneSession(createdSession)
		// Creation has no event payload, but it still needs the same durable
		// document/index recovery boundary as a compound mutation. Otherwise a
		// crash after the document rename can leave a valid Session permanently
		// absent from the only lookup/listing index.
		if err := s.writeRecoverableDocumentTransaction(doc, nil); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if documentWriteCommitted(err) {
			return out, &session.CommittedError{Err: err}
		}
		return session.Session{}, err
	}
	return out, nil
}

func (s *Store) Get(
	ctx context.Context,
	ref session.SessionRef,
) (session.Session, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.Session{}, err
	}
	defer s.mu.Unlock()

	var out session.Session
	if err := s.withRootReadLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		out = session.CloneSession(doc.Session)
		return nil
	}); err != nil {
		return session.Session{}, err
	}
	return out, nil
}

func (s *Store) List(
	ctx context.Context,
	req session.ListSessionsRequest,
) (session.SessionList, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.SessionList{}, err
	}
	defer s.mu.Unlock()

	var out session.SessionList
	if err := s.withRootReadLockContext(ctx, func() error {
		list, err := s.listFromSessionIndex(req)
		if err != nil {
			return err
		}
		out = list
		return nil
	}); err != nil {
		return session.SessionList{}, err
	}
	return out, nil
}

func (s *Store) AppendEvent(
	ctx context.Context,
	ref session.SessionRef,
	event *session.Event,
) (*session.Event, error) {
	return s.appendEventRequest(ctx, session.AppendEventRequest{SessionRef: ref, Event: event})
}

func (s *Store) appendEventRequest(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	event := req.Event
	if event == nil {
		return nil, session.ErrInvalidEvent
	}

	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	var out *session.Event
	if err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}

		existingEvents, err := s.eventsForDocumentContext(ctx, doc)
		if err != nil {
			return err
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(doc, []*session.Event{event}, existingEvents, nil, nil, req.ExpectedRevision, "", "")
		if err != nil {
			return err
		}
		normalized := tx.Prepared.Events[0]
		if !tx.Changed {
			out = session.CloneEvent(normalized)
			return nil
		}
		if err := s.writeDocumentWithEvents(nextDoc, tx.Prepared.Persisted); err != nil {
			return err
		}
		out = session.CloneEvent(normalized)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) prepareAppendTransactionForDocument(
	doc persistedDocument,
	events []*session.Event,
	existingEvents []*session.Event,
	mutate session.AppendSessionMutation,
	updateState session.AppendStateUpdate,
	expectedRevision *uint64,
	transactionID string,
	mutationDigest string,
) (persistedDocument, session.PreparedAppendTransaction, error) {
	existingForPrepare := existingEvents
	var existingIDs map[string]struct{}
	var lastSeq uint64
	if relevant, cachedIDs, cachedLastSeq, ok := s.cachedAppendPreparationInputs(doc, existingEvents, events); ok {
		existingForPrepare = relevant
		existingIDs = cachedIDs
		lastSeq = cachedLastSeq
	} else {
		existingIDs = existingEventIDSet(existingEvents)
		lastSeq = session.LastEventSeq(existingEvents)
	}
	tx, err := session.PrepareAppendTransaction(session.PrepareAppendTransactionRequest{
		Session:                  doc.Session,
		State:                    doc.State,
		Events:                   events,
		ExistingEvents:           existingForPrepare,
		ExistingIDs:              existingIDs,
		ExpectedRevision:         expectedRevision,
		TransactionID:            transactionID,
		MutationDigest:           mutationDigest,
		TransactionApplied:       doc.AppliedTransactions[strings.TrimSpace(transactionID)],
		AppliedTransactionDigest: doc.AppliedTransactionDigests[strings.TrimSpace(transactionID)],
		LastSeq:                  lastSeq,
		Now:                      s.now(),
		AllocateEventID:          s.ensureUniqueEventID,
		MutateSession:            mutate,
		UpdateState:              updateState,
	})
	if err != nil {
		return persistedDocument{}, session.PreparedAppendTransaction{}, err
	}
	doc.Session = tx.Session
	doc.State = cloneState(tx.State)
	if doc.PendingApprovals == nil {
		doc.PendingApprovals = pendingApprovalsFromEvents(existingEvents)
	}
	applyPendingApprovalEvents(doc.PendingApprovals, tx.Prepared.Persisted)
	if tx.RecordTransaction {
		if doc.AppliedTransactions == nil {
			doc.AppliedTransactions = map[string]bool{}
		}
		doc.AppliedTransactions[tx.TransactionID] = true
		if doc.AppliedTransactionDigests == nil {
			doc.AppliedTransactionDigests = map[string]string{}
		}
		doc.AppliedTransactionDigests[tx.TransactionID] = tx.TransactionDigest
	}
	return doc, tx, nil
}

func (s *Store) writeDocumentWithEvents(doc persistedDocument, events []*session.Event) error {
	events = persistedEvents(events)
	if len(events) == 0 {
		return s.writeDocument(doc)
	}
	if err := s.writeRecoverableDocumentTransaction(doc, events); err != nil {
		if documentWriteCommitted(err) {
			return &session.CommittedError{Err: err}
		}
		return err
	}
	return nil
}

func (s *Store) AppendEvents(
	ctx context.Context,
	req session.AppendEventsRequest,
) ([]*session.Event, error) {
	if len(req.Events) == 0 {
		return nil, nil
	}

	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	var out []*session.Event
	if err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}
		existingEvents, err := s.eventsForDocumentContext(ctx, doc)
		if err != nil {
			return err
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(doc, req.Events, existingEvents, nil, nil, req.ExpectedRevision, "", "")
		if err != nil {
			return err
		}
		if tx.Changed {
			if err := s.writeDocumentWithEvents(nextDoc, tx.Prepared.Persisted); err != nil {
				return err
			}
		}
		out = session.CloneEvents(tx.Prepared.Events)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) AppendEventsAndUpdateState(
	ctx context.Context,
	req session.AppendEventsAndUpdateStateRequest,
) ([]*session.Event, error) {
	if len(req.Events) == 0 && req.UpdateState == nil {
		return nil, nil
	}

	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	var out []*session.Event
	if err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}
		existingEvents, err := s.eventsForDocumentContext(ctx, doc)
		if err != nil {
			return err
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(doc, req.Events, existingEvents, nil, req.UpdateState, req.ExpectedRevision, req.TransactionID, req.MutationDigest)
		if err != nil {
			return err
		}
		if tx.Changed {
			if err := s.writeDocumentWithEvents(nextDoc, tx.Prepared.Persisted); err != nil {
				return err
			}
		}
		out = session.CloneEvents(tx.Prepared.Events)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) Events(
	ctx context.Context,
	req session.EventsRequest,
) ([]*session.Event, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	var out []*session.Event
	if err := s.withRootReadLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		events, err := s.eventsForDocumentContext(ctx, doc)
		if err != nil {
			return err
		}
		out = session.FilterEvents(events, req.Limit, req.IncludeTransient)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// EventsPage streams one bounded forward sequence page from the event log.
func (s *Store) EventsPage(
	ctx context.Context,
	req session.EventPageRequest,
) (session.EventPage, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.EventPage{}, err
	}
	defer s.mu.Unlock()

	var out session.EventPage
	if err := s.withRootReadLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		path, err := s.resolveWritePath(doc.Session)
		if err != nil {
			return err
		}
		out, err = s.readEventLogPage(ctx, path, req)
		return err
	}); err != nil {
		return session.EventPage{}, err
	}
	return out, nil
}

// EventCheckpoint returns one Session/event high-water cut while the Store
// and recovery locks exclude concurrent durable mutation.
func (s *Store) EventCheckpoint(
	ctx context.Context,
	ref session.SessionRef,
) (session.EventCheckpoint, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.EventCheckpoint{}, err
	}
	defer s.mu.Unlock()

	var out session.EventCheckpoint
	if err := s.withRootReadLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		path, err := s.resolveWritePath(doc.Session)
		if err != nil {
			return err
		}
		throughSeq, lastReplay, err := readEventLogCheckpoint(ctx, eventLogPath(path))
		if err != nil {
			return err
		}
		out = session.EventCheckpoint{
			Session:               session.CloneSession(doc.Session),
			ThroughSeq:            throughSeq,
			LastClientReplayEvent: lastReplay,
		}
		return nil
	}); err != nil {
		return session.EventCheckpoint{}, err
	}
	return out, nil
}

func (s *Store) BindController(
	ctx context.Context,
	ref session.SessionRef,
	binding session.ControllerBinding,
) (session.Session, error) {
	return s.bindControllerRequest(ctx, session.BindControllerRequest{SessionRef: ref, Binding: binding})
}

func (s *Store) bindControllerRequest(ctx context.Context, req session.BindControllerRequest) (session.Session, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.Session{}, err
	}
	defer s.mu.Unlock()

	var out session.Session
	if err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}
		doc.Session.Controller = session.CloneControllerBinding(req.Binding)
		doc.Session.Revision++
		doc.Session.UpdatedAt = s.now()
		if err := s.writeDocument(doc); err != nil {
			return err
		}
		out = session.CloneSession(doc.Session)
		return nil
	}); err != nil {
		return session.Session{}, err
	}
	return out, nil
}

func (s *Store) BindControllerWithEvent(
	ctx context.Context,
	req session.BindControllerWithEventRequest,
) (session.Session, *session.Event, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.Session{}, nil, err
	}
	defer s.mu.Unlock()
	var out session.Session
	var outEvent *session.Event
	if err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}
		existing, err := s.eventsForDocumentContext(ctx, doc)
		if err != nil {
			return err
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(
			doc,
			[]*session.Event{req.Event},
			existing,
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
			return err
		}
		normalized := tx.Prepared.Events[0]
		if err := s.writeDocumentWithEvents(nextDoc, tx.Prepared.Persisted); err != nil {
			return err
		}
		out = session.CloneSession(nextDoc.Session)
		outEvent = session.CloneEvent(normalized)
		return nil
	}); err != nil {
		return session.Session{}, nil, err
	}
	return out, outEvent, nil
}

func (s *Store) PutParticipant(
	ctx context.Context,
	ref session.SessionRef,
	binding session.ParticipantBinding,
) (session.Session, error) {
	return s.putParticipantRequest(ctx, session.PutParticipantRequest{SessionRef: ref, Binding: binding})
}

func (s *Store) putParticipantRequest(ctx context.Context, req session.PutParticipantRequest) (session.Session, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.Session{}, err
	}
	defer s.mu.Unlock()

	var out session.Session
	if err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}
		if err := session.CheckExpectedRevision(doc.Session, req.ExpectedRevision); err != nil {
			return err
		}
		if strings.TrimSpace(req.Binding.ID) != "" {
			expected := strings.TrimSpace(req.Binding.DelegationID)
			if req.ExpectedDelegationID != nil {
				expected = strings.TrimSpace(*req.ExpectedDelegationID)
			}
			if err := session.CheckParticipantDelegation(&doc.Session, req.Binding.ID, expected); err != nil {
				return err
			}
		}
		if session.PutParticipantBinding(&doc.Session, req.Binding) {
			doc.Session.Revision++
			doc.Session.UpdatedAt = s.now()
			out = session.CloneSession(doc.Session)
			if err := s.writeDocument(doc); err != nil {
				if documentWriteCommitted(err) {
					return &session.CommittedError{Err: err}
				}
				return err
			}
		}
		out = session.CloneSession(doc.Session)
		return nil
	}); err != nil {
		if session.IsCommitted(err) {
			return out, err
		}
		return session.Session{}, err
	}
	return out, nil
}

func (s *Store) PutParticipantWithEvent(
	ctx context.Context,
	req session.PutParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.Session{}, nil, err
	}
	defer s.mu.Unlock()

	var out session.Session
	var outEvent *session.Event
	if err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}
		existingEvents, err := s.eventsForDocumentContext(ctx, doc)
		if err != nil {
			return err
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(
			doc,
			[]*session.Event{req.Event},
			existingEvents,
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
			return err
		}
		normalizedEvent := tx.Prepared.Events[0]
		out = session.CloneSession(nextDoc.Session)
		outEvent = session.CloneEvent(normalizedEvent)
		if err := s.writeDocumentWithEvents(nextDoc, tx.Prepared.Persisted); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if session.IsCommitted(err) {
			return out, outEvent, err
		}
		return session.Session{}, nil, err
	}
	return out, outEvent, nil
}

func (s *Store) RemoveParticipant(
	ctx context.Context,
	ref session.SessionRef,
	participantID string,
) (session.Session, error) {
	return s.removeParticipantRequest(ctx, session.RemoveParticipantRequest{SessionRef: ref, ParticipantID: participantID})
}

func (s *Store) removeParticipantRequest(ctx context.Context, req session.RemoveParticipantRequest) (session.Session, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.Session{}, err
	}
	defer s.mu.Unlock()

	var out session.Session
	if err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}
		if err := session.CheckExpectedRevision(doc.Session, req.ExpectedRevision); err != nil {
			return err
		}
		if req.ExpectedDelegationID != nil {
			if err := session.CheckParticipantDelegation(&doc.Session, req.ParticipantID, *req.ExpectedDelegationID); err != nil {
				return err
			}
		}
		if session.RemoveParticipantBinding(&doc.Session, req.ParticipantID) {
			doc.Session.Revision++
			doc.Session.UpdatedAt = s.now()
			out = session.CloneSession(doc.Session)
			if err := s.writeDocument(doc); err != nil {
				if documentWriteCommitted(err) {
					return &session.CommittedError{Err: err}
				}
				return err
			}
		}
		out = session.CloneSession(doc.Session)
		return nil
	}); err != nil {
		if session.IsCommitted(err) {
			return out, err
		}
		return session.Session{}, err
	}
	return out, nil
}

func (s *Store) RemoveParticipantWithEvent(
	ctx context.Context,
	req session.RemoveParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.Session{}, nil, err
	}
	defer s.mu.Unlock()

	var out session.Session
	var outEvent *session.Event
	if err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}
		existingEvents, err := s.eventsForDocumentContext(ctx, doc)
		if err != nil {
			return err
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(
			doc,
			[]*session.Event{req.Event},
			existingEvents,
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
			return err
		}
		normalizedEvent := tx.Prepared.Events[0]
		out = session.CloneSession(nextDoc.Session)
		outEvent = session.CloneEvent(normalizedEvent)
		if err := s.writeDocumentWithEvents(nextDoc, tx.Prepared.Persisted); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if session.IsCommitted(err) {
			return out, outEvent, err
		}
		return session.Session{}, nil, err
	}
	return out, outEvent, nil
}

func (s *Store) SnapshotState(
	ctx context.Context,
	ref session.SessionRef,
) (map[string]any, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	var out map[string]any
	if err := s.withRootReadLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		out = cloneState(doc.State)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ReplaceState(
	ctx context.Context,
	req session.ReplaceStateRequest,
) (session.Session, error) {
	if err := session.ValidateState(req.State); err != nil {
		return session.Session{}, err
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return session.Session{}, err
	}
	defer s.mu.Unlock()

	var out session.Session
	err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}
		if err := session.CheckExpectedRevision(doc.Session, req.ExpectedRevision); err != nil {
			return err
		}
		doc.State = cloneState(req.State)
		doc.Session.Revision++
		doc.Session.UpdatedAt = s.now()
		out = session.CloneSession(doc.Session)
		if err := s.writeDocument(doc); err != nil {
			if documentWriteCommitted(err) {
				return &session.CommittedError{Err: err}
			}
			return err
		}
		return nil
	})
	return out, err
}

func (s *Store) UpdateState(
	ctx context.Context,
	req session.UpdateStateRequest,
) (session.Session, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.Session{}, err
	}
	defer s.mu.Unlock()

	var out session.Session
	err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}
		if err := session.CheckExpectedRevision(doc.Session, req.ExpectedRevision); err != nil {
			return err
		}
		if req.Update == nil {
			out = session.CloneSession(doc.Session)
			return nil
		}
		next, err := req.Update(cloneState(doc.State))
		if err != nil {
			return err
		}
		if err := session.ValidateState(next); err != nil {
			return err
		}
		doc.State = cloneState(next)
		doc.Session.Revision++
		doc.Session.UpdatedAt = s.now()
		out = session.CloneSession(doc.Session)
		if err := s.writeDocument(doc); err != nil {
			if documentWriteCommitted(err) {
				return &session.CommittedError{Err: err}
			}
			return err
		}
		return nil
	})
	return out, err
}

func (s *Store) LoadDocument(
	ctx context.Context,
	req session.LoadSessionRequest,
) (session.LoadedSession, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.LoadedSession{}, err
	}
	defer s.mu.Unlock()

	var out session.LoadedSession
	if err := s.withRootReadLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		events, err := s.eventsForDocumentContext(ctx, doc)
		if err != nil {
			return err
		}
		out = session.LoadedSession{
			Session: session.CloneSession(doc.Session),
			Events:  session.FilterEvents(events, req.Limit, req.IncludeTransient),
			State:   cloneState(doc.State),
		}
		return nil
	}); err != nil {
		return session.LoadedSession{}, err
	}
	return out, nil
}
