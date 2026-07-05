package file

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	_ "modernc.org/sqlite"
)

type sessionIndexEntry struct {
	Session   session.SessionSummary
	Path      string
	CreatedAt time.Time
}

func (s *Store) sessionIndexPath() string {
	return filepath.Join(s.rootDir, indexFilename)
}

func (s *Store) listFromSessionIndex(req session.ListSessionsRequest) (session.SessionList, error) {
	db, err := s.openSessionIndex()
	if err != nil {
		return session.SessionList{}, err
	}
	defer db.Close()

	query := `SELECT session_id, app_name, user_id, workspace_key, cwd, title, metadata_json, path, created_at_ns, updated_at_ns FROM sessions`
	args := make([]any, 0, 3)
	clauses := make([]string, 0, 3)
	if appName := strings.TrimSpace(req.AppName); appName != "" {
		clauses = append(clauses, "app_name = ?")
		args = append(args, appName)
	}
	if userID := strings.TrimSpace(req.UserID); userID != "" {
		clauses = append(clauses, "user_id = ?")
		args = append(args, userID)
	}
	if workspaceKey := strings.TrimSpace(req.WorkspaceKey); workspaceKey != "" {
		clauses = append(clauses, "workspace_key = ?")
		args = append(args, workspaceKey)
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY updated_at_ns DESC, session_id ASC"
	if req.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, req.Limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return session.SessionList{}, fmt.Errorf("agent-sdk/session/file: list session index: %w", err)
	}
	defer rows.Close()

	summaries := make([]session.SessionSummary, 0)
	for rows.Next() {
		entry, err := scanSessionIndexEntry(rows)
		if err != nil {
			return session.SessionList{}, err
		}
		if path := s.indexEntryPath(entry); path != "" {
			s.pathCache[pathCacheKey(entry.Session.SessionID, entry.Session.WorkspaceKey)] = path
		}
		summaries = append(summaries, entry.Session)
	}
	if err := rows.Err(); err != nil {
		return session.SessionList{}, fmt.Errorf("agent-sdk/session/file: scan session index: %w", err)
	}
	return session.SessionList{Sessions: session.CloneSessionSummaries(summaries)}, nil
}

func (s *Store) lookupSessionIndex(sessionID string) (sessionIndexEntry, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sessionIndexEntry{}, session.ErrInvalidSession
	}
	db, err := s.openSessionIndex()
	if err != nil {
		return sessionIndexEntry{}, err
	}
	defer db.Close()

	row := db.QueryRow(
		`SELECT session_id, app_name, user_id, workspace_key, cwd, title, metadata_json, path, created_at_ns, updated_at_ns FROM sessions WHERE session_id = ?`,
		sessionID,
	)
	entry, err := scanSessionIndexEntry(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sessionIndexEntry{}, session.ErrSessionNotFound
		}
		return sessionIndexEntry{}, err
	}
	return entry, nil
}

func (s *Store) upsertSessionIndex(sess session.Session, documentPath string) error {
	entry := s.sessionIndexEntry(sess, documentPath)
	db, err := s.openSessionIndex()
	if err != nil {
		return err
	}
	defer db.Close()

	metadata, err := json.Marshal(entry.Session.Metadata)
	if err != nil {
		return fmt.Errorf("agent-sdk/session/file: encode session index metadata: %w", err)
	}
	_, err = db.Exec(
		`INSERT INTO sessions (
			session_id, app_name, user_id, workspace_key, cwd, title, metadata_json, path, created_at_ns, updated_at_ns
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			app_name = excluded.app_name,
			user_id = excluded.user_id,
			workspace_key = excluded.workspace_key,
			cwd = excluded.cwd,
			title = excluded.title,
			metadata_json = excluded.metadata_json,
			path = excluded.path,
			created_at_ns = excluded.created_at_ns,
			updated_at_ns = excluded.updated_at_ns`,
		entry.Session.SessionID,
		entry.Session.AppName,
		entry.Session.UserID,
		entry.Session.WorkspaceKey,
		entry.Session.CWD,
		entry.Session.Title,
		string(metadata),
		entry.Path,
		timeToUnixNano(entry.CreatedAt),
		timeToUnixNano(entry.Session.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("agent-sdk/session/file: upsert session index: %w", err)
	}
	return syncDir(filepath.Dir(s.sessionIndexPath()))
}

func (s *Store) sessionIndexEntry(sess session.Session, documentPath string) sessionIndexEntry {
	relPath := documentPath
	if rel, err := filepath.Rel(s.rootDir, documentPath); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		relPath = rel
	}
	return sessionIndexEntry{
		Session: session.SessionSummary{
			SessionRef: sess.SessionRef,
			CWD:        sess.CWD,
			Title:      sess.Title,
			Metadata:   session.CloneState(sess.Metadata),
			UpdatedAt:  sess.UpdatedAt,
		},
		Path:      relPath,
		CreatedAt: sess.CreatedAt,
	}
}

