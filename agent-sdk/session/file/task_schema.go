package file

import (
	"database/sql"
	"fmt"
)

func (s *Store) openTaskIndex() (*sql.DB, error) {
	return s.openIndex(ensureTaskIndexSchema)
}

func ensureTaskIndexSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("agent-sdk/session/file: read session index version: %w", err)
	}
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS tasks (
	task_id TEXT PRIMARY KEY,
	revision INTEGER NOT NULL,
	kind TEXT NOT NULL,
	app_name TEXT NOT NULL,
	user_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	workspace_key TEXT NOT NULL,
	title TEXT NOT NULL,
	state TEXT NOT NULL,
	failure_diagnostic TEXT NOT NULL,
	running INTEGER NOT NULL,
	supports_input INTEGER NOT NULL,
	supports_cancel INTEGER NOT NULL,
	created_at_ns INTEGER NOT NULL,
	updated_at_ns INTEGER NOT NULL,
	heartbeat_at_ns INTEGER NOT NULL,
	lease_id TEXT NOT NULL,
	lease_owner_id TEXT NOT NULL,
	lease_revision INTEGER NOT NULL,
	lease_acquired_at_ns INTEGER NOT NULL,
	lease_expires_at_ns INTEGER NOT NULL,
	stdout_cursor INTEGER NOT NULL,
	stderr_cursor INTEGER NOT NULL,
	event_cursor INTEGER NOT NULL,
	handle TEXT NOT NULL,
	spec_json TEXT NOT NULL,
	context_usage_json TEXT NOT NULL,
	result_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	terminal_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS tasks_session_updated_idx ON tasks(session_id, updated_at_ns DESC);
CREATE INDEX IF NOT EXISTS tasks_session_kind_handle_idx ON tasks(session_id, kind, handle);
CREATE INDEX IF NOT EXISTS tasks_running_session_idx ON tasks(session_id) WHERE running != 0;
`)
	if err != nil {
		return fmt.Errorf("agent-sdk/session/file: initialize task table: %w", err)
	}
	if err := ensureTaskIndexColumns(db); err != nil {
		return err
	}
	if version != indexVersion {
		if _, err := db.Exec(`PRAGMA user_version = ` + fmt.Sprint(indexVersion)); err != nil {
			return fmt.Errorf("agent-sdk/session/file: set session index version: %w", err)
		}
	}
	return nil
}

func ensureTaskIndexColumns(db *sql.DB) error {
	columns := map[string]string{
		"revision":             "INTEGER NOT NULL DEFAULT 0",
		"failure_diagnostic":   "TEXT NOT NULL DEFAULT ''",
		"lease_id":             "TEXT NOT NULL DEFAULT ''",
		"lease_owner_id":       "TEXT NOT NULL DEFAULT ''",
		"lease_revision":       "INTEGER NOT NULL DEFAULT 0",
		"lease_acquired_at_ns": "INTEGER NOT NULL DEFAULT 0",
		"lease_expires_at_ns":  "INTEGER NOT NULL DEFAULT 0",
		"context_usage_json":   "TEXT NOT NULL DEFAULT 'null'",
	}
	rows, err := db.Query(`PRAGMA table_info(tasks)`)
	if err != nil {
		return fmt.Errorf("agent-sdk/session/file: inspect task index schema: %w", err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("agent-sdk/session/file: scan task index schema: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for name, definition := range columns {
		if existing[name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN ` + name + ` ` + definition); err != nil {
			return fmt.Errorf("agent-sdk/session/file: add task index column %s: %w", name, err)
		}
	}
	return nil
}
