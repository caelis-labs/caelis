package controlclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	controlport "github.com/caelis-labs/caelis/ports/controlclient"
)

type retentionTestStore interface {
	OperationStore
	Sweep(context.Context) (OperationSweepResult, error)
}

type fakeOperationClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeOperationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeOperationClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func TestOperationStoreRetentionWindowReplayConflictAndReuse(t *testing.T) {
	const retention = 2 * time.Hour
	config := OperationRetentionConfig{TerminalRetention: retention, SweepInterval: 24 * time.Hour}
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)}
			store := newRetentionTestStore(t, kind, config, clock)
			intent := operationStoreTestIntent("retention-window", "digest-a")
			if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
				t.Fatalf("Begin() = created %v, error %v", created, err)
			}
			want := operationStoreTestResult(intent, controlport.OutcomeCommitted)
			completed, err := store.Complete(context.Background(), intent, want)
			if err != nil || completed.Result == nil || *completed.Result != want {
				t.Fatalf("Complete() = %#v, %v", completed, err)
			}

			clock.Advance(retention - time.Nanosecond)
			replayed, created, err := store.Begin(context.Background(), intent)
			if err != nil || created || replayed.Result == nil || *replayed.Result != want {
				t.Fatalf("Begin(within window) = %#v, created %v, error %v", replayed, created, err)
			}
			changed := intent
			changed.Digest = "digest-b"
			if _, _, err := store.Begin(context.Background(), changed); !errors.Is(err, ErrOperationConflict) {
				t.Fatalf("Begin(changed within window) error = %v, want conflict", err)
			}
			result, err := store.Sweep(context.Background())
			if err != nil || result.RemovedTerminal != 0 {
				t.Fatalf("Sweep(within window) = %#v, %v", result, err)
			}

			clock.Advance(2 * time.Nanosecond)
			result, err = sweepRetentionCycle(context.Background(), store)
			if err != nil || result.RemovedTerminal != 1 {
				t.Fatalf("Sweep(after window) = %#v, %v", result, err)
			}
			fresh, created, err := store.Begin(context.Background(), changed)
			if err != nil || !created || fresh.Result != nil {
				t.Fatalf("Begin(reused after removal) = %#v, created %v, error %v", fresh, created, err)
			}
		})
	}
}

func TestOperationStoreSweepOnlyRemovesProvenTerminalRecords(t *testing.T) {
	const retention = time.Hour
	config := OperationRetentionConfig{TerminalRetention: retention, SweepInterval: 24 * time.Hour, SweepBatchSize: 64}
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC)}
			store := newRetentionTestStore(t, kind, config, clock)
			outcomes := map[string]controlport.Outcome{
				"committed":  controlport.OutcomeCommitted,
				"rejected":   controlport.OutcomeRejected,
				"conflicted": controlport.OutcomeConflicted,
				"accepted":   controlport.OutcomeAccepted,
				"unknown":    controlport.OutcomeUnknown,
			}
			intents := map[string]OperationIntent{}
			for name, outcome := range outcomes {
				intent := operationStoreTestIntent("state-"+name, "digest-"+name)
				intents[name] = intent
				if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
					t.Fatalf("Begin(%s) = created %v, error %v", name, created, err)
				}
				if _, err := store.Complete(context.Background(), intent, operationStoreTestResult(intent, outcome)); err != nil {
					t.Fatalf("Complete(%s) error = %v", name, err)
				}
			}
			inFlight := operationStoreTestIntent("state-in-flight", "digest-in-flight")
			intents["in-flight"] = inFlight
			if _, created, err := store.Begin(context.Background(), inFlight); err != nil || !created {
				t.Fatalf("Begin(in-flight) = created %v, error %v", created, err)
			}

			clock.Advance(retention + time.Nanosecond)
			result, err := sweepRetentionCycle(context.Background(), store)
			if err != nil || result.RemovedTerminal != 3 {
				t.Fatalf("Sweep() = %#v, %v; want three proven terminal removals", result, err)
			}
			for _, name := range []string{"committed", "rejected", "conflicted"} {
				record, created, err := store.Begin(context.Background(), intents[name])
				if err != nil || !created || record.Result != nil {
					t.Fatalf("Begin(%s after sweep) = %#v, created %v, error %v", name, record, created, err)
				}
			}
			for _, name := range []string{"accepted", "unknown", "in-flight"} {
				record, created, err := store.Begin(context.Background(), intents[name])
				if err != nil || created {
					t.Fatalf("Begin(%s protected) = %#v, created %v, error %v", name, record, created, err)
				}
				if name == "in-flight" && record.Result != nil {
					t.Fatalf("in-flight result = %#v, want nil", record.Result)
				}
				changed := intents[name]
				changed.Digest += "-changed"
				if _, _, err := store.Begin(context.Background(), changed); !errors.Is(err, ErrOperationConflict) {
					t.Fatalf("Begin(%s changed) error = %v, want conflict", name, err)
				}
			}
		})
	}
}

