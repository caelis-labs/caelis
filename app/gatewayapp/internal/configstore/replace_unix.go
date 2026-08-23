//go:build !windows

package configstore

import "os"

func replaceFileAtomic(source, destination string) error {
	return os.Rename(source, destination)
}
