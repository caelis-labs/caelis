package gatewayapp

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreStoreRejectsTargetControlSQLiteSidecarBeforeSwap(t *testing.T) {
	source := makeStoreBackupFixture(t, "sidecar-source")
	archive := writeStoreRestoreTestArchive(t, source)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			target := makeStoreBackupFixture(t, "sidecar-target")
			wantControl, err := storeRestorePathDigest(controlStoreDatabasePath(target))
			if err != nil {
				t.Fatal(err)
			}
			sidecar := controlStoreDatabasePath(target) + suffix
			if err := os.WriteFile(sidecar, []byte("uncheckpointed-"+suffix), 0o600); err != nil {
				t.Fatal(err)
			}
			memoryCalled := false
			_, err = RestoreStore(t.Context(), StoreRestoreOptions{
				StoreDir: target,
				Backup:   bytes.NewReader(archive),
				MemoryRestore: func(context.Context, io.Reader) error {
					memoryCalled = true
					return errors.New("MemoryRestore() ran despite Control sidecar")
				},
				MemoryCommit: func(context.Context) error {
					t.Fatal("MemoryCommit() ran despite Control sidecar")
					return nil
				},
				MemoryRollback: func(context.Context) error { return nil },
			})
			if err == nil || !strings.Contains(err.Error(), "sidecar") {
				t.Fatalf("RestoreStore() error = %v, want Control sidecar rejection", err)
			}
			if memoryCalled {
				t.Fatal("MemoryRestore() ran before target swap was rejected")
			}
			if got := readStoreBackupFile(t, sidecar); got != "uncheckpointed-"+suffix {
				t.Fatalf("Control sidecar changed or was deleted: %q", got)
			}
			gotControl, err := storeRestorePathDigest(controlStoreDatabasePath(target))
			if err != nil || gotControl != wantControl {
				t.Fatalf("target Control changed after sidecar rejection: %q, %v", gotControl, err)
			}
			if _, err := os.Stat(filepath.Join(target, storeRestoreJournalName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restore journal after sidecar rejection = %v", err)
			}
		})
	}
}

