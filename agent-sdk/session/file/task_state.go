package file

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

var _ task.StateReader = (*TaskStore)(nil)

// ReadTaskStates reads only lifecycle fields and the steering flag, without
// decoding or copying potentially large Task specifications and results.
func (s *TaskStore) ReadTaskStates(ctx context.Context, ref session.SessionRef, taskIDs []string) ([]task.StateSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("agent-sdk/session/file: task store is not initialized")
	}
	ref = session.NormalizeSessionRef(ref)
	if ref.SessionID == "" {
		return []task.StateSnapshot{}, nil
	}
	if err := s.store.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.store.mu.Unlock()
	var out []task.StateSnapshot
	err := s.store.withRootReadLockContext(ctx, func() error {
		var err error
		out, err = s.store.readTaskStates(ctx, ref, taskIDs)
		return err
	})
	return out, err
}

func (s *Store) readTaskStates(ctx context.Context, ref session.SessionRef, taskIDs []string) ([]task.StateSnapshot, error) {
	db, err := s.openSessionIndex()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Reuse the canonical metadata decoder: malformed values anywhere in that
	// map must still fail, including number overflow and duplicate-key behavior.
	// Large specification/result/context/terminal payloads are never selected.
	query := `SELECT task_id, revision, state, metadata_json FROM tasks WHERE session_id = ?`
	args := []any{ref.SessionID}
	if len(taskIDs) > 0 {
		query += ` AND task_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(taskIDs)), ",") + `)`
		for _, id := range taskIDs {
			args = append(args, strings.TrimSpace(id))
		}
	}
	query += ` ORDER BY updated_at_ns DESC, task_id ASC`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: read task states: %w", err)
	}
	defer rows.Close()
	out := []task.StateSnapshot{}
	for rows.Next() {
		var state task.StateSnapshot
		var metadataJSON string
		if err := rows.Scan(&state.TaskID, &state.Revision, &state.State, &metadataJSON); err != nil {
			return nil, fmt.Errorf("agent-sdk/session/file: scan task state: %w", err)
		}
		metadata, err := decodeTaskIndexMap(metadataJSON, "metadata")
		if err != nil {
			return nil, err
		}
		state.SupportsSteering, _ = metadata["supports_steering"].(bool)
		out = append(out, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: scan task states: %w", err)
	}
	return out, nil
}