func TestFileOperationStoreLegacyRejectedRemainsIndeterminate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 2, 30, 0, 0, time.UTC)}
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
	}, clock)
	intent := operationStoreTestIntent("legacy-rejected", "digest")
	intent.CreatedAt = clock.Now().Add(-3 * time.Hour)
	result := operationStoreTestResult(intent, controlport.OutcomeRejected)
	legacy := OperationRecord{
		Intent:    intent,
		Result:    &result,
		UpdatedAt: clock.Now().Add(-2 * time.Hour),
	}
	writeRawOperationRecord(t, store, intent, legacy)

	swept, err := sweepRetentionCycle(context.Background(), store)
	if err != nil || swept.RemovedTerminal != 0 || swept.RetainedIndeterminate == 0 {
		t.Fatalf("Sweep() = %#v, %v; want protected legacy rejection", swept, err)
	}
	replayed, created, err := store.Begin(context.Background(), intent)
	if err != nil || created || replayed.Result == nil || *replayed.Result != result || replayed.Version != 0 {
		t.Fatalf("Begin(legacy rejected) = %#v, created %v, error %v", replayed, created, err)
	}
	changed := intent
	changed.Digest = "changed"
	if _, _, err := store.Begin(context.Background(), changed); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("Begin(changed legacy rejected) error = %v, want conflict", err)
	}
}

func TestOperationStoreSweepRacingCompleteRetainsCommittedResult(t *testing.T) {
	config := OperationRetentionConfig{TerminalRetention: time.Hour, SweepInterval: 24 * time.Hour, SweepBatchSize: 64}
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)}
			primary, sweeper := newRetentionRaceStores(t, kind, config, clock)
			for index := range 32 {
				intent := operationStoreTestIntent(fmt.Sprintf("complete-race-%d", index), "digest")
				if _, created, err := primary.Begin(context.Background(), intent); err != nil || !created {
					t.Fatalf("Begin(%d) = created %v, error %v", index, created, err)
				}
				clock.Advance(2 * time.Hour)
				want := operationStoreTestResult(intent, controlport.OutcomeCommitted)
				start := make(chan struct{})
				errs := make(chan error, 2)
				go func() {
					<-start
					_, err := primary.Complete(context.Background(), intent, want)
					errs <- err
				}()
				go func() {
					<-start
					_, err := sweeper.Sweep(context.Background())
					errs <- err
				}()
				close(start)
				for range 2 {
					if err := <-errs; err != nil {
						t.Fatalf("race %d error = %v", index, err)
					}
				}
				reloaded, created, err := primary.Begin(context.Background(), intent)
				if err != nil || created || reloaded.Result == nil || *reloaded.Result != want {
					t.Fatalf("reloaded race %d = %#v, created %v, error %v", index, reloaded, created, err)
				}
			}
		})
	}
}

