package application

import (
	"context"
	"fmt"
)

// Worker is an application grant to an ordinary native Session. It carries no
// execution profile, callback catalog, private memory, or transcript.
type Worker struct {
	Scope
	SessionID string `json:"session_id"`
}

// WorkerSessionID reserves a distinct, deterministic native Session address.
func WorkerSessionID(scope Scope, operation string) string {
	return stableID("worker-", scope.PrincipalID, scope.ApplicationID, scope.ConnectionID, operation)
}

// PutWorker records an immutable creation grant before native Session creation.
// Only the Host creation path may call it, after persisting the creation intent.
func (s *Store) PutWorker(ctx context.Context, scope Scope, operation string) (Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := Worker{Scope: scope, SessionID: WorkerSessionID(scope, operation)}
	if !validID(operation) {
		return Worker{}, ErrInvalid
	}
	if _, err := s.active(ctx, scope); err != nil {
		return Worker{}, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_workers(session,principal,application,connection) VALUES(?,?,?,?) ON CONFLICT(session) DO NOTHING`, w.SessionID, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID)
	return w, err
}

// Worker authorizes only the exact enrolled connection, including readback
// after lease expiry. Mutation callers must additionally check CheckActive.
func (s *Store) Worker(ctx context.Context, scope Scope, id string) (Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.connection(ctx, scope); err != nil {
		return Worker{}, err
	}
	w := Worker{}
	err := s.db.QueryRowContext(ctx, `SELECT session,principal,application,connection FROM app_workers WHERE session=? AND principal=? AND application=? AND connection=?`, id, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID).Scan(&w.SessionID, &w.PrincipalID, &w.ApplicationID, &w.ConnectionID)
	return w, notFound(err)
}

// Workers lists only grants owned by the authenticated connection. A grant can
// precede Session creation; inspect its creation receipt before sending work.
func (s *Store) Workers(ctx context.Context, scope Scope) ([]Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.connection(ctx, scope); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT session FROM app_workers WHERE principal=? AND application=? AND connection=? ORDER BY session`, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Worker{}
	for rows.Next() {
		w := Worker{Scope: scope}
		if err := rows.Scan(&w.SessionID); err != nil {
			return nil, fmt.Errorf("read worker grant: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
