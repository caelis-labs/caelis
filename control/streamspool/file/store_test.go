package file

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/streamspool"
)

func TestPendingReaderWaitsForFirstAppend(t *testing.T) {
	store := newTestStore(t, Config{})
	writer := registerTestWriter(t, store, "session", "task", true)
	reader, err := store.Reader(context.Background(), writer.Key(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	type result struct {
		record streamspool.Record
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		record, readErr := reader.Next(context.Background())
		resultCh <- result{record: record, err: readErr}
	}()
	select {
	case got := <-resultCh:
		t.Fatalf("pending reader returned early: %#v", got)
	case <-time.After(20 * time.Millisecond):
	}

	if offset, err := writer.Append(context.Background(), 7, time.Unix(1, 2), []byte("first")); err != nil || offset != 0 {
		t.Fatalf("Append() = (%d, %v)", offset, err)
	}
	select {
	case got := <-resultCh:
		if got.err != nil || got.record.Offset != 0 || got.record.Type != 7 || string(got.record.Payload) != "first" {
			t.Fatalf("Next() = (%#v, %v)", got.record, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending reader did not wake")
	}
}

func TestEmptyTerminalWakesPendingReader(t *testing.T) {
	store := newTestStore(t, Config{})
	writer := registerTestWriter(t, store, "session", "empty", true)
	reader, err := store.Reader(context.Background(), writer.Key(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	errCh := make(chan error, 1)
	go func() {
		_, readErr := reader.Next(context.Background())
		errCh <- readErr
	}()
	if err := writer.FinishEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, streamspool.ErrEmptyTerminal) {
			t.Fatalf("Next() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("empty terminal did not wake reader")
	}
}

func TestEmptyTerminalWithoutReadersLeavesNoRegistryOrFiles(t *testing.T) {
	store := newTestStore(t, Config{})
	writer := registerTestWriter(t, store, "session", "zero-output", true)
	if err := writer.FinishEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	partitions := len(store.partitions)
	registrations := store.registrations
	store.mu.Unlock()
	if partitions != 0 || registrations != 0 {
		t.Fatalf("empty terminal registry = (%d partitions, %d registrations)", partitions, registrations)
	}
	entries, err := os.ReadDir(filepath.Join(store.root, streamspool.NamespaceTask.String()))
	if err != nil || len(entries) != 0 {
		t.Fatalf("zero-output task files = %d, %v", len(entries), err)
	}
}

func TestClosingReaderWakesBlockedNext(t *testing.T) {
	store := newTestStore(t, Config{})
	writer := registerTestWriter(t, store, "session", "close-reader", true)
	reader, err := store.Reader(context.Background(), writer.Key(), 0)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, readErr := reader.Next(context.Background())
		errCh <- readErr
	}()
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, streamspool.ErrClosed) {
			t.Fatalf("blocked Next() after Close error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader Close did not wake blocked Next")
	}
}

func TestMultipleReadersCrossRolledSegments(t *testing.T) {
	store := newTestStore(t, Config{SegmentBytes: 150, MaxSegmentsPerPartition: 16})
	writer := registerTestWriter(t, store, "session", "rolled", true)
	for i := range 8 {
		payload := []byte{byte('a' + i), byte('0' + i), byte('A' + i)}
		if _, err := writer.Append(context.Background(), uint16(i+1), time.Unix(int64(i), 0), payload); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}
	if err := writer.Seal(context.Background()); err != nil {
		t.Fatal(err)
	}
	for readerIndex := range 2 {
		reader, err := store.Reader(context.Background(), writer.Key(), 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 8 {
			record, err := reader.Next(context.Background())
			if err != nil {
				t.Fatalf("reader %d record %d: %v", readerIndex, i, err)
			}
			if record.Offset != streamspool.Offset(i) || record.Type != uint16(i+1) {
				t.Fatalf("reader %d record %d = %#v", readerIndex, i, record)
			}
		}
		if _, err := reader.Next(context.Background()); !errors.Is(err, io.EOF) {
			t.Fatalf("reader %d terminal error = %v", readerIndex, err)
		}
		_ = reader.Close()
	}
}

func TestConcurrentAppendsAreLinearized(t *testing.T) {
	store := newTestStore(t, Config{})
	writer := registerTestWriter(t, store, "session", "concurrent", true)
	const count = 64
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := writer.Append(context.Background(), 1, time.Now(), []byte{byte(i)})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Seal(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader, err := store.Reader(context.Background(), writer.Key(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for i := range count {
		record, err := reader.Next(context.Background())
		if err != nil || record.Offset != streamspool.Offset(i) {
			t.Fatalf("record %d = (%#v, %v)", i, record, err)
		}
	}
}

func TestHardQuotaPoisonsAndWakesReader(t *testing.T) {
	store := newTestStore(t, Config{
		MaxBytes:                  300,
		SegmentBytes:              1 << 20,
		PartitionAllocationCharge: 32,
		SegmentAllocationCharge:   16,
	})
	writer := registerTestWriter(t, store, "session", "quota", true)
	reader, err := store.Reader(context.Background(), writer.Key(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := writer.Append(context.Background(), 1, time.Now(), make([]byte, 80)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, readErr := reader.Next(context.Background())
		errCh <- readErr
	}()
	if _, err := writer.Append(context.Background(), 1, time.Now(), make([]byte, 200)); !errors.Is(err, streamspool.ErrLimit) {
		t.Fatalf("Append over quota error = %v", err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, streamspool.ErrUnavailable) {
			t.Fatalf("waiting reader error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("quota poison did not wake reader")
	}
}

func TestFirstRollFailureCleansDirectoryAndAccounting(t *testing.T) {
	store := newTestStore(t, Config{})
	writer := registerTestWriter(t, store, "session", "roll-failure", true)
	dir := partitionDir(store.root, writer.Key())
	if err := os.MkdirAll(filepath.Join(dir, segmentFilename(0)), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Append(context.Background(), 1, time.Now(), []byte("payload")); err == nil {
		t.Fatal("Append unexpectedly succeeded with a non-regular segment path")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed first-roll directory after Append = %v", err)
	}
	store.mu.Lock()
	usedBytes := store.usedBytes
	physicalParts := store.physicalParts
	segments := store.segmentCount
	partitions := len(store.partitions)
	store.mu.Unlock()
	if usedBytes != 0 || physicalParts != 0 || segments != 0 || partitions != 0 {
		t.Fatalf("failed first-roll accounting = bytes %d, parts %d, segments %d, registry %d", usedBytes, physicalParts, segments, partitions)
	}
}

func TestPerStreamQuotaPoisonsOnlyThatPartition(t *testing.T) {
	store := newTestStore(t, Config{
		MaxBytes:                  1 << 20,
		MaxStreamBytes:            300,
		SegmentBytes:              1 << 20,
		PartitionAllocationCharge: 32,
		SegmentAllocationCharge:   16,
	})
	limited := registerTestWriter(t, store, "session", "limited", true)
	if _, err := limited.Append(context.Background(), 1, time.Now(), make([]byte, 20)); err != nil {
		t.Fatal(err)
	}
	if _, err := limited.Append(context.Background(), 1, time.Now(), make([]byte, 120)); !errors.Is(err, streamspool.ErrLimit) {
		t.Fatalf("Append over per-stream quota error = %v", err)
	}

	independent := registerTestWriter(t, store, "session", "independent", true)
	if offset, err := independent.Append(context.Background(), 1, time.Now(), []byte("still available")); err != nil || offset != 0 {
		t.Fatalf("independent Append() = (%d, %v)", offset, err)
	}
}

func TestRegistrationAndReaderLimitsAreHard(t *testing.T) {
	store := newTestStore(t, Config{MaxRegistrations: 1, MaxReaders: 1})
	writer := registerTestWriter(t, store, "session", "first", true)
	_, err := store.Register(context.Background(), streamspool.LogicalKey{
		Namespace: streamspool.NamespaceTask,
		Digest:    streamspool.DigestStrings("session", "second"),
	}, streamspool.WriterOptions{OriginComplete: true})
	if !errors.Is(err, streamspool.ErrLimit) {
		t.Fatalf("second Register() error = %v", err)
	}
	reader, err := store.Reader(context.Background(), writer.Key(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := store.Reader(context.Background(), writer.Key(), 0); !errors.Is(err, streamspool.ErrLimit) {
		t.Fatalf("second Reader() error = %v", err)
	}
}

func TestStoreCloseWakesPendingReader(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	store, err := New(context.Background(), Config{RootDir: root, GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	writer := registerTestWriter(t, store, "session", "close-store", true)
	reader, err := store.Reader(context.Background(), writer.Key(), 0)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, readErr := reader.Next(context.Background())
		errCh <- readErr
	}()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, streamspool.ErrClosed) {
			t.Fatalf("blocked Next() after Store.Close error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Store.Close did not wake pending reader")
	}
	_ = reader.Close()
}

func TestOwnerLockRejectsSecondStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	first, err := New(context.Background(), Config{RootDir: root, GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, err = New(context.Background(), Config{RootDir: root, GCInterval: -1, OwnerLockWait: time.Millisecond})
	if !errors.Is(err, streamspool.ErrInUse) {
		t.Fatalf("second New() error = %v", err)
	}
}

func TestOwnerLockRejectsSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, ownerLockFilename)); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	if _, err := New(context.Background(), Config{RootDir: root, GCInterval: -1}); err == nil {
		t.Fatal("New accepted a symlink owner lock")
	}
}

func TestCollectRemovesExpiredTerminalPartition(t *testing.T) {
	now := time.Unix(100, 0)
	store := newTestStore(t, Config{Now: func() time.Time { return now }, TerminalTTL: time.Second})
	writer := registerTestWriter(t, store, "session", "collect", true)
	if _, err := writer.Append(context.Background(), 1, now, []byte("done")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Seal(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if err := store.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.Resolve(context.Background(), writer.Key().LogicalKey)
	if !errors.Is(err, streamspool.ErrNotFound) {
		t.Fatalf("Resolve after Collect error = %v", err)
	}
}

func TestCollectDefersPartitionWithActiveReader(t *testing.T) {
	now := time.Unix(100, 0)
	store := newTestStore(t, Config{Now: func() time.Time { return now }, TerminalTTL: time.Second})
	writer := registerTestWriter(t, store, "session", "leased", true)
	if _, err := writer.Append(context.Background(), 1, now, []byte("done")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Seal(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader, err := store.Reader(context.Background(), writer.Key(), 0)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if err := store.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve(context.Background(), writer.Key().LogicalKey); err != nil {
		t.Fatalf("active-reader partition was collected: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve(context.Background(), writer.Key().LogicalKey); !errors.Is(err, streamspool.ErrNotFound) {
		t.Fatalf("Resolve after reader release and Collect error = %v", err)
	}
}

func TestReaderLeaseRejectsPartitionRemovedAfterLookup(t *testing.T) {
	assertRejected := func(t *testing.T, store *Store, p *partition, key streamspool.Key) {
		t.Helper()
		if _, err := store.readerForPartition(p, key, 0); !errors.Is(err, streamspool.ErrNotFound) {
			t.Fatalf("readerForPartition() error = %v, want not found", err)
		}
		store.mu.Lock()
		readers := store.readerCount
		store.mu.Unlock()
		if readers != 0 {
			t.Fatalf("reader count = %d, want zero after rejected stale lease", readers)
		}
		p.mu.Lock()
		partitionReaders := p.readers
		p.mu.Unlock()
		if partitionReaders != 0 {
			t.Fatalf("partition reader count = %d, want zero after rejected stale lease", partitionReaders)
		}
	}

	// Capturing p before Remove/Collect and invoking readerForPartition after it
	// deterministically recreates lookup -> removal -> lease without scheduler
	// timing. Both removal paths must reject the stale instance.
	t.Run("Remove", func(t *testing.T) {
		store := newTestStore(t, Config{})
		writerHandle := registerTestWriter(t, store, "session", "reader-remove-window", true)
		if _, err := writerHandle.Append(context.Background(), 1, time.Now(), []byte("record")); err != nil {
			t.Fatal(err)
		}
		if err := writerHandle.Seal(context.Background()); err != nil {
			t.Fatal(err)
		}
		key := writerHandle.Key()
		p := writerHandle.(*writer).partition
		if err := store.Remove(context.Background(), key); err != nil {
			t.Fatal(err)
		}
		assertRejected(t, store, p, key)
	})

	t.Run("Collect", func(t *testing.T) {
		now := time.Unix(100, 0)
		store := newTestStore(t, Config{Now: func() time.Time { return now }, TerminalTTL: time.Second})
		writerHandle := registerTestWriter(t, store, "session", "reader-collect-window", true)
		if _, err := writerHandle.Append(context.Background(), 1, now, []byte("record")); err != nil {
			t.Fatal(err)
		}
		if err := writerHandle.Seal(context.Background()); err != nil {
			t.Fatal(err)
		}
		key := writerHandle.Key()
		p := writerHandle.(*writer).partition
		now = now.Add(2 * time.Second)
		if err := store.Collect(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertRejected(t, store, p, key)
	})
}

func TestNewStoreReclaimsOldEpoch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	first, err := New(context.Background(), Config{RootDir: root, GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	writer := registerTestWriter(t, first, "session", "old-epoch", true)
	if _, err := writer.Append(context.Background(), 1, time.Now(), []byte("old")); err != nil {
		t.Fatal(err)
	}
	oldKey := writer.Key()
	oldDir := partitionDir(root, oldKey)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := New(context.Background(), Config{RootDir: root, GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.Epoch() == oldKey.Epoch {
		t.Fatal("new Store reused the previous process epoch")
	}
	if _, err := os.Stat(oldDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old epoch directory after New = %v", err)
	}
	if _, err := second.Reader(context.Background(), oldKey, 0); !errors.Is(err, streamspool.ErrNotFound) {
		t.Fatalf("old epoch Reader() error = %v", err)
	}
}

func TestSpoolPathsUsePrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	store := newTestStore(t, Config{})
	writer := registerTestWriter(t, store, "secret-session", "secret-task", true)
	if _, err := writer.Append(context.Background(), 1, time.Now(), []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(store.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0o600)
		if entry.IsDir() {
			want = 0o700
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s permissions = %o, want %o", path, got, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsSymlinkNamespace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, streamspool.NamespaceTask.String())); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	if _, err := New(context.Background(), Config{RootDir: root, GCInterval: -1}); err == nil {
		t.Fatal("New accepted a symlink namespace")
	}
}

func TestAppendRejectsIntermediateSymlinkWithoutEscapingRoot(t *testing.T) {
	store := newTestStore(t, Config{})
	writer := registerTestWriter(t, store, "session", "intermediate-symlink", true)
	digest := writer.Key().Digest.Hex()
	prefix := filepath.Join(store.root, streamspool.NamespaceTask.String(), digest[:2])
	target := t.TempDir()
	if err := os.Symlink(target, prefix); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	if _, err := writer.Append(context.Background(), 1, time.Now(), []byte("payload")); err == nil {
		t.Fatal("Append accepted an intermediate symlink")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Append wrote outside the spool root: %v", entries)
	}
}

func TestCorruptRecordIsRejected(t *testing.T) {
	store := newTestStore(t, Config{})
	writer := registerTestWriter(t, store, "session", "corrupt", true)
	if _, err := writer.Append(context.Background(), 1, time.Now(), []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Seal(context.Background()); err != nil {
		t.Fatal(err)
	}
	var segmentPath string
	if err := filepath.WalkDir(store.root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			if _, ok := parseSegmentFilename(entry.Name()); ok {
				segmentPath = path
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(segmentPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0xff}, segmentHeaderSize+recordPrefixSize+recordFixedBody); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	reader, err := store.Reader(context.Background(), writer.Key(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.Next(context.Background()); !errors.Is(err, streamspool.ErrCorrupt) {
		t.Fatalf("Next corrupt error = %v", err)
	}
}

func newTestStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	if cfg.RootDir == "" {
		cfg.RootDir = filepath.Join(t.TempDir(), "spool")
	}
	if cfg.GCInterval == 0 {
		cfg.GCInterval = -1
	}
	store, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	return store
}

func registerTestWriter(t *testing.T, store *Store, sessionID, taskID string, originComplete bool) streamspool.Writer {
	t.Helper()
	w, err := store.Register(context.Background(), streamspool.LogicalKey{
		Namespace: streamspool.NamespaceTask,
		Digest:    streamspool.DigestStrings(sessionID, taskID),
	}, streamspool.WriterOptions{OriginComplete: originComplete})
	if err != nil {
		t.Fatal(err)
	}
	return w
}
