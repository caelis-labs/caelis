package appserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
)

// SessionWorkbenchStore persists read positions and a content-free summary of
// completed main Turns. It is owned by Control and never participates in replay.
type SessionWorkbenchStore struct {
	db *sql.DB
}

// RecordResult advances the producer's durable, user-visible result position.
func (s *SessionWorkbenchStore) RecordResult(ctx context.Context, id, owner string, seq uint64) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO session_workbench_results(session_id, owner_id, result_seq, failed)
VALUES (?, ?, ?, 0)
ON CONFLICT(session_id) DO UPDATE SET result_seq = excluded.result_seq
WHERE excluded.result_seq > result_seq
`, id, owner, seq)
	return err
}

// NewSessionWorkbenchStore initializes the overview tables on the Host Control DB.
// The caller owns db and keeps it open for the lifetime of the service.
func NewSessionWorkbenchStore(ctx context.Context, db *sql.DB) (*SessionWorkbenchStore, error) {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS session_workbench_results (
	session_id TEXT PRIMARY KEY,
	owner_id TEXT NOT NULL,
	result_seq INTEGER NOT NULL,
	failed INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS session_workbench_results_owner ON session_workbench_results(owner_id, result_seq);
CREATE TABLE IF NOT EXISTS session_workbench_reads (
	principal_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	through_seq INTEGER NOT NULL,
	PRIMARY KEY(principal_id, session_id)
);
`)
	if err != nil {
		return nil, fmt.Errorf("initialize Session workbench: %w", err)
	}
	return &SessionWorkbenchStore{db: db}, nil
}

// RecordTurn stores the latest confirmed main-Turn outcome and the latest
// completed visible result sequence, if the Turn produced one.
func (s *SessionWorkbenchStore) RecordTurn(ctx context.Context, id, owner string, seq uint64, failed bool) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO session_workbench_results(session_id, owner_id, result_seq, failed)
VALUES (?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
	result_seq = MAX(result_seq, excluded.result_seq), failed = excluded.failed
WHERE result_seq < excluded.result_seq OR failed != excluded.failed
`, id, owner, seq, failed)
	return err
}

func (s *SessionWorkbenchStore) acknowledge(ctx context.Context, principal, id string, seq uint64) error {
	if seq > math.MaxInt64 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO session_workbench_reads(principal_id, session_id, through_seq)
SELECT ?, session_id, ? FROM session_workbench_results
WHERE session_id = ? AND result_seq >= ?
ON CONFLICT(principal_id, session_id) DO UPDATE SET through_seq = excluded.through_seq
WHERE excluded.through_seq > through_seq
`, principal, seq, id, seq)
	return err
}

func (s *SessionWorkbenchStore) unreadSessions(ctx context.Context, principal Principal) ([]string, error) {
	query := `
SELECT r.session_id FROM session_workbench_results AS r
LEFT JOIN session_workbench_reads AS s ON s.session_id = r.session_id AND s.principal_id = ?
WHERE r.result_seq > COALESCE(s.through_seq, 0)`
	args := []any{principal.ID}
	if !principal.HasRole("admin") {
		query += " AND r.owner_id = ?"
		args = append(args, principal.ID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type sessionWorkbenchState struct {
	unread bool
	failed bool
}

func (s *SessionWorkbenchStore) states(ctx context.Context, principal string, ids []string) (map[string]sessionWorkbenchState, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT r.session_id, r.result_seq > COALESCE(s.through_seq, 0), r.failed
FROM session_workbench_results AS r
LEFT JOIN session_workbench_reads AS s ON s.session_id = r.session_id AND s.principal_id = ?
WHERE r.session_id IN (SELECT value FROM json_each(?))
`, principal, string(encoded))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[string]sessionWorkbenchState, len(ids))
	for rows.Next() {
		var id string
		var state sessionWorkbenchState
		if err := rows.Scan(&id, &state.unread, &state.failed); err != nil {
			return nil, err
		}
		states[id] = state
	}
	return states, rows.Err()
}
