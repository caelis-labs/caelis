//go:build !windows

package controlclient

import "os"

func replaceOperationStoreFile(from, to string) error {
	return os.Rename(from, to)
}
