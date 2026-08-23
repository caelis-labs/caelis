package configstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestReplaceFileAtomicWindowsRetriesTransientSharingViolation(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "config.json")
	source := filepath.Join(dir, "config.json.tmp")
	if err := os.WriteFile(destination, []byte("old-canonical-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new-canonical-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	destinationPtr, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		destinationPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		time.Sleep(25 * time.Millisecond)
		closed <- windows.CloseHandle(handle)
	}()

	replaceErr := replaceFileAtomic(source, destination)
	closeErr := <-closed
	if closeErr != nil {
		t.Fatalf("CloseHandle() error = %v", closeErr)
	}
	if replaceErr != nil {
		t.Fatalf("replaceFileAtomic() error = %v", replaceErr)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-canonical-config" {
		t.Fatalf("destination after replacement = %q", got)
	}
}

func TestAtomicWriteFileWindowsSharingViolationPreservesDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("old-canonical-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Errorf("CloseHandle() error = %v", err)
		}
	})

	writeErr := AtomicWriteFile(path, []byte("new-canonical-config"), 0o600, AtomicWriteOps{})
	if writeErr == nil || WriteCommitted(writeErr) {
		t.Fatalf("AtomicWriteFile() error = %v, want uncommitted sharing violation", writeErr)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old-canonical-config" {
		t.Fatalf("destination after failed atomic replacement = %q", got)
	}
}