func TestFileOperationStoreSweepReclaimsOldTempAndRetainsCorruptRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 4, 0, 0, 0, time.UTC)}
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		TempRetention:     time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
	}, clock)
	oldTemp := filepath.Join(root, ".operation-stale.tmp")
	youngTemp := filepath.Join(root, ".operation-young.tmp")
	unrelated := filepath.Join(root, "unrelated.tmp")
	for _, path := range []string{oldTemp, youngTemp, unrelated} {
		if err := os.WriteFile(path, []byte("residue"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := clock.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldTemp, old, old); err != nil {
		t.Fatal(err)
	}

	corruptIntent := operationStoreTestIntent("corrupt-record", "digest")
	corruptPath := store.path(corruptIntent)
	if err := os.WriteFile(corruptPath, []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(root, strings.Repeat("e", 64)+".json")
	if err := os.Symlink(corruptPath, symlinkPath); err != nil {
		t.Logf("symlink setup unavailable: %v", err)
	}

	result, err := sweepRetentionCycle(context.Background(), store)
	if err != nil || result.RemovedTemporary != 1 || result.Corrupt == 0 {
		t.Fatalf("Sweep() = %#v, %v", result, err)
	}
	assertPathMissing(t, oldTemp)
	assertPathExists(t, youngTemp)
	assertPathExists(t, unrelated)
	assertPathExists(t, corruptPath)
	if _, _, err := store.Begin(context.Background(), corruptIntent); err == nil {
		t.Fatal("Begin(corrupt) succeeded and could re-execute an unknown effect")
	}
	assertPathExists(t, corruptPath)
}

func TestFileOperationStoreParseableRetentionCorruptionFailsSafe(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 4, 30, 0, 0, time.UTC)}
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
	}, clock)
	intent := operationStoreTestIntent("parseable-corruption", "digest")
	record := expiredTestRecord(clock.Now(), intent)
	record.RetainUntil = record.UpdatedAt.Add(time.Nanosecond)
	writeRawOperationRecord(t, store, intent, record)
	missingSnapshotIntent := operationStoreTestIntent("missing-retention-snapshot", "digest")
	missingSnapshot := retainedTestRecord(clock.Now(), missingSnapshotIntent, controlport.OutcomeUnknown)
	missingSnapshot.TerminalRetentionNanoseconds = 0
	writeRawOperationRecord(t, store, missingSnapshotIntent, missingSnapshot)

	swept, err := sweepRetentionCycle(context.Background(), store)
	if err != nil || swept.RemovedTerminal != 0 || swept.Corrupt < 2 {
		t.Fatalf("Sweep() = %#v, %v; want corrupt record retained", swept, err)
	}
	assertPathExists(t, store.path(intent))
	assertPathExists(t, store.path(missingSnapshotIntent))
	if _, _, err := store.Begin(context.Background(), intent); err == nil {
		t.Fatal("Begin(parseable corruption) succeeded and could re-execute an unknown effect")
	}
	assertPathExists(t, store.path(intent))
}

func TestFileOperationStoreForegroundReadsRejectSymlinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 4, 45, 0, 0, time.UTC)}
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
	}, clock)
	intent := operationStoreTestIntent("symlink-record", "digest")
	record := expiredTestRecord(clock.Now(), intent)
	target := filepath.Join(root, ".valid-symlink-target")
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	canonical := store.path(intent)
	if err := os.Symlink(target, canonical); err != nil {
		t.Skipf("symlink setup unavailable: %v", err)
	}

	if _, _, err := store.Begin(context.Background(), intent); err == nil {
		t.Fatal("Begin(symlink) succeeded")
	}
	if _, err := store.Complete(context.Background(), intent, operationStoreTestResult(intent, controlport.OutcomeCommitted)); err == nil {
		t.Fatal("Complete(symlink) succeeded")
	}
	assertPathExists(t, canonical)
}

func TestFileOperationStoreBeginRejectsMisplacedExpiredRecord(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 4, 50, 0, 0, time.UTC)}
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
	}, clock)
	requested := operationStoreTestIntent("requested-operation", "digest-a")
	misplaced := operationStoreTestIntent("misplaced-operation", "digest-b")
	writeRawOperationRecord(t, store, requested, expiredTestRecord(clock.Now(), misplaced))
	path := store.path(requested)

	if _, _, err := store.Begin(context.Background(), requested); err == nil {
		t.Fatal("Begin(misplaced expired record) succeeded")
	}
	assertPathExists(t, path)
}

func TestFileOperationStoreSweepSyncsDirectoryAfterRecordRemoval(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 5, 0, 0, 0, time.UTC)}
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
	}, clock)
	intent := operationStoreTestIntent("fsync-removal", "digest")
	if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin() = created %v, error %v", created, err)
	}
	if _, err := store.Complete(context.Background(), intent, operationStoreTestResult(intent, controlport.OutcomeCommitted)); err != nil {
		t.Fatal(err)
	}
	path := store.path(intent)
	clock.Advance(time.Hour + time.Nanosecond)
	var syncCalls int
	store.syncDirectory = func(directory string) error {
		syncCalls++
		assertPathMissing(t, path)
		return syncOperationStoreDirectory(directory)
	}
	result, err := sweepRetentionCycle(context.Background(), store)
	if err != nil || result.RemovedTerminal != 1 || syncCalls == 0 {
		t.Fatalf("Sweep() = %#v, %v, sync calls %d", result, err, syncCalls)
	}
}

