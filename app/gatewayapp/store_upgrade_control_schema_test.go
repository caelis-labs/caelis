package gatewayapp

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareStoreUpgradeRejectsControlSchemaBeforeMemoryPrepare(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "upgrade-control-schema-test", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "upgrade-control-schema-workspace", WorkspaceCWD: workspace,
		Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}

	controlPath := controlStoreDatabasePath(storeDir)
	if err := os.Remove(controlPath); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", controlPath)
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
`)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := PrepareStoreUpgrade(t.Context(), storeDir); err == nil || !strings.Contains(strings.ToLower(err.Error()), "primary key") {
		t.Fatalf("PrepareStoreUpgrade() error = %v, want missing composite primary key rejection", err)
	}
	if _, err := os.Stat(filepath.Join(storeDir, storeUpgradeJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upgrade journal after rejected Control preflight = %v; Memory prepare must not run", err)
	}
}
