package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
)

// CheckPathAccess verifies that a path is allowed by the given constraints.
// It resolves symlinks before checking to prevent symlink escape.
// Returns nil if access is allowed, or an error describing the denial.
func CheckPathAccess(path string, access PathAccess, constraints Constraints) error {
	if len(constraints.Paths) == 0 {
		return nil // no constraints = no restrictions
	}

	// Resolve symlinks to prevent escape.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// If the file doesn't exist, resolve the parent directory.
		dir := filepath.Dir(path)
		resolvedDir, dirErr := filepath.EvalSymlinks(dir)
		if dirErr != nil {
			// Cannot resolve at all — fail closed.
			return fmt.Errorf("sandbox: cannot resolve path %q or its parent: %w", path, dirErr)
		}
		resolved = filepath.Join(resolvedDir, filepath.Base(path))
	}

	resolved = filepath.Clean(resolved)

	for _, rule := range constraints.Paths {
		ruleAbs, err := filepath.Abs(rule.Path)
		if err != nil {
			continue
		}
		// Resolve symlinks on the rule path too.
		ruleResolved, err := filepath.EvalSymlinks(ruleAbs)
		if err != nil {
			ruleResolved = ruleAbs
		}
		ruleResolved = filepath.Clean(ruleResolved)

		if isWithin(resolved, ruleResolved) {
			// Path matches this rule — check access level.
			if access == PathAccessWrite && rule.Access == PathAccessRead {
				return fmt.Errorf("sandbox: write denied for %q (read-only rule on %q)", path, rule.Path)
			}
			return nil // access allowed
		}
	}

	return fmt.Errorf("sandbox: %s denied for %q (no matching path rule)", access, resolved)
}

// isWithin checks if path is within or equal to root.
func isWithin(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// ConstrainedRead checks access then reads.
func ConstrainedRead(fs FileSystem, path string, constraints Constraints) ([]byte, error) {
	if err := CheckPathAccess(path, PathAccessRead, constraints); err != nil {
		return nil, err
	}
	return fs.Read(path)
}

// ConstrainedWrite checks access then writes.
func ConstrainedWrite(fs FileSystem, path string, data []byte, constraints Constraints) error {
	if err := CheckPathAccess(path, PathAccessWrite, constraints); err != nil {
		return err
	}
	return fs.Write(path, data)
}
