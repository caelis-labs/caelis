package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/session"
)

// Config holds configuration for the file-backed session service.
type Config struct {
	// RootDir is the base directory for session storage.
	RootDir string
}

// Service implements session.Service backed by the filesystem.
//
// Storage layout:
//
//	<root>/
//	  <app>/<user>/<workspace>/
//	    <session_id>.json        — session metadata
//	    <session_id>.events.jsonl — event log (one JSON per line)
type Service struct {
	mu      sync.RWMutex
	rootDir string
	counter int64
}

// New creates a new file-backed session service.
func New(cfg Config) (*Service, error) {
	if cfg.RootDir == "" {
		return nil, fmt.Errorf("file: RootDir is required")
	}
	if err := os.MkdirAll(cfg.RootDir, 0o755); err != nil {
		return nil, fmt.Errorf("file: create root: %w", err)
	}
	return &Service{rootDir: cfg.RootDir}, nil
}

func (s *Service) nextID() string {
	s.counter++
	return fmt.Sprintf("evt-%d-%d", time.Now().UnixNano(), s.counter)
}

func (s *Service) sessionDir(ref session.Ref) string {
	return filepath.Join(s.rootDir, ref.AppName, ref.UserID, ref.WorkspaceKey)
}

func (s *Service) sessionPath(ref session.Ref) string {
	return filepath.Join(s.sessionDir(ref), ref.SessionID+".json")
}

func (s *Service) eventsPath(ref session.Ref) string {
	return filepath.Join(s.sessionDir(ref), ref.SessionID+".events.jsonl")
}

func (s *Service) statePath(ref session.Ref) string {
	return filepath.Join(s.sessionDir(ref), ref.SessionID+".state.json")
}

func (s *Service) Create(_ context.Context, req session.CreateRequest) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess := session.Session{
		Ref: session.Ref{
			AppName:      req.AppName,
			UserID:       req.UserID,
			WorkspaceKey: req.WorkspaceKey,
			SessionID:    fmt.Sprintf("sess-%d", time.Now().UnixNano()),
		},
		Workspace:    req.Workspace.Clone(),
		Title:        req.Title,
		State:        req.State.Clone(),
		Controller:   req.Controller,
		Participants: make([]session.ParticipantBinding, len(req.Participants)),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	for i, p := range req.Participants {
		sess.Participants[i] = p.Clone()
	}

	dir := s.sessionDir(sess.Ref)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return session.Session{}, fmt.Errorf("file: mkdir: %w", err)
	}

	if err := s.writeSession(sess); err != nil {
		return session.Session{}, err
	}
	// Create empty events file.
	if err := os.WriteFile(s.eventsPath(sess.Ref), nil, 0o644); err != nil {
		return session.Session{}, fmt.Errorf("file: create events: %w", err)
	}

	return sess, nil
}

func (s *Service) Get(_ context.Context, ref session.Ref) (session.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readSession(ref)
}

func (s *Service) List(_ context.Context, req session.ListRequest) (session.ListResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sessions []session.Session
	baseDir := filepath.Join(s.rootDir, req.AppName)
	if req.UserID != "" {
		baseDir = filepath.Join(baseDir, req.UserID)
	}

	// Walk the directory tree to find session files.
	err := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".json") || strings.HasSuffix(info.Name(), ".events.jsonl") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var sess session.Session
		if err := json.Unmarshal(data, &sess); err != nil {
			return nil
		}
		if req.WorkspaceKey != "" && sess.Ref.WorkspaceKey != req.WorkspaceKey {
			return nil
		}
		sessions = append(sessions, sess)
		return nil
	})
	if err != nil {
		return session.ListResponse{}, fmt.Errorf("file: walk: %w", err)
	}

	if req.Limit > 0 && len(sessions) > req.Limit {
		sessions = sessions[:req.Limit]
	}
	return session.ListResponse{Sessions: sessions}, nil
}

