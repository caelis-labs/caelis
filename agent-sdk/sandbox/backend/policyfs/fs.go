package policyfs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
// Writes are enforced while reads remain broad except for explicit hidden
// paths.
func New(base sandbox.FileSystem, policyFn func() policy.Policy) sandbox.FileSystem {
	if base == nil || policyFn == nil {
		return base
	}
	return &policyFileSystem{base: base, policy: policyFn}
}

func (f *policyFileSystem) Getwd() (string, error)       { return f.base.Getwd() }
func (f *policyFileSystem) UserHomeDir() (string, error) { return f.base.UserHomeDir() }
func (f *policyFileSystem) Open(path string) (*os.File, error) {
	if err := f.checkReadPath(path); err != nil {
		return nil, err
	}
	return f.base.Open(path)
}

func (f *policyFileSystem) ReadDir(path string) ([]os.DirEntry, error) {
	if err := f.checkReadPath(path); err != nil {
		return nil, err
	}
	return f.base.ReadDir(path)
}

func (f *policyFileSystem) Stat(path string) (os.FileInfo, error) {
	if err := f.checkReadPath(path); err != nil {
		return nil, err
	}
	return f.base.Stat(path)
}

func (f *policyFileSystem) ReadFile(path string) ([]byte, error) {
	if err := f.checkReadPath(path); err != nil {
		return nil, err
	}
	return f.base.ReadFile(path)
}

func (f *policyFileSystem) Glob(pattern string) ([]string, error) {
	if err := f.checkReadPath(globReadRoot(pattern, f.base)); err != nil {
		return nil, err
	}
	return f.base.Glob(pattern)
}

func (f *policyFileSystem) WalkDir(root string, fn fs.WalkDirFunc) error {
	if err := f.checkReadPath(root); err != nil {
		return err
	}
	return f.base.WalkDir(root, fn)
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
	if fsboundary.IsWithinReadOnlySubpaths(targetPath, p.ReadOnlySubpaths, f.base) {
		return permissionError("write", targetPath, "path is under read-only sandbox subpath")
	}
	if fsboundary.IsWithinRoots(targetPath, p.WritableRoots, f.base) || fsboundary.IsWithinScratchRoots(targetPath, f.base) {
		return nil
	}
	return permissionError("write", targetPath, "path is outside writable sandbox roots")
}

func (f *policyFileSystem) checkReadPath(path string) error {
	p := f.policy()
	switch p.Type {
	case policy.TypeDangerFull, policy.TypeExternal:
		return nil
	}
	targetPath := fsboundary.ResolveAbsPath(path, f.base)
	if targetPath == "" {
		return nil
	}
	if fsboundary.IsWithinRoots(targetPath, p.HiddenRoots, f.base) {
		return permissionError("read", targetPath, "path is hidden by sandbox policy")
	}
	return nil
}

func permissionError(_ string, _ string, _ string) error {
	return fmt.Errorf("%w: %s", os.ErrPermission, sandbox.SandboxPermissionDeniedMessage)
}

func globReadRoot(pattern string, fsys sandbox.FileSystem) string {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return ""
	}
	if !filepath.IsAbs(trimmed) {
		wd, err := fsys.Getwd()
		if err != nil {
			return trimmed
		}
		trimmed = filepath.Join(wd, trimmed)
	}
	trimmed = filepath.Clean(trimmed)
	if !strings.ContainsAny(filepath.ToSlash(trimmed), "*?[") {
		return trimmed
	}
	volume := filepath.VolumeName(trimmed)
	rest := strings.TrimPrefix(trimmed, volume)
	rest = strings.TrimPrefix(rest, string(filepath.Separator))
	segments := strings.FieldsFunc(rest, func(r rune) bool { return r == filepath.Separator })
	metaIndex := len(segments)
	for i, segment := range segments {
		if strings.ContainsAny(segment, "*?[") {
			metaIndex = i
			break
		}
	}
	root := volume
	if filepath.IsAbs(trimmed) {
		if root == "" {
			root = string(filepath.Separator)
		} else if !strings.HasSuffix(root, string(filepath.Separator)) {
			root += string(filepath.Separator)
		}
	}
	if metaIndex > 0 {
		parts := append([]string{root}, segments[:metaIndex]...)
		root = filepath.Join(parts...)
	}
	if root == "" {
		root = "."
	}
	return filepath.Clean(root)
}
