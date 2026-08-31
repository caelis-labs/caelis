package appserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	operationPolicyRowID = 1
	operationSQLMaxRetry = 8
)

// SQLiteOperationStore is the Control-owned durable operation ledger. It
// stores point-addressed idempotency records as rows while retaining the same
// intent-before-effect, immutable-result, unknown-outcome, and retention
// semantics as FileOperationStore.
type SQLiteOperationStore struct {
	path               string
	db                 *sql.DB
	now                func() time.Time
	retention          normalizedOperationRetentionConfig
	retentionExplicit  bool
	policyMu           sync.Mutex
	initialized        bool
	effectiveRetention time.Duration
	sweepMu            sync.Mutex
	nextSweep          time.Time
	sweepCursor        sqliteOperationSweepCursor
}

type sqliteOperationSweepCursor struct {
	valid         bool
	retainUntilNS int64
	principalID   string
	operationID   string
}

// NewSQLiteOperationStoreWithConfig opens one Control-owned SQLite ledger.
// The caller must invoke Initialize before admitting commands.
func NewSQLiteOperationStoreWithConfig(path string, config OperationRetentionConfig) (*SQLiteOperationStore, error) {
	retention, err := normalizeOperationRetentionConfig(config)
	if err != nil {
		return nil, err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("controlclient: operation store database path is required")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("controlclient: resolve operation store database: %w", err)
	}
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("controlclient: create operation store directory: %w", err)
	}
	if err := requireSecureSQLiteOperationDirectory(dir); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("controlclient: secure operation store directory: %w", err)
	}
	if err := rejectUnsafeSQLiteOperationFile(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("controlclient: open operation store database: %w", err)
	}
	db.SetMaxOpenConns(1)
	closeWith := func(err error) (*SQLiteOperationStore, error) {
		return nil, errors.Join(err, db.Close())
	}
	for _, statement := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA synchronous = FULL`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return closeWith(fmt.Errorf("controlclient: configure operation store database: %w", err))
		}
	}
	if err := ensureSQLiteOperationSchema(db); err != nil {
		return closeWith(err)
	}
	if err := rejectUnsafeSQLiteOperationFile(path); err != nil {
		return closeWith(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return closeWith(fmt.Errorf("controlclient: secure operation store database: %w", err))
	}
	return &SQLiteOperationStore{
		path: path, db: db, now: time.Now, retention: retention,
		retentionExplicit: config.TerminalRetention != 0,
	}, nil
}

func requireSecureSQLiteOperationDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("controlclient: inspect operation store directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("controlclient: operation store directory is not a secure directory")
	}
	return nil
}

func rejectUnsafeSQLiteOperationFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("controlclient: inspect operation store database: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("controlclient: operation store database is not a secure regular file")
	}
	return nil
}

func ensureSQLiteOperationSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS control_operation_policy (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	version INTEGER NOT NULL,
	terminal_retention_ns INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS control_operations (
	principal_id TEXT NOT NULL,
	operation_id TEXT NOT NULL,
	action TEXT NOT NULL,
	session_id TEXT NOT NULL,
	outcome TEXT NOT NULL,
	created_at_ns INTEGER NOT NULL,
	updated_at_ns INTEGER NOT NULL,
	retain_until_ns INTEGER NOT NULL,
	record_json BLOB NOT NULL,
	PRIMARY KEY (principal_id, operation_id)
);
CREATE INDEX IF NOT EXISTS control_operations_updated_idx
	ON control_operations(updated_at_ns DESC);
CREATE INDEX IF NOT EXISTS control_operations_action_updated_idx
	ON control_operations(action, updated_at_ns DESC);
CREATE INDEX IF NOT EXISTS control_operations_session_updated_idx
	ON control_operations(session_id, updated_at_ns DESC)
	WHERE session_id <> '';
CREATE INDEX IF NOT EXISTS control_operations_retention_idx
	ON control_operations(retain_until_ns)
	WHERE retain_until_ns > 0;
`)
	if err != nil {
		return fmt.Errorf("controlclient: initialize operation store schema: %w", err)
	}
	return nil
}

