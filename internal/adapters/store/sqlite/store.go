// Package sqlite provides a core-native SQLite session store.
package sqlite

import (
	"context"
	"database/sql"
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
	_ "modernc.org/sqlite"
)

type Store struct {
	db     *sql.DB
	mu     sync.Mutex
	nextID atomic.Uint64
}

func New(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("store/sqlite: database path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", abs)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Create(ctx context.Context, req session.StartRequest) (session.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.Session{}, err
	}
	if s == nil || s.db == nil {
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

	existing, err := s.loadSession(ctx, id)
	if err == nil {
		return session.CloneSession(existing), nil
	}
	if !errors.Is(err, session.ErrNotFound) {
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
			Source:     "sqlite-store",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	sessionJSON, err := json.Marshal(active)
	if err != nil {
		return session.Session{}, err
	}
	stateJSON, err := json.Marshal(session.State{})
	if err != nil {
		return session.Session{}, err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO sessions(session_id, app_name, user_id, workspace_key, session_json, state_json, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		active.SessionID,
		active.AppName,
		active.UserID,
		active.WorkspaceKey,
		string(sessionJSON),
		string(stateJSON),
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return session.Session{}, err
	}
	return session.CloneSession(active), nil
}

func (s *Store) Load(ctx context.Context, ref session.Ref) (session.Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.Snapshot{}, err
	}
	if s == nil || s.db == nil {
		return session.Snapshot{}, session.ErrNotFound
	}
	id, err := normalizedSessionID(ref)
	if err != nil {
		return session.Snapshot{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	active, err := s.loadSession(ctx, id)
	if err != nil {
		return session.Snapshot{}, err
	}
	state, err := s.loadState(ctx, id)
	if err != nil {
		return session.Snapshot{}, err
	}
	events, rawCount, err := s.loadEvents(ctx, id, false)
	if err != nil {
		return session.Snapshot{}, err
	}
	return session.Snapshot{
		Session: session.CloneSession(active),
		Events:  events,
		State:   cloneState(state),
		Cursor:  session.Cursor(strconv.Itoa(rawCount)),
	}, nil
}

func (s *Store) Append(ctx context.Context, ref session.Ref, events []session.Event) (session.Cursor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil || s.db == nil {
		return "", session.ErrNotFound
	}
	id, err := normalizedSessionID(ref)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	active, rawCount, err := loadSessionForUpdate(ctx, tx, id)
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return session.Cursor(strconv.Itoa(rawCount)), tx.Commit()
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO events(session_id, seq, event_json, visibility, event_type, created_at)
VALUES(?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return "", err
	}
	defer stmt.Close()

	for _, event := range events {
		rawCount++
		next := session.CloneEvent(event)
		if strings.TrimSpace(next.ID) == "" {
			next.ID = fmt.Sprintf("evt-%d", rawCount)
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
		payload, err := json.Marshal(next)
		if err != nil {
			return "", err
		}
		if _, err := stmt.ExecContext(ctx, id, rawCount, string(payload), string(next.Visibility), string(next.Type), next.Time.Format(time.RFC3339Nano)); err != nil {
			return "", err
		}
	}
	now := time.Now().UTC()
	active.UpdatedAt = now
	sessionJSON, err := json.Marshal(active)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET session_json = ?, updated_at = ? WHERE session_id = ?`, string(sessionJSON), now.Format(time.RFC3339Nano), id); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return session.Cursor(strconv.Itoa(rawCount)), nil
}

func (s *Store) Events(ctx context.Context, query session.EventQuery) (session.EventPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.EventPage{}, err
	}
	if s == nil || s.db == nil {
		return session.EventPage{}, session.ErrNotFound
	}
	id, err := normalizedSessionID(query.Ref)
	if err != nil {
		return session.EventPage{}, err
	}
	after, err := parseCursor(query.After)
	if err != nil {
		return session.EventPage{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.loadSession(ctx, id); err != nil {
		return session.EventPage{}, err
	}
	rawCount, err := s.eventCount(ctx, id)
	if err != nil {
		return session.EventPage{}, err
	}
	if after > rawCount {
		after = rawCount
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, event_json FROM events WHERE session_id = ? AND seq > ? ORDER BY seq`, id, after)
	if err != nil {
		return session.EventPage{}, err
	}
	defer rows.Close()

	limit := query.Limit
	out := make([]session.Event, 0)
	nextCursor := after
	for rows.Next() {
		var seq int
		var raw string
		if err := rows.Scan(&seq, &raw); err != nil {
			return session.EventPage{}, err
		}
		nextCursor = seq
		var event session.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return session.EventPage{}, err
		}
		if !query.IncludeTransient && session.IsTransient(event) {
			continue
		}
		out = append(out, session.CloneEvent(event))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return session.EventPage{}, err
	}
	return session.EventPage{
		Events:     out,
		NextCursor: session.Cursor(strconv.Itoa(nextCursor)),
	}, nil
}

func (s *Store) UpdateState(ctx context.Context, ref session.Ref, patch session.StatePatch) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.db == nil {
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	active, state, err := loadSessionAndState(ctx, tx, id)
	if err != nil {
		return err
	}
	next, err := patch(cloneState(state))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	active.UpdatedAt = now
	sessionJSON, err := json.Marshal(active)
	if err != nil {
		return err
	}
	stateJSON, err := json.Marshal(cloneState(next))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET session_json = ?, state_json = ?, updated_at = ? WHERE session_id = ?`, string(sessionJSON), string(stateJSON), now.Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS sessions (
  session_id TEXT PRIMARY KEY,
  app_name TEXT NOT NULL,
  user_id TEXT NOT NULL,
  workspace_key TEXT NOT NULL DEFAULT '',
  session_json TEXT NOT NULL,
  state_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  event_json TEXT NOT NULL,
  visibility TEXT NOT NULL,
  event_type TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(session_id, seq),
  FOREIGN KEY(session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS events_session_seq_idx ON events(session_id, seq);
`)
	return err
}

func (s *Store) loadSession(ctx context.Context, id string) (session.Session, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT session_json FROM sessions WHERE session_id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Session{}, session.ErrNotFound
	}
	if err != nil {
		return session.Session{}, err
	}
	var active session.Session
	if err := json.Unmarshal([]byte(raw), &active); err != nil {
		return session.Session{}, err
	}
	return session.CloneSession(active), nil
}

func (s *Store) loadState(ctx context.Context, id string) (session.State, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT state_json FROM sessions WHERE session_id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeState(raw)
}

func (s *Store) loadEvents(ctx context.Context, id string, includeTransient bool) ([]session.Event, int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, event_json FROM events WHERE session_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var events []session.Event
	rawCount := 0
	for rows.Next() {
		var seq int
		var raw string
		if err := rows.Scan(&seq, &raw); err != nil {
			return nil, 0, err
		}
		rawCount = seq
		var event session.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, 0, err
		}
		if !includeTransient && session.IsTransient(event) {
			continue
		}
		events = append(events, session.CloneEvent(event))
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return events, rawCount, nil
}

func (s *Store) eventCount(ctx context.Context, id string) (int, error) {
	var rawCount sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM events WHERE session_id = ?`, id).Scan(&rawCount); err != nil {
		return 0, err
	}
	if !rawCount.Valid {
		return 0, nil
	}
	return int(rawCount.Int64), nil
}

func loadSessionForUpdate(ctx context.Context, tx *sql.Tx, id string) (session.Session, int, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT session_json FROM sessions WHERE session_id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Session{}, 0, session.ErrNotFound
	}
	if err != nil {
		return session.Session{}, 0, err
	}
	var active session.Session
	if err := json.Unmarshal([]byte(raw), &active); err != nil {
		return session.Session{}, 0, err
	}
	var rawCount sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM events WHERE session_id = ?`, id).Scan(&rawCount); err != nil {
		return session.Session{}, 0, err
	}
	if !rawCount.Valid {
		return session.CloneSession(active), 0, nil
	}
	return session.CloneSession(active), int(rawCount.Int64), nil
}

func loadSessionAndState(ctx context.Context, tx *sql.Tx, id string) (session.Session, session.State, error) {
	var sessionRaw string
	var stateRaw string
	err := tx.QueryRowContext(ctx, `SELECT session_json, state_json FROM sessions WHERE session_id = ?`, id).Scan(&sessionRaw, &stateRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Session{}, nil, session.ErrNotFound
	}
	if err != nil {
		return session.Session{}, nil, err
	}
	var active session.Session
	if err := json.Unmarshal([]byte(sessionRaw), &active); err != nil {
		return session.Session{}, nil, err
	}
	state, err := decodeState(stateRaw)
	if err != nil {
		return session.Session{}, nil, err
	}
	return session.CloneSession(active), state, nil
}

func decodeState(raw string) (session.State, error) {
	if strings.TrimSpace(raw) == "" {
		return session.State{}, nil
	}
	var state session.State
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, err
	}
	if state == nil {
		state = session.State{}
	}
	return cloneState(state), nil
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

func cloneState(in session.State) session.State {
	if in == nil {
		return nil
	}
	return session.State(maps.Clone(in))
}

var _ session.Store = (*Store)(nil)
