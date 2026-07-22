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

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

var _ taskapi.Store = (*TaskStore)(nil)
var _ taskapi.CASStore = (*TaskStore)(nil)
var _ taskapi.LeaseStore = (*TaskStore)(nil)

// NewTaskStore constructs one task store facade backed by the same file store
// index used for session metadata.
func NewTaskStore(store *Store) *TaskStore {
	if store == nil {
		store = NewStore(Config{})
	}
	return &TaskStore{store: store}
}

func (s *TaskStore) Upsert(ctx context.Context, entry *taskapi.Entry) error {
	_, err := s.put(ctx, entry, nil, session.RuntimeMutationGuard(ctx))
	return err
}

// Put conditionally persists one task state using task revision CAS.
func (s *TaskStore) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	expected := req.ExpectedRevision
	return s.put(ctx, req.Entry, &expected, session.RuntimeMutationGuard(ctx))
}

func (s *TaskStore) put(ctx context.Context, entry *taskapi.Entry, expected *uint64, guard session.MutationGuard) (*taskapi.Entry, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("agent-sdk/session/file: task store is not initialized")
	}
	entry = taskapi.SanitizeEntryForPersistence(entry, taskapi.ResultPersistenceCanonical)
	if entry == nil {
		return nil, nil
	}
	if strings.TrimSpace(entry.TaskID) == "" || strings.TrimSpace(entry.Session.SessionID) == "" {
		return nil, fmt.Errorf("agent-sdk/session/file: task_id and session_id are required")
	}

	if err := s.store.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.store.mu.Unlock()

	var out *taskapi.Entry
	err := s.store.withRootWriteLockContext(ctx, func() error {
		doc, err := s.store.readDocumentForRef(entry.Session)
		if err == nil {
			if err := validateFileMutationGuard(activeDocumentLease(doc), guard, s.store.now()); err != nil {
				return err
			}
		} else if !errors.Is(err, session.ErrSessionNotFound) || guard.Authority != "" {
			return err
		}
		out, err = s.store.upsertTaskIndex(entry, expected)
		return err
	})
	return out, err
}

// AcquireLease conditionally assigns one live worker lease.
func (s *TaskStore) AcquireLease(ctx context.Context, req taskapi.AcquireLeaseRequest) (*taskapi.Entry, error) {
	entry, err := s.Get(ctx, req.TaskID)
	if err != nil {
		return nil, err
	}
	if entry.Revision != req.ExpectedTaskRevision {
		return nil, &taskapi.RevisionConflictError{TaskID: entry.TaskID, Expected: req.ExpectedTaskRevision, Actual: entry.Revision}
	}
	now, expiresAt, err := taskLeaseTimes(req.Now, req.TTL)
	if err != nil {
		return nil, err
	}
	ownerID := strings.TrimSpace(req.OwnerID)
	leaseID := strings.TrimSpace(req.LeaseID)
	if ownerID == "" || leaseID == "" {
		return nil, &taskapi.LeaseConflictError{TaskID: entry.TaskID, Detail: "owner_id and lease_id are required"}
	}
	if entry.Lease.ID != "" && entry.Lease.ExpiresAt.After(now) && (entry.Lease.ID != leaseID || entry.Lease.OwnerID != ownerID) {
		return nil, &taskapi.LeaseConflictError{TaskID: entry.TaskID, Detail: "another live lease owns the task"}
	}
	leaseRevision := entry.Lease.Revision + 1
	entry.Lease = taskapi.Lease{ID: leaseID, OwnerID: ownerID, Revision: leaseRevision, AcquiredAt: now, HeartbeatAt: now, ExpiresAt: expiresAt}
	return s.Put(ctx, taskapi.PutRequest{Entry: entry, ExpectedRevision: req.ExpectedTaskRevision})
}

// HeartbeatLease conditionally renews one live worker lease.
func (s *TaskStore) HeartbeatLease(ctx context.Context, req taskapi.HeartbeatLeaseRequest) (*taskapi.Entry, error) {
	entry, err := s.Get(ctx, req.TaskID)
	if err != nil {
		return nil, err
	}
	if entry.Revision != req.ExpectedTaskRevision {
		return nil, &taskapi.RevisionConflictError{TaskID: entry.TaskID, Expected: req.ExpectedTaskRevision, Actual: entry.Revision}
	}
	if err := validateTaskLeaseOwner(entry, req.LeaseID, req.OwnerID, req.ExpectedLeaseRevision); err != nil {
		return nil, err
	}
	now, expiresAt, err := taskLeaseTimes(req.Now, req.TTL)
	if err != nil {
		return nil, err
	}
	if !entry.Lease.ExpiresAt.IsZero() && !entry.Lease.ExpiresAt.After(now) {
		return nil, &taskapi.LeaseConflictError{TaskID: entry.TaskID, Detail: "lease has expired"}
	}
	entry.Lease.Revision++
	entry.Lease.HeartbeatAt = now
	entry.Lease.ExpiresAt = expiresAt
	return s.Put(ctx, taskapi.PutRequest{Entry: entry, ExpectedRevision: req.ExpectedTaskRevision})
}

