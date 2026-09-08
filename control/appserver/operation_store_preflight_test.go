package appserver

import (
	"context"
	"database/sql"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestValidateSQLiteOperationStoreReadOnlyRejectsInvalidFilesWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "empty", data: []byte{}},
		{name: "non-sqlite", data: []byte("not a sqlite database")},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "control.sqlite")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotOperationPreflightTree(t, root)
			if err := ValidateSQLiteOperationStoreReadOnly(t.Context(), path); err == nil {
				t.Fatal("ValidateSQLiteOperationStoreReadOnly() succeeded for invalid database")
			}
			after := snapshotOperationPreflightTree(t, root)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("preflight changed the file tree:\n before=%#v\n after=%#v", before, after)
			}
		})
	}
}

func TestValidateSQLiteOperationStoreReadOnlyRejectsSidecarsWithoutMutation(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
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
			if err := os.WriteFile(path+suffix, []byte("pending sidecar"), 0o600); err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(path)
			before := snapshotOperationPreflightTree(t, root)
			if err := ValidateSQLiteOperationStoreReadOnly(t.Context(), path); err == nil || !strings.Contains(err.Error(), "requires owner recovery") {
				t.Fatalf("ValidateSQLiteOperationStoreReadOnly() error = %v, want sidecar rejection", err)
			}
			after := snapshotOperationPreflightTree(t, root)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("sidecar rejection changed the file tree:\n before=%#v\n after=%#v", before, after)
			}
		})
	}
}

func TestValidateSQLiteOperationStoreReadOnlyPreservesCheckpointedWALDatabase(t *testing.T) {
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
	var journalMode string
	if err := database.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&journalMode); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		_ = database.Close()
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	if _, err := database.Exec(`UPDATE control_operation_policy SET terminal_retention_ns = terminal_retention_ns + 1 WHERE id = 1`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	var busy, logFrames, checkpointedFrames int
	if err := database.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if busy != 0 || logFrames != 0 {
		_ = database.Close()
		t.Fatalf("checkpoint result busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointedFrames)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("checkpointed WAL sidecar %q = %v, want absent", suffix, err)
		}
	}

	root := filepath.Dir(path)
	before := snapshotOperationPreflightTree(t, root)
	if err := ValidateSQLiteOperationStoreReadOnly(t.Context(), path); err != nil {
		t.Fatalf("ValidateSQLiteOperationStoreReadOnly(checkpointed WAL) error = %v", err)
	}
	after := snapshotOperationPreflightTree(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("checkpointed WAL preflight changed the file tree:\n before=%#v\n after=%#v", before, after)
	}
}

func TestValidateSQLiteOperationStoreReadOnlyEscapesFilesystemPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control ?#%&.sqlite")
	sqlitePath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(sqlitePath, "/") {
		sqlitePath = "/" + sqlitePath
	}
	writableURI := (&url.URL{Scheme: "file", Path: sqlitePath, RawQuery: "mode=rwc"}).String()
	database, err := sql.Open("sqlite", writableURI)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureSQLiteOperationSchema(database); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO control_operation_policy (id, version, terminal_retention_ns) VALUES (?, ?, ?)`, operationPolicyRowID, operationRetentionPolicyVersion, int64(DefaultOperationTerminalRetention)); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	root := filepath.Dir(path)
	before := snapshotOperationPreflightTree(t, root)
	if err := ValidateSQLiteOperationStoreReadOnly(t.Context(), path); err != nil {
		t.Fatalf("ValidateSQLiteOperationStoreReadOnly(escaped path) error = %v", err)
	}
	after := snapshotOperationPreflightTree(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("escaped path preflight changed the file tree:\n before=%#v\n after=%#v", before, after)
	}
}

type operationPreflightTreeEntry struct {
	mode    fs.FileMode
	size    int64
	modTime time.Time
	data    []byte
}

func snapshotOperationPreflightTree(t *testing.T, root string) map[string]operationPreflightTreeEntry {
	t.Helper()
	result := make(map[string]operationPreflightTreeEntry)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot := operationPreflightTreeEntry{mode: info.Mode(), size: info.Size(), modTime: info.ModTime()}
		if !entry.IsDir() {
			snapshot.data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		result[filepath.ToSlash(relative)] = snapshot
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
