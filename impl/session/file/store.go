package file

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

const (
	documentKind    = "caelis.sdk.session"
	documentVersion = 1
	indexKind       = "caelis.sdk.session_index"
	indexVersion    = 1
	indexFilename   = ".sessions.index.json"
	lockFilename    = ".sessions.lock"
)

var storeRootLocks sync.Map

type storeRootLock struct {
	mu sync.Mutex
}

// Config defines one single-file durable session store instance.
type Config struct {
	RootDir            string
	SessionIDGenerator func() string
	EventIDGenerator   func() string
	Clock              func() time.Time
}

// Store is the file-backed implementation of session.Store.
type Store struct {
	mu                 sync.Mutex
	rootDir            string
	sessionIDGenerator func() string
	eventIDGenerator   func() string
	clock              func() time.Time
	idCounter          atomic.Uint64
	pathCache          map[string]string
}

// Service is the file-backed implementation of session.Service.
type Service struct {
	store *Store
}

type persistedDocument struct {
	Kind    string           `json:"kind"`
	Version int              `json:"version"`
	Session session.Session  `json:"session"`
	Events  []*session.Event `json:"events,omitempty"`
	State   map[string]any   `json:"state"`
}

type persistedSessionIndex struct {
	Kind     string                       `json:"kind"`
	Version  int                          `json:"version"`
	Sessions []persistedSessionIndexEntry `json:"sessions,omitempty"`
}

type persistedSessionIndexEntry struct {
	Session session.SessionSummary `json:"session"`
	Path    string                 `json:"path"`
}

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
	if s == nil || fn == nil {
		return nil
	}
	root := s.normalizedRootDir()
	lockValue, _ := storeRootLocks.LoadOrStore(root, &storeRootLock{})
	rootLock := lockValue.(*storeRootLock)
	rootLock.mu.Lock()
	defer rootLock.mu.Unlock()

	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	file, err := lockSessionStoreRoot(root)
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

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return session.Session{}, err
	}
	return session.CloneSession(doc.Session), nil
}

