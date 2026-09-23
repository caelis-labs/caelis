package bot

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

const (
	filesDirMode  os.FileMode = 0o700
	filesFileMode os.FileMode = 0o600
)

var (
	errFilesEscape     = fmt.Errorf("bot files: path is outside the files: %w", os.ErrPermission)
	errFilesSymlink    = fmt.Errorf("bot files: symbolic links are not allowed in the files: %w", os.ErrPermission)
	errFilesNotRegular = fmt.Errorf("%w: not a regular file", fs.ErrInvalid)
)

// privateFS is the root-confined sandbox.FileSystem handed to the builtin
// file tools. Every operation resolves through the files's os.Root, so a
// path can never name a location outside the files even if a component is
// swapped for a symlink.
type privateFS struct {
	owner *Files
}

var _ sandbox.FileSystem = (*privateFS)(nil)

// Getwd reports the resolved files root so relative paths resolve inside it.
func (f *privateFS) Getwd() (string, error) {
	_, base, _, err := f.resolve(".")
	return base, err
}

// UserHomeDir maps "~/" to the files root so tilde paths stay confined.
func (f *privateFS) UserHomeDir() (string, error) { return f.Getwd() }

func (f *privateFS) Open(name string) (*os.File, error) {
	root, _, rel, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	return filesOpenRegular(root, rel, name)
}

func (f *privateFS) ReadDir(name string) ([]os.DirEntry, error) {
	root, _, rel, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(root.FS(), filepath.ToSlash(rel))
	if err != nil {
		return nil, err
	}
	visible := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink == 0 {
			visible = append(visible, entry)
		}
	}
	return visible, nil
}

func (f *privateFS) Stat(name string) (os.FileInfo, error) {
	root, _, rel, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	return root.Stat(rel)
}

func (f *privateFS) ReadFile(name string) ([]byte, error) {
	root, _, rel, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	file, err := filesOpenRegular(root, rel, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

// filesOpenRegular types-checks the opened handle and attaches the caller's
// path to a non-regular rejection so every caller reports the same reason.
func filesOpenRegular(root *os.Root, rel, name string) (*os.File, error) {
	file, err := openFilesRegular(root, rel)
	if err != nil {
		if errors.Is(err, errFilesNotRegular) {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
		return nil, err
	}
	return file, nil
}

// WriteFile replaces the target atomically: it stages a private temporary file
// in the target directory, writes and syncs it, then renames it into place, so
// a reader observes either the previous file or the complete new one. No
// directory sync follows the rename, so this is not a power-loss durability
// guarantee.
func (f *privateFS) WriteFile(name string, data []byte, _ os.FileMode) error {
	root, base, err := f.root()
	if err != nil {
		return err
	}
	rel, err := filesRelative(base, name)
	if err != nil {
		return err
	}
	if rel == "." {
		return fmt.Errorf("%w: %q is not a file", fs.ErrInvalid, name)
	}
	parent := filepath.Dir(rel)
	if err := ensureFilesDir(root, parent); err != nil {
		return err
	}
	if info, err := root.Lstat(rel); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errFilesSymlink
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %q is not a regular file", fs.ErrInvalid, name)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	suffix, err := filesTempSuffix()
	if err != nil {
		return err
	}
	tmpRel := filepath.Join(parent, "."+filepath.Base(rel)+".tmp-"+suffix)
	file, err := root.OpenFile(tmpRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filesFileMode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = root.Remove(tmpRel)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = root.Remove(tmpRel)
		return err
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(tmpRel)
		return err
	}
	if err := root.Rename(tmpRel, rel); err != nil {
		_ = root.Remove(tmpRel)
		return err
	}
	return nil
}

func (f *privateFS) Glob(pattern string) ([]string, error) {
	root, base, err := f.root()
	if err != nil {
		return nil, err
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, nil
	}
	rel, err := filesRelative(base, pattern)
	if err != nil {
		return nil, err
	}
	found, err := fs.Glob(root.FS(), filepath.ToSlash(rel))
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, match := range found {
		if err := rejectFilesSymlinks(root, filepath.FromSlash(match), true); err != nil {
			continue
		}
		matches = append(matches, filepath.Join(base, filepath.FromSlash(match)))
	}
	return matches, nil
}

func (f *privateFS) WalkDir(name string, fn fs.WalkDirFunc) error {
	root, base, rel, err := f.resolve(name)
	if err != nil {
		return err
	}
	return fs.WalkDir(root.FS(), filepath.ToSlash(rel), func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return fn(filepath.Join(base, filepath.FromSlash(p)), d, werr)
		}
		if d != nil && d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		return fn(filepath.Join(base, filepath.FromSlash(p)), d, nil)
	})
}

func (f *privateFS) MkdirAll(name string, _ os.FileMode) error {
	root, base, err := f.root()
	if err != nil {
		return err
	}
	rel, err := filesRelative(base, name)
	if err != nil {
		return err
	}
	return ensureFilesDir(root, rel)
}

func (f *privateFS) root() (*os.Root, string, error) {
	owner := f.owner
	owner.openMu.Lock()
	defer owner.openMu.Unlock()
	root, err := owner.prepareLocked(false)
	if err != nil {
		return nil, "", err
	}
	return root, owner.rootPath, nil
}

// resolve maps a caller path to an os.Root-relative path, rejecting symlinks
// and any path that leaves the files.
func (f *privateFS) resolve(name string) (*os.Root, string, string, error) {
	root, base, err := f.root()
	if err != nil {
		return nil, "", "", err
	}
	rel, err := filesRelative(base, name)
	if err != nil {
		return nil, "", "", err
	}
	if err := rejectFilesSymlinks(root, rel, true); err != nil {
		return nil, "", "", err
	}
	return root, base, rel, nil
}

// filesRelative maps a caller path to an os.Root-relative path. It accepts
// paths relative to the files and absolute paths inside it, and rejects
// every path that resolves outside the files.
func filesRelative(base, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: path is required", fs.ErrInvalid)
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	rel, err := filepath.Rel(base, filepath.Clean(value))
	if err != nil {
		return "", errFilesEscape
	}
	rel = filepath.Clean(rel)
	switch {
	case rel == ".":
		return ".", nil
	case rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(rel) || !filepath.IsLocal(rel):
		return "", errFilesEscape
	default:
		return rel, nil
	}
}

// rejectFilesSymlinks refuses any component of rel that is a symlink. When
// includeFinal is false the last component may be absent (a new file).
func rejectFilesSymlinks(root *os.Root, rel string, includeFinal bool) error {
	if rel == "." {
		return nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if !includeFinal {
		parts = parts[:len(parts)-1]
	}
	current := ""
	for _, name := range parts {
		if name == "" || name == "." || name == ".." {
			return errFilesEscape
		}
		current = filepath.Join(current, name)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errFilesSymlink
		}
	}
	return nil
}

// ensureFilesDir creates any missing directory components of rel, rejecting
// symlink and non-directory components along the way.
func ensureFilesDir(root *os.Root, rel string) error {
	if rel == "." {
		return nil
	}
	current := ""
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		if name == "" || name == "." || name == ".." {
			return errFilesEscape
		}
		current = filepath.Join(current, name)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(current, filesDirMode); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errFilesSymlink
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: %q is not a directory", fs.ErrInvalid, current)
		}
	}
	return nil
}

func filesTempSuffix() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
