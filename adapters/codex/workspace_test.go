package codex

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkspacePolicyRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform-specific privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "outside")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	policy := WorkspacePolicy{AllowedRoots: []string{root}, WritableRoots: []string{root}}
	if _, err := policy.validate(link, nil); err == nil {
		t.Fatal("validate() accepted a cwd that escapes through a symlink")
	}
}

func TestWorkspacePolicyRequiresWritableAuthority(t *testing.T) {
	root := t.TempDir()
	readOnly := filepath.Join(root, "read-only")
	if err := os.Mkdir(readOnly, 0o755); err != nil {
		t.Fatal(err)
	}
	policy := WorkspacePolicy{AllowedRoots: []string{root}, WritableRoots: []string{root}}
	got, err := policy.validate(root, []string{readOnly})
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := cleanAbsolute(root)
	if err != nil {
		t.Fatal(err)
	}
	wantReadOnly, err := cleanAbsolute(readOnly)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != wantRoot || got[1] != wantReadOnly {
		t.Fatalf("validated roots = %v", got)
	}

	policy.WritableRoots = []string{readOnly}
	if _, err := policy.validate(root, nil); err == nil {
		t.Fatal("validate() accepted a cwd outside writable authority")
	}
}
