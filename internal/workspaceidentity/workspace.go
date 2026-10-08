// Package workspaceidentity owns the process-derived workspace address and
// the compatibility lookup for addresses already persisted by older clients.
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
	key := canonical
	if key == string(filepath.Separator) {
		key = "workspace:" + key
	}
	return session.WorkspaceRef{Key: key, CWD: canonical}, nil
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
