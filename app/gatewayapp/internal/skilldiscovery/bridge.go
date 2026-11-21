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

// DiscoverMetaRequest discovers Skills using the product's configured system roots.
func DiscoverMetaRequest(req skill.DiscoverRequest) []skill.Meta {
	return sdkfs.DiscoverMetaRequest(prepareDiscovery(req))
}

// DiscoverLegacyPluginCopies detects regular copies of plugin Skills for diagnostics.
func DiscoverLegacyPluginCopies(req skill.DiscoverRequest) []skill.Meta {
	return sdkfs.DiscoverLegacyPluginCopies(prepareDiscovery(req))
}

func prepareDiscovery(req skill.DiscoverRequest) skill.DiscoverRequest {
	if req.Dirs == nil {
		req.Dirs = DefaultDiscoveryDirs(req.WorkspaceDir)
	}
	if systemDiscoveryRequested(req.Dirs) {
		if systemRoot, err := Ensure(); err != nil {
			req.Dirs = withoutDiscoveryDir(req.Dirs, systemRoot)
		}
	}
	return req
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
