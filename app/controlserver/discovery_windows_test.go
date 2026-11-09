//go:build windows

package controlserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A separate process tests the exact byte lock used by the production owner;
// no scheduling sleeps, filesystem retries or swallowed sharing errors.
func TestWindowsDiscoveryLockProbe(t *testing.T) {
	path := os.Getenv("CAELIS_DISCOVERY_LOCK_PROBE")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path+".lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_FAIL_IMMEDIATELY|windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped)
	blocked := errors.Is(err, windows.ERROR_LOCK_VIOLATION)
	if os.Getenv("CAELIS_DISCOVERY_EXPECT_LOCKED") == "1" {
		if !blocked {
			t.Fatalf("reader did not exclude cross-process mutation: %v", err)
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWindowsDiscoveryReaderCloseBeforeMutation(t *testing.T) {
	path := DefaultDiscoveryFile(t.TempDir())
	record := testDiscoveryRecord()
	if err := PublishDiscoveryRecord(path, record); err != nil {
		t.Fatal(err)
	}
	t.Run("legacy-held-reader-red-control", func(t *testing.T) {
		// This is LoadDiscoveryRecord's original os.Open sharing mode, pinned
		// through the mutation to deterministically reproduce the original error.
		reader, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		err = RemoveDiscoveryRecord(path, record.InstanceID)
		closeErr := reader.Close()
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatalf("legacy control did not reproduce sharing violation: %v", err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("failed removal lost the original record")
		}
		t.Logf("original held os.Open handle -> %v", err)
	})
	probe := func(locked bool) error {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsDiscoveryLockProbe$")
		flag := "0"
		if locked {
			flag = "1"
		}
		command.Env = append(os.Environ(), "CAELIS_DISCOVERY_LOCK_PROBE="+path, "CAELIS_DISCOVERY_EXPECT_LOCKED="+flag)
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("lock probe: %w: %s", err, output)
		}
		return nil
	}
	t.Run("managed-snapshot-green", func(t *testing.T) {
		err := withDiscoveryRecord(path, func(snapshot DiscoveryRecord) error {
			if snapshot.InstanceID != record.InstanceID {
				return errors.New("wrong snapshot")
			}
			if err := probe(true); err != nil {
				return err
			}
			// An exclusive open can succeed only after the managed read file was
			// closed; the cross-process mutation lock above is still held.
			wide, err := windows.UTF16PtrFromString(path)
			if err != nil {
				return err
			}
			handle, err := windows.CreateFile(wide, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				return fmt.Errorf("managed reader was not closed before mutation: %w", err)
			}
			return windows.CloseHandle(handle)
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := probe(false); err != nil {
			t.Fatal(err)
		}
		if err := RemoveDiscoveryRecord(path, record.InstanceID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove=%v", err)
		}
		next := testDiscoveryRecord()
		if err := PublishDiscoveryRecord(path, next); err != nil {
			t.Fatal(err)
		}
		if err := RemoveDiscoveryRecord(path, record.InstanceID); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadDiscoveryRecord(path)
		if err != nil || loaded.InstanceID != next.InstanceID {
			t.Fatal("old owner removed replacement")
		}
		t.Logf("reader close -> unlock -> remove -> publish replacement succeeded (%s)", filepath.Base(path))
	})
}
