package appserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

// SessionWorkbenchStore persists read positions and a content-free summary of
// completed main Turns. It is owned by Control and never participates in replay.
type SessionWorkbenchStore struct {
	db     *sql.DB
	events SessionWorkbenchEvents
}

// NewSessionWorkbenchStore initializes the overview tables on the Host Control DB.
// The caller owns db and keeps it open for the lifetime of the service.
func NewSessionWorkbenchStore(ctx context.Context, db *sql.DB, events SessionWorkbenchEvents) (*SessionWorkbenchStore, error) {
	if err := initSessionWorkbench(ctx, db); err != nil {
		return nil, fmt.Errorf("initialize Session workbench: %w", err)
	}
	return &SessionWorkbenchStore{db: db, events: events}, nil
}

func initSessionWorkbench(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS session_workbench_results (
    session_id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    result_seq INTEGER NOT NULL,
    failed INTEGER NOT NULL,
    outcome_order INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS session_workbench_results_owner ON session_workbench_results(owner_id, result_seq);
CREATE TABLE IF NOT EXISTS session_workbench_reads (
    principal_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    through_seq INTEGER NOT NULL,
    PRIMARY KEY(principal_id, session_id)
);
CREATE TABLE IF NOT EXISTS session_workbench_pending_turns (
    turn_order INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    after_seq INTEGER NOT NULL
);
`)
	if err != nil {
		return err
	}
	// Workbench stores created before ordered outcomes keep their reads and status.
	var hasOrder bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pragma_table_info('session_workbench_results') WHERE name = 'outcome_order')`).Scan(&hasOrder); err != nil {
		return err
	}
	if !hasOrder {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE session_workbench_results ADD COLUMN outcome_order INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SessionWorkbenchStore) acknowledge(ctx context.Context, principal, id string, seq uint64) error {
	if seq > math.MaxInt64 {
		return errorcode.New(errorcode.InvalidArgument, "read sequence is outside the Session event range")
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO session_workbench_reads (principal_id, session_id, through_seq)
SELECT ?, session_id, ? FROM session_workbench_results WHERE session_id = ? AND result_seq >= ?
ON CONFLICT(principal_id, session_id) DO UPDATE SET through_seq = MAX(through_seq, excluded.through_seq)
`, principal, seq, id, seq)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 0 {
		return err
	}

	// A canonical replay can expose a durable result before its overview index
	// is written, or display an older result that predates the workbench.
	ref := session.SessionRef{SessionID: id}
	page, err := s.events.EventsPage(ctx, session.EventPageRequest{
		SessionRef: ref, AfterSeq: seq - 1, ThroughSeq: seq, Limit: 1, Visibility: session.EventPageClientReplay,
	})
	if err != nil {
		return err
	}
	if len(page.Events) == 0 || projection.DurableSessionResultSequence(ref, page.Events[0]) != seq {
		return errorcode.New(errorcode.InvalidArgument, "read sequence does not identify a durable Session result")
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO session_workbench_reads (principal_id, session_id, through_seq) VALUES (?, ?, ?)
ON CONFLICT(principal_id, session_id) DO UPDATE SET through_seq = MAX(through_seq, excluded.through_seq)
`, principal, id, seq)
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