func (s *SQLiteOperationStore) Initialize(ctx context.Context) error {
	_, err := s.ensureRetentionPolicy(ctx)
	return err
}

func (s *SQLiteOperationStore) EffectiveTerminalRetention(ctx context.Context) (time.Duration, error) {
	return s.ensureRetentionPolicy(ctx)
}

func (s *SQLiteOperationStore) ensureRetentionPolicy(ctx context.Context) (time.Duration, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("controlclient: operation store database is unavailable")
	}
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	var terminalNS int64
	err := s.db.QueryRowContext(ctx, `
SELECT terminal_retention_ns
FROM control_operation_policy WHERE id = ?`, operationPolicyRowID).Scan(&terminalNS)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO control_operation_policy
	(id, version, terminal_retention_ns)
VALUES (?, ?, ?)`, operationPolicyRowID, operationRetentionPolicyVersion,
			int64(s.retention.TerminalRetention))
		if err == nil {
			err = s.db.QueryRowContext(ctx, `
SELECT terminal_retention_ns
FROM control_operation_policy WHERE id = ?`, operationPolicyRowID).Scan(&terminalNS)
		}
	}
	if err != nil {
		return 0, fmt.Errorf("controlclient: read operation retention policy: %w", err)
	}
	terminal := time.Duration(terminalNS)
	if terminal <= 0 {
		return 0, errors.New("controlclient: invalid operation retention policy")
	}
	if s.initialized {
		if terminal != s.effectiveRetention {
			return 0, ErrOperationRetentionPolicyChanged
		}
		return terminal, nil
	}
	if s.retentionExplicit && terminal != s.retention.TerminalRetention {
		terminal = s.retention.TerminalRetention
		if _, err := s.db.ExecContext(ctx, `