func TestFileOperationStoreInterruptedSweepFailsSafeAfterRemove(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 6, 0, 0, 0, time.UTC)}
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
	}, clock)
	intent := operationStoreTestIntent("interrupted-removal", "digest")
	if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin() = created %v, error %v", created, err)
	}
	if _, err := store.Complete(context.Background(), intent, operationStoreTestResult(intent, controlport.OutcomeCommitted)); err != nil {
		t.Fatal(err)
	}
	path := store.path(intent)
	clock.Advance(time.Hour + time.Nanosecond)
	wantErr := errors.New("injected directory sync interruption")
	store.syncDirectory = func(string) error {
		assertPathMissing(t, path)
		return wantErr
	}
	result, err := sweepRetentionCycle(context.Background(), store)
	if !errors.Is(err, wantErr) || result.RemovedTerminal != 1 {
		t.Fatalf("Sweep() = %#v, %v", result, err)
	}
	assertPathMissing(t, path)
	store.syncDirectory = syncOperationStoreDirectory
	if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin(after interrupted cleanup) = created %v, error %v", created, err)
	}
}

func TestFileOperationStoreSparseSweepClosesDirectoryCursor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 6, 30, 0, 0, time.UTC)}
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
		SweepTimeLimit:    time.Second,
	}, clock)
	intent := operationStoreTestIntent("sparse-directory", "digest")
	writeRawOperationRecord(t, store, intent, retainedTestRecord(clock.Now(), intent, controlport.OutcomeUnknown))

	result, err := store.Sweep(context.Background())
	if err != nil || result.More {
		t.Fatalf("Sweep() = %#v, %v; want completed sparse traversal", result, err)
	}
	if store.scanDirectory != nil {
		t.Fatal("completed sparse traversal retained an open directory cursor")
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove operation store after completed sweep: %v", err)
	}
}

func TestFileOperationStoreSweepIsBoundedAndCursorMakesProgress(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)}
	const (
		batchSize   = 17
		deleteLimit = 8
		expired     = 180
		protected   = 120
		corrupt     = 20
	)
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    batchSize,
		SweepDeleteLimit:  deleteLimit,
		SweepTimeLimit:    time.Second,
	}, clock)
	for index := range protected {
		intent := operationStoreTestIntent(fmt.Sprintf("bounded-unknown-%03d", index), "digest")
		writeRawOperationRecord(t, store, intent, retainedTestRecord(clock.Now(), intent, controlport.OutcomeUnknown))
	}
	for index := range expired {
		intent := operationStoreTestIntent(fmt.Sprintf("bounded-expired-%03d", index), "digest")
		writeRawOperationRecord(t, store, intent, expiredTestRecord(clock.Now(), intent))
	}
	for index := range corrupt {
		intent := operationStoreTestIntent(fmt.Sprintf("bounded-corrupt-%03d", index), "digest")
		if err := os.WriteFile(store.path(intent), []byte("{broken"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var removed int
	moreSeen := false
	for attempt := range 128 {
		result, err := store.Sweep(context.Background())
		if err != nil {
			t.Fatalf("Sweep(%d) error = %v", attempt, err)
		}
		if result.Scanned > batchSize || result.RemovedTerminal+result.RemovedTemporary > deleteLimit {
			t.Fatalf("Sweep(%d) exceeded bounds: %#v", attempt, result)
		}
		moreSeen = moreSeen || result.More
		removed += result.RemovedTerminal
		if removed == expired {
			break
		}
	}
	if removed != expired || !moreSeen {
		t.Fatalf("removed %d/%d records, saw More=%v", removed, expired, moreSeen)
	}
	unknownIntent := operationStoreTestIntent("bounded-unknown-000", "digest")
	corruptIntent := operationStoreTestIntent("bounded-corrupt-000", "digest")
	assertPathExists(t, store.path(unknownIntent))
	assertPathExists(t, store.path(corruptIntent))
}

func TestFileOperationStoreSweepHonorsSoftTimeLimit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)}
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
		SweepDeleteLimit:  64,
		SweepTimeLimit:    time.Nanosecond,
	}, clock)
	for index := range 64 {
		intent := operationStoreTestIntent(fmt.Sprintf("time-bound-%03d", index), "digest")
		writeRawOperationRecord(t, store, intent, expiredTestRecord(clock.Now(), intent))
	}
	result, err := store.Sweep(context.Background())
	if err != nil || result.Scanned > 1 || !result.More {
		t.Fatalf("Sweep() = %#v, %v", result, err)
	}
}