func (s *Store) List(
	_ context.Context,
	req session.ListSessionsRequest,
) (session.SessionList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	index, err := s.readSessionIndex()
	if err == nil {
		return s.listFromSessionIndex(index, req), nil
	}

	var out session.SessionList
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

func (s *Store) listFromDocuments(req session.ListSessionsRequest) (session.SessionList, error) {
	index, err := s.rebuildSessionIndexFromDocuments()
	if err != nil {
		return session.SessionList{}, err
	}
	return s.listFromSessionIndex(index, req), nil
}

func (s *Store) rebuildSessionIndexFromDocuments() (persistedSessionIndex, error) {
	paths, err := s.listDocumentPaths()
	if err != nil {
		return persistedSessionIndex{}, err
	}

	entries := make([]persistedSessionIndexEntry, 0, len(paths))
	for _, path := range paths {
		doc, err := s.readDocumentAt(path)
		if err != nil {
			return persistedSessionIndex{}, err
		}
		s.pathCache[pathCacheKey(doc.Session.SessionID, doc.Session.WorkspaceKey)] = path
		entries = append(entries, s.sessionIndexEntry(doc.Session, path))
	}
	index := persistedSessionIndex{Sessions: entries}
	if err := s.writeSessionIndex(index); err != nil {
		return persistedSessionIndex{}, err
	}
	index.Kind = indexKind
	index.Version = indexVersion
	index.Sessions = cloneSessionIndexEntries(entries)
	return index, nil
}

func (s *Store) listFromSessionIndex(index persistedSessionIndex, req session.ListSessionsRequest) session.SessionList {
	summaries := make([]session.SessionSummary, 0, len(index.Sessions))
	appName := strings.TrimSpace(req.AppName)
	userID := strings.TrimSpace(req.UserID)
	workspaceKey := strings.TrimSpace(req.WorkspaceKey)
	for _, entry := range index.Sessions {
		summary := session.CloneSessionSummaries([]session.SessionSummary{entry.Session})[0]
		if appName != "" && summary.AppName != appName {
			continue
		}
		if userID != "" && summary.UserID != userID {
			continue
		}
		if workspaceKey != "" && summary.WorkspaceKey != workspaceKey {
			continue
		}
		if path := s.indexEntryPath(entry); path != "" {
			s.pathCache[pathCacheKey(summary.SessionID, summary.WorkspaceKey)] = path
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	if req.Limit > 0 && len(summaries) > req.Limit {
		summaries = summaries[:req.Limit]
	}
	return session.SessionList{Sessions: session.CloneSessionSummaries(summaries)}
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

	doc, err := s.readDocumentForRef(req.SessionRef)
	if err != nil {
		return nil, err
	}
	events, err := s.eventsForDocument(doc)
	if err != nil {
		return nil, err
	}
	return session.FilterEvents(events, req.Limit, req.IncludeTransient), nil
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

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return nil, err
	}
	return cloneState(doc.State), nil
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

	doc, err := s.readDocumentForRef(req.SessionRef)
	if err != nil {
		return session.LoadedSession{}, err
	}
	events, err := s.eventsForDocument(doc)
	if err != nil {
		return session.LoadedSession{}, err
	}
	return session.LoadedSession{
		Session: session.CloneSession(doc.Session),
		Events:  session.FilterEvents(events, req.Limit, req.IncludeTransient),
		State:   cloneState(doc.State),
	}, nil
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
	return s.store.LoadDocument(ctx, req)
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
	return s.store.AppendEvent(ctx, req.SessionRef, req.Event)
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

func (s *Service) RemoveParticipant(
	ctx context.Context,
	req session.RemoveParticipantRequest,
) (session.Session, error) {
	return s.store.RemoveParticipant(ctx, req.SessionRef, req.ParticipantID)
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

func (s *Store) readDocumentForRef(ref session.SessionRef) (persistedDocument, error) {
	normalized := session.NormalizeSessionRef(ref)
	if normalized.SessionID == "" {
		return persistedDocument{}, session.ErrInvalidSession
	}
	doc, err := s.readDocument(normalized.SessionID, normalized.WorkspaceKey)
	if err != nil {
		return persistedDocument{}, err
	}
	if !matchesRef(doc.Session, normalized) {
		return persistedDocument{}, session.ErrSessionNotFound
	}
	return doc, nil
}

func (s *Store) readDocument(sessionID string, workspaceKey ...string) (persistedDocument, error) {
	path, err := s.resolveDocumentPath(sessionID, firstNonEmpty(workspaceKey...))
	if err != nil {
		return persistedDocument{}, err
	}
	return s.readDocumentAt(path)
}

func (s *Store) readDocumentAt(path string) (persistedDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return persistedDocument{}, session.ErrSessionNotFound
		}
		return persistedDocument{}, err
	}
	var doc persistedDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return persistedDocument{}, fmt.Errorf("impl/session/file: decode %s: %w", path, err)
	}
	if doc.Kind != documentKind || doc.Version != documentVersion {
		return persistedDocument{}, fmt.Errorf(
			"impl/session/file: unsupported document %q version %d",
			doc.Kind,
			doc.Version,
		)
	}
	doc.Session = session.CloneSession(doc.Session)
	doc.Events = session.CloneEvents(doc.Events)
	doc.State = cloneState(doc.State)
	return doc, nil
}

func (s *Store) writeDocument(doc persistedDocument) error {
	doc.Kind = documentKind
	doc.Version = documentVersion
	doc.Session = session.CloneSession(doc.Session)
	if err := s.migrateDocumentEventsToLog(&doc); err != nil {
		return err
	}
	doc.Events = nil
	doc.State = cloneState(doc.State)

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("impl/session/file: encode session %q: %w", doc.Session.SessionID, err)
	}
	data = append(data, '\n')

	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	s.pathCache[pathCacheKey(doc.Session.SessionID, doc.Session.WorkspaceKey)] = path
	if err := s.upsertSessionIndex(doc.Session, path); err != nil {
		return err
	}
	return nil
}

func (s *Store) sessionIndexPath() string {
	return filepath.Join(s.rootDir, indexFilename)
}

func (s *Store) readSessionIndex() (persistedSessionIndex, error) {
	data, err := os.ReadFile(s.sessionIndexPath())
	if err != nil {
		return persistedSessionIndex{}, err
	}
	var index persistedSessionIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return persistedSessionIndex{}, fmt.Errorf("impl/session/file: decode session index: %w", err)
	}
	if index.Kind != indexKind || index.Version != indexVersion {
		return persistedSessionIndex{}, fmt.Errorf(
			"impl/session/file: unsupported session index %q version %d",
			index.Kind,
			index.Version,
		)
	}
	index.Sessions = cloneSessionIndexEntries(index.Sessions)
	return index, nil
}