func (s *Store) indexEntryPath(entry sessionIndexEntry) string {
	path := strings.TrimSpace(entry.Path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(s.rootDir, path)
}

type sessionIndexScanner interface {
	Scan(dest ...any) error
}

func scanSessionIndexEntry(scanner sessionIndexScanner) (sessionIndexEntry, error) {
	var (
		sessionID    string
		appName      string
		userID       string
		workspaceKey string
		cwd          string
		title        string
		metadataRaw  string
		path         string
		createdAtNS  int64
		updatedAtNS  int64
	)
	if err := scanner.Scan(
		&sessionID,
		&appName,
		&userID,
		&workspaceKey,
		&cwd,
		&title,
		&metadataRaw,
		&path,
		&createdAtNS,
		&updatedAtNS,
	); err != nil {
		return sessionIndexEntry{}, err
	}
	metadata := map[string]any(nil)
	if strings.TrimSpace(metadataRaw) != "" && strings.TrimSpace(metadataRaw) != "null" {
		if err := json.Unmarshal([]byte(metadataRaw), &metadata); err != nil {
			return sessionIndexEntry{}, fmt.Errorf("agent-sdk/session/file: decode session index metadata: %w", err)
		}
	}
	return sessionIndexEntry{
		Session: session.SessionSummary{
			SessionRef: session.NormalizeSessionRef(session.SessionRef{
				AppName:      appName,
				UserID:       userID,
				SessionID:    sessionID,
				WorkspaceKey: workspaceKey,
			}),
			CWD:       strings.TrimSpace(cwd),
			Title:     strings.TrimSpace(title),
			Metadata:  session.CloneState(metadata),
			UpdatedAt: unixNanoToTime(updatedAtNS),
		},
		Path:      strings.TrimSpace(path),
		CreatedAt: unixNanoToTime(createdAtNS),
	}, nil
}

func (s *Store) openSessionIndex() (*sql.DB, error) {
	path := s.sessionIndexPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: open session index: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("agent-sdk/session/file: configure session index busy timeout: %w", err)
	}
	if err := ensureSessionIndexSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func ensureSessionIndexSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("agent-sdk/session/file: read session index version: %w", err)
	}
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS sessions (
	session_id TEXT PRIMARY KEY,
	app_name TEXT NOT NULL,
	user_id TEXT NOT NULL,
	workspace_key TEXT NOT NULL,
	cwd TEXT NOT NULL,
	title TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	path TEXT NOT NULL,
	created_at_ns INTEGER NOT NULL,
	updated_at_ns INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_workspace_updated_idx ON sessions(workspace_key, updated_at_ns DESC);
CREATE INDEX IF NOT EXISTS sessions_app_user_updated_idx ON sessions(app_name, user_id, updated_at_ns DESC);
CREATE TABLE IF NOT EXISTS tasks (
	task_id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	app_name TEXT NOT NULL,
	user_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	workspace_key TEXT NOT NULL,
	title TEXT NOT NULL,
	state TEXT NOT NULL,
	running INTEGER NOT NULL,
	supports_input INTEGER NOT NULL,
	supports_cancel INTEGER NOT NULL,
	created_at_ns INTEGER NOT NULL,
	updated_at_ns INTEGER NOT NULL,
	heartbeat_at_ns INTEGER NOT NULL,
	stdout_cursor INTEGER NOT NULL,
	stderr_cursor INTEGER NOT NULL,
	event_cursor INTEGER NOT NULL,
	handle TEXT NOT NULL,
	spec_json TEXT NOT NULL,
	result_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	terminal_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS tasks_session_updated_idx ON tasks(session_id, updated_at_ns DESC);
CREATE INDEX IF NOT EXISTS tasks_session_kind_handle_idx ON tasks(session_id, kind, handle);
`)
	if err != nil {
		return fmt.Errorf("agent-sdk/session/file: initialize session index: %w", err)
	}
	if version != indexVersion {
		if _, err := db.Exec(`PRAGMA user_version = ` + fmt.Sprint(indexVersion)); err != nil {
			return fmt.Errorf("agent-sdk/session/file: set session index version: %w", err)
		}
	}
	return nil
}

func timeToUnixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixNano()
}

func unixNanoToTime(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

func sortSessionSummaries(summaries []session.SessionSummary) {
	sort.Slice(summaries, func(i, j int) bool {
		if !summaries[i].UpdatedAt.Equal(summaries[j].UpdatedAt) {
			return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
		}
		return summaries[i].SessionID < summaries[j].SessionID
	})
}
