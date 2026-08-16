//go:build windows

package appserver

import "testing"

func TestSyncOperationStoreDirectoryDoesNotFlushReadOnlyWindowsDirectory(t *testing.T) {
	if err := syncOperationStoreDirectory(t.TempDir()); err != nil {
		t.Fatalf("syncOperationStoreDirectory() error = %v", err)
	}
}
