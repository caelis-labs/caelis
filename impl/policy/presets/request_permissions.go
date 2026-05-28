package presets

import (
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/policy"
)

var protectedControlDirNames = map[string]struct{}{
	".git":    {},
	".hg":     {},
	".svn":    {},
	".jj":     {},
	".codex":  {},
	".agents": {},
}

func permissionRequestWritePaths(input policy.ToolContext) ([]string, error) {
	args, err := policy.CallArgs(input.Call)
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, nil
	}
	return resolvePathsAgainstWorkspace(stringValues(args["write"]), input.Options.WorkspaceRoot), nil
}

func protectedControlDirInPath(path string) (string, bool) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return "", false
	}
	for _, part := range splitPathParts(path) {
		if protectedControlDirName(part) {
			return part, true
		}
	}
	return "", false
}

func protectedControlDirName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for protected := range protectedControlDirNames {
		if strings.EqualFold(name, protected) {
			return true
		}
	}
	return false
}

func splitPathParts(path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	volume := filepath.VolumeName(path)
	path = strings.TrimPrefix(path, volume)
	path = strings.Trim(path, `/\`)
	if path == "" {
		return nil
	}
	return strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
}