func (s *Store) writeSessionIndex(index persistedSessionIndex) error {
	index.Kind = indexKind
	index.Version = indexVersion
	index.Sessions = cloneSessionIndexEntries(index.Sessions)
	sort.Slice(index.Sessions, func(i, j int) bool {
		return index.Sessions[i].Session.UpdatedAt.After(index.Sessions[j].Session.UpdatedAt)
	})
	data, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("impl/session/file: encode session index: %w", err)
	}
	data = append(data, '\n')
	path := s.sessionIndexPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (s *Store) upsertSessionIndex(sess session.Session, documentPath string) error {
	index, err := s.readSessionIndex()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			index = persistedSessionIndex{}
		} else {
			index, err = s.rebuildSessionIndexFromDocuments()
			if err != nil {
				return err
			}
		}
	}
	entry := s.sessionIndexEntry(sess, documentPath)
	key := pathCacheKey(sess.SessionID, sess.WorkspaceKey)
	replaced := false
	for i := range index.Sessions {
		if pathCacheKey(index.Sessions[i].Session.SessionID, index.Sessions[i].Session.WorkspaceKey) == key {
			index.Sessions[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		index.Sessions = append(index.Sessions, entry)
	}
	return s.writeSessionIndex(index)
}

func (s *Store) sessionIndexEntry(sess session.Session, documentPath string) persistedSessionIndexEntry {
	relPath := documentPath
	if rel, err := filepath.Rel(s.rootDir, documentPath); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		relPath = rel
	}
	return persistedSessionIndexEntry{
		Session: session.SessionSummary{
			SessionRef: sess.SessionRef,
			CWD:        sess.CWD,
			Title:      sess.Title,
			UpdatedAt:  sess.UpdatedAt,
		},
		Path: relPath,
	}
}

func (s *Store) indexEntryPath(entry persistedSessionIndexEntry) string {
	path := strings.TrimSpace(entry.Path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(s.rootDir, path)
}

func cloneSessionIndexEntries(entries []persistedSessionIndexEntry) []persistedSessionIndexEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]persistedSessionIndexEntry, len(entries))
	for i, entry := range entries {
		out[i] = persistedSessionIndexEntry{
			Session: session.CloneSessionSummaries([]session.SessionSummary{entry.Session})[0],
			Path:    strings.TrimSpace(entry.Path),
		}
	}
	return out
}

func (s *Store) eventsForDocument(doc persistedDocument) ([]*session.Event, error) {
	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return nil, err
	}
	events := persistedEvents(doc.Events)
	logEvents, err := s.readEventLog(path)
	if err != nil {
		return nil, err
	}
	events = append(events, logEvents...)
	return session.CloneEvents(events), nil
}

func (s *Store) migrateDocumentEventsToLog(doc *persistedDocument) error {
	if doc == nil || len(doc.Events) == 0 {
		return nil
	}
	events := persistedEvents(doc.Events)
	doc.Events = nil
	if len(events) == 0 {
		return nil
	}
	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return err
	}
	existing, err := s.readEventLogIDs(path)
	if err != nil {
		return err
	}
	missing := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		id := strings.TrimSpace(event.ID)
		if id != "" && existing[id] {
			continue
		}
		missing = append(missing, event)
	}
	if len(missing) == 0 {
		return nil
	}
	return s.appendEventLog(path, missing)
}

func (s *Store) appendEventLog(documentPath string, events []*session.Event) error {
	events = persistedEvents(events)
	if len(events) == 0 {
		return nil
	}
	path := eventLogPath(documentPath)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if err := truncatePartialEventLogTail(path); err != nil {
		return err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, event := range events {
		if err := encoder.Encode(session.CloneEvent(event)); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		file.Close()
		return err
	}
	written, err := file.Write(buf.Bytes())
	if err != nil || written != buf.Len() {
		if err == nil {
			err = io.ErrShortWrite
		}
		_ = file.Truncate(offset)
		_ = file.Sync()
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Truncate(offset)
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return syncDir(dir)
}

func (s *Store) readEventLog(documentPath string) ([]*session.Event, error) {
	path := eventLogPath(documentPath)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	events := make([]*session.Event, 0)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			var event session.Event
			if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return nil, fmt.Errorf("impl/session/file: decode event log %s: %w", path, err)
			}
			events = append(events, session.CloneEvent(&event))
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return events, nil
}

func truncatePartialEventLogTail(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], size-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	const chunkSize = 4096
	buf := make([]byte, chunkSize)
	offset := size
	for offset > 0 {
		n := int64(len(buf))
		if offset < n {
			n = offset
		}
		offset -= n
		chunk := buf[:n]
		if _, err := file.ReadAt(chunk, offset); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] == '\n' {
				if err := file.Truncate(offset + int64(i) + 1); err != nil {
					return err
				}
				return file.Sync()
			}
		}
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	return file.Sync()
}

func (s *Store) readEventLogIDs(documentPath string) (map[string]bool, error) {
	events, err := s.readEventLog(documentPath)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if id := strings.TrimSpace(event.ID); id != "" {
			ids[id] = true
		}
	}
	return ids, nil
}

func eventLogPath(documentPath string) string {
	return strings.TrimSuffix(documentPath, ".json") + ".events.jsonl"
}

