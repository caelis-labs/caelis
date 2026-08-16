//go:build !windows

package appserver

import "os"

func replaceOperationStoreFile(from, to string) error {
	return os.Rename(from, to)
}
