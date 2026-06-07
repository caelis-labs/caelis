package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

func TestConstrainedFS_AllowedPath(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "workspace")
	os.MkdirAll(allowed, 0o755)
	os.WriteFile(filepath.Join(allowed, "file.txt"), []byte("hello"), 0o644)

	b := New()
	fs, err := b.FileSystem(context.Background(), sandbox.Constraints{
		Paths: []sandbox.PathRule{
			{Path: allowed, Access: "write"},
		},
	})
	if err != nil {
		t.Fatalf("FileSystem: %v", err)
	}

	// Read allowed path.
	data, err := fs.Read(filepath.Join(allowed, "file.txt"))
	if err != nil {
		t.Fatalf("Read allowed: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("got %q, want %q", string(data), "hello")
	}

	// Write allowed path.
	err = fs.Write(filepath.Join(allowed, "new.txt"), []byte("world"))
	if err != nil {
		t.Fatalf("Write allowed: %v", err)
	}
}

func TestConstrainedFS_DeniedPath(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "workspace")
	denied := filepath.Join(dir, "secrets")
	os.MkdirAll(allowed, 0o755)
	os.MkdirAll(denied, 0o755)
	os.WriteFile(filepath.Join(denied, "key.pem"), []byte("secret"), 0o644)

	b := New()
	fs, err := b.FileSystem(context.Background(), sandbox.Constraints{
		Paths: []sandbox.PathRule{
			{Path: allowed, Access: "write"},
		},
	})
	if err != nil {
		t.Fatalf("FileSystem: %v", err)
	}

	// Read denied path.
	_, err = fs.Read(filepath.Join(denied, "key.pem"))
	if err == nil {
		t.Error("expected error reading denied path")
	}

	// Write denied path.
	err = fs.Write(filepath.Join(denied, "new.txt"), []byte("hack"))
	if err == nil {
		t.Error("expected error writing denied path")
	}

	// List denied path.
	_, err = fs.List(denied)
	if err == nil {
		t.Error("expected error listing denied path")
	}

	// Stat denied path.
	_, err = fs.Stat(filepath.Join(denied, "key.pem"))
	if err == nil {
		t.Error("expected error stating denied path")
	}

	// Delete denied path.
	err = fs.Delete(filepath.Join(denied, "key.pem"))
	if err == nil {
		t.Error("expected error deleting denied path")
	}
}

func TestConstrainedFS_ReadOnlyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "readonly")
	os.MkdirAll(path, 0o755)
	os.WriteFile(filepath.Join(path, "data.txt"), []byte("read me"), 0o644)

	b := New()
	fs, err := b.FileSystem(context.Background(), sandbox.Constraints{
		Paths: []sandbox.PathRule{
			{Path: path, Access: "read"},
		},
	})
	if err != nil {
		t.Fatalf("FileSystem: %v", err)
	}

	// Read allowed.
	data, err := fs.Read(filepath.Join(path, "data.txt"))
	if err != nil {
		t.Fatalf("Read allowed: %v", err)
	}
	if string(data) != "read me" {
		t.Errorf("got %q", string(data))
	}

	// Write denied (read-only rule).
	err = fs.Write(filepath.Join(path, "new.txt"), []byte("nope"))
	if err == nil {
		t.Error("expected error writing to read-only path")
	}
}

func TestConstrainedFS_NoConstraints(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("free"), 0o644)

	b := New()
	fs, err := b.FileSystem(context.Background(), sandbox.Constraints{})
	if err != nil {
		t.Fatalf("FileSystem: %v", err)
	}

	data, err := fs.Read(filepath.Join(dir, "file.txt"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(data) != "free" {
		t.Errorf("got %q", string(data))
	}
}

func TestConstrainedFS_SymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "workspace")
	denied := filepath.Join(dir, "secrets")
	os.MkdirAll(allowed, 0o755)
	os.MkdirAll(denied, 0o755)
	os.WriteFile(filepath.Join(denied, "key.pem"), []byte("secret"), 0o644)

	// Symlink inside allowed pointing to denied.
	os.Symlink(denied, filepath.Join(allowed, "escape"))

	b := New()
	fs, err := b.FileSystem(context.Background(), sandbox.Constraints{
		Paths: []sandbox.PathRule{{Path: allowed, Access: sandbox.PathAccessWrite}},
	})
	if err != nil {
		t.Fatalf("FileSystem: %v", err)
	}

	// Reading through symlink should be denied.
	_, err = fs.Read(filepath.Join(allowed, "escape", "key.pem"))
	if err == nil {
		t.Error("expected symlink escape to be denied")
	}
}