func (s *Service) Fork(_ context.Context, req session.ForkRequest) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	orig, err := s.readSession(req.Source)
	if err != nil {
		return session.Session{}, err
	}

	forked := orig.Clone()
	forked.Ref.SessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
	if req.Title != "" {
		forked.Title = req.Title
	}
	forked.CreatedAt = time.Now()
	forked.UpdatedAt = time.Now()

	dir := s.sessionDir(forked.Ref)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return session.Session{}, fmt.Errorf("file: mkdir: %w", err)
	}
	if err := s.writeSession(forked); err != nil {
		return session.Session{}, err
	}

	// Copy events from source.
	srcEvts, err := s.readEvents(req.Source, session.EventsRequest{})
	if err != nil {
		return session.Session{}, err
	}
	for _, e := range srcEvts {
		e.SessionRef = forked.Ref
		e.ID = s.nextID()
		if err := s.appendEventFile(forked.Ref, e); err != nil {
			return session.Session{}, err
		}
	}
	state, err := s.readStructuredState(req.Source)
	if err != nil {
		return session.Session{}, err
	}
	if len(state) > 0 {
		if err := s.writeStructuredState(forked.Ref, state); err != nil {
			return session.Session{}, err
		}
	}

	return forked, nil
}

func (s *Service) Delete(_ context.Context, ref session.Ref) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sPath := s.sessionPath(ref)
	if _, err := os.Stat(sPath); os.IsNotExist(err) {
		return fmt.Errorf("session not found: %s", ref)
	}
	if err := os.Remove(sPath); err != nil {
		return fmt.Errorf("file: delete session: %w", err)
	}
	// Best-effort remove auxiliary files after the canonical session file
	// has been deleted.
	os.Remove(s.eventsPath(ref))
	os.Remove(s.statePath(ref))
	return nil
}

func (s *Service) AppendEvent(_ context.Context, ref session.Ref, evt session.Event) (session.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify session exists.
	if _, err := os.Stat(s.sessionPath(ref)); os.IsNotExist(err) {
		return session.Event{}, fmt.Errorf("session not found: %s", ref)
	}

	persisted := evt.Clone()
	persisted.ID = s.nextID()
	persisted.SessionRef = ref

	// Canonicalize and validate before persisting.
	session.CanonicalizeEvent(&persisted)
	if err := session.ValidateEvent(&persisted); err != nil {
		return session.Event{}, fmt.Errorf("event validation: %w", err)
	}

	// Reject transient events — they should not be persisted.
	if persisted.Visibility.IsTransient() {
		return session.Event{}, fmt.Errorf("transient events (overlay/ui_only) cannot be persisted")
	}

	if persisted.CreatedAt.IsZero() {
		persisted.CreatedAt = time.Now()
	}

	if err := s.appendEventFile(ref, persisted); err != nil {
		return session.Event{}, err
	}

	// Update session timestamp.
	sess, err := s.readSession(ref)
	if err == nil {
		sess.UpdatedAt = persisted.CreatedAt
		s.writeSession(sess)
	}

	return persisted, nil
}

func (s *Service) Events(_ context.Context, req session.EventsRequest) ([]session.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readEvents(req.SessionRef, req)
}

func (s *Service) UpdateState(_ context.Context, ref session.Ref, fn func(session.State) (session.State, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.readSession(ref)
	if err != nil {
		return err
	}
	newState, err := fn(sess.State.Clone())
	if err != nil {
		return err
	}
	sess.State = newState
	sess.UpdatedAt = time.Now()
	return s.writeSession(sess)
}

// ─── Internal helpers ────────────────────────────────────────────────

func (s *Service) writeSession(sess session.Session) error {
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("file: marshal session: %w", err)
	}
	if err := os.WriteFile(s.sessionPath(sess.Ref), data, 0o644); err != nil {
		return fmt.Errorf("file: write session: %w", err)
	}
	return nil
}

func (s *Service) readSession(ref session.Ref) (session.Session, error) {
	data, err := os.ReadFile(s.sessionPath(ref))
	if err != nil {
		if os.IsNotExist(err) {
			return session.Session{}, fmt.Errorf("session not found: %s", ref)
		}
		return session.Session{}, fmt.Errorf("file: read session: %w", err)
	}
	var sess session.Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return session.Session{}, fmt.Errorf("file: unmarshal session: %w", err)
	}
	return sess, nil
}

func (s *Service) appendEventFile(ref session.Ref, evt session.Event) error {
	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("file: marshal event: %w", err)
	}
	data = append(data, '\n')
	f, err := os.OpenFile(s.eventsPath(ref), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("file: open events: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("file: write event: %w", err)
	}
	return nil
}

