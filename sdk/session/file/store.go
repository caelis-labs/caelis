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

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
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
	Kind    string              `json:"kind"`
	Version int                 `json:"version"`
	Session sdksession.Session  `json:"session"`
	Events  []*sdksession.Event `json:"events,omitempty"`
	State   map[string]any      `json:"state"`
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

	doc, err := s.readDocument(ref.SessionID, ref.WorkspaceKey)
	switch {
	case err == nil:
		if !matchesRef(doc.Session, ref) {
			return sdksession.Session{}, sdksession.ErrSessionNotFound
		}
		return sdksession.CloneSession(doc.Session), nil
	case !errors.Is(err, sdksession.ErrSessionNotFound):
		return sdksession.Session{}, err
	}

	now := s.now()
	session := sdksession.Session{
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
		Session: session,
		State:   map[string]any{},
	}
	if err := s.writeDocument(doc); err != nil {
		return sdksession.Session{}, err
	}
	return sdksession.CloneSession(session), nil
}

func (s *Store) Get(
	_ context.Context,
	ref sdksession.SessionRef,
) (sdksession.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return sdksession.Session{}, err
	}
	return sdksession.CloneSession(doc.Session), nil
}

func (s *Store) List(
	_ context.Context,
	req sdksession.ListSessionsRequest,
) (sdksession.SessionList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	paths, err := s.listDocumentPaths()
	if err != nil {
		return sdksession.SessionList{}, err
	}

	summaries := make([]sdksession.SessionSummary, 0, len(paths))
	for _, path := range paths {
		doc, err := s.readDocumentAt(path)
		if err != nil {
			return sdksession.SessionList{}, err
		}
		s.pathCache[pathCacheKey(doc.Session.SessionID, doc.Session.WorkspaceKey)] = path
		session := doc.Session
		if req.AppName != "" && session.AppName != strings.TrimSpace(req.AppName) {
			continue
		}
		if req.UserID != "" && session.UserID != strings.TrimSpace(req.UserID) {
			continue
		}
		if req.WorkspaceKey != "" && session.WorkspaceKey != strings.TrimSpace(req.WorkspaceKey) {
			continue
		}
		summaries = append(summaries, sdksession.SessionSummary{
			SessionRef: session.SessionRef,
			CWD:        session.CWD,
			Title:      session.Title,
			UpdatedAt:  session.UpdatedAt,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	if req.Limit > 0 && len(summaries) > req.Limit {
		summaries = summaries[:req.Limit]
	}
	return sdksession.SessionList{Sessions: sdksession.CloneSessionSummaries(summaries)}, nil
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

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return nil, err
	}

	normalized := sdksession.CloneEvent(event)
	if normalized.ID == "" {
		normalized.ID = s.nextID("event", s.eventIDGenerator)
	}
	normalized.SessionID = doc.Session.SessionID
	if normalized.Time.IsZero() {
		normalized.Time = s.now()
	}
	if normalized.Type == "" {
		normalized.Type = sdksession.EventTypeOf(normalized)
	}
	if normalized.Visibility == "" {
		normalized.Visibility = sdksession.VisibilityCanonical
	}
	if !shouldPersistEvent(normalized) {
		return sdksession.CloneEvent(normalized), nil
	}

	doc.Events = append(doc.Events, normalized)
	doc.Session.UpdatedAt = normalized.Time
	if doc.Session.Title == "" && normalized.Text != "" {
		doc.Session.Title = truncateTitle(normalized.Text)
	}
	if err := s.writeDocument(doc); err != nil {
		return nil, err
	}
	return sdksession.CloneEvent(normalized), nil
}

func (s *Store) Events(
	_ context.Context,
	req sdksession.EventsRequest,
) ([]*sdksession.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readDocumentForRef(req.SessionRef)
	if err != nil {
		return nil, err
	}
	return sdksession.FilterEvents(doc.Events, req.Limit, req.IncludeTransient), nil
}

func (s *Store) BindController(
	_ context.Context,
	ref sdksession.SessionRef,
	binding sdksession.ControllerBinding,
) (sdksession.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return sdksession.Session{}, err
	}
	doc.Session.Controller = sdksession.CloneControllerBinding(binding)
	doc.Session.UpdatedAt = s.now()
	if err := s.writeDocument(doc); err != nil {
		return sdksession.Session{}, err
	}
	return sdksession.CloneSession(doc.Session), nil
}

func (s *Store) PutParticipant(
	_ context.Context,
	ref sdksession.SessionRef,
	binding sdksession.ParticipantBinding,
) (sdksession.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return sdksession.Session{}, err
	}
	normalized := sdksession.CloneParticipantBinding(binding)
	for i := range doc.Session.Participants {
		if doc.Session.Participants[i].ID == normalized.ID && normalized.ID != "" {
			doc.Session.Participants[i] = normalized
			doc.Session.UpdatedAt = s.now()
			if err := s.writeDocument(doc); err != nil {
				return sdksession.Session{}, err
			}
			return sdksession.CloneSession(doc.Session), nil
		}
	}
	doc.Session.Participants = append(doc.Session.Participants, normalized)
	doc.Session.UpdatedAt = s.now()
	if err := s.writeDocument(doc); err != nil {
		return sdksession.Session{}, err
	}
	return sdksession.CloneSession(doc.Session), nil
}

