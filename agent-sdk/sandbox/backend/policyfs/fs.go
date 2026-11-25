package policyfs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/fsboundary"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
)

type policyFileSystem struct {
	base   sandbox.FileSystem
	policy func() policy.Policy
}

// New constrains file-tool filesystem access using the active sandbox policy.
func New(base sandbox.FileSystem, policyFn func() policy.Policy) sandbox.FileSystem {
	if base == nil || policyFn == nil {
		return base
	}
	return &policyFileSystem{base: base, policy: policyFn}
}

func (f *policyFileSystem) Getwd() (string, error)       { return f.base.Getwd() }
func (f *policyFileSystem) UserHomeDir() (string, error) { return f.base.UserHomeDir() }
func (f *policyFileSystem) Open(path string) (*os.File, error) {
	p, err := f.readPolicy()
	if err != nil {
		return nil, err
	}
	path, err = f.checkReadPath(path, p)
	if err != nil {
		return nil, err
	}
	file, err := f.base.Open(path)
	if err != nil || !p.restricted() {
		return file, err
	}
	// A raw directory handle would allow enumeration without the policy.
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: not a regular file", fs.ErrInvalid)
	}
	return file, nil
}

func (f *policyFileSystem) ReadDir(path string) ([]os.DirEntry, error) {
	p, err := f.readPolicy()
	if err != nil {
		return nil, err
	}
	return f.readDir(path, p)
}

func (f *policyFileSystem) Stat(path string) (os.FileInfo, error) {
	path, err := f.readPath(path)
	if err != nil {
		return nil, err
	}
	return f.base.Stat(path)
}

func (f *policyFileSystem) ReadFile(path string) ([]byte, error) {
	path, err := f.readPath(path)
	if err != nil {
		return nil, err
	}
	return f.base.ReadFile(path)
}

func (f *policyFileSystem) Glob(pattern string) ([]string, error) {
	p, err := f.readPolicy()
	if err != nil {
		return nil, err
	}
	if !p.restricted() {
		return f.base.Glob(pattern)
	}
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}
	matches, err := f.glob(pattern, p, 0)
	slices.Sort(matches)
	return matches, err
}

func (f *policyFileSystem) WalkDir(root string, fn fs.WalkDirFunc) error {
	p, err := f.readPolicy()
	if err != nil {
		return err
	}
	path, err := f.checkReadPath(root, p)
	if err != nil {
		return err
	}
	if !p.restricted() {
		return f.base.WalkDir(path, fn)
	}
	return f.base.WalkDir(path, func(name string, entry fs.DirEntry, walkErr error) error {
		if _, err := f.checkReadPath(name, p); err != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		return fn(name, entry, walkErr)
	})
}

func (f *policyFileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	if err := f.checkWritePath(path); err != nil {
		return err
	}
	return f.base.WriteFile(path, data, perm)
}

func (f *policyFileSystem) MkdirAll(path string, perm os.FileMode) error {
	if err := f.checkWritePath(path); err != nil {
		return err
	}
	mkdirer, ok := f.base.(interface {
		MkdirAll(string, os.FileMode) error
	})
	if !ok {
		return fmt.Errorf("sandbox filesystem does not support recursive directory creation")
	}
	return mkdirer.MkdirAll(path, perm)
}

func (f *policyFileSystem) checkWritePath(path string) error {
	p := f.policy()
	if p.ResourceLimits != nil {
		target := fsboundary.ResolveAbsPath(path, f.base)
		if target != "" && fsboundary.IsWithinRoots(target, p.ResourceLimits.WritePaths, f.base) {
			return nil
		}
		return permissionError("write", path, "outside explicit writable roots")
	}
	switch p.Type {
	case policy.TypeDangerFull, policy.TypeExternal:
		return nil
	case policy.TypeReadOnly:
		return permissionError("write", path, "read-only sandbox policy")
	}
	targetPath := fsboundary.ResolveAbsPath(path, f.base)
	if targetPath == "" {
		return nil
	}
	if fsboundary.IsWithinRoots(targetPath, p.HiddenRoots, f.base) {
		return permissionError("write", targetPath, "path is hidden by sandbox policy")
	}
	workDir, err := f.base.Getwd()
	if err != nil {
		return permissionError("write", targetPath, "sandbox working directory is unavailable")
	}
	readOnlyPaths, err := policy.ReadOnlyPaths(p, workDir)
	if err != nil {
		return permissionError("write", targetPath, "sandbox protected paths are unavailable")
	}
	if fsboundary.IsWithinReadOnlySubpaths(targetPath, readOnlyPaths, f.base) {
		return permissionError("write", targetPath, "path is under read-only sandbox subpath")
	}
	if fsboundary.IsWithinRoots(targetPath, p.WritableRoots, f.base) || fsboundary.IsWithinScratchRoots(targetPath, f.base) {
		return nil
	}
	return permissionError("write", targetPath, "path is outside writable sandbox roots")
}

