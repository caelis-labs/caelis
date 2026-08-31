package appserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSQLiteOperationStorePersistsImmutableResultAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control", "control.sqlite")
	first := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	intent := operationStoreTestIntent("sqlite-restart", "digest-a")
	if _, created, err := first.Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin() = created %v, error %v", created, err)
	}
	want := operationStoreTestResult(intent, OutcomeCommitted)
	if _, err := first.Complete(context.Background(), intent, want); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	got, created, err := second.Begin(context.Background(), intent)
	if err != nil || created || got.Result == nil || !sameCommandResult(*got.Result, want) {
		t.Fatalf("Begin(after restart) = %#v, created %v, error %v", got, created, err)
	}
	changed := want
	changed.Outcome = OutcomeRejected
	if _, err := second.Complete(context.Background(), intent, changed); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("Complete(changed) error = %v, want conflict", err)
	}
}

func TestSQLiteOperationStoreConcurrentBeginAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	first := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	second := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	stores := []*SQLiteOperationStore{first, second}
	intent := operationStoreTestIntent("sqlite-concurrent", "digest-a")
	const workers = 32
	start := make(chan struct{})
	errs := make(chan error, workers)
	var created atomic.Int32
	var wait sync.WaitGroup
	for index := range workers {
		wait.Add(1)
		go func(store *SQLiteOperationStore) {
			defer wait.Done()
			<-start
			_, wasCreated, err := store.Begin(context.Background(), intent)
			if err != nil {
				errs <- err
				return
			}
			if wasCreated {
				created.Add(1)
			}
		}(stores[index%len(stores)])
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Begin() error = %v", err)
	}
	if got := created.Load(); got != 1 {
		t.Fatalf("created count = %d, want exactly one", got)
	}
}

func TestSQLiteOperationStoreSweepVerifiesSemanticRecordBeforeDelete(t *testing.T) {
	base := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	store := newTestSQLiteOperationStore(t, filepath.Join(t.TempDir(), "control.sqlite"), OperationRetentionConfig{
		TerminalRetention: time.Hour,
	})
	store.now = func() time.Time { return base }
	intent := operationStoreTestIntent("indexed-corruption", "digest-a")
	if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin() = created %v, error %v", created, err)
	}
	if _, err := store.Complete(context.Background(), intent, operationStoreTestResult(intent, OutcomeCommitted)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
UPDATE control_operations SET retain_until_ns = ?
WHERE principal_id = ? AND operation_id = ?`, base.Add(-time.Minute).UnixNano(), intent.PrincipalID, intent.OperationID); err != nil {
		t.Fatal(err)
	}
	result, err := store.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Corrupt != 1 || result.RemovedTerminal != 0 {
		t.Fatalf("Sweep() = %#v, want corrupt retained row", result)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM control_operations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("operation row count = %d, %v; want 1", count, err)
	}
}

func TestSQLiteOperationStoreSweepAdvancesPastRetainedCorruptRow(t *testing.T) {
	base := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	store := newTestSQLiteOperationStore(t, filepath.Join(t.TempDir(), "control.sqlite"), OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepBatchSize:    1,
		SweepDeleteLimit:  1,
	})
	now := base
	store.now = func() time.Time { return now }
	corrupt := operationStoreTestIntent("a-corrupt", "digest-a")
	valid := operationStoreTestIntent("b-valid", "digest-b")
	for _, intent := range []OperationIntent{corrupt, valid} {
		if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
			t.Fatalf("Begin(%s) = created %v, error %v", intent.OperationID, created, err)
		}
		if _, err := store.Complete(context.Background(), intent, operationStoreTestResult(intent, OutcomeCommitted)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`
UPDATE control_operations SET retain_until_ns = ?
WHERE principal_id = ? AND operation_id = ?`, base.Add(-time.Minute).UnixNano(), corrupt.PrincipalID, corrupt.OperationID); err != nil {
		t.Fatal(err)
	}
	now = base.Add(2 * time.Hour)
	first, err := store.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Scanned != 1 || first.Corrupt != 1 || !first.More {
		t.Fatalf("first Sweep() = %#v, want one corrupt row and more work", first)
	}
	second, err := store.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Scanned != 1 || second.RemovedTerminal != 1 || second.More {
		t.Fatalf("second Sweep() = %#v, want valid expired row removed", second)
	}
}

func TestSQLiteOperationStoreRejectsSymlinkedDatabasePaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	controlDir := filepath.Join(root, "control")
	if err := os.Symlink(outside, controlDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewSQLiteOperationStoreWithConfig(filepath.Join(controlDir, "control.sqlite"), OperationRetentionConfig{}); err == nil || !strings.Contains(err.Error(), "secure directory") {
		t.Fatalf("symlinked directory error = %v", err)
	}

	regularDir := filepath.Join(t.TempDir(), "control")
	if err := os.MkdirAll(regularDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.sqlite")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(regularDir, "control.sqlite")
	if err := os.Symlink(target, databasePath); err != nil {
		t.Skipf("database symlink unavailable: %v", err)
	}
	if _, err := NewSQLiteOperationStoreWithConfig(databasePath, OperationRetentionConfig{}); err == nil || !strings.Contains(err.Error(), "secure regular file") {
		t.Fatalf("symlinked database error = %v", err)
	}
}

func TestSQLiteOperationStoreSweepRemovesOnlyExpiredTerminalRows(t *testing.T) {
	base := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	store := newTestSQLiteOperationStore(t, filepath.Join(t.TempDir(), "control.sqlite"), OperationRetentionConfig{
		TerminalRetention: time.Hour,
	})
	now := base
	store.now = func() time.Time { return now }
	intent := operationStoreTestIntent("expired", "digest-a")
	if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin() = created %v, error %v", created, err)
	}
	if _, err := store.Complete(context.Background(), intent, operationStoreTestResult(intent, OutcomeCommitted)); err != nil {
		t.Fatal(err)
	}
	now = base.Add(2 * time.Hour)
	result, err := store.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedTerminal != 1 || result.Corrupt != 0 {
		t.Fatalf("Sweep() = %#v, want one removed terminal row", result)
	}
}

func newTestSQLiteOperationStore(t *testing.T, path string, config OperationRetentionConfig) *SQLiteOperationStore {
	t.Helper()
	store := newTestSQLiteOperationStoreUninitialized(t, path, config)
	if err := store.Initialize(context.Background()); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return store
}

func newTestSQLiteOperationStoreUninitialized(t *testing.T, path string, config OperationRetentionConfig) *SQLiteOperationStore {
	t.Helper()
	store, err := NewSQLiteOperationStoreWithConfig(path, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
