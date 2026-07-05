//go:build !windows

package skilldiscovery

import "os"

func isLinkOrReparsePoint(_ string, info os.FileInfo) (bool, error) {
	return info.Mode()&os.ModeSymlink != 0, nil
}
