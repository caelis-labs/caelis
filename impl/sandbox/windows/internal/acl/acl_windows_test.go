//go:build windows

package acl

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestReadAndModifyFileDACL(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadFileDACL(dir); err != nil {
		t.Fatalf("ReadFileDACL() error = %v", err)
	}
	if err := ModifyFileDACL(dir, Entry{
		Principal: "S-1-1-0",
		Rights:    ReadExecute,
		Mode:      Grant,
		Inherit:   true,
	}); err != nil {
		t.Fatalf("ModifyFileDACL() error = %v", err)
	}
	descriptor, err := ReadFileDACL(dir)
	if err != nil {
		t.Fatalf("ReadFileDACL(after modify) error = %v", err)
	}
	if !descriptor.HasDACL() {
		t.Fatal("ReadFileDACL(after modify).HasDACL() = false, want true")
	}
}

func TestModifyFileDACLIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	entry := Entry{
		Principal: "S-1-1-0",
		Rights:    ReadExecute,
		Mode:      Grant,
		Inherit:   true,
	}
	if err := ModifyFileDACL(dir, entry); err != nil {
		t.Fatalf("ModifyFileDACL(first) error = %v", err)
	}
	first, err := ReadFileDACL(dir)
	if err != nil {
		t.Fatalf("ReadFileDACL(first) error = %v", err)
	}
	firstDACL, _, err := first.sd.DACL()
	if err != nil {
		t.Fatalf("first DACL() error = %v", err)
	}
	if err := ModifyFileDACL(dir, entry); err != nil {
		t.Fatalf("ModifyFileDACL(second) error = %v", err)
	}
	second, err := ReadFileDACL(dir)
	if err != nil {
		t.Fatalf("ReadFileDACL(second) error = %v", err)
	}
	secondDACL, _, err := second.sd.DACL()
	if err != nil {
		t.Fatalf("second DACL() error = %v", err)
	}
	if secondDACL.AceCount != firstDACL.AceCount {
		t.Fatalf("AceCount after repeated ModifyFileDACL = %d, want %d", secondDACL.AceCount, firstDACL.AceCount)
	}
}

func TestRemoveFileDACLPrincipalsRemovesDenyWrite(t *testing.T) {
	dir := t.TempDir()
	keep := Entry{
		Principal: "S-1-5-21-1-2-3-4",
		Rights:    Modify,
		Mode:      Grant,
		Inherit:   true,
	}
	entry := Entry{
		Principal: "S-1-5-21-1-2-3-5",
		Rights:    Write,
		Mode:      Deny,
		Inherit:   true,
	}
	if err := ModifyFileDACL(dir, keep, entry); err != nil {
		t.Fatalf("ModifyFileDACL() error = %v", err)
	}
	if missing, err := MissingFileDACLEntries(dir, entry); err != nil || len(missing) != 0 {
		t.Fatalf("deny entry before remove = %#v/%v, want present", missing, err)
	}
	if err := RemoveFileDACLPrincipals(dir, entry.Principal); err != nil {
		t.Fatalf("RemoveFileDACLPrincipals() error = %v", err)
	}
	missing, err := MissingFileDACLEntries(dir, entry)
	if err != nil {
		t.Fatalf("MissingFileDACLEntries(after remove) error = %v", err)
	}
	if len(missing) == 0 {
		t.Fatalf("deny entry remained after remove")
	}
	if missing, err := MissingFileDACLEntries(dir, keep); err != nil || len(missing) != 0 {
		t.Fatalf("kept grant after remove = %#v/%v, want present", missing, err)
	}
}

func TestReplaceAndWriteFileDACL(t *testing.T) {
	dir := t.TempDir()
	username := os.Getenv("USERNAME")
	if username == "" {
		t.Skip("USERNAME unavailable")
	}
	if err := ReplaceFileDACL(dir, false, Entry{
		Principal: username,
		Rights:    Modify,
		Mode:      Grant,
		Inherit:   true,
	}); err != nil {
		t.Fatalf("ReplaceFileDACL() error = %v", err)
	}
	descriptor, err := ReadFileDACL(dir)
	if err != nil {
		t.Fatalf("ReadFileDACL(after replace) error = %v", err)
	}
	if err := WriteFileDACL(dir, descriptor, false); err != nil {
		t.Fatalf("WriteFileDACL() error = %v", err)
	}
}

func TestWriteDenyMaskDoesNotDenySynchronize(t *testing.T) {
	mask := rightsMask(Write)
	if mask&windows.SYNCHRONIZE != 0 {
		t.Fatalf("rightsMask(Write) = %#x, must not deny SYNCHRONIZE needed by read-only opens", mask)
	}
	for _, want := range []windows.ACCESS_MASK{
		windows.FILE_WRITE_DATA,
		windows.FILE_APPEND_DATA,
		windows.FILE_WRITE_EA,
		windows.FILE_WRITE_ATTRIBUTES,
		windows.DELETE,
		fileDeleteChild,
	} {
		if mask&want == 0 {
			t.Fatalf("rightsMask(Write) = %#x, missing write/delete bit %#x", mask, want)
		}
	}
}
