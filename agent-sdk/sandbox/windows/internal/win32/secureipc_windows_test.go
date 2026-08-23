//go:build windows

package win32

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStableDirectoryCreatesIPCFilesExactlyOnce(t *testing.T) {
	dir, err := OpenStableDirectory(t.TempDir(), "")
	if err != nil {
		t.Fatalf("OpenStableDirectory() error = %v", err)
	}
	defer dir.Close()
	file, identity, err := dir.CreateNewFile("repair.result.json")
	if err != nil {
		t.Fatalf("CreateNewFile() error = %v", err)
	}
	defer file.Close()
	if identity == (FileIdentity{}) {
		t.Fatal("CreateNewFile() returned empty identity")
	}
	if _, _, err := dir.CreateNewFile("repair.result.json"); err == nil {
		t.Fatal("CreateNewFile(existing) succeeded, want CREATE_NEW failure")
	}
}

func TestOpenStableDirectoryRejectsUnexpectedOwner(t *testing.T) {
	path := t.TempDir()
	dir, err := OpenStableDirectory(path, "")
	if err != nil {
		t.Fatalf("OpenStableDirectory() error = %v", err)
	}
	ownerSID := dir.OwnerSID()
	if err := dir.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	unexpected := "S-1-1-0"
	if strings.EqualFold(ownerSID, unexpected) {
		unexpected = "S-1-5-18"
	}
	if _, err := OpenStableDirectory(path, unexpected); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("OpenStableDirectory(unexpected owner) error = %v, want owner mismatch", err)
	}
}

func TestOpenStableDirectoryRejectsJunctionAncestor(t *testing.T) {
	ownerSID, err := CurrentProcessUserSID()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	child := filepath.Join(target, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		output, junctionErr := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
		if junctionErr != nil {
			t.Skipf("directory symlink/junction unavailable: symlink=%v junction=%v (%s)", err, junctionErr, strings.TrimSpace(string(output)))
		}
		defer func() { _ = os.Remove(link) }()
	}
	if _, err := OpenStableDirectory(filepath.Join(link, "child"), ownerSID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "reparse") {
		t.Fatalf("OpenStableDirectory(junction ancestor) error = %v, want reparse rejection", err)
	}
}
