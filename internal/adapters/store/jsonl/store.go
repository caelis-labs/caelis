// Package jsonl provides a core-native JSONL session store.
package jsonl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
)

type Store struct {
	root   string
	mu     sync.Mutex
	nextID atomic.Uint64
}

func New(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("store/jsonl: root directory is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, "sessions"), 0o755); err != nil {
		return nil, err
	}
	return &Store{root: abs}, nil
}

func (s *Store) Create(ctx context.Context, req session.StartRequest) (session.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.Session{}, err
	}
	if s == nil {
		return session.Session{}, session.ErrNotFound
	}
	appName := strings.TrimSpace(req.AppName)
	userID := strings.TrimSpace(req.UserID)
	if appName == "" || userID == "" {
		return session.Session{}, fmt.Errorf("%w: app name and user id are required", session.ErrInvalid)
	}
	id := strings.TrimSpace(req.PreferredSessionID)
	if id == "" {
		id = fmt.Sprintf("sess-%d-%d", time.Now().UTC().UnixNano(), s.nextID.Add(1))
	}
	if err := validateID(id); err != nil {
		return session.Session{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, err := s.loadLocked(session.Ref{SessionID: id}); err == nil {
		return session.CloneSession(existing.Session), nil
	} else if !errors.Is(err, session.ErrNotFound) {
		return session.Session{}, err
	}

	now := time.Now().UTC()
	active := session.Session{
		Ref: session.NormalizeRef(session.Ref{
			AppName:      appName,
			UserID:       userID,
			SessionID:    id,
			WorkspaceKey: req.Workspace.Key,
		}),
		Workspace: req.Workspace,
		Title:     strings.TrimSpace(req.Title),
		Meta:      maps.Clone(req.Meta),
		Controller: session.ControllerBinding{
			Kind:       session.ControllerBuiltin,
			ID:         "builtin",
			AttachedAt: now,
			Source:     "jsonl-store",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := os.MkdirAll(s.sessionDir(id), 0o755); err != nil {
		return session.Session{}, err
	}
	if err := writeJSONFile(s.sessionFile(id), active); err != nil {
		return session.Session{}, err
	}
	if _, err := os.OpenFile(s.eventsFile(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		return session.Session{}, err
	}
	return session.CloneSession(active), nil
}

func (s *Store) List(ctx context.Context, query session.ListQuery) (session.SessionPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.SessionPage{}, err
	}
	if s == nil {
		return session.SessionPage{}, session.ErrNotFound
	}
	query = session.NormalizeListQuery(query)
	after, err := session.ParseOffsetCursor(query.After)
	if err != nil {
		return session.SessionPage{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(filepath.Join(s.root, "sessions"))
	if err != nil {
		return session.SessionPage{}, err
	}
	matches := make([]session.SessionSummary, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return session.SessionPage{}, err
		}
		if !entry.IsDir() {
			continue
		}
		ref := session.Ref{SessionID: entry.Name()}
		snapshot, err := s.loadLocked(ref)
		if errors.Is(err, session.ErrNotFound) {
			continue
		}
		if err != nil {
			return session.SessionPage{}, err
		}
		if !session.SessionMatchesListQuery(snapshot.Session, query) {
			continue
		}
		events, err := s.loadEventsLocked(ref)
		if err != nil {
			return session.SessionPage{}, err
		}
		matches = append(matches, session.SessionSummary{
			Session:     session.CloneSession(snapshot.Session),
			EventCount:  len(events),
			LastEventAt: session.LastEventTime(events),
		})
	}
	session.SortSessionSummaries(matches)
	return session.PageSessionSummaries(matches, after, query.Limit), nil
}

func (s *Store) Load(ctx context.Context, ref session.Ref) (session.Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.Snapshot{}, err
	}
	if s == nil {
		return session.Snapshot{}, session.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(ref)
}

func (s *Store) Append(ctx context.Context, ref session.Ref, events []session.Event) (session.Cursor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil {
		return "", session.ErrNotFound
	}
	id, err := normalizedSessionID(ref)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.loadLocked(ref)
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return snapshot.Cursor, nil
	}
	rawEvents, err := s.loadEventsLocked(ref)
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(s.eventsFile(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	count := len(rawEvents)
	for _, event := range events {
		count++
		next := session.CloneEvent(event)
		if strings.TrimSpace(next.ID) == "" {
			next.ID = fmt.Sprintf("evt-%d", count)
		}
		if strings.TrimSpace(next.SessionID) == "" {
			next.SessionID = id
		}
		if next.Time.IsZero() {
			next.Time = time.Now().UTC()
		}
		if next.Visibility == "" {
			next.Visibility = session.VisibilityCanonical
		}
		if err := encoder.Encode(next); err != nil {
			return "", err
		}
	}
	active := snapshot.Session
	active.UpdatedAt = time.Now().UTC()
	if err := writeJSONFile(s.sessionFile(id), active); err != nil {
		return "", err
	}
	return session.Cursor(strconv.Itoa(count)), nil
}

func (s *Store) Events(ctx context.Context, query session.EventQuery) (session.EventPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.EventPage{}, err
	}
	if s == nil {
		return session.EventPage{}, session.ErrNotFound
	}
	after, err := parseCursor(query.After)
	if err != nil {
		return session.EventPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.loadEventsLocked(query.Ref)
	if err != nil {
		return session.EventPage{}, err
	}
	if after > len(events) {
		after = len(events)
	}
	limit := query.Limit
	out := make([]session.Event, 0)
	for _, event := range events[after:] {
		if !query.IncludeTransient && session.IsTransient(event) {
			after++
			continue
		}
		out = append(out, session.CloneEvent(event))
		after++
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return session.EventPage{
		Events:     out,
		NextCursor: session.Cursor(strconv.Itoa(after)),
	}, nil
}

func (s *Store) UpdateState(ctx context.Context, ref session.Ref, patch session.StatePatch) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return session.ErrNotFound
	}
	if patch == nil {
		return nil
	}
	id, err := normalizedSessionID(ref)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.loadLocked(ref)
	if err != nil {
		return err
	}
	next, err := patch(cloneState(snapshot.State))
	if err != nil {
		return err
	}
	if err := writeJSONFile(s.stateFile(id), cloneState(next)); err != nil {
		return err
	}
	active := snapshot.Session
	active.UpdatedAt = time.Now().UTC()
	return writeJSONFile(s.sessionFile(id), active)
}

func (s *Store) loadLocked(ref session.Ref) (session.Snapshot, error) {
	id, err := normalizedSessionID(ref)
	if err != nil {
		return session.Snapshot{}, err
	}
	active := session.Session{}
	if err := readJSONFile(s.sessionFile(id), &active); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return session.Snapshot{}, session.ErrNotFound
		}
		return session.Snapshot{}, err
	}
	events, err := s.loadEventsLocked(ref)
	if err != nil {
		return session.Snapshot{}, err
	}
	state := session.State{}
	if err := readJSONFile(s.stateFile(id), &state); err != nil && !errors.Is(err, os.ErrNotExist) {
		return session.Snapshot{}, err
	}
	return session.Snapshot{
		Session: session.CloneSession(active),
		Events:  filterEvents(events, false),
		State:   cloneState(state),
		Cursor:  session.Cursor(strconv.Itoa(len(events))),
	}, nil
}

func (s *Store) loadEventsLocked(ref session.Ref) ([]session.Event, error) {
	id, err := normalizedSessionID(ref)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(s.eventsFile(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []session.Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event session.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, err
		}
		events = append(events, session.CloneEvent(event))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func filterEvents(events []session.Event, includeTransient bool) []session.Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]session.Event, 0, len(events))
	for _, event := range events {
		if !includeTransient && session.IsTransient(event) {
			continue
		}
		out = append(out, session.CloneEvent(event))
	}
	return out
}

func (s *Store) sessionDir(id string) string {
	return filepath.Join(s.root, "sessions", id)
}

func (s *Store) sessionFile(id string) string {
	return filepath.Join(s.sessionDir(id), "session.json")
}

func (s *Store) eventsFile(id string) string {
	return filepath.Join(s.sessionDir(id), "events.jsonl")
}

func (s *Store) stateFile(id string) string {
	return filepath.Join(s.sessionDir(id), "state.json")
}

func normalizedSessionID(ref session.Ref) (string, error) {
	id := strings.TrimSpace(ref.SessionID)
	if id == "" {
		return "", fmt.Errorf("%w: session id is required", session.ErrInvalid)
	}
	if err := validateID(id); err != nil {
		return "", err
	}
	return id, nil
}

func validateID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("%w: session id is required", session.ErrInvalid)
	}
	if id == "." || id == ".." {
		return fmt.Errorf("%w: invalid session id", session.ErrInvalid)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("%w: invalid session id", session.ErrInvalid)
		}
	}
	return nil
}

func parseCursor(cursor session.Cursor) (int, error) {
	raw := strings.TrimSpace(string(cursor))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w: invalid event cursor", session.ErrInvalid)
	}
	return value, nil
}

func readJSONFile(path string, out any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(file).Decode(out)
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp := path + ".tmp"
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	encodeErr := encoder.Encode(value)
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(temp)
		return encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(temp)
		return closeErr
	}
	return os.Rename(temp, path)
}

func cloneState(in session.State) session.State {
	if in == nil {
		return nil
	}
	return session.State(maps.Clone(in))
}

var _ session.Store = (*Store)(nil)
