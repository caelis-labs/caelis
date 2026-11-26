package sdkctx

import (
	"path/filepath"
	"runtime"
	"strings"
)

// CompareCWD orders native paths after cleaning separators and dot components.
// Windows comparisons also fold case, including non-ASCII directory names.
// It does not access the filesystem: listing persisted metadata must work even
// when a workspace is unavailable and must not depend on current symlink targets.
func CompareCWD(left, right string) int {
	return strings.Compare(cwdKey(left), cwdKey(right))
}

func cwdKey(path string) string {
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
