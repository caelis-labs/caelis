package appserver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestSQLiteOperationTransactionRollsBackEffectAndReceiptTogether(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteOperationStore(t, filepath.Join(t.TempDir(), "control.sqlite"), OperationRetentionConfig{})
	if _, err := store.db.Exec(`CREATE TABLE test_effect(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	intent := operationStoreTestIntent("atomic-config", "digest")
	if _, fresh, err := store.Begin(ctx, intent); err != nil || !fresh {
		t.Fatalf("begin: %v, %v", fresh, err)
	}
	result := operationStoreTestResult(intent, OutcomeCommitted)
	result.Configuration = &sessionprofile.Configuration{SessionID: "session", Revision: 2, Profile: sessionprofile.Profile{Instructions: "original"}}
	for _, commit := range []bool{false, true} {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO test_effect VALUES('committed')`); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := store.CompleteTransaction(ctx, tx, intent, result); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		record, found, err := store.Read(ctx, intent.PrincipalID, intent.OperationID)
		if err != nil || !found {
			t.Fatalf("read = %+v, %v, %v", record, found, err)
		}
		var effects int
		if err := store.db.QueryRow(`SELECT count(*) FROM test_effect`).Scan(&effects); err != nil {
			t.Fatal(err)
		}
		if commit {
			if effects != 1 || record.Result == nil || !sameCommandResult(*record.Result, result) {
				t.Fatalf("atomic commit: effects=%d receipt=%+v", effects, record.Result)
			}
		} else if effects != 0 || record.Result != nil {
			t.Fatalf("rollback left effect=%d receipt=%+v", effects, record.Result)
		}
	}
	if _, found, err := store.Read(ctx, "another-owner", intent.OperationID); err != nil || found {
		t.Fatalf("cross-principal read = %v, %v", found, err)
	}
}

func TestOperationReadDoesNotAdmitOrShareMutableReceipt(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	intent := operationStoreTestIntent("read-original", "digest")
	if _, found, err := store.Read(ctx, intent.PrincipalID, intent.OperationID); err != nil || found {
		t.Fatalf("missing read = %v, %v", found, err)
	}
	if _, fresh, err := store.Begin(ctx, intent); err != nil || !fresh {
		t.Fatalf("read admitted intent: fresh=%v, %v", fresh, err)
	}
	result := operationStoreTestResult(intent, OutcomeCommitted)
	result.Configuration = &sessionprofile.Configuration{SessionID: "s", Revision: 1, Profile: sessionprofile.Profile{SkillRoots: []string{"original"}}}
	if _, err := store.Complete(ctx, intent, result); err != nil {
		t.Fatal(err)
	}
	result.Configuration.Profile.SkillRoots[0] = "caller mutation"
	read, _, err := store.Read(ctx, intent.PrincipalID, intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	read.Result.Configuration.Profile.SkillRoots[0] = "reader mutation"
	again, _, err := store.Read(ctx, intent.PrincipalID, intent.OperationID)
	if err != nil || again.Result.Configuration.Profile.SkillRoots[0] != "original" {
		t.Fatalf("mutable receipt escaped: %+v, %v", again, err)
	}
}
