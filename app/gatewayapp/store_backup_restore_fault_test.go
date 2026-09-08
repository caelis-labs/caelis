package gatewayapp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreStoreSyncsRenameParentsAndRetainsJournalOnRollbackFailure(t *testing.T) {
	source := makeStoreBackupFixture(t, "restore-fault-source")
	var archive bytes.Buffer
	if _, err := WriteStoreBackup(t.Context(), StoreBackupOptions{
		StoreDir: source,
		MemoryBackup: func(_ context.Context, output io.Writer) error {
			_, err := io.WriteString(output, "memory-snapshot")
			return err
		},
	}, &archive); err != nil {
		t.Fatal(err)
	}
	target := makeStoreBackupFixture(t, "restore-fault-target")
	var synced []string
	setStoreRestoreHooks(t, func(oldPath, newPath string) error {
		if strings.Contains(oldPath, storeRestoreRollbackPrefix) {
			return errors.New("injected rollback rename failure")
		}
		return os.Rename(oldPath, newPath)
	}, func(path string) error {
		synced = append(synced, filepath.Clean(path))
		return nil
	})
	_, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir: target,
		Backup:   bytes.NewReader(archive.Bytes()),
		MemoryRestore: func(context.Context, io.Reader) error {
			return errors.New("injected Memory restore failure")
		},
		MemoryCommit: func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error {
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "injected rollback rename failure") {
		t.Fatalf("RestoreStore() error = %v, want retained rollback failure", err)
	}
	journal, err := readStoreRestoreJournal(target)
	if err != nil {
		t.Fatalf("read failed restore journal: %v", err)
	}
	if journal.State != "applying" || journal.RollbackDir == "" {
		t.Fatalf("failed restore journal = %+v, want applying recovery state", journal)
	}
	if _, err := os.Stat(journal.RollbackDir); err != nil {
		t.Fatalf("rollback evidence missing: %v", err)
	}
	if !containsPath(synced, target) || !containsPath(synced, journal.RollbackDir) || !containsPath(synced, journal.StageDir) {
		t.Fatalf("sync paths = %#v, want Store, rollback, and stage directories", synced)
	}

	clearStoreRestoreHooks()
	if _, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir:       target,
		Backup:         bytes.NewReader(archive.Bytes()),
		MemoryRestore:  func(context.Context, io.Reader) error { return nil },
		MemoryCommit:   func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error { return nil },
	}); err != nil {
		t.Fatalf("RestoreStore() recovery retry error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, storeRestoreJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore journal remains after recovery retry: %v", err)
	}
}

func TestRestoreStoreJournalFailureRollsBackTheRecordedRename(t *testing.T) {
	source := makeStoreBackupFixture(t, "restore-journal-source")
	var archive bytes.Buffer
	if _, err := WriteStoreBackup(t.Context(), StoreBackupOptions{
		StoreDir: source,
		MemoryBackup: func(_ context.Context, output io.Writer) error {
			_, err := io.WriteString(output, "memory-snapshot")
			return err
		},
	}, &archive); err != nil {
		t.Fatal(err)
	}
	target := makeStoreBackupFixture(t, "restore-journal-target")
	originalConfig := readStoreBackupFile(t, filepath.Join(target, "config.json"))
	var injected bool
	setStoreRestoreJournalHook(t, func(journal storeRestoreJournal) error {
		if journal.State == "applying" && len(journal.Applied) == 1 && journal.Pending == "" && !injected {
			injected = true
			return errors.New("injected restore journal failure")
		}
		return nil
	})
	_, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir: target,
		Backup:   bytes.NewReader(archive.Bytes()),
		MemoryRestore: func(context.Context, io.Reader) error {
			t.Fatal("MemoryRestore() ran after journal failure")
			return nil
		},
		MemoryCommit: func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error {
			t.Fatal("MemoryRollback() ran before Memory restore")
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "injected restore journal failure") {
		t.Fatalf("RestoreStore() error = %v, want journal failure", err)
	}
	if !injected {
		t.Fatal("restore journal failure hook was not reached")
	}
	if got := readStoreBackupFile(t, filepath.Join(target, "config.json")); got != originalConfig {
		t.Fatalf("config after journal failure = %q, want original %q", got, originalConfig)
	}
	if _, err := os.Stat(filepath.Join(target, storeRestoreJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore journal remains after rollback: %v", err)
	}
}

func setStoreRestoreHooks(t *testing.T, rename func(string, string) error, syncDirectory func(string) error) {
	t.Helper()
	storeRestoreHooks.Lock()
	storeRestoreHooks.rename = rename
	storeRestoreHooks.syncDirectory = syncDirectory
	storeRestoreHooks.Unlock()
	t.Cleanup(clearStoreRestoreHooks)
}

func setStoreRestoreJournalHook(t *testing.T, writeJournal func(storeRestoreJournal) error) {
	t.Helper()
	storeRestoreHooks.Lock()
	storeRestoreHooks.writeJournal = writeJournal
	storeRestoreHooks.Unlock()
	t.Cleanup(clearStoreRestoreHooks)
}

func clearStoreRestoreHooks() {
	storeRestoreHooks.Lock()
	storeRestoreHooks.rename = nil
	storeRestoreHooks.syncDirectory = nil
	storeRestoreHooks.writeJournal = nil
	storeRestoreHooks.Unlock()
}

func containsPath(paths []string, want string) bool {
	want = filepath.Clean(want)
	for _, path := range paths {
		if filepath.Clean(path) == want {
			return true
		}
	}
	return false
}
