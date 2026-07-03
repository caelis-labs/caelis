package file

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
)

var _ taskapi.Store = (*TaskStore)(nil)

// NewTaskStore constructs one task store facade backed by the same file store
// index used for session metadata.
func NewTaskStore(store *Store) *TaskStore {
	if store == nil {
		store = NewStore(Config{})
	}
	return &TaskStore{store: store}
}

func (s *TaskStore) Upsert(_ context.Context, entry *taskapi.Entry) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("impl/session/file: task store is not initialized")
	}
	entry = taskapi.SanitizeEntryForPersistence(entry, taskapi.ResultPersistenceCanonical)
	if entry == nil {
		return nil
	}
	if strings.TrimSpace(entry.TaskID) == "" || strings.TrimSpace(entry.Session.SessionID) == "" {
		return fmt.Errorf("impl/session/file: task_id and session_id are required")
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	return s.store.withRootWriteLock(func() error {
		return s.store.upsertTaskIndex(entry)
	})
}

func (s *TaskStore) Get(_ context.Context, taskID string) (*taskapi.Entry, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("impl/session/file: task %q not found", strings.TrimSpace(taskID))
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("impl/session/file: task_id is required")
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	var out *taskapi.Entry
	if err := s.store.withRootReadLock(func() error {
		entry, err := s.store.getTaskIndex(taskID)
		if err != nil {
			return err
		}
		out = entry
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *TaskStore) ListSession(_ context.Context, ref session.SessionRef) ([]*taskapi.Entry, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("impl/session/file: task store is not initialized")
	}
	ref = session.NormalizeSessionRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return []*taskapi.Entry{}, nil
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	var out []*taskapi.Entry
	if err := s.store.withRootReadLock(func() error {
		entries, err := s.store.listTaskIndex(ref)
		if err != nil {
			return err
		}
		out = entries
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *TaskStore) GetSessionTaskByHandle(_ context.Context, ref session.SessionRef, kind taskapi.Kind, handle string) (*taskapi.Entry, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("impl/session/file: task handle %q not found", strings.TrimSpace(handle))
	}
	ref = session.NormalizeSessionRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return nil, fmt.Errorf("impl/session/file: session_id is required")
	}
	kind = taskapi.Kind(strings.TrimSpace(string(kind)))
	if kind == "" {
		return nil, fmt.Errorf("impl/session/file: task kind is required")
	}
	handle = taskapi.NormalizeHandle(handle)
	if handle == "" {
		return nil, fmt.Errorf("impl/session/file: task handle is required")
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	var out *taskapi.Entry
	if err := s.store.withRootReadLock(func() error {
		entry, err := s.store.getSessionTaskIndexByHandle(ref, kind, handle)
		if err != nil {
			return err
		}
		out = entry
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) upsertTaskIndex(entry *taskapi.Entry) error {
	row, err := taskIndexRowFromEntry(entry)
	if err != nil {
		return err
	}
	db, err := s.openSessionIndex()
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.Exec(
		`INSERT INTO tasks (
			task_id, kind, app_name, user_id, session_id, workspace_key, title, state,
			running, supports_input, supports_cancel,
			created_at_ns, updated_at_ns, heartbeat_at_ns,
			stdout_cursor, stderr_cursor, event_cursor,
			handle, spec_json, result_json, metadata_json, terminal_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			kind = excluded.kind,
			app_name = excluded.app_name,
			user_id = excluded.user_id,
			session_id = excluded.session_id,
			workspace_key = excluded.workspace_key,
			title = excluded.title,
			state = excluded.state,
			running = excluded.running,
			supports_input = excluded.supports_input,
			supports_cancel = excluded.supports_cancel,
			created_at_ns = excluded.created_at_ns,
			updated_at_ns = excluded.updated_at_ns,
			heartbeat_at_ns = excluded.heartbeat_at_ns,
			stdout_cursor = excluded.stdout_cursor,
			stderr_cursor = excluded.stderr_cursor,
			event_cursor = excluded.event_cursor,
			handle = excluded.handle,
			spec_json = excluded.spec_json,
			result_json = excluded.result_json,
			metadata_json = excluded.metadata_json,
			terminal_json = excluded.terminal_json`,
		row.taskID,
		row.kind,
		row.appName,
		row.userID,
		row.sessionID,
		row.workspaceKey,
		row.title,
		row.state,
		boolToSQLite(row.running),
		boolToSQLite(row.supportsInput),
		boolToSQLite(row.supportsCancel),
		timeToUnixNano(row.createdAt),
		timeToUnixNano(row.updatedAt),
		timeToUnixNano(row.heartbeatAt),
		row.stdoutCursor,
		row.stderrCursor,
		row.eventCursor,
		row.handle,
		row.specJSON,
		row.resultJSON,
		row.metadataJSON,
		row.terminalJSON,
	)
	if err != nil {
		return fmt.Errorf("impl/session/file: upsert task index: %w", err)
	}
	return syncDir(filepath.Dir(s.sessionIndexPath()))
}

func (s *Store) getTaskIndex(taskID string) (*taskapi.Entry, error) {
	db, err := s.openSessionIndex()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	row := db.QueryRow(taskIndexSelectSQL()+` WHERE task_id = ?`, taskID)
	entry, err := scanTaskIndexEntry(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("impl/session/file: task %q not found", taskID)
		}
		return nil, err
	}
	return entry, nil
}

func (s *Store) listTaskIndex(ref session.SessionRef) ([]*taskapi.Entry, error) {
	db, err := s.openSessionIndex()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(taskIndexSelectSQL()+` WHERE session_id = ? ORDER BY updated_at_ns DESC, task_id ASC`, strings.TrimSpace(ref.SessionID))
	if err != nil {
		return nil, fmt.Errorf("impl/session/file: list task index: %w", err)
	}
	defer rows.Close()

	out := make([]*taskapi.Entry, 0)
	for rows.Next() {
		entry, err := scanTaskIndexEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("impl/session/file: scan task index: %w", err)
	}
	return out, nil
}

func (s *Store) getSessionTaskIndexByHandle(ref session.SessionRef, kind taskapi.Kind, handle string) (*taskapi.Entry, error) {
	db, err := s.openSessionIndex()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(
		taskIndexSelectSQL()+` WHERE session_id = ? AND kind = ? AND handle = ? ORDER BY updated_at_ns DESC, task_id ASC LIMIT 2`,
		strings.TrimSpace(ref.SessionID),
		strings.TrimSpace(string(kind)),
		taskapi.NormalizeHandle(handle),
	)
	if err != nil {
		return nil, fmt.Errorf("impl/session/file: lookup task handle: %w", err)
	}
	defer rows.Close()

	matches := make([]*taskapi.Entry, 0, 2)
	for rows.Next() {
		entry, err := scanTaskIndexEntry(rows)
		if err != nil {
			return nil, err
		}
		matches = append(matches, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("impl/session/file: scan task handle lookup: %w", err)
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("impl/session/file: task handle %q not found", handle)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("impl/session/file: task handle %q is ambiguous", handle)
	}
}

func taskIndexSelectSQL() string {
	return `SELECT
		task_id, kind, app_name, user_id, session_id, workspace_key, title, state,
		running, supports_input, supports_cancel,
		created_at_ns, updated_at_ns, heartbeat_at_ns,
		stdout_cursor, stderr_cursor, event_cursor,
		spec_json, result_json, metadata_json, terminal_json
	FROM tasks`
}

type taskIndexRow struct {
	taskID         string
	kind           string
	appName        string
	userID         string
	sessionID      string
	workspaceKey   string
	title          string
	state          string
	running        bool
	supportsInput  bool
	supportsCancel bool
	createdAt      time.Time
	updatedAt      time.Time
	heartbeatAt    time.Time
	stdoutCursor   int64
	stderrCursor   int64
	eventCursor    int64
	handle         string
	specJSON       string
	resultJSON     string
	metadataJSON   string
	terminalJSON   string
}

func taskIndexRowFromEntry(entry *taskapi.Entry) (taskIndexRow, error) {
	if entry == nil {
		return taskIndexRow{}, fmt.Errorf("impl/session/file: task entry is nil")
	}
	specJSON, err := taskIndexJSON(entry.Spec)
	if err != nil {
		return taskIndexRow{}, fmt.Errorf("impl/session/file: encode task spec: %w", err)
	}
	resultJSON, err := taskIndexJSON(entry.Result)
	if err != nil {
		return taskIndexRow{}, fmt.Errorf("impl/session/file: encode task result: %w", err)
	}
	metadataJSON, err := taskIndexJSON(entry.Metadata)
	if err != nil {
		return taskIndexRow{}, fmt.Errorf("impl/session/file: encode task metadata: %w", err)
	}
	terminalJSON, err := taskIndexJSON(entry.Terminal)
	if err != nil {
		return taskIndexRow{}, fmt.Errorf("impl/session/file: encode task terminal: %w", err)
	}
	return taskIndexRow{
		taskID:         strings.TrimSpace(entry.TaskID),
		kind:           strings.TrimSpace(string(entry.Kind)),
		appName:        strings.TrimSpace(entry.Session.AppName),
		userID:         strings.TrimSpace(entry.Session.UserID),
		sessionID:      strings.TrimSpace(entry.Session.SessionID),
		workspaceKey:   strings.TrimSpace(entry.Session.WorkspaceKey),
		title:          strings.TrimSpace(entry.Title),
		state:          strings.TrimSpace(string(entry.State)),
		running:        entry.Running,
		supportsInput:  entry.SupportsInput,
		supportsCancel: entry.SupportsCancel,
		createdAt:      entry.CreatedAt,
		updatedAt:      entry.UpdatedAt,
		heartbeatAt:    entry.HeartbeatAt,
		stdoutCursor:   entry.StdoutCursor,
		stderrCursor:   entry.StderrCursor,
		eventCursor:    entry.EventCursor,
		handle:         taskapi.NormalizeHandle(firstNonEmpty(taskIndexString(entry.Result, "handle"), taskIndexString(entry.Metadata, "handle"), taskIndexString(entry.Spec, "handle"))),
		specJSON:       specJSON,
		resultJSON:     resultJSON,
		metadataJSON:   metadataJSON,
		terminalJSON:   terminalJSON,
	}, nil
}

func scanTaskIndexEntry(scanner sessionIndexScanner) (*taskapi.Entry, error) {
	var (
		taskID         string
		kind           string
		appName        string
		userID         string
		sessionID      string
		workspaceKey   string
		title          string
		state          string
		running        int64
		supportsInput  int64
		supportsCancel int64
		createdAtNS    int64
		updatedAtNS    int64
		heartbeatAtNS  int64
		stdoutCursor   int64
		stderrCursor   int64
		eventCursor    int64
		specRaw        string
		resultRaw      string
		metadataRaw    string
		terminalRaw    string
	)
	if err := scanner.Scan(
		&taskID,
		&kind,
		&appName,
		&userID,
		&sessionID,
		&workspaceKey,
		&title,
		&state,
		&running,
		&supportsInput,
		&supportsCancel,
		&createdAtNS,
		&updatedAtNS,
		&heartbeatAtNS,
		&stdoutCursor,
		&stderrCursor,
		&eventCursor,
		&specRaw,
		&resultRaw,
		&metadataRaw,
		&terminalRaw,
	); err != nil {
		return nil, err
	}
	spec, err := decodeTaskIndexMap(specRaw, "spec")
	if err != nil {
		return nil, err
	}
	result, err := decodeTaskIndexMap(resultRaw, "result")
	if err != nil {
		return nil, err
	}
	metadata, err := decodeTaskIndexMap(metadataRaw, "metadata")
	if err != nil {
		return nil, err
	}
	terminal, err := decodeTaskIndexTerminal(terminalRaw)
	if err != nil {
		return nil, err
	}
	return taskapi.CloneEntry(&taskapi.Entry{
		TaskID: strings.TrimSpace(taskID),
		Kind:   taskapi.Kind(strings.TrimSpace(kind)),
		Session: session.NormalizeSessionRef(session.SessionRef{
			AppName:      appName,
			UserID:       userID,
			SessionID:    sessionID,
			WorkspaceKey: workspaceKey,
		}),
		Title:          strings.TrimSpace(title),
		State:          taskapi.State(strings.TrimSpace(state)),
		Running:        sqliteBool(running),
		SupportsInput:  sqliteBool(supportsInput),
		SupportsCancel: sqliteBool(supportsCancel),
		CreatedAt:      unixNanoToTime(createdAtNS),
		UpdatedAt:      unixNanoToTime(updatedAtNS),
		HeartbeatAt:    unixNanoToTime(heartbeatAtNS),
		StdoutCursor:   stdoutCursor,
		StderrCursor:   stderrCursor,
		EventCursor:    eventCursor,
		Spec:           spec,
		Result:         result,
		Metadata:       metadata,
		Terminal:       terminal,
	}), nil
}

func taskIndexJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeTaskIndexMap(raw string, field string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, fmt.Errorf("impl/session/file: decode task %s: %w", field, err)
	}
	return out, nil
}

func decodeTaskIndexTerminal(raw string) (sandbox.TerminalRef, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return sandbox.TerminalRef{}, nil
	}
	var out sandbox.TerminalRef
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return sandbox.TerminalRef{}, fmt.Errorf("impl/session/file: decode task terminal: %w", err)
	}
	return sandbox.CloneTerminalRef(out), nil
}

func taskIndexString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	if text, ok := values[key].(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func boolToSQLite(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sqliteBool(value int64) bool {
	return value != 0
}
