//go:build windows

package windows

import (
	"errors"
	"os"
	"testing"
)

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
