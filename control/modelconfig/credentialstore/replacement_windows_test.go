//go:build windows

package credentialstore

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestReplacementRollbackRetriesTransientSharingViolation(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := BuildReference("openai", "windows-rollback-sharing")
	if err := store.Put(context.Background(), ref, "previous-secret"); err != nil {
		t.Fatal(err)
	}
	txn, err := store.BeginReplacement(context.Background(), []Replacement{{
		Ref: ref, Source: Source{APIKey: "uncommitted-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	pathPtr, err := windows.UTF16PtrFromString(store.path(ref))
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
	closed := make(chan error, 1)
	go func() {
		time.Sleep(25 * time.Millisecond)
		closed <- windows.CloseHandle(handle)
	}()

	rollbackErr := txn.Rollback()
	if closeErr := <-closed; closeErr != nil {
		t.Fatalf("CloseHandle() error = %v", closeErr)
	}
	if rollbackErr != nil {
		t.Fatalf("Rollback() error = %v, want transient sharing conflict recovered", rollbackErr)
	}
	got, err := store.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got != "previous-secret" {
		t.Fatalf("credential after rollback = %q, want previous source", got)
	}
	if _, err := os.Stat(store.path(ref)); err != nil {
		t.Fatalf("restored credential file error = %v", err)
	}
}
