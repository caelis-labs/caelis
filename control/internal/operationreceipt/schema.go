package operationreceipt

import (
	"context"
	"database/sql"
	"fmt"
)

// SchemaDatabase supports initialization inside the one Store migration transaction.
type SchemaDatabase interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// EnsureSchema defines the single Control operation ledger. Runtime stores and
// versioned data importers use the same schema; it never admits an operation.
func EnsureSchema(ctx context.Context, db SchemaDatabase) error {
	_, err := db.ExecContext(ctx, `
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
		return fmt.Errorf("control operation schema: %w", err)
	}
	return nil
}
