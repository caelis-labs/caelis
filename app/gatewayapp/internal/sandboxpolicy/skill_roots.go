package sandboxpolicy

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/fsboundary"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
)

// ExternalSkillReadRoots returns resolved content directories for valid skills
// that live outside the resolved workspace. Workspace-local skills inherit the
// workspace-write policy and do not need narrower duplicate roots.
func ExternalSkillReadRoots(workspaceDir string, metas []skill.Meta) []string {
	workspaceResolved, ok := resolvedExistingDirectory(workspaceDir)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(metas))
	seen := make(map[string]struct{}, len(metas))
	for _, meta := range metas {
		root, err := skillfs.RootDir(meta.Path)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		resolved = filepath.Clean(resolved)
		if fsboundary.PathIsUnder(resolved, workspaceResolved) {
			continue
		}
		if _, exists := seen[resolved]; exists {
			continue
		}
		seen[resolved] = struct{}{}
		out = append(out, resolved)
	}
	return out
}

func resolvedExistingDirectory(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", false
	}
	return filepath.Clean(resolved), true
}