// ReleaseLease conditionally clears one worker lease.
func (s *TaskStore) ReleaseLease(ctx context.Context, req taskapi.ReleaseLeaseRequest) (*taskapi.Entry, error) {
	entry, err := s.Get(ctx, req.TaskID)
	if err != nil {
		return nil, err
	}
	if entry.Revision != req.ExpectedTaskRevision {
		return nil, &taskapi.RevisionConflictError{TaskID: entry.TaskID, Expected: req.ExpectedTaskRevision, Actual: entry.Revision}
	}
	if err := validateTaskLeaseOwner(entry, req.LeaseID, req.OwnerID, req.ExpectedLeaseRevision); err != nil {
		return nil, err
	}
	entry.Lease = taskapi.Lease{}
	return s.Put(ctx, taskapi.PutRequest{Entry: entry, ExpectedRevision: req.ExpectedTaskRevision})
}

func taskLeaseTimes(now time.Time, ttl time.Duration) (time.Time, time.Time, error) {
	if ttl <= 0 {
		return time.Time{}, time.Time{}, &taskapi.LeaseConflictError{Detail: "positive TTL is required"}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	return now, now.Add(ttl), nil
}

func validateTaskLeaseOwner(entry *taskapi.Entry, leaseID, ownerID string, expectedRevision uint64) error {
	if entry == nil || entry.Lease.ID == "" {
		return &taskapi.LeaseConflictError{Detail: "task has no active lease"}
	}
	if entry.Lease.ID != strings.TrimSpace(leaseID) || entry.Lease.OwnerID != strings.TrimSpace(ownerID) || entry.Lease.Revision != expectedRevision {
		return &taskapi.LeaseConflictError{TaskID: entry.TaskID, Detail: "lease identity, owner, or revision mismatch"}
	}
	return nil
}

func (s *TaskStore) Get(ctx context.Context, taskID string) (*taskapi.Entry, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("agent-sdk/session/file: task %q not found", strings.TrimSpace(taskID))
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("agent-sdk/session/file: task_id is required")
	}

	if err := s.store.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.store.mu.Unlock()

	var out *taskapi.Entry
	if err := s.store.withRootReadLockContext(ctx, func() error {
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

func (s *TaskStore) ListSession(ctx context.Context, ref session.SessionRef) ([]*taskapi.Entry, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("agent-sdk/session/file: task store is not initialized")
	}
	ref = session.NormalizeSessionRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return []*taskapi.Entry{}, nil
	}

	if err := s.store.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.store.mu.Unlock()

	var out []*taskapi.Entry
	if err := s.store.withRootReadLockContext(ctx, func() error {
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

func (s *TaskStore) GetSessionTaskByHandle(ctx context.Context, ref session.SessionRef, handle string) (*taskapi.Entry, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("agent-sdk/session/file: task handle %q not found", strings.TrimSpace(handle))
	}
	ref = session.NormalizeSessionRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return nil, fmt.Errorf("agent-sdk/session/file: session_id is required")
	}
	handle = taskapi.NormalizeHandle(handle)
	if handle == "" {
		return nil, fmt.Errorf("agent-sdk/session/file: task handle is required")
	}

	if err := s.store.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.store.mu.Unlock()

	var out *taskapi.Entry
	if err := s.store.withRootReadLockContext(ctx, func() error {
		entry, err := s.store.getSessionTaskIndexByHandle(ref, handle)
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

func (s *Store) upsertTaskIndex(entry *taskapi.Entry, expected *uint64) (*taskapi.Entry, error) {
	db, err := s.openSessionIndex()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var current uint64
	if err := tx.QueryRow(`SELECT revision FROM tasks WHERE task_id = ?`, strings.TrimSpace(entry.TaskID)).Scan(&current); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("agent-sdk/session/file: read task revision: %w", err)
	}
	if expected != nil && *expected != current {
		return nil, &taskapi.RevisionConflictError{TaskID: entry.TaskID, Expected: *expected, Actual: current}
	}
	next := taskapi.CloneEntry(entry)
	next.Revision = current + 1
	row, err := taskIndexRowFromEntry(next)
	if err != nil {
		return nil, err
	}
	if row.handle != "" {
		var conflictingTaskID string
		err := tx.QueryRow(
			`SELECT task_id FROM tasks WHERE session_id = ? AND handle = ? AND task_id <> ? LIMIT 1`,
			row.sessionID, row.handle, row.taskID,
		).Scan(&conflictingTaskID)
		switch {
		case err == nil:
			return nil, &taskapi.HandleConflictError{SessionID: row.sessionID, Handle: row.handle, TaskID: conflictingTaskID}
		case !errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("agent-sdk/session/file: check task handle uniqueness: %w", err)
		}
	}

	_, err = tx.Exec(
		`INSERT INTO tasks (
			task_id, revision, kind, app_name, user_id, session_id, workspace_key, title, state,
			running, supports_input, supports_cancel,
			created_at_ns, updated_at_ns, heartbeat_at_ns, lease_id, lease_owner_id, lease_revision, lease_acquired_at_ns, lease_expires_at_ns,
			stdout_cursor, stderr_cursor, event_cursor,
			handle, spec_json, result_json, metadata_json, terminal_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			revision = excluded.revision,
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
			lease_id = excluded.lease_id,
			lease_owner_id = excluded.lease_owner_id,
			lease_revision = excluded.lease_revision,
			lease_acquired_at_ns = excluded.lease_acquired_at_ns,
			lease_expires_at_ns = excluded.lease_expires_at_ns,
			stdout_cursor = excluded.stdout_cursor,
			stderr_cursor = excluded.stderr_cursor,
			event_cursor = excluded.event_cursor,
			handle = excluded.handle,
			spec_json = excluded.spec_json,
			result_json = excluded.result_json,
			metadata_json = excluded.metadata_json,
			terminal_json = excluded.terminal_json`,
		row.taskID,
		row.revision,
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
		row.leaseID,
		row.leaseOwnerID,
		row.leaseRevision,
		timeToUnixNano(row.leaseAcquiredAt),
		timeToUnixNano(row.leaseExpiresAt),
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
		return nil, fmt.Errorf("agent-sdk/session/file: upsert task index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: commit task index: %w", err)
	}
	if err := s.injectTransactionFault("task_index_post_commit"); err != nil {
		return taskapi.CloneEntry(next), &session.CommittedError{Err: err}
	}
	if err := syncDir(filepath.Dir(s.sessionIndexPath())); err != nil {
		return taskapi.CloneEntry(next), &session.CommittedError{Err: err}
	}
	return taskapi.CloneEntry(next), nil
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
			return nil, fmt.Errorf("agent-sdk/session/file: task %q not found", taskID)
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
		return nil, fmt.Errorf("agent-sdk/session/file: list task index: %w", err)
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
		return nil, fmt.Errorf("agent-sdk/session/file: scan task index: %w", err)
	}
	return out, nil
}

func (s *Store) getSessionTaskIndexByHandle(ref session.SessionRef, handle string) (*taskapi.Entry, error) {
	db, err := s.openSessionIndex()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(
		taskIndexSelectSQL()+` WHERE session_id = ? AND handle = ? ORDER BY updated_at_ns DESC, task_id ASC LIMIT 2`,
		strings.TrimSpace(ref.SessionID),
		taskapi.NormalizeHandle(handle),
	)
	if err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: lookup task handle: %w", err)
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
		return nil, fmt.Errorf("agent-sdk/session/file: scan task handle lookup: %w", err)
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("agent-sdk/session/file: task handle %q not found", handle)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("agent-sdk/session/file: task handle %q is ambiguous", handle)
	}
}

func taskIndexSelectSQL() string {
	return `SELECT
		task_id, revision, kind, app_name, user_id, session_id, workspace_key, title, state,
		running, supports_input, supports_cancel,
		created_at_ns, updated_at_ns, heartbeat_at_ns, lease_id, lease_owner_id, lease_revision, lease_acquired_at_ns, lease_expires_at_ns,
		stdout_cursor, stderr_cursor, event_cursor, handle,
		spec_json, result_json, metadata_json, terminal_json
	FROM tasks`
}

type taskIndexRow struct {
	taskID          string
	revision        uint64
	kind            string
	appName         string
	userID          string
	sessionID       string
	workspaceKey    string
	title           string
	state           string
	running         bool
	supportsInput   bool
	supportsCancel  bool
	createdAt       time.Time
	updatedAt       time.Time
	heartbeatAt     time.Time
	leaseID         string
	leaseOwnerID    string
	leaseRevision   uint64
	leaseAcquiredAt time.Time
	leaseExpiresAt  time.Time
	stdoutCursor    int64
	stderrCursor    int64
	eventCursor     int64
	handle          string
	specJSON        string
	resultJSON      string
	metadataJSON    string
	terminalJSON    string
}

func taskIndexRowFromEntry(entry *taskapi.Entry) (taskIndexRow, error) {
	if entry == nil {
		return taskIndexRow{}, fmt.Errorf("agent-sdk/session/file: task entry is nil")
	}
	specJSON, err := taskIndexJSON(entry.Spec)
	if err != nil {
		return taskIndexRow{}, fmt.Errorf("agent-sdk/session/file: encode task spec: %w", err)
	}
	resultJSON, err := taskIndexJSON(entry.Result)
	if err != nil {
		return taskIndexRow{}, fmt.Errorf("agent-sdk/session/file: encode task result: %w", err)
	}
	metadataJSON, err := taskIndexJSON(entry.Metadata)
	if err != nil {
		return taskIndexRow{}, fmt.Errorf("agent-sdk/session/file: encode task metadata: %w", err)
	}
	terminalJSON, err := taskIndexJSON(entry.Terminal)
	if err != nil {
		return taskIndexRow{}, fmt.Errorf("agent-sdk/session/file: encode task terminal: %w", err)
	}
	return taskIndexRow{
		taskID:          strings.TrimSpace(entry.TaskID),
		revision:        entry.Revision,
		kind:            strings.TrimSpace(string(entry.Kind)),
		appName:         strings.TrimSpace(entry.Session.AppName),
		userID:          strings.TrimSpace(entry.Session.UserID),
		sessionID:       strings.TrimSpace(entry.Session.SessionID),
		workspaceKey:    strings.TrimSpace(entry.Session.WorkspaceKey),
		title:           strings.TrimSpace(entry.Title),
		state:           strings.TrimSpace(string(entry.State)),
		running:         entry.Running,
		supportsInput:   entry.SupportsInput,
		supportsCancel:  entry.SupportsCancel,
		createdAt:       entry.CreatedAt,
		updatedAt:       entry.UpdatedAt,
		heartbeatAt:     entry.Lease.HeartbeatAt,
		leaseID:         strings.TrimSpace(entry.Lease.ID),
		leaseOwnerID:    strings.TrimSpace(entry.Lease.OwnerID),
		leaseRevision:   entry.Lease.Revision,
		leaseAcquiredAt: entry.Lease.AcquiredAt,
		leaseExpiresAt:  entry.Lease.ExpiresAt,
		stdoutCursor:    entry.StdoutCursor,
		stderrCursor:    entry.StderrCursor,
		eventCursor:     entry.EventCursor,
		handle:          taskapi.NormalizeHandle(firstNonEmpty(entry.Handle, taskIndexString(entry.Result, "handle"), taskIndexString(entry.Metadata, "handle"), taskIndexString(entry.Spec, "handle"))),
		specJSON:        specJSON,
		resultJSON:      resultJSON,
		metadataJSON:    metadataJSON,
		terminalJSON:    terminalJSON,
	}, nil
}

func scanTaskIndexEntry(scanner sessionIndexScanner) (*taskapi.Entry, error) {
	var (
		taskID            string
		revision          uint64
		kind              string
		appName           string
		userID            string
		sessionID         string
		workspaceKey      string
		title             string
		state             string
		running           int64
		supportsInput     int64
		supportsCancel    int64
		createdAtNS       int64
		updatedAtNS       int64
		heartbeatAtNS     int64
		leaseID           string
		leaseOwnerID      string
		leaseRevision     uint64
		leaseAcquiredAtNS int64
		leaseExpiresAtNS  int64
		stdoutCursor      int64
		stderrCursor      int64
		eventCursor       int64
		handle            string
		specRaw           string
		resultRaw         string
		metadataRaw       string
		terminalRaw       string
	)
	if err := scanner.Scan(
		&taskID,
		&revision,
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
		&leaseID,
		&leaseOwnerID,
		&leaseRevision,
		&leaseAcquiredAtNS,
		&leaseExpiresAtNS,
		&stdoutCursor,
		&stderrCursor,
		&eventCursor,
		&handle,
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
		Handle: taskapi.NormalizeHandle(firstNonEmpty(
			handle,
			taskIndexString(result, "handle"),
			taskIndexString(metadata, "handle"),
			taskIndexString(spec, "handle"),
		)),
		Revision: revision,
		Kind:     taskapi.Kind(strings.TrimSpace(kind)),
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
		Lease: taskapi.Lease{
			ID: strings.TrimSpace(leaseID), OwnerID: strings.TrimSpace(leaseOwnerID), Revision: leaseRevision,
			AcquiredAt: unixNanoToTime(leaseAcquiredAtNS), HeartbeatAt: unixNanoToTime(heartbeatAtNS), ExpiresAt: unixNanoToTime(leaseExpiresAtNS),
		},
		StdoutCursor: stdoutCursor,
		StderrCursor: stderrCursor,
		EventCursor:  eventCursor,
		Spec:         spec,
		Result:       result,
		Metadata:     metadata,
		Terminal:     terminal,
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
		return nil, fmt.Errorf("agent-sdk/session/file: decode task %s: %w", field, err)
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
		return sandbox.TerminalRef{}, fmt.Errorf("agent-sdk/session/file: decode task terminal: %w", err)
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