func TestRestoreStoreRejectsStagedControlSchemaBeforeSwap(t *testing.T) {
	source := makeStoreBackupFixture(t, "future-control-source")
	database, err := sql.Open("sqlite", controlStoreDatabasePath(source))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA user_version = 99`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	archive := writeStoreRestoreTestArchive(t, source)
	target := makeStoreBackupFixture(t, "future-control-target")
	wantControl, err := storeRestorePathDigest(controlStoreDatabasePath(target))
	if err != nil {
		t.Fatal(err)
	}
	wantConfig := readStoreBackupFile(t, filepath.Join(target, "config.json"))
	assertRestoreRejectedBeforeSwap(t, target, archive, "unsupported operation store schema version")
	if got := readStoreBackupFile(t, filepath.Join(target, "config.json")); got != wantConfig {
		t.Fatalf("target config changed after staged Control rejection: %q", got)
	}
	gotControl, err := storeRestorePathDigest(controlStoreDatabasePath(target))
	if err != nil || gotControl != wantControl {
		t.Fatalf("target Control changed after staged schema rejection: %q, %v", gotControl, err)
	}
}

func TestRestoreStoreRejectsStagedSessionSchemaBeforeSwap(t *testing.T) {
	source := makeStoreBackupFixture(t, "future-session-source")
	path := filepath.Join(source, "sessions", "rollout-future.json")
	if err := os.WriteFile(path, []byte(`{"kind":"caelis.sdk.session","version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := writeStoreRestoreTestArchive(t, source)
	target := makeStoreBackupFixture(t, "future-session-target")
	wantSessions, err := storeRestorePathDigest(filepath.Join(target, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	assertRestoreRejectedBeforeSwap(t, target, archive, "unsupported document")
	gotSessions, err := storeRestorePathDigest(filepath.Join(target, "sessions"))
	if err != nil || gotSessions != wantSessions {
		t.Fatalf("target sessions changed after staged schema rejection: %q, %v", gotSessions, err)
	}
}

func TestRestoreStoreSyncsStagedTreeBeforeFirstSwap(t *testing.T) {
	source := makeStoreBackupFixture(t, "staged-sync-source")
	archive := writeStoreRestoreTestArchive(t, source)
	target := makeStoreBackupFixture(t, "staged-sync-target")
	syncedFiles := map[string]bool{}
	setStoreRestoreFileSyncHook(t, func(path string) error {
		if strings.Contains(path, storeRestoreStagePrefix) {
			syncedFiles[filepath.Base(path)] = true
		}
		return nil
	})
	renamed := false
	setStoreRestoreHooks(t, func(oldPath, newPath string) error {
		// Check at the first destructive rename, not after restore returns:
		// later installed-file syncs cannot satisfy the staging barrier.
		for _, name := range []string{"config.json", controlStoreDatabaseFile, "accepted.jsonl"} {
			if !syncedFiles[name] {
				t.Fatalf("rename %s before staged %s was synced", oldPath, name)
			}
		}
		renamed = true
		return os.Rename(oldPath, newPath)
	}, nil)
	if _, err := RestoreStore(t.Context(), storeRestoreTestOptions(target, archive)); err != nil {
		t.Fatalf("RestoreStore() error = %v", err)
	}
	if !renamed {
		t.Fatal("restore did not exercise component swaps")
	}
}

func TestRestoreStoreStagedFileSyncFailureLeavesTargetUnchanged(t *testing.T) {
	source := makeStoreBackupFixture(t, "staged-sync-fail-source")
	archive := writeStoreRestoreTestArchive(t, source)
	target := makeStoreBackupFixture(t, "staged-sync-fail-target")
	wantControl, err := storeRestorePathDigest(controlStoreDatabasePath(target))
	if err != nil {
		t.Fatal(err)
	}
	setStoreRestoreFileSyncHook(t, func(string) error {
		return errors.New("injected staged file sync failure")
	})
	memoryCalled := false
	_, err = RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir: target,
		Backup:   bytes.NewReader(archive),
		MemoryRestore: func(context.Context, io.Reader) error {
			memoryCalled = true
			return nil
		},
		MemoryCommit:   func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "injected staged file sync failure") {
		t.Fatalf("RestoreStore() error = %v, want staged file sync failure", err)
	}
	if memoryCalled {
		t.Fatal("MemoryRestore() ran after staged sync failure")
	}
	gotControl, err := storeRestorePathDigest(controlStoreDatabasePath(target))
	if err != nil || gotControl != wantControl {
		t.Fatalf("target Control changed after staged sync failure: %q, %v", gotControl, err)
	}
	if _, err := os.Stat(filepath.Join(target, storeRestoreJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore journal after staged sync failure = %v", err)
	}
}

func TestRestoreStoreRecoveryFinalizeDoesNotCommitUnsyncedInstalledTree(t *testing.T) {
	storeDir := makeStoreBackupFixture(t, "unsynced-commit")
	rollbackDir := filepath.Join(filepath.Dir(storeDir), storeRestoreRollbackPrefix+"unsynced")
	stageDir := filepath.Join(filepath.Dir(storeDir), storeRestoreStagePrefix+"unsynced")
	if err := os.MkdirAll(rollbackDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeStoreRestoreJournal(storeDir, storeRestoreJournal{
		Format: StoreBackupFormat, State: "applying", StoreDir: storeDir,
		StageDir: stageDir, RollbackDir: rollbackDir,
		Applied: []string{
			"config.json",
			filepath.Join("control", controlStoreDatabaseFile),
			"sessions",
		},
		MemoryCommitPending: true,
	}); err != nil {
		t.Fatal(err)
	}
	committed := false
	setStoreRestoreFileSyncHook(t, func(string) error {
		return errors.New("injected installed tree sync failure")
	})
	_, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir: storeDir,
		Backup:   strings.NewReader("not-an-archive"),
		MemoryRestore: func(context.Context, io.Reader) error {
			t.Fatal("MemoryRestore() ran during unsynced finalize")
			return nil
		},
		MemoryCommit: func(context.Context) error {
			committed = true
			return nil
		},
		MemoryRollback: func(context.Context) error {
			t.Fatal("MemoryRollback() ran during unsynced finalize")
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unsynced restored Store") {
		t.Fatalf("RestoreStore() error = %v, want unsynced commit refusal", err)
	}
	if committed {
		t.Fatal("MemoryCommit() ran for an unsynced installed tree")
	}
	if _, err := os.Stat(filepath.Join(storeDir, storeRestoreJournalName)); err != nil {
		t.Fatalf("restore journal was removed after unsynced finalize: %v", err)
	}
	if _, err := os.Stat(rollbackDir); err != nil {
		t.Fatalf("rollback evidence was removed after unsynced finalize: %v", err)
	}
	if _, err := os.Stat(stageDir); err != nil {
		t.Fatalf("stage evidence was removed after unsynced finalize: %v", err)
	}
}

func writeStoreRestoreTestArchive(t *testing.T, storeDir string) []byte {
	t.Helper()
	var archive bytes.Buffer
	if _, err := WriteStoreBackup(t.Context(), StoreBackupOptions{
		StoreDir: storeDir,
		MemoryBackup: func(_ context.Context, output io.Writer) error {
			_, err := io.WriteString(output, "memory-snapshot")
			return err
		},
	}, &archive); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func storeRestoreTestOptions(storeDir string, archive []byte) StoreRestoreOptions {
	return StoreRestoreOptions{
		StoreDir:       storeDir,
		Backup:         bytes.NewReader(archive),
		MemoryRestore:  func(context.Context, io.Reader) error { return nil },
		MemoryCommit:   func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error { return nil },
	}
}

func assertRestoreRejectedBeforeSwap(t *testing.T, target string, archive []byte, want string) {
	t.Helper()
	memoryCalled := false
	_, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir: target,
		Backup:   bytes.NewReader(archive),
		MemoryRestore: func(context.Context, io.Reader) error {
			memoryCalled = true
			return nil
		},
		MemoryCommit:   func(context.Context) error { t.Fatal("MemoryCommit() ran before swap rejection"); return nil },
		MemoryRollback: func(context.Context) error { return nil },
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("RestoreStore() error = %v, want %q", err, want)
	}
	if memoryCalled {
		t.Fatal("MemoryRestore() ran before staged owner validation")
	}
	if _, err := os.Stat(filepath.Join(target, storeRestoreJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore journal after staged rejection = %v", err)
	}
}

func setStoreRestoreFileSyncHook(t *testing.T, syncFile func(string) error) {
	t.Helper()
	storeRestoreHooks.Lock()
	storeRestoreHooks.syncFile = syncFile
	storeRestoreHooks.Unlock()
	t.Cleanup(func() {
		storeRestoreHooks.Lock()
		storeRestoreHooks.syncFile = nil
		storeRestoreHooks.Unlock()
	})
}
