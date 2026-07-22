//go:build windows

package controlclient

import (
	"errors"
	"os"
	"path/filepath"
)

// Windows cannot fsync a directory through os.File. Atomically moving the
// canonical record with MOVEFILE_WRITE_THROUGH makes key removal durable; a
// crash may leave only the non-canonical GC temp for a later sweep.
func prepareOperationStoreRecordRemoval(path string) (string, error) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".operation-gc-*.tmp")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := replaceOperationStoreFile(path, tempPath); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	return tempPath, nil
}

func cleanupRemovedOperationStoreRecord(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
