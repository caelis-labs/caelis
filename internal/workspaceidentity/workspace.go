// Package workspaceidentity owns the process-derived workspace address and
// the compatibility lookup for addresses already persisted by older clients.
package workspaceidentity

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// FromCWD derives a stable collision-free workspace address from a current
// working directory.
func FromCWD(cwd string) (session.WorkspaceRef, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(cwd))
	if err != nil {
		return session.WorkspaceRef{}, fmt.Errorf("workspaceidentity: resolve CWD: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return session.WorkspaceRef{}, fmt.Errorf("workspaceidentity: resolve CWD: %w", err)
	}
	canonical = filepath.Clean(canonical)
	key := canonical
	if key == string(filepath.Separator) {
		key = "workspace:" + key
	}
	return session.WorkspaceRef{Key: key, CWD: canonical}, nil
}
