// Package workspaceidentity derives workspace addresses from working directories.
package workspaceidentity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/fsboundary"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// FromCWD derives a stable collision-free workspace address from a current
// working directory.
func FromCWD(cwd string) (session.WorkspaceRef, error) {
	canonical, err := CanonicalDirectory(cwd)
	if err != nil {
		return session.WorkspaceRef{}, err
	}
	return session.WorkspaceRef{Key: KeyForDirectory(canonical), CWD: canonical}, nil
}

// KeyForDirectory returns the workspace key for a canonical absolute path.
// Stored paths can use it even when the directory is no longer on disk.
func KeyForDirectory(canonical string) string {
	if canonical == string(filepath.Separator) {
		return "workspace:" + canonical
	}
	return canonical
}

// CanonicalDirectory resolves an existing directory to its physical absolute
// path, including Windows junctions and symbolic links.
func CanonicalDirectory(cwd string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(cwd))
	if err != nil {
		return "", fmt.Errorf("workspaceidentity: resolve CWD: %w", err)
	}
	canonical, err := fsboundary.ResolveExistingPath(absolute)
	if err != nil {
		return "", fmt.Errorf("workspaceidentity: resolve CWD: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspaceidentity: CWD is not a directory")
	}
	return filepath.Clean(canonical), nil
}