UPDATE control_operation_policy
SET terminal_retention_ns = ?
WHERE id = ?`, int64(terminal), operationPolicyRowID); err != nil {
			return 0, fmt.Errorf("controlclient: update operation retention policy: %w", err)
		}
	}
	s.effectiveRetention = terminal
	s.initialized = true
	return terminal, nil
}

func (s *SQLiteOperationStore) AcquireExecution(ctx context.Context, intent OperationIntent) (OperationExecutionLease, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("controlclient: operation store database is unavailable")
	}
	key := "sqlite:" + s.path + ":" + operationKey(intent.PrincipalID, intent.OperationID)
	return acquireOperationExecutionGate(ctx, key)
}

func (s *SQLiteOperationStore) Begin(ctx context.Context, intent OperationIntent) (OperationRecord, bool, error) {
	s.opportunisticSweep(ctx)
	retention, err := s.ensureRetentionPolicy(ctx)
	if err != nil {
		return OperationRecord{}, false, err
	}
	ctx = contextOrBackground(ctx)
	principalID := strings.TrimSpace(intent.PrincipalID)
	operationID := strings.TrimSpace(intent.OperationID)
	for range operationSQLMaxRetry {
		record, raw, err := s.loadOperationRecord(ctx, principalID, operationID)
		if errors.Is(err, sql.ErrNoRows) {
			intent.CreatedAt = operationStoreNow(s.now)
			record = OperationRecord{
				Version: operationRecordSchemaVersion, Intent: intent,
				TerminalRetentionNanoseconds: int64(retention), UpdatedAt: intent.CreatedAt,
			}
			inserted, err := s.insertOperationRecord(ctx, record)
			if err != nil {
				return OperationRecord{}, false, err
			}
			if inserted {
				return cloneOperationRecord(record), true, nil
			}
			continue
		}
		if err != nil {
			return OperationRecord{}, false, err
		}
		disposition, _, err := classifyOperationRecord(record, operationStoreNow(s.now), s.effectiveRetention)
		if err != nil {
			return OperationRecord{}, false, err
		}
		if disposition == operationRecordExpiredTerminal {
			_, err := s.deleteOperationRecordCAS(ctx, principalID, operationID, raw)
			if err != nil {
				return OperationRecord{}, false, err
			}
			continue
		}
		materialized, changed, err := materializeTerminalRetention(record, s.effectiveRetention)
		if err != nil {
			return OperationRecord{}, false, err
		}
		if changed {
			updated, err := s.updateOperationRecordCAS(ctx, raw, materialized)
			if err != nil {
				return OperationRecord{}, false, err
			}
			if !updated {
				continue
			}
			record = materialized
		}
		if !sameOperationIntent(record.Intent, intent) {
			return OperationRecord{}, false, ErrOperationConflict
		}
		return cloneOperationRecord(record), false, nil
	}
	return OperationRecord{}, false, errors.New("controlclient: operation store contention did not converge")
}

func (s *SQLiteOperationStore) Complete(ctx context.Context, intent OperationIntent, result CommandResult) (OperationRecord, error) {
	if strings.TrimSpace(result.OperationID) != strings.TrimSpace(intent.OperationID) {
		return OperationRecord{}, ErrOperationConflict
	}
	if !result.Outcome.Valid() {
		return OperationRecord{}, errors.New("controlclient: valid operation outcome is required")
	}
	if _, err := s.ensureRetentionPolicy(ctx); err != nil {
		return OperationRecord{}, err
	}
	ctx = contextOrBackground(ctx)
	principalID := strings.TrimSpace(intent.PrincipalID)
	operationID := strings.TrimSpace(intent.OperationID)
	for range operationSQLMaxRetry {
		record, raw, err := s.loadOperationRecord(ctx, principalID, operationID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !sameOperationIntent(record.Intent, intent)) {
			return OperationRecord{}, ErrOperationConflict
		}
		if err != nil {
			return OperationRecord{}, err
		}
		if record.Result != nil {
			if !sameCommandResult(*record.Result, result) {
				return cloneOperationRecord(record), ErrOperationConflict
			}
			return cloneOperationRecord(record), nil
		}
		copyResult := cloneCommandResult(result)
		record.Result = &copyResult
		record.Version = operationRecordSchemaVersion
		if record.TerminalRetentionNanoseconds <= 0 {
			record.TerminalRetentionNanoseconds = int64(s.effectiveRetention)
		}
		record.UpdatedAt = monotonicOperationTime(operationStoreNow(s.now), record.UpdatedAt, record.Intent.CreatedAt)
		if terminalOperationOutcome(result.Outcome) {
			record.RetainUntil = record.UpdatedAt.Add(time.Duration(record.TerminalRetentionNanoseconds))
		} else {
			record.RetainUntil = time.Time{}
		}
		updated, err := s.updateOperationRecordCAS(ctx, raw, record)
		if err != nil {
			return OperationRecord{}, err
		}
		if updated {
			return cloneOperationRecord(record), nil
		}
	}
	return OperationRecord{}, errors.New("controlclient: operation completion contention did not converge")
}

func (s *SQLiteOperationStore) loadOperationRecord(ctx context.Context, principalID, operationID string) (OperationRecord, []byte, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `
SELECT record_json FROM control_operations
WHERE principal_id = ? AND operation_id = ?`, principalID, operationID).Scan(&raw)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	var record OperationRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return OperationRecord{}, nil, fmt.Errorf("controlclient: decode operation record: %w", err)
	}
	if err := validateOperationRecord(record); err != nil {
		return OperationRecord{}, nil, err
	}
	if strings.TrimSpace(record.Intent.PrincipalID) != principalID || strings.TrimSpace(record.Intent.OperationID) != operationID {
		return OperationRecord{}, nil, errors.New("controlclient: operation row does not match its intent")
	}
	return cloneOperationRecord(record), append([]byte(nil), raw...), nil
}

func (s *SQLiteOperationStore) insertOperationRecord(ctx context.Context, record OperationRecord) (bool, error) {
	raw, outcome, retainUntil, err := encodeSQLiteOperationRecord(record)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO control_operations
	(principal_id, operation_id, action, session_id, outcome, created_at_ns, updated_at_ns, retain_until_ns, record_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(record.Intent.PrincipalID), strings.TrimSpace(record.Intent.OperationID), string(record.Intent.Action),
		strings.TrimSpace(record.Intent.SessionID), outcome, record.Intent.CreatedAt.UnixNano(), record.UpdatedAt.UnixNano(), retainUntil, raw)
	if err != nil {
		return false, fmt.Errorf("controlclient: insert operation record: %w", err)
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *SQLiteOperationStore) updateOperationRecordCAS(ctx context.Context, previous []byte, record OperationRecord) (bool, error) {
	raw, outcome, retainUntil, err := encodeSQLiteOperationRecord(record)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE control_operations
SET action = ?, session_id = ?, outcome = ?, created_at_ns = ?, updated_at_ns = ?, retain_until_ns = ?, record_json = ?
WHERE principal_id = ? AND operation_id = ? AND record_json = ?`,
		string(record.Intent.Action), strings.TrimSpace(record.Intent.SessionID), outcome, record.Intent.CreatedAt.UnixNano(),
		record.UpdatedAt.UnixNano(), retainUntil, raw, strings.TrimSpace(record.Intent.PrincipalID),
		strings.TrimSpace(record.Intent.OperationID), previous)
	if err != nil {
		return false, fmt.Errorf("controlclient: update operation record: %w", err)
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *SQLiteOperationStore) deleteOperationRecordCAS(ctx context.Context, principalID, operationID string, previous []byte) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
DELETE FROM control_operations
WHERE principal_id = ? AND operation_id = ? AND record_json = ?`, principalID, operationID, previous)
	if err != nil {
		return false, fmt.Errorf("controlclient: delete operation record: %w", err)
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func encodeSQLiteOperationRecord(record OperationRecord) ([]byte, string, int64, error) {
	if err := validateOperationRecord(record); err != nil {
		return nil, "", 0, err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, "", 0, fmt.Errorf("controlclient: encode operation record: %w", err)
	}
	if len(raw) > maxOperationStoreJSONSize {
		return nil, "", 0, errors.New("controlclient: operation store JSON exceeds size limit")
	}
	outcome := ""
	if record.Result != nil {
		outcome = string(record.Result.Outcome)
	}
	retainUntil := int64(0)
	if !record.RetainUntil.IsZero() {
		retainUntil = record.RetainUntil.UnixNano()
	}
	return raw, outcome, retainUntil, nil
}

// Sweep removes one bounded batch of proven terminal rows. Indeterminate rows
// have no deletion deadline and are never selected.
func (s *SQLiteOperationStore) Sweep(ctx context.Context) (OperationSweepResult, error) {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	if _, err := s.ensureRetentionPolicy(ctx); err != nil {
		return OperationSweepResult{}, err
	}
	ctx = contextOrBackground(ctx)
	nowTime := operationStoreNow(s.now)
	now := nowTime.UnixNano()
	cursor := s.sweepCursor
	rows, err := s.db.QueryContext(ctx, `
