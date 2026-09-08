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
