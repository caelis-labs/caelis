package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

// NewStore constructs one new file-backed session store.
func NewStore(cfg Config) *Store {
	store := &Store{
		rootDir:            strings.TrimSpace(cfg.RootDir),
		sessionIDGenerator: cfg.SessionIDGenerator,
		eventIDGenerator:   cfg.EventIDGenerator,
		clock:              cfg.Clock,
		pathCache:          map[string]string{},
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
	return s.withRootLock(storeRootLockExclusive, fn)
}

func (s *Store) withRootReadLock(fn func() error) error {
	return s.withRootLock(storeRootLockShared, fn)
}

func (s *Store) withRootLock(mode storeRootLockMode, fn func() error) error {
	if s == nil || fn == nil {
		return nil
	}
	root := s.normalizedRootDir()
	lockValue, _ := storeRootLocks.LoadOrStore(root, &storeRootLock{})
	rootLock := lockValue.(*storeRootLock)
	if mode == storeRootLockShared {
		rootLock.mu.RLock()
		defer rootLock.mu.RUnlock()
	} else {
		rootLock.mu.Lock()
		defer rootLock.mu.Unlock()
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	file, err := lockSessionStoreRoot(root, mode)
	if err != nil {
		return err
	}
	defer func() {
		_ = unlockSessionStoreRoot(file)
	}()
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
	_ context.Context,
	req session.StartSessionRequest,
) (session.Session, error) {
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

	var out session.Session
	if err := s.withRootWriteLock(func() error {
		doc, err := s.readDocument(ref.SessionID)
		switch {
		case err == nil:
			if !matchesRef(doc.Session, ref) {
				return fmt.Errorf(
					"impl/session/file: session %q already exists with different identity: %w",
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
			Kind:    documentKind,
			Version: documentVersion,
			Session: createdSession,
			State:   map[string]any{},
		}
		if err := s.writeDocument(doc); err != nil {
			return err
		}
		out = session.CloneSession(createdSession)
		return nil
	}); err != nil {
		return session.Session{}, err
	}
	return out, nil
}

func (s *Store) Get(
	_ context.Context,
	ref session.SessionRef,
) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out session.Session
	if err := s.withRootReadLock(func() error {
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
	_ context.Context,
	req session.ListSessionsRequest,
) (session.SessionList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out session.SessionList
	if err := s.withRootReadLock(func() error {
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
	_ context.Context,
	ref session.SessionRef,
	event *session.Event,
) (*session.Event, error) {
	if event == nil {
		return nil, session.ErrInvalidEvent
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var out *session.Event
	if err := s.withRootWriteLock(func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}

		existingEvents, err := s.eventsForDocument(doc)
		if err != nil {
			return err
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(doc, []*session.Event{event}, existingEvents, nil, nil)
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
) (persistedDocument, session.PreparedAppendTransaction, error) {
	tx, err := session.PrepareAppendTransaction(session.PrepareAppendTransactionRequest{
		Session:         doc.Session,
		State:           doc.State,
		Events:          events,
		ExistingIDs:     existingEventIDSet(existingEvents),
		Now:             s.now(),
		AllocateEventID: s.ensureUniqueEventID,
		MutateSession:   mutate,
		UpdateState:     updateState,
	})
	if err != nil {
		return persistedDocument{}, session.PreparedAppendTransaction{}, err
	}
	doc.Session = tx.Session
	doc.State = cloneState(tx.State)
	return doc, tx, nil
}

func (s *Store) writeDocumentWithEvents(doc persistedDocument, events []*session.Event) error {
	events = persistedEvents(events)
	if len(events) == 0 {
		return s.writeDocument(doc)
	}
	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return err
	}
	rollbackLog, err := s.appendEventLogTransaction(path, events)
	if err != nil {
		return err
	}
	if err := s.writeDocument(doc); err != nil {
		if documentWriteCommitted(err) {
			return err
		}
		if rollbackErr := rollbackLog(); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
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

	var out []*session.Event
	if err := s.withRootWriteLock(func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		existingEvents, err := s.eventsForDocument(doc)
		if err != nil {
			return err
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(doc, req.Events, existingEvents, nil, nil)
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
	_ context.Context,
	req session.AppendEventsAndUpdateStateRequest,
) ([]*session.Event, error) {
	if len(req.Events) == 0 && req.UpdateState == nil {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*session.Event
	if err := s.withRootWriteLock(func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		existingEvents, err := s.eventsForDocument(doc)
		if err != nil {
			return err
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(doc, req.Events, existingEvents, nil, req.UpdateState)
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
	_ context.Context,
	req session.EventsRequest,
) ([]*session.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*session.Event
	if err := s.withRootReadLock(func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		events, err := s.eventsForDocument(doc)
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

func (s *Store) BindController(
	_ context.Context,
	ref session.SessionRef,
	binding session.ControllerBinding,
) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out session.Session
	if err := s.withRootWriteLock(func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		doc.Session.Controller = session.CloneControllerBinding(binding)
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

func (s *Store) PutParticipant(
	_ context.Context,
	ref session.SessionRef,
	binding session.ParticipantBinding,
) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out session.Session
	if err := s.withRootWriteLock(func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		if session.PutParticipantBinding(&doc.Session, binding) {
			doc.Session.UpdatedAt = s.now()
			if err := s.writeDocument(doc); err != nil {
				return err
			}
		}
		out = session.CloneSession(doc.Session)
		return nil
	}); err != nil {
		return session.Session{}, err
	}
	return out, nil
}

func (s *Store) PutParticipantWithEvent(
	_ context.Context,
	req session.PutParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out session.Session
	var outEvent *session.Event
	if err := s.withRootWriteLock(func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		existingEvents, err := s.eventsForDocument(doc)
		if err != nil {
			return err
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(
			doc,
			[]*session.Event{req.Event},
			existingEvents,
			func(activeSession *session.Session, _ session.PreparedAppendEvents) (bool, error) {
				return session.PutParticipantBinding(activeSession, req.Binding), nil
			},
			nil,
		)
		if err != nil {
			return err
		}
		normalizedEvent := tx.Prepared.Events[0]
		if err := s.writeDocumentWithEvents(nextDoc, tx.Prepared.Persisted); err != nil {
			return err
		}
		out = session.CloneSession(nextDoc.Session)
		outEvent = session.CloneEvent(normalizedEvent)
		return nil
	}); err != nil {
		return session.Session{}, nil, err
	}
	return out, outEvent, nil
}

func (s *Store) RemoveParticipant(
	_ context.Context,
	ref session.SessionRef,
	participantID string,
) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out session.Session
	if err := s.withRootWriteLock(func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		if session.RemoveParticipantBinding(&doc.Session, participantID) {
			doc.Session.UpdatedAt = s.now()
			if err := s.writeDocument(doc); err != nil {
				return err
			}
		}
		out = session.CloneSession(doc.Session)
		return nil
	}); err != nil {
		return session.Session{}, err
	}
	return out, nil
}

func (s *Store) RemoveParticipantWithEvent(
	_ context.Context,
	req session.RemoveParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out session.Session
	var outEvent *session.Event
	if err := s.withRootWriteLock(func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		existingEvents, err := s.eventsForDocument(doc)
		if err != nil {
			return err
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(
			doc,
			[]*session.Event{req.Event},
			existingEvents,
			func(activeSession *session.Session, _ session.PreparedAppendEvents) (bool, error) {
				return session.RemoveParticipantBinding(activeSession, req.ParticipantID), nil
			},
			nil,
		)
		if err != nil {
			return err
		}
		normalizedEvent := tx.Prepared.Events[0]
		if err := s.writeDocumentWithEvents(nextDoc, tx.Prepared.Persisted); err != nil {
			return err
		}
		out = session.CloneSession(nextDoc.Session)
		outEvent = session.CloneEvent(normalizedEvent)
		return nil
	}); err != nil {
		return session.Session{}, nil, err
	}
	return out, outEvent, nil
}

func (s *Store) SnapshotState(
	_ context.Context,
	ref session.SessionRef,
) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out map[string]any
	if err := s.withRootWriteLock(func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		if doc.State == nil {
			doc.State = map[string]any{}
			if err := s.writeDocument(doc); err != nil {
				return err
			}
		}
		out = cloneState(doc.State)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ReplaceState(
	_ context.Context,
	ref session.SessionRef,
	state map[string]any,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.withRootWriteLock(func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		doc.State = cloneState(state)
		doc.Session.UpdatedAt = s.now()
		return s.writeDocument(doc)
	})
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

	return s.withRootWriteLock(func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		next, err := update(cloneState(doc.State))
		if err != nil {
			return err
		}
		doc.State = cloneState(next)
		doc.Session.UpdatedAt = s.now()
		return s.writeDocument(doc)
	})
}

func (s *Store) LoadDocument(
	_ context.Context,
	req session.LoadSessionRequest,
) (session.LoadedSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out session.LoadedSession
	if err := s.withRootReadLock(func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		events, err := s.eventsForDocument(doc)
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
