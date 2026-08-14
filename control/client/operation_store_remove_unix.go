//go:build !windows

package controlclient

import "os"

func prepareOperationStoreRecordRemoval(path string) (string, error) {
	return "", os.Remove(path)
}

func cleanupRemovedOperationStoreRecord(string) error {
	return nil
}
