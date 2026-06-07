package sandbox_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/stretchr/testify/require"
)

func TestCheckPathAccess_AllowedPath(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "workspace")
	require.NoError(t, os.MkdirAll(allowed, 0o755))

	err := sandbox.CheckPathAccess(filepath.Join(allowed, "file.txt"), sandbox.PathAccessRead, sandbox.Constraints{
		Paths: []sandbox.PathRule{{Path: allowed, Access: sandbox.PathAccessWrite}},
	})
	if err != nil {
		t.Errorf("expected allowed, got %v", err)
	}
}

func TestCheckPathAccess_DeniedPath(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "workspace")
	denied := filepath.Join(dir, "secrets")
	require.NoError(t, os.MkdirAll(allowed, 0o755))
	require.NoError(t, os.MkdirAll(denied, 0o755))

	err := sandbox.CheckPathAccess(filepath.Join(denied, "key.pem"), sandbox.PathAccessRead, sandbox.Constraints{
		Paths: []sandbox.PathRule{{Path: allowed, Access: sandbox.PathAccessWrite}},
	})
	if err == nil {
		t.Error("expected denied for path outside allowed root")
	}
}

func TestCheckPathAccess_ReadOnlyDeniesWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	require.NoError(t, os.MkdirAll(path, 0o755))

	err := sandbox.CheckPathAccess(filepath.Join(path, "file"), sandbox.PathAccessWrite, sandbox.Constraints{
		Paths: []sandbox.PathRule{{Path: path, Access: sandbox.PathAccessRead}},
	})
	if err == nil {
		t.Error("expected write denied on read-only path")
	}
}

func TestCheckPathAccess_SymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "workspace")
	denied := filepath.Join(dir, "secrets")
	require.NoError(t, os.MkdirAll(allowed, 0o755))
	require.NoError(t, os.MkdirAll(denied, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(denied, "key.pem"), []byte("secret"), 0o644))

	// Create symlink inside allowed that points to denied.
	symlink := filepath.Join(allowed, "escape")
	require.NoError(t, os.Symlink(denied, symlink))

	// Reading through the symlink should be denied because the resolved
	// path is outside the allowed root.
	err := sandbox.CheckPathAccess(filepath.Join(symlink, "key.pem"), sandbox.PathAccessRead, sandbox.Constraints{
		Paths: []sandbox.PathRule{{Path: allowed, Access: sandbox.PathAccessWrite}},
	})
	if err == nil {
		t.Error("expected symlink escape to be denied")
	}
}

func TestCheckPathAccess_NoConstraints(t *testing.T) {
	err := sandbox.CheckPathAccess("/any/path", sandbox.PathAccessRead, sandbox.Constraints{})
	if err != nil {
		t.Errorf("expected no constraints = allowed, got %v", err)
	}
}

func TestCheckPathAccess_NestedPaths(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "project")
	nested := filepath.Join(root, "src", "pkg")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	err := sandbox.CheckPathAccess(filepath.Join(nested, "file.go"), sandbox.PathAccessRead, sandbox.Constraints{
		Paths: []sandbox.PathRule{{Path: root, Access: sandbox.PathAccessWrite}},
	})
	if err != nil {
		t.Errorf("expected nested path allowed, got %v", err)
	}
}
