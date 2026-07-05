package skilldiscovery

import (
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
	sdkfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
)

func DefaultDiscoveryDirs(workspaceDir string) []string {
	out := []string{"~/.caelis/skills/.system"}
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir != "" {
		out = append(out,
			filepath.Join(workspaceDir, ".claude", "skills"),
			filepath.Join(workspaceDir, ".opencode", "skills"),
			filepath.Join(workspaceDir, ".opencode", "skill"),
			filepath.Join(workspaceDir, ".agents", "skills"),
			filepath.Join(workspaceDir, "skills"),
		)
	}
	out = append(out, "~/.caelis/skills")
	out = append(out,
		"~/.claude/skills",
		"~/.config/opencode/skills",
		"~/.config/opencode/skill",
		"~/.agents/skills",
	)
	return out
}

func DiscoverMeta(dirs []string, workspaceDir string) ([]skill.Meta, error) {
	return DiscoverMetaRequest(skill.DiscoverRequest{
		Dirs:         dirs,
		WorkspaceDir: workspaceDir,
	})
}

func DiscoverMetaRequest(req skill.DiscoverRequest) ([]skill.Meta, error) {
	dirs := discoveryDirs(req.Dirs, req.WorkspaceDir)
	if systemRoot, err := maybeEnsureSystemSkills(dirs); err != nil {
		dirs = withoutDiscoveryDir(dirs, systemRoot)
	}
	req.Dirs = dirs
	return sdkfs.DiscoverMetaRequest(req)
}

func DiscoverLegacyPluginCopies(req skill.DiscoverRequest) ([]skill.Meta, error) {
	dirs := discoveryDirs(req.Dirs, req.WorkspaceDir)
	if systemRoot, err := maybeEnsureSystemSkills(dirs); err != nil {
		dirs = withoutDiscoveryDir(dirs, systemRoot)
	}
	req.Dirs = dirs
	return sdkfs.DiscoverLegacyPluginCopies(req)
}

func DiscoverPluginBundleMeta(bundles []skill.PluginBundle) ([]skill.Meta, error) {
	return sdkfs.DiscoverPluginBundleMeta(bundles)
}

func discoveryDirs(dirs []string, workspaceDir string) []string {
	if len(dirs) == 0 {
		return DefaultDiscoveryDirs(workspaceDir)
	}
	return dirs
}

func maybeEnsureSystemSkills(dirs []string) (string, error) {
	if len(dirs) == 0 || systemDiscoveryRequested(dirs) {
		systemRoot, err := Ensure()
		return systemRoot, err
	}
	return "", nil
}

func systemDiscoveryRequested(dirs []string) bool {
	systemRoot, rootErr := Root()
	for _, dir := range dirs {
		if rootErr == nil {
			resolved, err := sdkfs.ResolvePath(dir)
			if err == nil && sameDiscoveryPath(resolved, systemRoot) {
				return true
			}
		}
		if filepath.ToSlash(filepath.Clean(strings.TrimSpace(dir))) == "~/.caelis/skills/.system" {
			return true
		}
	}
	return false
}

func withoutDiscoveryDir(dirs []string, skip string) []string {
	skip = filepath.Clean(strings.TrimSpace(skip))
	if skip == "" || skip == "." {
		return append([]string(nil), dirs...)
	}
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		resolved, err := sdkfs.ResolvePath(dir)
		if err == nil && sameDiscoveryPath(resolved, skip) {
			continue
		}
		out = append(out, dir)
	}
	return out
}

func sameDiscoveryPath(a string, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if a == b {
		return true
	}
	return strings.EqualFold(a, b)
}
