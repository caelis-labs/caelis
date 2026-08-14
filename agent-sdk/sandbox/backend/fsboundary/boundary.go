package fsboundary

import (
	"os"
	"path/filepath"
	"strings"
)

type PathContext interface {
	Getwd() (string, error)
	UserHomeDir() (string, error)
}

func ResolveAbsPath(path string, ctx PathContext) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		if ctx != nil {
			home, err := ctx.UserHomeDir()
			if err == nil {
				path = filepath.Join(home, path[2:])
			}
		} else {
			home, err := os.UserHomeDir()
			if err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
	}
	if !filepath.IsAbs(path) {
		var wd string
		if ctx != nil {
			wd, _ = ctx.Getwd()
		}
		if wd == "" {
			wd, _ = os.Getwd()
		}
		path = filepath.Join(wd, path)
	}
	return filepath.Clean(path)
}

func ResolveAbsRoot(root string, ctx PathContext) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	if filepath.IsAbs(root) {
		return filepath.Clean(root)
	}
	var wd string
	if ctx != nil {
		wd, _ = ctx.Getwd()
	}
	if wd == "" {
		wd, _ = os.Getwd()
	}
	if wd == "" {
		return ""
	}
	return filepath.Clean(filepath.Join(wd, root))
}

func ResolvePathWithSymlinks(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}

	cur := clean
	suffix := make([]string, 0, 4)
	for {
		if cur == "" {
			return clean
		}
		if _, err := os.Lstat(cur); err == nil {
			resolved, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return clean
			}
			resolved = filepath.Clean(resolved)
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean
		}
		suffix = append(suffix, filepath.Base(cur))
		cur = parent
	}
}

func PathIsUnder(target, root string) bool {
	if target == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(target, prefix)
}

func IsWithinRoots(target string, roots []string, ctx PathContext) bool {
	absTarget := ResolvePathWithSymlinks(ResolveAbsPath(target, ctx))
	for _, root := range roots {
		absRoot := ResolvePathWithSymlinks(ResolveAbsRoot(root, ctx))
		if absRoot == "" {
			continue
		}
		if PathIsUnder(absTarget, absRoot) {
			return true
		}
	}
	return false
}

func IsWithinScratchRoots(target string, ctx PathContext) bool {
	absTarget := ResolvePathWithSymlinks(ResolveAbsPath(target, ctx))
	for _, root := range scratchRoots(ctx) {
		if PathIsUnder(absTarget, root) {
			return true
		}
	}
	return false
}

func IsWithinReadOnlySubpaths(target string, subpaths []string, ctx PathContext) bool {
	absTarget := ResolvePathWithSymlinks(ResolveAbsPath(target, ctx))
	for _, subpath := range subpaths {
		absRoot := ResolvePathWithSymlinks(ResolveAbsRoot(subpath, ctx))
		if absRoot == "" {
			continue
		}
		if PathIsUnder(absTarget, absRoot) {
			return true
		}
	}
	return false
}

func scratchRoots(ctx PathContext) []string {
	roots := make([]string, 0, 5)
	if tmp := strings.TrimSpace(os.TempDir()); tmp != "" {
		roots = append(roots, ResolvePathWithSymlinks(filepath.Clean(tmp)))
	}
	for _, tmpRoot := range []string{"/tmp", "/var/tmp", "/private/tmp"} {
		if strings.TrimSpace(tmpRoot) != "" {
			roots = append(roots, ResolvePathWithSymlinks(filepath.Clean(tmpRoot)))
		}
	}
	var home string
	if ctx != nil {
		home, _ = ctx.UserHomeDir()
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if strings.TrimSpace(home) != "" {
		roots = append(roots, ResolvePathWithSymlinks(filepath.Join(home, ".cache")))
	}
	return normalizeStringList(roots)
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