func TestFileOperationStoreOpportunisticSweepUsesMinimumInterval(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)}
	const interval = 2 * time.Hour
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     interval,
		SweepBatchSize:    64,
		SweepTimeLimit:    time.Second,
	}, clock)
	seed := operationStoreTestIntent("interval-seed", "digest")
	if _, created, err := store.Begin(context.Background(), seed); err != nil || !created {
		t.Fatalf("Begin(seed) = created %v, error %v", created, err)
	}
	expiredIntent := operationStoreTestIntent("interval-expired", "digest")
	writeRawOperationRecord(t, store, expiredIntent, expiredTestRecord(clock.Now(), expiredIntent))
	within := operationStoreTestIntent("interval-within", "digest")
	if _, created, err := store.Begin(context.Background(), within); err != nil || !created {
		t.Fatalf("Begin(within) = created %v, error %v", created, err)
	}
	assertPathExists(t, store.path(expiredIntent))
	clock.Advance(interval + time.Nanosecond)
	after := operationStoreTestIntent("interval-after", "digest")
	if _, created, err := store.Begin(context.Background(), after); err != nil || !created {
		t.Fatalf("Begin(after) = created %v, error %v", created, err)
	}
	assertPathMissing(t, store.path(expiredIntent))
}

func TestFileOperationStoreOpportunisticSweepCatchesUpAcrossBegins(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 9, 30, 0, 0, time.UTC)}
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    7,
		SweepDeleteLimit:  3,
		SweepTimeLimit:    time.Second,
	}, clock)
	var expiredPaths []string
	for index := range 40 {
		intent := operationStoreTestIntent(fmt.Sprintf("catch-up-expired-%03d", index), "digest")
		writeRawOperationRecord(t, store, intent, expiredTestRecord(clock.Now(), intent))
		expiredPaths = append(expiredPaths, store.path(intent))
	}

	for attempt := range 80 {
		intent := operationStoreTestIntent(fmt.Sprintf("catch-up-trigger-%03d", attempt), "digest")
		if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
			t.Fatalf("Begin(trigger %d) = created %v, error %v", attempt, created, err)
		}
		remaining := 0
		for _, path := range expiredPaths {
			if _, err := os.Lstat(path); err == nil {
				remaining++
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
		if remaining == 0 {
			return
		}
	}
	t.Fatal("opportunistic sweeps did not catch up without advancing the minimum interval clock")
}

func TestFileOperationStoreConcurrentSweepWaitHonorsContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 9, 45, 0, 0, time.UTC)}
	store := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
		SweepTimeLimit:    time.Second,
	}, clock)
	intent := operationStoreTestIntent("cancelled-sweep-wait", "digest")
	if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
		t.Fatalf("Begin() = created %v, error %v", created, err)
	}
	if _, err := store.Complete(context.Background(), intent, operationStoreTestResult(intent, controlport.OutcomeCommitted)); err != nil {
		t.Fatal(err)
	}
	if _, err := sweepRetentionCycle(context.Background(), store); err != nil {
		t.Fatalf("Sweep(before expiry) error = %v", err)
	}
	clock.Advance(time.Hour + time.Nanosecond)

	syncStarted := make(chan struct{})
	releaseSync := make(chan struct{})
	var syncOnce sync.Once
	store.syncDirectory = func(directory string) error {
		syncOnce.Do(func() {
			close(syncStarted)
			<-releaseSync
		})
		return syncOperationStoreDirectory(directory)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := store.Sweep(context.Background())
		firstDone <- err
	}()
	select {
	case <-syncStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first Sweep did not reach directory sync")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() {
		_, err := store.Sweep(ctx)
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			close(releaseSync)
			t.Fatalf("second Sweep error = %v, want deadline exceeded", err)
		}
	case <-time.After(500 * time.Millisecond):
		close(releaseSync)
		<-secondDone
		t.Fatal("second Sweep ignored its context while another sweep held the root lock")
	}
	close(releaseSync)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Sweep error = %v", err)
	}
}

