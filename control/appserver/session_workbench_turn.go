package appserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

// SessionWorkbenchEvents supplies the durable positions and results used to
// rebuild interrupted Turn projections.
type SessionWorkbenchEvents interface {
	session.EventCheckpointReader
	session.PagedReader
}

// SessionWorkbenchTurn identifies one admitted main Turn and its starting cut.
type SessionWorkbenchTurn struct {
	order    int64
	ref      session.SessionRef
	id       string
	afterSeq uint64
}

// BeginTurn persists a recovery position before the producer can append results.
// SQLite assigns the order while the Kernel still owns the Session's active slot.
func (s *SessionWorkbenchStore) BeginTurn(ctx context.Context, ref session.SessionRef, turnID string) (SessionWorkbenchTurn, error) {
	cut, err := s.events.EventCheckpoint(ctx, ref)
	if err != nil {
		return SessionWorkbenchTurn{}, err
	}
	turn := SessionWorkbenchTurn{ref: cut.Session.SessionRef, id: turnID, afterSeq: cut.ThroughSeq}
	err = s.db.QueryRowContext(ctx, `
INSERT INTO session_workbench_pending_turns (session_id, owner_id, turn_id, after_seq)
VALUES (?, ?, ?, ?) RETURNING turn_order
`, turn.ref.SessionID, turn.ref.UserID, turn.id, turn.afterSeq).Scan(&turn.order)
	return turn, err
}

// RecordResult publishes a committed result while its Turn is still active.
func (s *SessionWorkbenchStore) RecordResult(ctx context.Context, turn SessionWorkbenchTurn, seq uint64) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO session_workbench_results (session_id, owner_id, result_seq, failed)
VALUES (?, ?, ?, 0)
ON CONFLICT(session_id) DO UPDATE SET result_seq = excluded.result_seq
WHERE excluded.result_seq > result_seq
`, turn.ref.SessionID, turn.ref.UserID, seq)
	return err
}

// FinishTurn reconciles the producer's durable results, records its confirmed
// outcome, and clears the recovery position in a single projection transaction.
func (s *SessionWorkbenchStore) FinishTurn(ctx context.Context, turn SessionWorkbenchTurn, lastObserved uint64, state string) (uint64, error) {
	seq, err := s.turnResult(ctx, turn, lastObserved)
	if err != nil {
		return 0, err
	}
	order := turn.order
	if state == eventstream.LifecycleStateUnknown {
		order = 0
	}
	return seq, s.settleTurn(ctx, turn, seq, order, state == eventstream.LifecycleStateFailed)
}

func (s *SessionWorkbenchStore) turnResult(ctx context.Context, turn SessionWorkbenchTurn, lastObserved uint64) (uint64, error) {
	cut, err := s.events.EventCheckpoint(ctx, turn.ref)
	if err != nil {
		return 0, err
	}
	seq := lastObserved
	for after := max(turn.afterSeq, seq); after < cut.ThroughSeq; {
		page, err := s.events.EventsPage(ctx, session.EventPageRequest{
			SessionRef: turn.ref, AfterSeq: after, ThroughSeq: cut.ThroughSeq, Visibility: session.EventPageClientReplay,
		})
		if err != nil {
			return 0, err
		}
		for _, event := range page.Events {
			if event.Scope != nil && event.Scope.TurnID == turn.id {
				seq = max(seq, projection.DurableSessionResultSequence(turn.ref, event))
			}
		}
		if !page.HasMore {
			break
		}
		if page.NextSeq <= after {
			return 0, fmt.Errorf("Session workbench event page did not advance from sequence %d", after)
		}
		after = page.NextSeq
	}
	return seq, nil
}

func (s *SessionWorkbenchStore) settleTurn(ctx context.Context, turn SessionWorkbenchTurn, seq uint64, order int64, failed bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if seq != 0 || order != 0 {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO session_workbench_results (session_id, owner_id, result_seq, failed, outcome_order)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
    result_seq = MAX(result_seq, excluded.result_seq),
    failed = CASE WHEN excluded.outcome_order > outcome_order THEN excluded.failed ELSE failed END,
    outcome_order = MAX(outcome_order, excluded.outcome_order)
`, turn.ref.SessionID, turn.ref.UserID, seq, failed, order); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_workbench_pending_turns WHERE turn_order = ?`, turn.order); err != nil {
		return err
	}
	return tx.Commit()
}

// RecoverPending rebuilds the results of interrupted projections at Host
// startup, before new producers can enroll. A pending Turn alone proves no
// terminal outcome.
func (s *SessionWorkbenchStore) RecoverPending(ctx context.Context) error {
	pending, err := s.pendingTurns(ctx)
	if err != nil {
		return err
	}
	for _, turn := range pending {
		seq, err := s.turnResult(ctx, turn, 0)
		if err != nil && !errors.Is(err, session.ErrSessionNotFound) {
			return fmt.Errorf("recover Session workbench Turn %q: %w", turn.id, err)
		}
		if err := s.settleTurn(ctx, turn, seq, 0, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionWorkbenchStore) pendingTurns(ctx context.Context) ([]SessionWorkbenchTurn, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT turn_order, session_id, owner_id, turn_id, after_seq FROM session_workbench_pending_turns ORDER BY turn_order
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []SessionWorkbenchTurn
	for rows.Next() {
		var turn SessionWorkbenchTurn
		if err := rows.Scan(&turn.order, &turn.ref.SessionID, &turn.ref.UserID, &turn.id, &turn.afterSeq); err != nil {
			return nil, err
		}
		turns = append(turns, turn)
	}
	return turns, rows.Err()
}
