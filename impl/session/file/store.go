package file

import (
	"context"
	"errors"
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
		doc, err := s.readDocument(ref.SessionID, ref.WorkspaceKey)
		switch {
		case err == nil:
			if !matchesRef(doc.Session, ref) {
				return session.ErrSessionNotFound
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
		index, err := s.readSessionIndex()
		if err != nil {
			return err
		}
		out = s.listFromSessionIndex(index, req)
		return nil
	}); err == nil {
		return out, nil
	}

	if err := s.withRootWriteLock(func() error {
		index, err := s.readSessionIndex()
		if err == nil {
			out = s.listFromSessionIndex(index, req)
			return nil
		}
		list, err := s.listFromDocuments(req)
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

		normalized := session.CanonicalizeEvent(event)
		normalized.SessionID = doc.Session.SessionID
		if normalized.Time.IsZero() {
			normalized.Time = s.now()
		}
		if normalized.Type == "" {
			normalized.Type = session.EventTypeOf(normalized)
		}
		if normalized.Visibility == "" {
			normalized.Visibility = session.VisibilityCanonical
		}
		if err := session.ValidateDurableCoreEvent(normalized); err != nil {
			return err
		}
		if !shouldPersistEvent(normalized) {
			out = session.CloneEvent(normalized)
			return nil
		}

		existingEvents, err := s.eventsForDocument(doc)
		if err != nil {
			return err
		}
		s.ensureUniqueEventID(normalized, existingEventIDSet(existingEvents))
		if err := s.migrateDocumentEventsToLog(&doc); err != nil {
			return err
		}
		path, err := s.resolveWritePath(doc.Session)
		if err != nil {
			return err
		}
		if err := s.appendEventLog(path, []*session.Event{normalized}); err != nil {
			return err
		}
		doc.Session.UpdatedAt = normalized.Time
		if doc.Session.Title == "" {
			if text := session.EventText(normalized); text != "" {
				doc.Session.Title = truncateTitle(text)
			}
		}
		if err := s.writeDocument(doc); err != nil {
			return err
		}
		out = session.CloneEvent(normalized)
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
		normalized := session.CloneParticipantBinding(binding)
		for i := range doc.Session.Participants {
			if doc.Session.Participants[i].ID == normalized.ID && normalized.ID != "" {
				doc.Session.Participants[i] = normalized
				doc.Session.UpdatedAt = s.now()
				if err := s.writeDocument(doc); err != nil {
					return err
				}
				out = session.CloneSession(doc.Session)
				return nil
			}
		}
		doc.Session.Participants = append(doc.Session.Participants, normalized)
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
		participantID = strings.TrimSpace(participantID)
		if participantID == "" {
			out = session.CloneSession(doc.Session)
			return nil
		}
		filtered := doc.Session.Participants[:0]
		for _, item := range doc.Session.Participants {
			if strings.TrimSpace(item.ID) == participantID {
				continue
			}
			filtered = append(filtered, item)
		}
		doc.Session.Participants = append([]session.ParticipantBinding(nil), filtered...)
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