func TestFileOperationStoreRootPolicyChangeFailsOpenStoresClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	first, err := NewFileOperationStoreWithConfig(root, OperationRetentionConfig{TerminalRetention: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if err := first.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	adopter := NewFileOperationStore(root)
	t.Cleanup(func() { _ = adopter.Close() })
	if err := adopter.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	reconfigured, err := NewFileOperationStoreWithConfig(root, OperationRetentionConfig{TerminalRetention: 3 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reconfigured.Close() })
	if err := reconfigured.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	intent := operationStoreTestIntent("policy-changed", "digest")
	if _, _, err := first.Begin(context.Background(), intent); !errors.Is(err, ErrOperationRetentionPolicyChanged) {
		t.Fatalf("first Begin() error = %v, want policy changed", err)
	}
	if _, _, err := adopter.Begin(context.Background(), intent); !errors.Is(err, ErrOperationRetentionPolicyChanged) {
		t.Fatalf("adopter Begin() error = %v, want policy changed", err)
	}
	record, created, err := reconfigured.Begin(context.Background(), intent)
	if err != nil || !created || time.Duration(record.TerminalRetentionNanoseconds) != 3*time.Hour {
		t.Fatalf("reconfigured Begin() = %#v, created %v, error %v", record, created, err)
	}
}

func TestFileOperationStorePolicyChangeAllowsInFlightComplete(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 9, 55, 0, 0, time.UTC)}
	first := newFileRetentionTestStore(t, root, OperationRetentionConfig{TerminalRetention: 2 * time.Hour}, clock)
	intent := operationStoreTestIntent("policy-change-in-flight", "digest")
	started, created, err := first.Begin(context.Background(), intent)
	if err != nil || !created {
		t.Fatalf("Begin() = %#v, created %v, error %v", started, created, err)
	}
	reconfigured := newFileRetentionTestStore(t, root, OperationRetentionConfig{TerminalRetention: 3 * time.Hour}, clock)
	want := operationStoreTestResult(intent, controlport.OutcomeCommitted)
	completed, err := first.Complete(context.Background(), intent, want)
	if err != nil || completed.Result == nil || *completed.Result != want ||
		time.Duration(completed.TerminalRetentionNanoseconds) != 2*time.Hour {
		t.Fatalf("Complete(after policy change) = %#v, %v", completed, err)
	}
	if _, _, err := first.Begin(context.Background(), intent); !errors.Is(err, ErrOperationRetentionPolicyChanged) {
		t.Fatalf("old store Begin() error = %v, want policy changed", err)
	}
	replayed, created, err := reconfigured.Begin(context.Background(), intent)
	if err != nil || created || replayed.Result == nil || *replayed.Result != want {
		t.Fatalf("new store Begin(replay) = %#v, created %v, error %v", replayed, created, err)
	}
}

func TestFileOperationStoreLegacyRetentionUsesPolicyHighWaterMark(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operations")
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)}
	original := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: 30 * 24 * time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
	}, clock)
	intent := operationStoreTestIntent("legacy-policy-high-water", "digest")
	intent.CreatedAt = clock.Now().Add(-20 * 24 * time.Hour)
	result := operationStoreTestResult(intent, controlport.OutcomeCommitted)
	legacy := OperationRecord{
		Intent:    intent,
		Result:    &result,
		UpdatedAt: clock.Now().Add(-10 * 24 * time.Hour),
	}
	writeRawOperationRecord(t, original, intent, legacy)

	lowered := newFileRetentionTestStore(t, root, OperationRetentionConfig{
		TerminalRetention: 24 * time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
	}, clock)
	swept, err := sweepRetentionCycle(context.Background(), lowered)
	if err != nil || swept.RemovedTerminal != 0 || swept.RetainedTerminal == 0 {
		t.Fatalf("Sweep() = %#v, %v; want legacy record retained by 30-day high-water mark", swept, err)
	}
	materialized, err := readOperationRecord(lowered.path(intent))
	if err != nil {
		t.Fatal(err)
	}
	wantRetention := 30 * 24 * time.Hour
	wantDeadline := legacy.UpdatedAt.Add(wantRetention)
	if materialized.Version != operationRecordSchemaVersion ||
		time.Duration(materialized.TerminalRetentionNanoseconds) != wantRetention ||
		!materialized.RetainUntil.Equal(wantDeadline) {
		t.Fatalf("materialized legacy record = %#v, want retention %v and deadline %v", materialized, wantRetention, wantDeadline)
	}
}

