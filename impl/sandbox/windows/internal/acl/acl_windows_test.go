//go:build windows

package acl

import (
	"os"
	"testing"
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
