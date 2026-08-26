package codex

import (
	"fmt"
	"path/filepath"
	"strings"
)

// WorkspacePolicy is the complete path authority granted to one ACP channel.
// It is re-evaluated for every Session lifecycle request.
type WorkspacePolicy struct {
	AllowedRoots  []string
	WritableRoots []string
}

// ConnectionOptions configures one ACP client connection over a shared Backend.
type ConnectionOptions struct {
	ConnectionID string
	Workspace    WorkspacePolicy
}

func (p WorkspacePolicy) validate(cwd string, additional []string) ([]string, error) {
	cwd, err := cleanAbsolute(cwd)
	if err != nil {
		return nil, fmt.Errorf("codex adapter: invalid cwd: %w", err)
	}
	roots := make([]string, 0, len(p.AllowedRoots))
	for _, root := range p.AllowedRoots {
		clean, cleanErr := cleanAbsolute(root)
		if cleanErr != nil {
			return nil, fmt.Errorf("codex adapter: invalid allowed root: %w", cleanErr)
		}
		roots = append(roots, clean)
	}
	if len(roots) == 0 || !withinAny(cwd, roots) {
		return nil, fmt.Errorf("codex adapter: cwd %q is outside the authorized workspace", cwd)
	}
	writableRoots := make([]string, 0, len(p.WritableRoots))
	for _, root := range p.WritableRoots {
		clean, cleanErr := cleanAbsolute(root)
		if cleanErr != nil {
			return nil, fmt.Errorf("codex adapter: invalid writable root: %w", cleanErr)
		}
		writableRoots = append(writableRoots, clean)
	}
	if len(writableRoots) == 0 || !withinAny(cwd, writableRoots) {
		return nil, fmt.Errorf("codex adapter: cwd %q is outside the authorized writable workspace", cwd)
	}
	result := make([]string, 0, len(additional)+1)
	result = append(result, cwd)
	for _, raw := range additional {
		path, pathErr := cleanAbsolute(raw)
		if pathErr != nil {
			return nil, fmt.Errorf("codex adapter: invalid additional directory: %w", pathErr)
		}
		if !withinAny(path, roots) {
			return nil, fmt.Errorf("codex adapter: additional directory %q is outside the authorized workspace", path)
		}
		if !withinAny(path, writableRoots) {
			return nil, fmt.Errorf("codex adapter: additional directory %q is outside the authorized writable workspace", path)
		}
		result = append(result, path)
	}
	return deduplicate(result), nil
}

func cleanAbsolute(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q must be absolute", path)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("resolved path %q must be absolute", resolved)
	}
	return filepath.Clean(resolved), nil
}

func withinAny(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func deduplicate(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
