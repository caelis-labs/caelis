package collaboration

import (
	"context"
	"errors"
	"hash/fnv"
	"strings"
	"sync"
	"time"
)

const closedSessionSweepInterval = time.Minute

var sessionTables = [...]string{
	"collaboration_mailbox", "collaboration_messages", "collaboration_readers",
	"collaboration_seen", "collaboration_retention", "collaboration_setups",
}

func (s *Service) mailLock(id string) *sync.Mutex {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return &s.mailLocks[h.Sum64()%uint64(len(s.mailLocks))]
}

// InterruptSession discards pending Agent mail and fails queued user inputs,
// preserving shared history, reader positions, and sending or terminal receipts.
// The Host must first revoke old members' input authority in canonical membership
// and cancel their in-flight Runtime admissions. This method does not revoke
// membership or prevent new authorized work in the Session.
func (s *Service) InterruptSession(ctx context.Context, id string) error {
	lock := s.mailLock(id)
	lock.Lock()
	defer lock.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `DELETE FROM collaboration_mailbox WHERE session=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE collaboration_user_inputs SET state='failed',detail='Session interrupted before delivery',updated=? WHERE session=? AND state='queued'`, s.now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// CleanupClosedSession discards retained collaboration data only after the
// backend proves permanent Session closure. Active Sessions are unchanged;
// unavailable lifecycle state returns an error without authorizing cleanup.
func (s *Service) CleanupClosedSession(ctx context.Context, id string) error {
	_, err := s.backend.List(ctx, id)
	if errors.Is(err, ErrSessionClosed) {
		return s.purgeClosedSession(ctx, id)
	}
	return err
}

func (s *Service) purgeClosedSession(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range sessionTables {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE session=?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Sweep independently of delivery: a closed Session can retain only shared
// history, a reader position or a setup marker after its mailbox was drained.
// The initial sweep also reconciles closure interrupted by Host shutdown.
func (s *Service) runClosedSessionCleanup(ctx context.Context, report func(error)) {
	tick := time.NewTicker(closedSessionSweepInterval)
	defer tick.Stop()
	for {
		s.cleanupClosedSessions(ctx, report)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Service) cleanupClosedSessions(ctx context.Context, report func(error)) {
	reportError := func(err error) {
		if err != nil && ctx.Err() == nil && report != nil {
			report(err)
		}
	}
	queries := make([]string, len(sessionTables))
	for i, table := range sessionTables {
		queries[i] = "SELECT session FROM " + table
	}
	query := "SELECT session FROM (" + strings.Join(queries, " UNION ") + ") WHERE session>? ORDER BY session LIMIT 128"
	for after := ""; ctx.Err() == nil; {
		rows, err := s.db.QueryContext(ctx, query, after)
		if err != nil {
			reportError(err)
			return
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			reportError(err)
			return
		}
		if len(ids) == 0 {
			return
		}
		for _, id := range ids {
			attempt, cancel := context.WithTimeout(ctx, lifecycleQueryTimeout)
			reportError(s.CleanupClosedSession(attempt, id))
			cancel()
		}
		after = ids[len(ids)-1]
	}
}
