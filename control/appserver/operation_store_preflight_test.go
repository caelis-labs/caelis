package appserver

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSQLiteOperationStoreReadOnlyChecksDurableRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	store, err := NewSQLiteOperationStoreWithConfig(path, OperationRetentionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Initialize(ctx); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	intent := OperationIntent{
		PrincipalID: "preflight-principal", OperationID: "preflight-operation",
		Action: ActionPrompt, SessionID: "preflight-session", Digest: "preflight-digest",
	}
	if _, _, err := store.Begin(ctx, intent); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSQLiteOperationStoreReadOnly(ctx, path); err != nil {
		t.Fatalf("ValidateSQLiteOperationStoreReadOnly(valid) error = %v", err)
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE control_operations SET record_json = ?`, []byte(`{"version":99}`)); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSQLiteOperationStoreReadOnly(ctx, path); err == nil || !strings.Contains(err.Error(), "unsupported operation record version") {
		t.Fatalf("ValidateSQLiteOperationStoreReadOnly(future record) error = %v", err)
	}
}

func TestValidateSQLiteOperationStoreReadOnlyRejectsMissingCompositePrimaryKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`
CREATE TABLE control_operation_policy (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	version INTEGER NOT NULL,
	terminal_retention_ns INTEGER NOT NULL
);
CREATE TABLE control_operations (
	principal_id TEXT NOT NULL,
	operation_id TEXT NOT NULL,
	action TEXT NOT NULL,
	session_id TEXT NOT NULL,
	outcome TEXT NOT NULL,
	created_at_ns INTEGER NOT NULL,
	updated_at_ns INTEGER NOT NULL,
	retain_until_ns INTEGER NOT NULL,
	record_json BLOB NOT NULL
);
CREATE INDEX control_operations_updated_idx
	ON control_operations(updated_at_ns DESC);
CREATE INDEX control_operations_action_updated_idx
	ON control_operations(action, updated_at_ns DESC);
CREATE INDEX control_operations_session_updated_idx
	ON control_operations(session_id, updated_at_ns DESC)
	WHERE session_id <> '';
CREATE INDEX control_operations_retention_idx
	ON control_operations(retain_until_ns)
	WHERE retain_until_ns > 0;
`)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	err = ValidateSQLiteOperationStoreReadOnly(t.Context(), path)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "primary key") {
		t.Fatalf("ValidateSQLiteOperationStoreReadOnly(missing composite primary key) error = %v", err)
	}
}

func TestValidateSQLiteOperationStoreReadOnlyRejectsMissingRequiredIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	store, err := NewSQLiteOperationStoreWithConfig(path, OperationRetentionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Initialize(t.Context()); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP INDEX control_operations_retention_idx`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	err = ValidateSQLiteOperationStoreReadOnly(t.Context(), path)
	if err == nil || !strings.Contains(err.Error(), "required index") {
		t.Fatalf("ValidateSQLiteOperationStoreReadOnly(missing required index) error = %v", err)
	}
}