func (s *Store) resolveWritePath(sess session.Session) (string, error) {
	key := pathCacheKey(sess.SessionID, sess.WorkspaceKey)
	if path, ok := s.pathCache[key]; ok && strings.TrimSpace(path) != "" {
		return path, nil
	}
	if path, err := s.findDocumentPath(sess.SessionID, sess.WorkspaceKey); err == nil {
		s.pathCache[key] = path
		return path, nil
	} else if !errors.Is(err, session.ErrSessionNotFound) {
		return "", err
	}
	return s.newDocumentPath(sess), nil
}

func (s *Store) resolveDocumentPath(sessionID string, workspaceKey string) (string, error) {
	if strings.TrimSpace(workspaceKey) == "" {
		return s.findDocumentPath(sessionID, workspaceKey)
	}
	key := pathCacheKey(sessionID, workspaceKey)
	if path, ok := s.pathCache[key]; ok && strings.TrimSpace(path) != "" {
		return path, nil
	}
	path, err := s.findDocumentPath(sessionID, workspaceKey)
	if err != nil {
		return "", err
	}
	s.pathCache[key] = path
	return path, nil
}

func (s *Store) findDocumentPath(sessionID string, workspaceKey string) (string, error) {
	searchRoot := s.rootDir
	requireUnique := true
	if key := strings.TrimSpace(workspaceKey); key != "" {
		searchRoot = filepath.Join(searchRoot, workspaceDirName(key))
		requireUnique = false
	}
	found := make([]string, 0, 1)
	walkErr := filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() == indexFilename || filepath.Ext(d.Name()) != ".json" {
			return nil
		}
		if strings.HasSuffix(d.Name(), "-"+sanitizeSessionID(sessionID)+".json") {
			found = append(found, path)
			if !requireUnique {
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		if os.IsNotExist(walkErr) {
			return "", session.ErrSessionNotFound
		}
		return "", walkErr
	}
	switch len(found) {
	case 0:
		return "", session.ErrSessionNotFound
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf(
			"impl/session/file: session %q matches multiple workspaces; workspace key is required: %w",
			strings.TrimSpace(sessionID),
			session.ErrAmbiguousSession,
		)
	}
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Store) listDocumentPaths() ([]string, error) {
	paths := make([]string, 0)
	err := filepath.WalkDir(s.rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() == indexFilename || filepath.Ext(d.Name()) != ".json" {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *Store) newDocumentPath(session session.Session) string {
	at := session.CreatedAt
	if at.IsZero() {
		at = s.now()
	}
	dayDir := filepath.Join(
		s.rootDir,
		workspaceDirName(session.WorkspaceKey),
		at.UTC().Format("2006"),
		at.UTC().Format("01"),
		at.UTC().Format("02"),
	)
	name := fmt.Sprintf(
		"rollout-%s-%s.json",
		at.UTC().Format("2006-01-02T15-04-05"),
		sanitizeSessionID(session.SessionID),
	)
	return filepath.Join(dayDir, name)
}

func (s *Store) nextID(prefix string, custom func() string) string {
	if custom != nil {
		if id := strings.TrimSpace(custom()); id != "" {
			return id
		}
	}
	if strings.TrimSpace(prefix) == "session" {
		return nextSessionID()
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

func (s *Store) now() time.Time {
	return s.clock()
}

func matchesRef(sess session.Session, ref session.SessionRef) bool {
	ref = session.NormalizeSessionRef(ref)
	if ref.SessionID != "" && sess.SessionID != ref.SessionID {
		return false
	}
	if ref.AppName != "" && sess.AppName != ref.AppName {
		return false
	}
	if ref.UserID != "" && sess.UserID != ref.UserID {
		return false
	}
	if ref.WorkspaceKey != "" && sess.WorkspaceKey != ref.WorkspaceKey {
		return false
	}
	return true
}

func sanitizeSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "session"
	}
	var b strings.Builder
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

func workspaceDirName(workspaceKey string) string {
	workspaceKey = strings.TrimSpace(workspaceKey)
	if workspaceKey == "" {
		return "workspace"
	}
	return sanitizeSessionID(workspaceKey)
}

func pathCacheKey(sessionID string, workspaceKey string) string {
	return workspaceDirName(workspaceKey) + ":" + sanitizeSessionID(sessionID)
}

func nextSessionID() string {
	var raw [7]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("s-%d", time.Now().UTC().UnixNano())
	}
	token := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))
	if len(token) > 12 {
		token = token[:12]
	}
	return "s-" + token
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func shouldPersistEvent(event *session.Event) bool {
	return event != nil && !session.IsTransient(event)
}

func persistedEvents(events []*session.Event) []*session.Event {
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if !shouldPersistEvent(event) {
			continue
		}
		out = append(out, session.CloneEvent(event))
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	return cloneState(in)
}

func cloneState(in map[string]any) map[string]any {
	out := session.CloneState(in)
	if out == nil {
		return map[string]any{}
	}
	return out
}

func truncateTitle(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 80 {
		return text[:80]
	}
	return text
}
