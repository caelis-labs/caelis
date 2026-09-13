//go:build windows

package windows

import (
	"errors"
	"os"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/acl"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/win32"
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
