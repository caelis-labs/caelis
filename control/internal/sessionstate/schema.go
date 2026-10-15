// Package sessionstate defines the durable adjuncts of canonical Sessions.
// Session history, execution and approvals remain owned by the native Runtime.
package sessionstate

import (
	"context"
	"database/sql"
)

// Version is the principal-owned Session adjunct Store format.
const Version = 3

// SchemaDatabase permits schema creation in the startup migration transaction.
type SchemaDatabase interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// EnsureSchema creates only Session adjunct tables, never a transcript or ledger.
func EnsureSchema(ctx context.Context, db SchemaDatabase) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS session_profiles (
 session TEXT PRIMARY KEY, principal TEXT NOT NULL, body BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS session_configurations (
 session TEXT NOT NULL, revision INTEGER NOT NULL, body BLOB NOT NULL,
 PRIMARY KEY(session,revision), FOREIGN KEY(session) REFERENCES session_profiles(session)
);
CREATE TABLE IF NOT EXISTS client_tool_calls (
 principal TEXT NOT NULL, session TEXT NOT NULL, call TEXT NOT NULL,
 turn TEXT NOT NULL, item TEXT NOT NULL, digest TEXT NOT NULL, body BLOB NOT NULL,
 state TEXT NOT NULL, result BLOB, PRIMARY KEY(session,call), UNIQUE(session,turn,item)
);
CREATE INDEX IF NOT EXISTS client_tool_calls_pending ON client_tool_calls(principal,session) WHERE state='pending';
CREATE TABLE IF NOT EXISTS session_resources (
 id TEXT PRIMARY KEY, principal TEXT NOT NULL, session TEXT NOT NULL,
 operation TEXT NOT NULL, digest TEXT NOT NULL, body BLOB NOT NULL, data BLOB NOT NULL,
 UNIQUE(session,operation)
);
CREATE TABLE IF NOT EXISTS session_background_grants (
 id TEXT PRIMARY KEY, principal TEXT NOT NULL, session TEXT NOT NULL,
 operation TEXT NOT NULL, source TEXT NOT NULL, authorization_operation TEXT NOT NULL,
 revoked INTEGER NOT NULL DEFAULT 0, UNIQUE(principal,operation)
);`)
	return err
}
