//go:build !windows

package controlclient

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncOperationStoreDirectory(t *testing.T) {
	root := t.TempDir()
	if err := syncOperationStoreDirectory(root); err != nil {
		t.Fatalf("syncOperationStoreDirectory() error = %v", err)
	}

	missing := filepath.Join(root, "missing")
	if err := syncOperationStoreDirectory(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("syncOperationStoreDirectory(missing) error = %v, want os.ErrNotExist", err)
	}
}
