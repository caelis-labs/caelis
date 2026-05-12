package file

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
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
)

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

	doc, err := s.readDocument(ref.SessionID, ref.WorkspaceKey)
	switch {
	case err == nil:
		if !matchesRef(doc.Session, ref) {
			return session.Session{}, session.ErrSessionNotFound
		}
		return session.CloneSession(doc.Session), nil
	case !errors.Is(err, session.ErrSessionNotFound):
		return session.Session{}, err
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
		return session.Session{}, err
	}
	return session.CloneSession(createdSession), nil
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

	paths, err := s.listDocumentPaths()
	if err != nil {
		return session.SessionList{}, err
	}

	summaries := make([]session.SessionSummary, 0, len(paths))
	for _, path := range paths {
		doc, err := s.readDocumentAt(path)
		if err != nil {
			return session.SessionList{}, err
		}
		s.pathCache[pathCacheKey(doc.Session.SessionID, doc.Session.WorkspaceKey)] = path
		storedSession := doc.Session
		if req.AppName != "" && storedSession.AppName != strings.TrimSpace(req.AppName) {
			continue
		}
		if req.UserID != "" && storedSession.UserID != strings.TrimSpace(req.UserID) {
			continue
		}
		if req.WorkspaceKey != "" && storedSession.WorkspaceKey != strings.TrimSpace(req.WorkspaceKey) {
			continue
		}
		summaries = append(summaries, session.SessionSummary{
			SessionRef: storedSession.SessionRef,
			CWD:        storedSession.CWD,
			Title:      storedSession.Title,
			UpdatedAt:  storedSession.UpdatedAt,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	if req.Limit > 0 && len(summaries) > req.Limit {
		summaries = summaries[:req.Limit]
	}
	return session.SessionList{Sessions: session.CloneSessionSummaries(summaries)}, nil
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

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return nil, err
	}

	normalized := session.CanonicalizeEvent(event)
	if normalized.ID == "" {
		normalized.ID = s.nextID("event", s.eventIDGenerator)
	}
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
		return session.CloneEvent(normalized), nil
	}

	doc.Events = append(doc.Events, normalized)
	doc.Session.UpdatedAt = normalized.Time
	if doc.Session.Title == "" {
		if text := session.EventText(normalized); text != "" {
			doc.Session.Title = truncateTitle(text)
		}
	}
	if err := s.writeDocument(doc); err != nil {
		return nil, err
	}
	return session.CloneEvent(normalized), nil
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
	return session.FilterEvents(doc.Events, req.Limit, req.IncludeTransient), nil
}

func (s *Store) BindController(
	_ context.Context,
	ref session.SessionRef,
	binding session.ControllerBinding,
) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return session.Session{}, err
	}
	doc.Session.Controller = session.CloneControllerBinding(binding)
	doc.Session.UpdatedAt = s.now()
	if err := s.writeDocument(doc); err != nil {
		return session.Session{}, err
	}
	return session.CloneSession(doc.Session), nil
}

func (s *Store) PutParticipant(
	_ context.Context,
	ref session.SessionRef,
	binding session.ParticipantBinding,
) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return session.Session{}, err
	}
	normalized := session.CloneParticipantBinding(binding)
	for i := range doc.Session.Participants {
		if doc.Session.Participants[i].ID == normalized.ID && normalized.ID != "" {
			doc.Session.Participants[i] = normalized
			doc.Session.UpdatedAt = s.now()
			if err := s.writeDocument(doc); err != nil {
				return session.Session{}, err
			}
			return session.CloneSession(doc.Session), nil
		}
	}
	doc.Session.Participants = append(doc.Session.Participants, normalized)
	doc.Session.UpdatedAt = s.now()
	if err := s.writeDocument(doc); err != nil {
		return session.Session{}, err
	}
	return session.CloneSession(doc.Session), nil
}

func (s *Store) RemoveParticipant(
	_ context.Context,
	ref session.SessionRef,
	participantID string,
) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return session.Session{}, err
	}
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return session.CloneSession(doc.Session), nil
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
		return session.Session{}, err
	}
	return session.CloneSession(doc.Session), nil
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

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return err
	}
	doc.State = cloneState(state)
	doc.Session.UpdatedAt = s.now()
	return s.writeDocument(doc)
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
	return session.LoadedSession{
		Session: session.CloneSession(doc.Session),
		Events:  session.FilterEvents(doc.Events, req.Limit, req.IncludeTransient),
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
	doc.Events = persistedEvents(doc.Events)
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
	return nil
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
		if d.IsDir() || filepath.Ext(d.Name()) != ".json" {
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
		if d.IsDir() || filepath.Ext(d.Name()) != ".json" {
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
