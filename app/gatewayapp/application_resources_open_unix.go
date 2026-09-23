//go:build unix

package gatewayapp

import (
	"os"

	"golang.org/x/sys/unix"
)

// openConfinedArtifact never waits for a writer if an execution task replaces a
// regular file with a FIFO between Lstat and Open. Root.OpenFile keeps the path
// confined; the caller checks fstat and identity before reading any bytes.
func openConfinedArtifact(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}
