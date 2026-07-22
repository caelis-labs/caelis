//go:build windows

package controlclient

import "testing"

func TestSyncOperationStoreDirectoryDoesNotFlushReadOnlyWindowsDirectory(t *testing.T) {
	if err := syncOperationStoreDirectory(t.TempDir()); err != nil {
		t.Fatalf("syncOperationStoreDirectory() error = %v", err)
	}
}
