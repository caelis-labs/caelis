//go:build !windows

package resources

import "os"

func isLinkOrReparsePoint(_ string, info os.FileInfo) (bool, error) {
	return info.Mode()&os.ModeSymlink != 0, nil
}
