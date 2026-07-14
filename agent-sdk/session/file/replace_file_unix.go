//go:build !windows

package file

import "os"

func replaceFile(from, to string) error {
	return os.Rename(from, to)
}
