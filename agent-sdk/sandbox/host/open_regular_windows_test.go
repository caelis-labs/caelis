//go:build windows

package host

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenRegularFileRejectsDeviceWithoutBlocking(t *testing.T) {
	t.Parallel()

	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	fsys := rt.FileSystem()

	done := make(chan error, 1)
	go func() {
		file, openErr := fsys.Open(`\\.\NUL`)
		if file != nil {
			_ = file.Close()
		}
		done <- openErr
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errNotRegularFile) {
			t.Fatalf("Open(NUL) error = %v, want %v", err, errNotRegularFile)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open(NUL) blocked")
	}
}

func TestOpenRegularFileAllowsRegularFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	rt, err := New(Config{CWD: dir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	file, err := rt.FileSystem().Open(path)
	if err != nil {
		t.Fatalf("Open(regular) error = %v", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("opened mode = %s, want regular file", info.Mode())
	}
}
