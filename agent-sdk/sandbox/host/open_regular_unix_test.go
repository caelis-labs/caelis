//go:build unix

package host

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestOpenRegularFileRejectsFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "blocked.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("Mkfifo() error = %v", err)
	}

	fsys := mustHostFileSystem(t, dir)
	err := mustCompleteSoon(t, func() error {
		file, openErr := fsys.Open(fifoPath)
		if file != nil {
			_ = file.Close()
		}
		return openErr
	})
	if !errors.Is(err, errNotRegularFile) {
		t.Fatalf("Open(fifo) error = %v, want %v", err, errNotRegularFile)
	}
}

func TestOpenRegularFileRejectsDeviceAndReplacementRace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fsys := mustHostFileSystem(t, dir)
	path := filepath.Join(dir, "swapped.txt")
	if err := os.WriteFile(path, []byte("regular\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	file, err := fsys.Open(path)
	if err != nil {
		t.Fatalf("Open(regular) error = %v", err)
	}
	_ = file.Close()

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Mkfifo(replacement) error = %v", err)
	}
	err = mustCompleteSoon(t, func() error {
		replaced, openErr := fsys.Open(path)
		if replaced != nil {
			_ = replaced.Close()
		}
		return openErr
	})
	if !errors.Is(err, errNotRegularFile) {
		t.Fatalf("Open(replaced fifo) error = %v, want %v", err, errNotRegularFile)
	}

	dev, err := fsys.Open("/dev/null")
	if dev != nil {
		_ = dev.Close()
	}
	if !errors.Is(err, errNotRegularFile) {
		t.Fatalf("Open(/dev/null) error = %v, want %v", err, errNotRegularFile)
	}
}

func TestOpenRegularFileAllowsRegularFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	fsys := mustHostFileSystem(t, dir)
	file, err := fsys.Open(path)
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

func mustHostFileSystem(t *testing.T, cwd string) sandbox.FileSystem {
	t.Helper()
	rt, err := New(Config{CWD: cwd})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt.FileSystem()
}

func mustCompleteSoon(t *testing.T, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- fn()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("open blocked on a special file")
		return nil
	}
}
