package gatewayapp

import (
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestReadStoreUpgradePreflightPreservesCheckpointedWALDatabase(t *testing.T) {
	storeDir := stoppedStoreUpgradeFixture(t, "checkpointed-wal-readonly")
	controlPath := controlStoreDatabasePath(storeDir)
	database, err := sql.Open("sqlite", controlPath)
	if err != nil {
		t.Fatal(err)
	}
	var journalMode string
	if err := database.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&journalMode); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if journalMode != "wal" {
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
		if _, err := os.Stat(controlPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("checkpointed WAL sidecar %q = %v, want absent", suffix, err)
		}
	}

	before := snapshotStoreUpgradePreflightTree(t, storeDir)
	if _, err := readStoreUpgradePreflight(t.Context(), storeDir); err != nil {
		t.Fatalf("readStoreUpgradePreflight(checkpointed WAL) error = %v", err)
	}
	after := snapshotStoreUpgradePreflightTree(t, storeDir)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Store upgrade preflight changed the file tree:\n before=%#v\n after=%#v", before, after)
	}
}

type storeUpgradePreflightTreeEntry struct {
	mode    fs.FileMode
	size    int64
	modTime time.Time
	data    []byte
}

func snapshotStoreUpgradePreflightTree(t *testing.T, root string) map[string]storeUpgradePreflightTreeEntry {
	t.Helper()
	result := make(map[string]storeUpgradePreflightTreeEntry)
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
		snapshot := storeUpgradePreflightTreeEntry{mode: info.Mode(), size: info.Size(), modTime: info.ModTime()}
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
