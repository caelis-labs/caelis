package fsboundary

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	resolved, err := ResolvePath(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return resolved
}

// ResolvePath resolves symbolic links, including those above a missing suffix.
// An existing component that cannot be resolved is an error.
func ResolvePath(path string) (string, error) {
	if path == "" {
		return "", os.ErrInvalid
	}
	cur := filepath.Clean(path)
	var suffix []string
	for {
		resolved, err := ResolveExistingPath(cur)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		// A dangling link also returns ErrNotExist from resolution. Only
		// unresolved components that are themselves absent can be appended.
		if _, statErr := os.Lstat(cur); statErr == nil {
			return "", err
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		suffix = append(suffix, filepath.Base(cur))
		cur = parent
	}
}

// PathIsUnder reports whether the target is the root or one of its descendants.
func PathIsUnder(target, root string) bool {
	if target == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if runtime.GOOS != "windows" {
		return true
	}

	// Windows Rel accepts case variants. Confirm that a spelling difference
	// actually names the same directory on filesystems with case-sensitive paths.
	ancestor := filepath.Clean(target)
	for ; rel != "."; rel = filepath.Dir(rel) {
		ancestor = filepath.Dir(ancestor)
	}
	root = filepath.Clean(root)
	if ancestor == root {
		return true
	}
	ancestorInfo, ancestorErr := os.Stat(ancestor)
	rootInfo, rootErr := os.Stat(root)
	return ancestorErr == nil && rootErr == nil && os.SameFile(ancestorInfo, rootInfo)
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
