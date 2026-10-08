//go:build !windows

package fsboundary

import "path/filepath"

// ResolveExistingPath resolves an existing path to its physical spelling,
// including symbolic links and Windows junctions. Absolute input stays absolute.
func ResolveExistingPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
