package gatewayapp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreUpgradeUnknownCommitCannotBecomeRollbackable(t *testing.T) {
	storeDir := t.TempDir()
	journal := storeUpgradeJournal{
		Format: storeUpgradeJournalFormat, State: "prepared", StoreDir: storeDir,
		TargetWriter: currentStoreUpgradeWriter(), PreflightDigest: "preflight",
		CreatedAt: time.Now().UTC(),
	}
	commitErr := errors.New("owner commit outcome unknown")
	_, err := commitStoreUpgradeGeneration(storeDir, journal, func() error {
		pending, err := readStoreUpgradeJournal(storeDir)
		if err != nil || pending.State != "committing" {
			t.Fatalf("owner commit admitted before durable decision: %+v, %v", pending, err)
		}
		return commitErr
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("commit = %v", err)
	}
	pending, err := readStoreUpgradeJournal(storeDir)
	if err != nil || pending.State != "committing" {
		t.Fatalf("unknown outcome lost: %+v, %v", pending, err)
	}
	if _, err := RollbackStoreUpgrade(t.Context(), storeDir); err == nil || !strings.Contains(err.Error(), "commit outcome is unresolved") {
		t.Fatalf("rollback of unknown commit = %v", err)
	}
	if err := rejectPendingStoreUpgrade(storeDir); err == nil {
		t.Fatal("unknown commit admitted a writer")
	}
	report, err := commitStoreUpgradeGeneration(storeDir, pending, func() error { return nil })
	if err != nil || report.State != "committed" {
		t.Fatalf("commit retry = %+v, %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(storeDir, storeUpgradeJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful commit retained recovery journal: %v", err)
	}
}
