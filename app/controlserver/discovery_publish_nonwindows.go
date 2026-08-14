//go:build !windows

package controlserver

import "os"

func replaceDiscoveryFile(source string, target string) error {
	return os.Rename(source, target)
}
