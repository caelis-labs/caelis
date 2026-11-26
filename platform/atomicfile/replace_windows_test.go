//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestReplaceRetriesTransientErrorsWithSourceIntact(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "ledger.tmp")
	destination := filepath.Join(dir, "ledger.json")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(destination) error = %v", err)
	}

	attempts := 0
	err := replaceWith(source, destination, func(from, to string) error {
		attempts++
		if data, readErr := os.ReadFile(from); readErr != nil || string(data) != "new" {
			t.Fatalf("source on attempt %d = %q/%v, want intact", attempts, data, readErr)
		}
		switch attempts {
		case 1:
			return windows.ERROR_ACCESS_DENIED
		case 2:
			return windows.ERROR_SHARING_VIOLATION
		case 3:
			return windows.ERROR_LOCK_VIOLATION
		default:
			return os.Rename(from, to)
		}
	}, func(time.Duration) {})
	if err != nil {
		t.Fatalf("replaceWith() error = %v", err)
	}
	if attempts != 4 {
		t.Fatalf("replace attempts = %d, want 4", attempts)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "new" {
		t.Fatalf("destination after replace = %q/%v, want new", data, err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source after commit error = %v, want not exist", err)
	}
}

func TestReplaceTerminalFailureLeavesSourceForCallerCleanup(t *testing.T) {
	source := filepath.Join(t.TempDir(), "ledger.tmp")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	attempts := 0
	err := replaceWith(source, filepath.Join(t.TempDir(), "ledger.json"), func(from, _ string) error {
		attempts++
		if _, statErr := os.Stat(from); statErr != nil {
			t.Fatalf("source on attempt %d error = %v, want intact", attempts, statErr)
		}
		return windows.ERROR_SHARING_VIOLATION
	}, func(time.Duration) {})
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("replaceWith() error = %v, want sharing violation", err)
	}
	if attempts != windowsReplaceRetryLimit {
		t.Fatalf("replace attempts = %d, want %d", attempts, windowsReplaceRetryLimit)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source after terminal failure error = %v, want caller-owned temporary file", err)
	}
}
