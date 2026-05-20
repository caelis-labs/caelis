package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Normalize returns one stable Windows-oriented path spelling for policy use.
// Existing paths are resolved through EvalSymlinks so short names and junctions
// collapse where the OS can resolve them.
func Normalize(path string) string {
	normalized, _ := NormalizeWithBase("", path)
	return normalized
}

// NormalizeWithBase resolves relative paths against base before cleaning.
func NormalizeWithBase(base string, path string) (string, error) {
	value := strings.TrimSpace(path)
	if value == "" {
		return "", nil
	}
	value = stripWindowsExtendedPrefix(value)
	if isWindowsUNCPath(value) {
		return filepath.Clean(value), nil
	}
	if !filepath.IsAbs(value) {
		base = strings.TrimSpace(base)
		if base != "" {
			value = filepath.Join(base, value)
		}
	}
	if abs, err := filepath.Abs(value); err == nil {
		value = abs
	}
	value = filepath.Clean(value)
	if resolved, err := filepath.EvalSymlinks(value); err == nil && strings.TrimSpace(resolved) != "" {
		value = filepath.Clean(resolved)
	}
	return value, nil
}

// Key returns the comparison key used by Windows path policy maps.
func Key(path string) string {
	value := Normalize(path)
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}

// Dedupe normalizes and removes duplicate paths. Windows keys are
// case-insensitive.
func Dedupe(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, raw := range paths {
		normalized := Normalize(raw)
		if normalized == "" {
			continue
		}
		key := normalized
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CompactCovered normalizes paths and removes roots already covered by an
// earlier or later ancestor root.
func CompactCovered(paths []string) []string {
	paths = Dedupe(paths)
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		covered := false
		for _, existing := range out {
			if IsUnder(path, existing) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		kept := out[:0]
		for _, existing := range out {
			if !IsUnder(existing, path) {
				kept = append(kept, existing)
			}
		}
		out = append(kept, path)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// IsUnder reports whether target is equal to or below root.
func IsUnder(target string, root string) bool {
	targetKey := Key(target)
	rootKey := Key(root)
	if targetKey == "" || rootKey == "" {
		return false
	}
	if targetKey == rootKey {
		return true
	}
	sep := string(os.PathSeparator)
	if !strings.HasSuffix(rootKey, sep) {
		rootKey += sep
	}
	return strings.HasPrefix(targetKey, rootKey)
}

func stripWindowsExtendedPrefix(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	switch {
	case strings.HasPrefix(path, `\\?\UNC\`):
		return `\\` + strings.TrimPrefix(path, `\\?\UNC\`)
	case strings.HasPrefix(path, `\\?\`):
		return strings.TrimPrefix(path, `\\?\`)
	default:
		return path
	}
}

func isWindowsUNCPath(path string) bool {
	return runtime.GOOS == "windows" && strings.HasPrefix(path, `\\`) && !strings.HasPrefix(path, `\\?\`)
}