type readPolicy struct {
	ceiling []policy.ReadRoot
	hidden  []string
}

func (p readPolicy) restricted() bool {
	return p.ceiling != nil || len(p.hidden) > 0
}

func (f *policyFileSystem) readPolicy() (readPolicy, error) {
	p := f.policy()
	roots, err := p.ReadCeiling()
	if err != nil {
		return readPolicy{}, permissionError("read", "", "invalid read ceiling")
	}
	result := readPolicy{ceiling: roots}
	if p.Type != policy.TypeDangerFull && p.Type != policy.TypeExternal {
		result.hidden = p.HiddenRoots
	}
	return result, nil
}

func (f *policyFileSystem) readPath(path string) (string, error) {
	p, err := f.readPolicy()
	if err != nil {
		return "", err
	}
	return f.checkReadPath(path, p)
}

func (f *policyFileSystem) checkReadPath(path string, p readPolicy) (string, error) {
	if !p.restricted() {
		return path, nil
	}
	if path == "" {
		return "", permissionError("read", path, "missing path")
	}
	if !filepath.IsAbs(path) {
		wd, err := f.base.Getwd()
		if err != nil || !filepath.IsAbs(wd) || filepath.VolumeName(path) != "" || os.IsPathSeparator(path[0]) {
			return "", permissionError("read", path, "cannot resolve path against sandbox working directory")
		}
		path = filepath.Join(wd, path)
	}
	path = filepath.Clean(path)
	if p.ceiling != nil {
		target, err := fsboundary.ResolvePath(path)
		if err != nil {
			return "", permissionError("read", path, "cannot resolve path against read ceiling")
		}
		allowed := false
		for _, root := range p.ceiling {
			if fsboundary.PathIsUnder(target, root.Physical) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", permissionError("read", path, "outside explicit read ceiling")
		}
	}
	if len(p.hidden) > 0 && fsboundary.IsWithinRoots(path, p.hidden, f.base) {
		return "", permissionError("read", path, "path is hidden by sandbox policy")
	}
	return path, nil
}

func (f *policyFileSystem) readDir(path string, p readPolicy) ([]os.DirEntry, error) {
	path, err := f.checkReadPath(path, p)
	if err != nil {
		return nil, err
	}
	entries, err := f.base.ReadDir(path)
	if len(entries) == 0 || !p.restricted() {
		return entries, err
	}
	visible := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if _, err := f.checkReadPath(filepath.Join(path, entry.Name()), p); err == nil {
			visible = append(visible, entry)
		}
	}
	return visible, err
}

func permissionError(_ string, _ string, _ string) error {
	return fmt.Errorf("%w: %s", os.ErrPermission, sandbox.SandboxPermissionDeniedMessage)
}

// glob expands each wildcard component through the same policy as ReadDir so
// a disallowed directory cannot be enumerated by a later component.
func (f *policyFileSystem) glob(pattern string, p readPolicy, depth int) ([]string, error) {
	// Match filepath.Glob's recursion limit for untrusted patterns.
	if depth >= 10000 {
		return nil, filepath.ErrBadPattern
	}
	if !globHasMeta(pattern) {
		path, err := f.checkReadPath(pattern, p)
		if err != nil {
			return nil, err
		}
		// As in filepath.Glob, an inaccessible entry is omitted.
		if _, err := f.base.Stat(path); err == nil {
			return []string{pattern}, nil
		}
		return nil, nil
	}
	dir, file := filepath.Split(pattern)
	if dir == "" {
		dir = "."
	}
	dirs := []string{dir}
	if globHasMeta(dir) {
		var err error
		parent := strings.TrimRightFunc(dir, func(r rune) bool {
			return r <= 127 && os.IsPathSeparator(byte(r))
		})
		dirs, err = f.glob(parent, p, depth+1)
		if err != nil {
			return nil, err
		}
	} else if _, err := f.checkReadPath(dir, p); err != nil {
		return nil, err
	}
	var matches []string
	for _, dir := range dirs {
		entries, _ := f.readDir(dir, p)
		for _, entry := range entries {
			if matched, _ := filepath.Match(file, entry.Name()); matched {
				matches = append(matches, filepath.Join(dir, entry.Name()))
			}
		}
	}
	return matches, nil
}

func globHasMeta(path string) bool {
	magic := "*?["
	if filepath.Separator != '\\' {
		magic += `\`
	}
	return strings.ContainsAny(path[len(filepath.VolumeName(path)):], magic)
}
