package memoryhost

import (
	"bytes"
	"context"
	"testing"
)

func TestOfflineOwnerBackupRestoreLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	host, err := Open(t.Context(), Config{
		DataDir:     dataDir,
		Credentials: func(context.Context, string) (string, error) { return "unused", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	var snapshot bytes.Buffer
	if err := host.Backup(t.Context(), &snapshot); err != nil {
		t.Fatalf("Host.Backup() error = %v", err)
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareUpgrade(t.Context(), dataDir); err != nil {
		t.Fatalf("PrepareUpgrade() error = %v", err)
	}
	if _, err := RollbackRestore(t.Context(), dataDir); err != nil {
		t.Fatalf("RollbackRestore() error = %v", err)
	}
	if _, err := Restore(t.Context(), dataDir, bytes.NewReader(snapshot.Bytes())); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if err := CommitRestore(dataDir); err != nil {
		t.Fatalf("CommitRestore() error = %v", err)
	}
}