func (s *Service) readEvents(ref session.Ref, req session.EventsRequest) ([]session.Event, error) {
	data, err := os.ReadFile(s.eventsPath(ref))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session not found: %s", ref)
		}
		return nil, fmt.Errorf("file: read events: %w", err)
	}

	var events []session.Event
	found := req.AfterID == ""
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var evt session.Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			return nil, fmt.Errorf("file: corrupt JSONL line: %w", err)
		}
		if !found {
			if evt.ID == req.AfterID {
				found = true
			}
			continue
		}
		if len(req.Kinds) > 0 {
			match := false
			for _, k := range req.Kinds {
				if evt.Kind == k {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		events = append(events, evt)
		if req.Limit > 0 && len(events) >= req.Limit {
			break
		}
	}
	return events, nil
}

func (s *Service) readStructuredState(ref session.Ref) (map[string]any, error) {
	data, err := os.ReadFile(s.statePath(ref))
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]any), nil
		}
		return nil, fmt.Errorf("file: read structured state: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return make(map[string]any), nil
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("file: unmarshal structured state: %w", err)
	}
	if state == nil {
		state = make(map[string]any)
	}
	return state, nil
}

func (s *Service) writeStructuredState(ref session.Ref, state map[string]any) error {
	if state == nil {
		state = make(map[string]any)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("file: marshal structured state: %w", err)
	}
	if err := os.WriteFile(s.statePath(ref), data, 0o644); err != nil {
		return fmt.Errorf("file: write structured state: %w", err)
	}
	return nil
}

func cloneStructuredState(state map[string]any) (map[string]any, error) {
	if state == nil {
		return make(map[string]any), nil
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("file: marshal structured state clone: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("file: unmarshal structured state clone: %w", err)
	}
	if out == nil {
		out = make(map[string]any)
	}
	return out, nil
}

// Compile-time interface check.
var _ session.Service = (*Service)(nil)

// ─── Optional interface implementations ──────────────────────────────

// BindController updates the controller binding for a session.
func (s *Service) BindController(_ context.Context, ref session.Ref, binding session.ControllerBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.readSession(ref)
	if err != nil {
		return err
	}
	sess.Controller = binding
	sess.UpdatedAt = time.Now()
	return s.writeSession(sess)
}

// PutParticipant adds or updates a participant in a session.
func (s *Service) PutParticipant(_ context.Context, ref session.Ref, p session.ParticipantBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.readSession(ref)
	if err != nil {
		return err
	}
	for i, existing := range sess.Participants {
		if existing.ID == p.ID {
			sess.Participants[i] = p
			sess.UpdatedAt = time.Now()
			return s.writeSession(sess)
		}
	}
	sess.Participants = append(sess.Participants, p)
	sess.UpdatedAt = time.Now()
	return s.writeSession(sess)
}

// RemoveParticipant removes a participant from a session.
func (s *Service) RemoveParticipant(_ context.Context, ref session.Ref, participantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.readSession(ref)
	if err != nil {
		return err
	}
	for i, p := range sess.Participants {
		if p.ID == participantID {
			sess.Participants = append(sess.Participants[:i], sess.Participants[i+1:]...)
			sess.UpdatedAt = time.Now()
			return s.writeSession(sess)
		}
	}
	return fmt.Errorf("participant not found: %s", participantID)
}

// SnapshotState returns a deep copy of the structured state.
func (s *Service) SnapshotState(_ context.Context, ref session.Ref) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.readSession(ref); err != nil {
		return nil, err
	}
	state, err := s.readStructuredState(ref)
	if err != nil {
		return nil, err
	}
	return cloneStructuredState(state)
}

// ReplaceState replaces the entire structured state.
func (s *Service) ReplaceState(_ context.Context, ref session.Ref, state map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readSession(ref); err != nil {
		return err
	}
	cloned, err := cloneStructuredState(state)
	if err != nil {
		return err
	}
	return s.writeStructuredState(ref, cloned)
}

// Compile-time optional interface checks.
var (
	_ session.ControllerManager  = (*Service)(nil)
	_ session.ParticipantManager = (*Service)(nil)
	_ session.StructuredState    = (*Service)(nil)
)
