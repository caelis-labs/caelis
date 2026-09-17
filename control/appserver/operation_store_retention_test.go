package appserver

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
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
	for _, kind := range []string{"memory", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)}
			store := newRetentionTestStore(t, kind, config, clock)
			intent := operationStoreTestIntent("retention-window", "digest-a")
			if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
				t.Fatalf("Begin() = created %v, error %v", created, err)
			}
			want := operationStoreTestResult(intent, OutcomeCommitted)
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
	for _, kind := range []string{"memory", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC)}
			store := newRetentionTestStore(t, kind, config, clock)
			outcomes := map[string]Outcome{
				"committed":  OutcomeCommitted,
				"rejected":   OutcomeRejected,
				"conflicted": OutcomeConflicted,
				"accepted":   OutcomeAccepted,
				"unknown":    OutcomeUnknown,
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

func TestOperationStoreSweepRacingCompleteRetainsCommittedResult(t *testing.T) {
	config := OperationRetentionConfig{TerminalRetention: time.Hour, SweepInterval: 24 * time.Hour, SweepBatchSize: 64}
	for _, kind := range []string{"memory", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)}
			primary, sweeper := newRetentionRaceStores(t, kind, config, clock)
			for index := range 32 {
				intent := operationStoreTestIntent(fmt.Sprintf("complete-race-%d", index), "digest")
				if _, created, err := primary.Begin(context.Background(), intent); err != nil || !created {
					t.Fatalf("Begin(%d) = created %v, error %v", index, created, err)
				}
				clock.Advance(2 * time.Hour)
				want := operationStoreTestResult(intent, OutcomeCommitted)
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

func TestOperationStoreSweepHonorsSoftTimeLimit(t *testing.T) {
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)}
	store, err := NewMemoryOperationStoreWithConfig(OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     24 * time.Hour,
		SweepBatchSize:    64,
		SweepDeleteLimit:  64,
		SweepTimeLimit:    time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.now = clock.Now
	store.elapsed = func(time.Time) time.Duration { return time.Nanosecond }
	// Two terminal records suffice to prove the soft deadline stops before the next one.
	for index := range 2 {
		intent := operationStoreTestIntent(fmt.Sprintf("time-bound-%03d", index), "digest")
		if _, created, err := store.Begin(context.Background(), intent); err != nil || !created {
			t.Fatalf("Begin(%d) = created %v, error %v", index, created, err)
		}
		if _, err := store.Complete(context.Background(), intent, operationStoreTestResult(intent, OutcomeCommitted)); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(time.Hour + time.Nanosecond)
	result, err := store.Sweep(context.Background())
	if err != nil || result.Scanned > 1 || !result.More {
		t.Fatalf("Sweep() = %#v, %v", result, err)
	}
}

func TestSQLiteOperationStoreOpportunisticSweepUsesMinimumInterval(t *testing.T) {
	const interval = 2 * time.Hour
	clock := &fakeOperationClock{now: time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)}
	store := newTestSQLiteOperationStore(t, filepath.Join(t.TempDir(), "control.sqlite"), OperationRetentionConfig{
		TerminalRetention: time.Hour,
		SweepInterval:     interval,
		SweepBatchSize:    64,
		SweepTimeLimit:    time.Second,
	})
	store.now = clock.Now
	expired := operationStoreTestIntent("interval-expired", "digest")
	if _, created, err := store.Begin(context.Background(), expired); err != nil || !created {
		t.Fatalf("Begin(expired) = created %v, error %v", created, err)
	}
	if _, err := store.Complete(context.Background(), expired, operationStoreTestResult(expired, OutcomeCommitted)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour + time.Nanosecond)
	within := operationStoreTestIntent("interval-within", "digest")
	if _, created, err := store.Begin(context.Background(), within); err != nil || !created {
		t.Fatalf("Begin(within) = created %v, error %v", created, err)
	}
	if got := countSQLiteOperationRows(t, store, expired.OperationID); got != 1 {
		t.Fatalf("expired rows before minimum interval = %d, want retained", got)
	}
	clock.Advance(interval)
	after := operationStoreTestIntent("interval-after", "digest")
	if _, created, err := store.Begin(context.Background(), after); err != nil || !created {
		t.Fatalf("Begin(after) = created %v, error %v", created, err)
	}
	if got := countSQLiteOperationRows(t, store, expired.OperationID); got != 0 {
		t.Fatalf("expired rows after minimum interval = %d, want reclaimed", got)
	}
}

func countSQLiteOperationRows(t *testing.T, store *SQLiteOperationStore, operationID string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM control_operations WHERE operation_id = ?`,
		operationID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSQLiteOperationStoreRootPolicyChangeFailsOpenStoresClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	first := newTestSQLiteOperationStoreUninitialized(t, path, OperationRetentionConfig{TerminalRetention: 2 * time.Hour})
	if err := first.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	adopter := newTestSQLiteOperationStoreUninitialized(t, path, OperationRetentionConfig{})
	if err := adopter.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	reconfigured := newTestSQLiteOperationStoreUninitialized(t, path, OperationRetentionConfig{TerminalRetention: 3 * time.Hour})
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
	case "sqlite":
		store := newTestSQLiteOperationStore(t, filepath.Join(t.TempDir(), "control.sqlite"), config)
		store.now = clock.Now
		return store
	default:
		t.Fatalf("unknown store kind %q", kind)
		return nil
	}
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
	path := filepath.Join(t.TempDir(), "control.sqlite")
	primary := newTestSQLiteOperationStore(t, path, config)
	primary.now = clock.Now
	sweeper := newTestSQLiteOperationStore(t, path, config)
	sweeper.now = clock.Now
	return primary, sweeper
}

func operationStoreTestResult(intent OperationIntent, outcome Outcome) CommandResult {
	return CommandResult{
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