func (s *Store) RemoveParticipant(
	_ context.Context,
	ref sdksession.SessionRef,
	participantID string,
) (sdksession.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readDocumentForRef(ref)
	if err != nil {
		return sdksession.Session{}, err
	}
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return sdksession.CloneSession(doc.Session), nil
	}
	filtered := doc.Session.Participants[:0]
	for _, item := range doc.Session.Participants {
		if strings.TrimSpace(item.ID) == participantID {
			continue
		}
		filtered = append(filtered, item)
	}
	doc.Session.Participants = append([]sdksession.ParticipantBinding(nil), filtered...)
	doc.Session.UpdatedAt = s.now()
	if err := s.writeDocument(doc); err != nil {
		return sdksession.Session{}, err
	}
	return sdksession.CloneSession(doc.Session), nil
}

func (s *Store) SnapshotState(
	_ context.Context,
	ref sdksession.SessionRef,
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
	ref sdksession.SessionRef,
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
	ref sdksession.SessionRef,
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

func (s *Store) readDocumentForRef(ref sdksession.SessionRef) (persistedDocument, error) {
	normalized := sdksession.NormalizeSessionRef(ref)
	if normalized.SessionID == "" {
		return persistedDocument{}, sdksession.ErrInvalidSession
	}
	doc, err := s.readDocument(normalized.SessionID, normalized.WorkspaceKey)
	if err != nil {
		return persistedDocument{}, err
	}
	if !matchesRef(doc.Session, normalized) {
		return persistedDocument{}, sdksession.ErrSessionNotFound
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
			return persistedDocument{}, sdksession.ErrSessionNotFound
		}
		return persistedDocument{}, err
	}
	var doc persistedDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return persistedDocument{}, fmt.Errorf("sdk/session/file: decode %s: %w", path, err)
	}
	if doc.Kind != documentKind || doc.Version != documentVersion {
		return persistedDocument{}, fmt.Errorf(
			"sdk/session/file: unsupported document %q version %d",
			doc.Kind,
			doc.Version,
		)
	}
	doc.Session = sdksession.CloneSession(doc.Session)
	doc.Events = sdksession.CloneEvents(doc.Events)
	doc.State = cloneState(doc.State)
	return doc, nil
}

func (s *Store) writeDocument(doc persistedDocument) error {
	doc.Kind = documentKind
	doc.Version = documentVersion
	doc.Session = sdksession.CloneSession(doc.Session)
	doc.Events = persistedEvents(doc.Events)
	doc.State = cloneState(doc.State)

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("sdk/session/file: encode session %q: %w", doc.Session.SessionID, err)
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
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	s.pathCache[pathCacheKey(doc.Session.SessionID, doc.Session.WorkspaceKey)] = path
	return nil
}

func (s *Store) resolveWritePath(session sdksession.Session) (string, error) {
	key := pathCacheKey(session.SessionID, session.WorkspaceKey)
	if path, ok := s.pathCache[key]; ok && strings.TrimSpace(path) != "" {
		return path, nil
	}
	if path, err := s.findDocumentPath(session.SessionID, session.WorkspaceKey); err == nil {
		s.pathCache[key] = path
		return path, nil
	} else if !errors.Is(err, sdksession.ErrSessionNotFound) {
		return "", err
	}
	return s.newDocumentPath(session), nil
}

func (s *Store) resolveDocumentPath(sessionID string, workspaceKey string) (string, error) {
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
	if key := strings.TrimSpace(workspaceKey); key != "" {
		searchRoot = filepath.Join(searchRoot, workspaceDirName(key))
	}
	var found string
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
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		if os.IsNotExist(walkErr) {
			return "", sdksession.ErrSessionNotFound
		}
		return "", walkErr
	}
	if strings.TrimSpace(found) == "" {
		return "", sdksession.ErrSessionNotFound
	}
	return found, nil
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

func (s *Store) newDocumentPath(session sdksession.Session) string {
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

func matchesRef(session sdksession.Session, ref sdksession.SessionRef) bool {
	ref = sdksession.NormalizeSessionRef(ref)
	if ref.SessionID != "" && session.SessionID != ref.SessionID {
		return false
	}
	if ref.AppName != "" && session.AppName != ref.AppName {
		return false
	}
	if ref.UserID != "" && session.UserID != ref.UserID {
		return false
	}
	if ref.WorkspaceKey != "" && session.WorkspaceKey != ref.WorkspaceKey {
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
	workspaceKey = strings.TrimSpace(workspaceKey)
	if workspaceKey == "" {
		return "*:" + sanitizeSessionID(sessionID)
	}
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

func shouldPersistEvent(event *sdksession.Event) bool {
	return event != nil && !sdksession.IsTransient(event)
}

func persistedEvents(events []*sdksession.Event) []*sdksession.Event {
	out := make([]*sdksession.Event, 0, len(events))
	for _, event := range events {
		if !shouldPersistEvent(event) {
			continue
		}
		out = append(out, sdksession.CloneEvent(event))
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	return cloneState(in)
}

func cloneState(in map[string]any) map[string]any {
	out := sdksession.CloneState(in)
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
