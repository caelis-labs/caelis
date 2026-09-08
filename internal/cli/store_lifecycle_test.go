package cli

import (
	"path/filepath"
	"testing"
)

func TestRejectStoreBackupOutputInsideStore(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := rejectStoreBackupOutputInsideStore(storeDir, filepath.Join(storeDir, "backup.zip")); err == nil {
		t.Fatal("backup output inside Store was accepted")
	}
	if err := rejectStoreBackupOutputInsideStore(storeDir, filepath.Join(filepath.Dir(storeDir), "backup.zip")); err != nil {
		t.Fatalf("sibling backup output rejected: %v", err)
	}
	if err := rejectStoreBackupOutputInsideStore(storeDir, filepath.Join(storeDir, "..", "store-backup.zip")); err != nil {
		t.Fatalf("normalized sibling backup output rejected: %v", err)
	}
}
