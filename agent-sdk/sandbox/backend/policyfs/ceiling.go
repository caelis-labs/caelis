package policyfs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// openPhysicalParent opens each already resolved component relative to the
// preceding directory handle. Check the identity before accepting each handle
// so a replaced component cannot redirect an approved read.
func openPhysicalParent(path string) (*os.Root, string, error) {
	volumeRoot := filepath.VolumeName(path) + string(filepath.Separator)
	parent, err := os.OpenRoot(volumeRoot)
	if err != nil {
		return nil, "", err
	}
	relative := strings.TrimPrefix(filepath.Dir(path), volumeRoot)
	if relative != "" {
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			next, err := openPhysicalDirectory(parent, component)
			_ = parent.Close()
			if err != nil {
				return nil, "", err
			}
			parent = next
		}
	}
	return parent, filepath.Base(path), nil
}

func physicalInfo(parent *os.Root, name string) (os.FileInfo, error) {
	info, err := parent.Lstat(name)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, permissionError("read", name, "resolved path changed")
	}
	return info, err
}

func openPhysicalDirectory(parent *os.Root, name string) (*os.Root, error) {
	before, err := physicalInfo(parent, name)
	if err != nil {
		return nil, err
	}
	next, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := next.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = next.Close()
		return nil, permissionError("read", name, "resolved directory changed")
	}
	return next, nil
}

func openPhysicalFile(path string) (*os.File, error) {
	parent, name, err := openPhysicalParent(path)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	before, err := physicalInfo(parent, name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: not a regular file", fs.ErrInvalid)
	}
	file, err := parent.OpenFile(name, os.O_RDONLY|ceilingOpenFlags, 0)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, permissionError("read", name, "resolved file changed")
	}
	return file, nil
}

func (f *policyFileSystem) readPhysicalDir(path, target string, policy readPolicy) ([]os.DirEntry, error) {
	parent, name, err := openPhysicalParent(target)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	dir, err := openPhysicalDirectory(parent, name)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	file, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	names, err := file.Readdirnames(-1)
	slices.Sort(names)
	entries := make([]os.DirEntry, 0, len(names))
	for _, name := range names {
		if _, err := f.checkReadPath(filepath.Join(path, name), policy); err != nil {
			continue
		}
		info, infoErr := dir.Lstat(name)
		if infoErr == nil {
			// Keep Info bound to the anchored read even after the directory closes.
			entries = append(entries, fs.FileInfoToDirEntry(info))
		}
	}
	return entries, err
}

type ceilingWalk struct {
	fsys   *policyFileSystem
	policy readPolicy
	dir    string
}

func (w ceilingWalk) path(name string) string {
	return filepath.Join(w.dir, filepath.FromSlash(name))
}

func (w ceilingWalk) Open(name string) (fs.File, error) {
	_, target, err := w.fsys.resolveReadPath(w.path(name), w.policy)
	if err != nil {
		return nil, err
	}
	return openPhysicalFile(target)
}

func (w ceilingWalk) Stat(name string) (fs.FileInfo, error) {
	return w.fsys.stat(w.path(name), w.policy)
}

func (w ceilingWalk) ReadDir(name string) ([]fs.DirEntry, error) {
	return w.fsys.readDir(w.path(name), w.policy)
}