SELECT principal_id, operation_id, action, session_id, outcome,
       created_at_ns, updated_at_ns, retain_until_ns, record_json
FROM control_operations
WHERE retain_until_ns > 0 AND retain_until_ns <= ?
  AND (? = 0 OR retain_until_ns > ?
       OR (retain_until_ns = ? AND principal_id > ?)
       OR (retain_until_ns = ? AND principal_id = ? AND operation_id > ?))
ORDER BY retain_until_ns, principal_id, operation_id
LIMIT ?`, now, boolToSQLite(cursor.valid), cursor.retainUntilNS,
		cursor.retainUntilNS, cursor.principalID,
		cursor.retainUntilNS, cursor.principalID, cursor.operationID,
		s.retention.SweepBatchSize+1)
	if err != nil {
		return OperationSweepResult{}, fmt.Errorf("controlclient: select expired operation records: %w", err)
	}
	type candidate struct {
		principalID   string
		operationID   string
		action        string
		sessionID     string
		outcome       string
		createdAtNS   int64
		updatedAtNS   int64
		retainUntilNS int64
		raw           []byte
	}
	candidates := make([]candidate, 0, s.retention.SweepBatchSize+1)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(
			&item.principalID, &item.operationID, &item.action, &item.sessionID, &item.outcome,
			&item.createdAtNS, &item.updatedAtNS, &item.retainUntilNS, &item.raw,
		); err != nil {
			rows.Close()
			return OperationSweepResult{}, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return OperationSweepResult{}, err
	}
	if len(candidates) == 0 {
		s.sweepCursor = sqliteOperationSweepCursor{}
		return OperationSweepResult{}, nil
	}
	result := OperationSweepResult{}
	processLimit := min(len(candidates), s.retention.SweepBatchSize)
	started := time.Now()
	for _, candidate := range candidates[:processLimit] {
		if result.Scanned > 0 && time.Since(started) >= s.retention.SweepTimeLimit {
			break
		}
		if result.RemovedTerminal >= s.retention.SweepDeleteLimit {
			break
		}
		result.Scanned++
		s.sweepCursor = sqliteOperationSweepCursor{
			valid: true, retainUntilNS: candidate.retainUntilNS,
			principalID: candidate.principalID, operationID: candidate.operationID,
		}
		var record OperationRecord
		if err := json.Unmarshal(candidate.raw, &record); err != nil {
			result.Corrupt++
			continue
		}
		_, outcome, retainUntil, err := encodeSQLiteOperationRecord(record)
		if err != nil || strings.TrimSpace(record.Intent.PrincipalID) != candidate.principalID ||
			strings.TrimSpace(record.Intent.OperationID) != candidate.operationID ||
			string(record.Intent.Action) != candidate.action || strings.TrimSpace(record.Intent.SessionID) != candidate.sessionID ||
			outcome != candidate.outcome || record.Intent.CreatedAt.UnixNano() != candidate.createdAtNS ||
			record.UpdatedAt.UnixNano() != candidate.updatedAtNS || retainUntil != candidate.retainUntilNS {
			result.Corrupt++
			continue
		}
		disposition, _, err := classifyOperationRecord(record, nowTime, s.effectiveRetention)
		if err != nil {
			result.Corrupt++
			continue
		}
		if disposition != operationRecordExpiredTerminal {
			if disposition == operationRecordRetainedTerminal {
				result.RetainedTerminal++
			} else {
				result.RetainedIndeterminate++
			}
			continue
		}
		removed, err := s.deleteOperationRecordCAS(ctx, candidate.principalID, candidate.operationID, candidate.raw)
		if err != nil {
			return result, err
		}
		if removed {
			result.RemovedTerminal++
		}
	}
	result.More = result.Scanned < len(candidates) || len(candidates) > s.retention.SweepBatchSize
	if !result.More {
		s.sweepCursor = sqliteOperationSweepCursor{}
	}
	return result, nil
}

func boolToSQLite(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *SQLiteOperationStore) opportunisticSweep(ctx context.Context) {
	if s == nil {
		return
	}
	s.sweepMu.Lock()
	now := operationStoreNow(s.now)
	if !s.nextSweep.IsZero() && now.Before(s.nextSweep) {
		s.sweepMu.Unlock()
		return
	}
	s.nextSweep = now.Add(s.retention.SweepInterval)
	s.sweepMu.Unlock()
	sweepCtx, cancel := context.WithTimeout(contextOrBackground(ctx), s.retention.SweepTimeLimit)
	defer cancel()
	result, err := s.Sweep(sweepCtx)
	if err == nil && result.More {
		s.sweepMu.Lock()
		s.nextSweep = now
		s.sweepMu.Unlock()
	}
}

func (s *SQLiteOperationStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

var _ DurableOperationStore = (*SQLiteOperationStore)(nil)
