package host

import (
	"github.com/OnslaughtSnail/caelis/sandbox"
)

// constrainedFS wraps a hostFS with path-based access control from
// sandbox.Constraints. It uses sandbox.CheckPathAccess for symlink-safe
// path enforcement.
type constrainedFS struct {
	inner       *hostFS
	constraints sandbox.Constraints
}

// newConstrainedFS creates a filesystem that enforces path rules.
func newConstrainedFS(inner *hostFS, c sandbox.Constraints) *constrainedFS {
	return &constrainedFS{inner: inner, constraints: c}
}

func (fs *constrainedFS) Read(path string) ([]byte, error) {
	return sandbox.ConstrainedRead(fs.inner, path, fs.constraints)
}

func (fs *constrainedFS) Write(path string, data []byte) error {
	return sandbox.ConstrainedWrite(fs.inner, path, data, fs.constraints)
}

func (fs *constrainedFS) List(path string) ([]string, error) {
	if err := sandbox.CheckPathAccess(path, sandbox.PathAccessRead, fs.constraints); err != nil {
		return nil, err
	}
	return fs.inner.List(path)
}

func (fs *constrainedFS) Exists(path string) (bool, error) {
	if err := sandbox.CheckPathAccess(path, sandbox.PathAccessRead, fs.constraints); err != nil {
		return false, err
	}
	return fs.inner.Exists(path)
}

func (fs *constrainedFS) Delete(path string) error {
	if err := sandbox.CheckPathAccess(path, sandbox.PathAccessWrite, fs.constraints); err != nil {
		return err
	}
	return fs.inner.Delete(path)
}

func (fs *constrainedFS) Stat(path string) (sandbox.FileInfo, error) {
	if err := sandbox.CheckPathAccess(path, sandbox.PathAccessRead, fs.constraints); err != nil {
		return sandbox.FileInfo{}, err
	}
	return fs.inner.Stat(path)
}

// Compile-time interface check.
var _ sandbox.FileSystem = (*constrainedFS)(nil)