func newRetentionTestStore(
	t *testing.T,
	kind string,
	config OperationRetentionConfig,
	clock *fakeOperationClock,
) retentionTestStore {
	t.Helper()
	switch kind {
	case "memory":
		store, err := NewMemoryOperationStoreWithConfig(config)
		if err != nil {
			t.Fatal(err)
		}
		store.now = clock.Now
		return store
	case "file":
		return newFileRetentionTestStore(t, filepath.Join(t.TempDir(), "operations"), config, clock)
	default:
		t.Fatalf("unknown store kind %q", kind)
		return nil
	}
}

func newFileRetentionTestStore(
	t *testing.T,
	root string,
	config OperationRetentionConfig,
	clock *fakeOperationClock,
) *FileOperationStore {
	t.Helper()
	store, err := NewFileOperationStoreWithConfig(root, config)
	if err != nil {
		t.Fatal(err)
	}
	store.now = clock.Now
	if err := store.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func newRetentionRaceStores(
	t *testing.T,
	kind string,
	config OperationRetentionConfig,
	clock *fakeOperationClock,
) (retentionTestStore, retentionTestStore) {
	t.Helper()
	if kind == "memory" {
		store := newRetentionTestStore(t, kind, config, clock)
		return store, store
	}
	root := filepath.Join(t.TempDir(), "operations")
	return newFileRetentionTestStore(t, root, config, clock), newFileRetentionTestStore(t, root, config, clock)
}

func operationStoreTestResult(intent OperationIntent, outcome controlport.Outcome) controlport.CommandResult {
	return controlport.CommandResult{
		OperationID: intent.OperationID,
		SessionID:   intent.SessionID,
		Outcome:     outcome,
		Revision:    7,
	}
}

func sweepRetentionCycle(ctx context.Context, store retentionTestStore) (OperationSweepResult, error) {
	var total OperationSweepResult
	startedTraversal := false
	for range 1024 {
		result, err := store.Sweep(ctx)
		total.Scanned += result.Scanned
		total.RemovedTerminal += result.RemovedTerminal
		total.RemovedTemporary += result.RemovedTemporary
		total.RetainedTerminal += result.RetainedTerminal
		total.RetainedIndeterminate += result.RetainedIndeterminate
		total.Corrupt += result.Corrupt
		total.More = result.More
		startedTraversal = startedTraversal || result.Scanned > 0
		if err != nil || (!result.More && startedTraversal) {
			return total, err
		}
	}
	return total, errors.New("operation retention sweep did not complete a bounded traversal")
}

func retainedTestRecord(now time.Time, intent OperationIntent, outcome controlport.Outcome) OperationRecord {
	created := now.Add(-2 * time.Hour)
	updated := now.Add(-time.Hour)
	intent.CreatedAt = created
	result := operationStoreTestResult(intent, outcome)
	record := OperationRecord{
		Version:                      operationRecordSchemaVersion,
		Intent:                       intent,
		Result:                       &result,
		TerminalRetentionNanoseconds: int64(time.Hour),
		UpdatedAt:                    updated,
	}
	if terminalOperationOutcome(outcome) {
		record.RetainUntil = updated.Add(time.Hour)
	}
	return record
}

func expiredTestRecord(now time.Time, intent OperationIntent) OperationRecord {
	return retainedTestRecord(now, intent, controlport.OutcomeCommitted)
}

func writeRawOperationRecord(t *testing.T, store *FileOperationStore, intent OperationIntent, record OperationRecord) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path(intent), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("path %q should exist: %v", path, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %q error = %v, want not exist", path, err)
	}
}
