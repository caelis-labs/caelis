//go:build windows

package gatewayapp

import "os"

// Windows has no filesystem FIFOs. Root.Open confines the NT file open to the
// directory handle; the caller verifies a regular file and its original identity
// before reading. Local path validation excludes device and named-pipe paths.
func openConfinedArtifact(root *os.Root, path string) (*os.File, error) {
	return root.Open(path)
}
