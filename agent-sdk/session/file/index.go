package file

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/internal/sdkctx"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"modernc.org/sqlite"
)

func init() {
	sqlite.MustRegisterCollationUtf8("caelis_session_cwd", sdkctx.CompareCWD)
}

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
	args := make([]any, 0, 6)
	clauses := make([]string, 0, 4)
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
	if cwd := strings.TrimSpace(req.CWD); cwd != "" {
		// Compare both spellings before LIMIT, including rows written by older
		// clients. Keep the stored CWD and workspace identity unchanged.
		clauses = append(clauses, "cwd = ? COLLATE caelis_session_cwd")
		args = append(args, cwd)
	}
	if encoded := strings.TrimSpace(req.Cursor); encoded != "" {
		cursor, err := session.DecodeSessionListCursor(encoded)
		if err != nil {
			return session.SessionList{}, err
		}
		updatedAtNS := timeToUnixNano(cursor.UpdatedAt)
		clauses = append(clauses, "(updated_at_ns < ? OR (updated_at_ns = ? AND session_id > ?))")
		args = append(args, updatedAtNS, updatedAtNS, cursor.SessionID)
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY updated_at_ns DESC, session_id ASC"
	queryLimit := req.Limit
	if queryLimit > 0 {
		query += " LIMIT ?"
		args = append(args, queryLimit+1)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return session.SessionList{}, fmt.Errorf("agent-sdk/session/file: list session index: %w", err)
	}
	defer rows.Close()

	summaries := make([]session.SessionSummary, 0)
	for rows.Next() {
		entry, err := scanSessionIndexEntry(rows)
		if err == nil {
			var path string
			path, err = s.indexEntryPath(entry)
			if err == nil {
				s.pathCache[entry.Session.SessionID] = path
			}
		}
		if errors.Is(err, session.ErrUnreadableSession) {
			delete(s.pathCache, entry.Session.SessionID)
		} else if err != nil {
			return session.SessionList{}, err
		}
		summaries = append(summaries, entry.Session)
	}
	if err := rows.Err(); err != nil {
		return session.SessionList{}, fmt.Errorf("agent-sdk/session/file: scan session index: %w", err)
	}
	hasMore := queryLimit > 0 && len(summaries) > queryLimit
	if hasMore {
		summaries = summaries[:queryLimit]
	}
	nextCursor := ""
	if hasMore && len(summaries) > 0 {
		last := summaries[len(summaries)-1]
		nextCursor, err = session.EncodeSessionListCursor(session.SessionListCursor{UpdatedAt: last.UpdatedAt, SessionID: last.SessionID})
		if err != nil {
			return session.SessionList{}, err
		}
	}
	return session.SessionList{Sessions: session.CloneSessionSummaries(summaries), NextCursor: nextCursor}, nil
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
			return sessionIndexEntry{}, s.unindexedTransactionError(db, sessionID)
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
	return s.durability.SyncDirectory(filepath.Dir(s.sessionIndexPath()))
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

func (s *Store) indexEntryPath(entry sessionIndexEntry) (string, error) {
	path := entry.Path
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(s.rootDir, path)
		if err != nil {
			return "", fmt.Errorf("%w: invalid indexed document path: %w", session.ErrUnreadableSession, err)
		}
		path = rel
	}
	if !filepath.IsLocal(path) || path == "." {
		return "", fmt.Errorf("%w: invalid indexed path for Session %q", session.ErrUnreadableSession, entry.Session.SessionID)
	}
	return filepath.Join(s.rootDir, path), nil
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
	entry := sessionIndexEntry{
		Session: session.SessionSummary{
			SessionRef: session.NormalizeSessionRef(session.SessionRef{
				AppName:      appName,
				UserID:       userID,
				SessionID:    sessionID,
				WorkspaceKey: workspaceKey,
			}),
			CWD:       strings.TrimSpace(cwd),
			Title:     strings.TrimSpace(title),
			UpdatedAt: unixNanoToTime(updatedAtNS),
		},
		Path:      strings.TrimSpace(path),
		CreatedAt: unixNanoToTime(createdAtNS),
	}
	if strings.TrimSpace(metadataRaw) != "" {
		if err := json.Unmarshal([]byte(metadataRaw), &entry.Session.Metadata); err != nil {
			entry.Session.Metadata = nil
			return entry, fmt.Errorf("%w: decode metadata for Session %q: %w", session.ErrUnreadableSession, sessionID, err)
		}
	}
	return entry, nil
}

func (s *Store) openSessionIndex() (*sql.DB, error) {
	return s.openIndex(ensureSessionIndexSchema)
}

func (s *Store) openIndex(ensureSchema func(*sql.DB) error) (*sql.DB, error) {
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
	if err := s.durability.ConfigureSQLite(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("agent-sdk/session/file: configure session index durability: %w", err)
	}
	if err := ensureSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func ensureSessionIndexSchema(db *sql.DB) error {
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
CREATE TABLE IF NOT EXISTS unreadable_transactions (
	path_key TEXT NOT NULL,
	path TEXT NOT NULL,
	PRIMARY KEY(path_key, path)
);
CREATE INDEX IF NOT EXISTS sessions_workspace_updated_idx ON sessions(workspace_key, updated_at_ns DESC);
CREATE INDEX IF NOT EXISTS sessions_app_user_updated_idx ON sessions(app_name, user_id, updated_at_ns DESC);
`)
	if err != nil {
		return fmt.Errorf("agent-sdk/session/file: initialize session index: %w", err)
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
