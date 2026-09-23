// Package cwdpath defines lexical working-directory matching for Session lists.
package cwdpath

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Compare orders native paths after cleaning separators and dot components.
// Windows comparisons also fold case, including non-ASCII directory names.
// It does not access the filesystem: listing persisted metadata must work even
// when a workspace is unavailable and must not depend on current symlink targets.
func Compare(left, right string) int {
	return strings.Compare(key(left), key(right))
}

func key(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}
