package policyfs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/fsboundary"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/policy"
)

type policyFileSystem struct {
	base   sdksandbox.FileSystem
	policy func() policy.Policy
}

// newPolicyFileSystem constrains file-tool filesystem access using the active
// sandbox policy. Writes are always enforced. Reads remain broad unless the
// policy declares ReadableRoots, which lets applications opt into a narrower
// capability model without forcing that behavioral change globally.
func New(base sdksandbox.FileSystem, policyFn func() policy.Policy) sdksandbox.FileSystem {
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
	if len(p.ReadableRoots) == 0 {
		return nil
	}
	if fsboundary.IsWithinRoots(targetPath, p.ReadableRoots, f.base) ||
		fsboundary.IsWithinRoots(targetPath, p.WritableRoots, f.base) ||
		fsboundary.IsWithinScratchRoots(targetPath, f.base) {
		return nil
	}
	return permissionError("read", targetPath, "path is outside readable sandbox roots")
}

func permissionError(_ string, _ string, _ string) error {
	return fmt.Errorf("%w: %s", os.ErrPermission, sdksandbox.SandboxPermissionDeniedMessage)
}

func globReadRoot(pattern string, fsys sdksandbox.FileSystem) string {
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
