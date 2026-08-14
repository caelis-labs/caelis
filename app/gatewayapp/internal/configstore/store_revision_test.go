package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreSaveAdvancesPersistedConfigurationRevision(t *testing.T) {
	root := t.TempDir()
	first := New(root)
	if err := first.Save(AppConfig{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(root).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigurationRevision != 1 {
		t.Fatalf("first revision = %d, want 1", loaded.ConfigurationRevision)
	}
	if err := New(root).Save(loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = first.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigurationRevision != 2 {
		t.Fatalf("second revision = %d, want 2", loaded.ConfigurationRevision)
	}
}

func TestStoreCompareAndSaveAllowsOneCrossInstanceWriter(t *testing.T) {
	root := t.TempDir()
	seed := New(root)
	if err := seed.Save(AppConfig{}); err != nil {
		t.Fatal(err)
	}
	base, err := seed.Load()
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		doc AppConfig
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, mode := range []string{"manual", "auto-review"} {
		go func(mode string) {
			candidate := base
			candidate.Runtime.ApprovalMode = mode
			ready.Done()
			<-start
			saved, saveErr := New(root).CompareAndSave(context.Background(), base.ConfigurationRevision, candidate)
			results <- result{doc: saved, err: saveErr}
		}(mode)
	}
	ready.Wait()
	close(start)

	committed := 0
	conflicted := 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			committed++
			if result.doc.ConfigurationRevision != 2 {
				t.Fatalf("committed revision = %d, want 2", result.doc.ConfigurationRevision)
			}
		case errors.Is(result.err, ErrConfigurationRevisionConflict):
			conflicted++
		default:
			t.Fatalf("CompareAndSave() error = %v", result.err)
		}
	}
	if committed != 1 || conflicted != 1 {
		t.Fatalf("committed/conflicted = %d/%d, want 1/1", committed, conflicted)
	}
	current, err := seed.Load()
	if err != nil {
		t.Fatal(err)
	}
	if current.ConfigurationRevision != 2 {
		t.Fatalf("persisted revision = %d, want 2", current.ConfigurationRevision)
	}
}

func TestStoreCompareAndSaveReturnsCommittedRevisionAfterDurabilityFault(t *testing.T) {
	fault := errors.New("directory fsync failed")
	store := New(t.TempDir())
	store.writeOps.FsyncDir = func(string) error { return fault }
	saved, err := store.CompareAndSave(context.Background(), 0, AppConfig{})
	if !errors.Is(err, fault) || !WriteCommitted(err) {
		t.Fatalf("CompareAndSave() error = %v, want committed %v", err, fault)
	}
	if saved.ConfigurationRevision != 1 {
		t.Fatalf("returned revision = %d, want 1", saved.ConfigurationRevision)
	}
	store.writeOps = AtomicWriteOps{}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigurationRevision != 1 {
		t.Fatalf("persisted revision = %d, want 1", loaded.ConfigurationRevision)
	}
	if _, err := store.CompareAndSave(context.Background(), 0, loaded); !errors.Is(err, ErrConfigurationRevisionConflict) {
		t.Fatalf("retry error = %v, want revision conflict", err)
	}
}

func TestStoreCompareAndSaveRejectsPreCanceledRequestWithoutWriting(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.CompareAndSave(ctx, 0, AppConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CompareAndSave() error = %v, want context canceled", err)
	}
	loaded, err := New(root).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigurationRevision != 0 {
		t.Fatalf("persisted revision = %d, want 0", loaded.ConfigurationRevision)
	}
}

func TestStoreLoadCurrentSchemaDoesNotWaitForConfigurationFileLock(t *testing.T) {
	root := t.TempDir()
	seed := New(root)
	if err := seed.Save(AppConfig{Runtime: RuntimeConfig{ApprovalMode: "manual"}}); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireFileLock(context.Background(), seed.Path()+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got, err := New(root).LoadContext(ctx)
	if err != nil {
		t.Fatalf("LoadContext() error = %v, want current snapshot", err)
	}
	if got.ConfigurationRevision != 1 || got.Runtime.ApprovalMode != "manual" {
		t.Fatalf("LoadContext() = %#v, want complete revision 1 document", got)
	}
}

func TestStoreLegacyLoadContextHonorsMigrationFileLockDeadline(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if err := os.WriteFile(store.Path(), []byte(`{"models":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireFileLock(context.Background(), store.Path()+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := New(root).LoadContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LoadContext() error = %v, want deadline exceeded", err)
	}
}

func TestStoreLoadCurrentSchemaDoesNotWaitForSlowWriterInSameStore(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if err := store.Save(AppConfig{}); err != nil {
		t.Fatal(err)
	}
	externalLock, err := acquireFileLock(context.Background(), store.Path()+".lock")
	if err != nil {
		t.Fatal(err)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = externalLock.Close()
		}
	}()

	firstResult := make(chan error, 1)
	go func() {
		loaded, loadErr := store.Load()
		if loadErr == nil {
			_, loadErr = store.CompareAndSave(context.Background(), loaded.ConfigurationRevision, loaded)
		}
		firstResult <- loadErr
	}()
	gateDeadline := time.Now().Add(time.Second)
	for len(store.gate.token) != 0 && time.Now().Before(gateDeadline) {
		time.Sleep(time.Millisecond)
	}
	if len(store.gate.token) != 0 {
		t.Fatal("writer did not acquire the process gate")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	loaded, err := store.LoadContext(ctx)
	if err != nil {
		t.Fatalf("LoadContext() error = %v, want current snapshot", err)
	}
	if loaded.ConfigurationRevision != 1 {
		t.Fatalf("LoadContext() revision = %d, want 1", loaded.ConfigurationRevision)
	}
	if err := externalLock.Close(); err != nil {
		t.Fatal(err)
	}
	lockHeld = false
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationFileLockHonorsContextCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json.lock")
	first, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireFileLock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock error = %v, want deadline exceeded", err)
	}
}
