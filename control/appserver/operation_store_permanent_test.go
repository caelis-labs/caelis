package appserver

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteOperationStorePermanentReceiptSurvivesCompletionAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	store := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{TerminalRetention: time.Hour})
	created := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	intent := operationStoreTestIntent("imported-permanent", "original-intent")
	intent.CreatedAt = created
	record := OperationRecord{Version: operationRecordSchemaVersion, RetainForever: true, Intent: intent, UpdatedAt: created}
	if inserted, err := store.insertOperationRecord(t.Context(), record); err != nil || !inserted {
		t.Fatalf("insert permanent intent: %v, %v", inserted, err)
	}
	store.now = func() time.Time { return created.AddDate(10, 0, 0) }
	if _, fresh, err := store.Begin(t.Context(), intent); err != nil || fresh {
		t.Fatalf("permanent intent redispatched: %v, %v", fresh, err)
	}
	want := operationStoreTestResult(intent, OutcomeCommitted)
	completed, err := store.Complete(t.Context(), intent, want)
	if err != nil || !completed.RetainForever || !completed.RetainUntil.IsZero() || completed.TerminalRetentionNanoseconds != 0 {
		t.Fatalf("completion lost permanent retention: %+v, %v", completed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{TerminalRetention: time.Hour})
	reopened.now = func() time.Time { return created.AddDate(20, 0, 0) }
	if sweep, err := reopened.Sweep(t.Context()); err != nil || sweep.RemovedTerminal != 0 {
		t.Fatalf("permanent receipt swept: %+v, %v", sweep, err)
	}
	got, found, err := reopened.Read(t.Context(), intent.PrincipalID, intent.OperationID)
	if err != nil || !found || got.Result == nil || !sameCommandResult(*got.Result, want) {
		t.Fatalf("original receipt missing: %+v, %v, %v", got, found, err)
	}
	changed := intent
	changed.Digest = "changed"
	if _, _, err := reopened.Begin(t.Context(), changed); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed imported intent did not conflict: %v", err)
	}
}

func TestOperationRecordVersionAndPermanentRetentionValidation(t *testing.T) {
	now := time.Now().UTC()
	intent := operationStoreTestIntent("version", "digest")
	intent.CreatedAt = now
	old := OperationRecord{Version: 1, Intent: intent, UpdatedAt: now, TerminalRetentionNanoseconds: int64(time.Hour)}
	if err := validateOperationRecord(old); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*OperationRecord){
		func(r *OperationRecord) { r.Version = 99 },
		func(r *OperationRecord) { r.RetainForever = true },
		func(r *OperationRecord) { r.Version = operationRecordSchemaVersion; r.RetainForever = true },
		func(r *OperationRecord) {
			r.Version = operationRecordSchemaVersion
			r.RetainForever = true
			r.TerminalRetentionNanoseconds = 0
			r.RetainUntil = now
		},
	} {
		bad := old
		mutate(&bad)
		if err := validateOperationRecord(bad); err == nil {
			t.Fatalf("accepted inconsistent retention: %+v", bad)
		}
	}
}
