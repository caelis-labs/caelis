//go:build windows

package windows

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/acl"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/win32"
	"golang.org/x/sys/windows"
)

func TestProtectHostAuthorityAlreadyOwnedWithModifyAccess(t *testing.T) {
	root := t.TempDir()
	sid, err := win32.CurrentProcessUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := acl.ReplaceFileOwnerAndDACL(root, sid, true, acl.Entry{Principal: sid, Rights: acl.FullControl, Mode: acl.Set}); err != nil {
		// A newly created directory already has the non-elevated user's owner.
		info, inspectErr := acl.InspectFileDACL(root)
		if inspectErr != nil || info.OwnerSID != sid {
			t.Fatalf("prepare Host-owned directory: %v (%v)", err, inspectErr)
		}
	}
	if err := acl.ReplaceFileDACL(root, true, acl.Entry{Principal: sid, Rights: acl.Modify, Mode: acl.Set}); err != nil {
		t.Fatal(err)
	}
	if err := protectHostAuthorityObject(root, sid, true); err != nil {
		t.Fatalf("protect already-owned authority without WRITE_OWNER: %v", err)
	}
}

func TestReplaceHostReceiptFileRetriesTransientErrorsWithSourceIntact(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "ledger.tmp")
	destination := filepath.Join(dir, "ledger.json")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	transientErrors := []error{
		windows.ERROR_ACCESS_DENIED,
		windows.ERROR_SHARING_VIOLATION,
		windows.ERROR_LOCK_VIOLATION,
	}
	attempts, sleeps := 0, 0
	err := replaceHostReceiptFileWith(source, destination, func(from, to string) error {
		attempts++
		if data, err := os.ReadFile(from); err != nil || string(data) != "new" {
			t.Fatalf("source on attempt %d = %q, %v; want intact contents", attempts, data, err)
		}
		if attempts <= len(transientErrors) {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: transientErrors[attempts-1]}
		}
		return os.Rename(from, to)
	}, func(delay time.Duration) {
		if delay != 10*time.Millisecond {
			t.Fatalf("retry delay = %v, want 10ms", delay)
		}
		sleeps++
	})
	if err != nil {
		t.Fatalf("replaceHostReceiptFileWith() error = %v", err)
	}
	if attempts != 4 || sleeps != 3 {
		t.Fatalf("replacement took %d attempts and %d sleeps, want 4 attempts and 3 sleeps", attempts, sleeps)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "new" {
		t.Fatalf("destination after replacement = %q, %v; want new contents", data, err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source after replacement error = %v, want removed by rename", err)
	}
}

func TestReplaceHostReceiptFileRetryExhaustionLeavesSourceForCaller(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "ledger.tmp")
	destination := filepath.Join(dir, "ledger.json")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	attempts, sleeps := 0, 0
	err := replaceHostReceiptFileWith(source, destination, func(from, to string) error {
		attempts++
		if data, err := os.ReadFile(from); err != nil || string(data) != "new" {
			t.Fatalf("source on attempt %d = %q, %v; want intact contents", attempts, data, err)
		}
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: windows.ERROR_SHARING_VIOLATION}
	}, func(time.Duration) {
		sleeps++
	})
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("replaceHostReceiptFileWith() error = %v, want sharing violation", err)
	}
	if attempts != 20 || sleeps != 19 {
		t.Fatalf("replacement took %d attempts and %d sleeps, want 20 attempts and 19 sleeps", attempts, sleeps)
	}
	if data, err := os.ReadFile(source); err != nil || string(data) != "new" {
		t.Fatalf("source after retry exhaustion = %q, %v; want intact contents for caller cleanup", data, err)
	}
}

func TestPersistHostReceiptLedgerCleansTemporaryFileAfterReplaceFailure(t *testing.T) {
	root := t.TempDir()
	rt := &runtime{hostReceiptAuthorityRoot: root}
	path := rt.hostReceiptLedgerPath()
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(existing ledger) error = %v", err)
	}
	replaceErr := errors.New("replace failed")
	var temporary string
	err := rt.persistHostReceiptLedgerWithReplace(hostReceiptLedger{}, func(source, destination string) error {
		temporary = source
		if destination != path {
			t.Fatalf("replace destination = %q, want %q", destination, path)
		}
		if _, statErr := os.Stat(source); statErr != nil {
			t.Fatalf("temporary source before replace error = %v", statErr)
		}
		return replaceErr
	})
	if !errors.Is(err, replaceErr) {
		t.Fatalf("persistHostReceiptLedgerWithReplace() error = %v, want replace failure", err)
	}
	if temporary == "" {
		t.Fatal("replace seam did not observe temporary source")
	}
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary source after terminal failure error = %v, want removed", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old" {
		t.Fatalf("ledger after failed commit = %q/%v, want old contents", data, err)
	}
}
