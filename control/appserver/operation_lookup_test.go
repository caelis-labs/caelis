package appserver

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestOperationLookupNeverAdmitsOrRefreshesWork(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "sqlite"}[durable], func(t *testing.T) {
			now := time.Now()
			var store OperationStore
			if durable {
				s := newTestSQLiteOperationStore(t, filepath.Join(t.TempDir(), "control.sqlite"), OperationRetentionConfig{TerminalRetention: time.Hour})
				s.now = func() time.Time { return now }
				store = s
			} else {
				s, err := NewMemoryOperationStoreWithConfig(OperationRetentionConfig{TerminalRetention: time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				s.now = func() time.Time { return now }
				store = s
			}
			intent := operationStoreTestIntent("lookup", "request")
			if _, found, err := store.Lookup(t.Context(), intent); err != nil || found {
				t.Fatalf("missing lookup = %v, %v", found, err)
			}
			if _, fresh, err := store.Begin(t.Context(), intent); err != nil || !fresh {
				t.Fatalf("lookup admitted an intent: fresh = %v, %v", fresh, err)
			}
			if record, found, err := store.Lookup(t.Context(), intent); err != nil || !found || record.Result != nil {
				t.Fatalf("incomplete lookup = %+v, %v, %v", record, found, err)
			}
			result := operationStoreTestResult(intent, OutcomeCommitted)
			if _, err := store.Complete(t.Context(), intent, result); err != nil {
				t.Fatal(err)
			}
			if record, found, err := store.Lookup(t.Context(), intent); err != nil || !found || record.Result == nil || !sameCommandResult(*record.Result, result) {
				t.Fatalf("committed lookup = %+v, %v, %v", record, found, err)
			}
			changed := intent
			changed.Action = ActionWorkerCreate
			if _, _, err := store.Lookup(t.Context(), changed); !errors.Is(err, ErrOperationConflict) {
				t.Fatalf("changed intent lookup = %v", err)
			}
			now = now.Add(2 * time.Hour)
			if _, found, err := store.Lookup(t.Context(), intent); err != nil || found {
				t.Fatalf("expired lookup = %v, %v", found, err)
			}
		})
	}
}
