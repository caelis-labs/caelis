package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/internal/testenv"
)

func TestEnsureRejectsSystemRootSymlink(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	root := filepath.Join(home, ".caelis", "skills", ".system")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatalf("mkdir root parent: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	symlinkDirOrSkip(t, outside, root)

	_, err := Ensure()
	if err == nil || !strings.Contains(err.Error(), "linked path") {
		t.Fatalf("Ensure() error = %v, want linked path refusal", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "skill-creator")); !os.IsNotExist(err) {
		t.Fatalf("outside skill stat err = %v, want not created", err)
	}
}

func TestEnsureRejectsSystemSkillSymlink(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	root := filepath.Join(home, ".caelis", "skills", ".system")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	symlinkDirOrSkip(t, outside, filepath.Join(root, "skill-creator"))

	_, err := Ensure()
	if err == nil || !strings.Contains(err.Error(), "linked path") {
		t.Fatalf("Ensure() error = %v, want linked path refusal", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("outside SKILL.md stat err = %v, want not created", err)
	}
}

func TestEnsureRevalidatesRootAfterSuccessfulMaterialization(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	root := filepath.Join(home, ".caelis", "skills", ".system")
	outside := filepath.Join(t.TempDir(), "outside")

	if _, err := Ensure(); err != nil {
		t.Fatalf("Ensure() initial error = %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll(%s) error = %v", root, err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	symlinkDirOrSkip(t, outside, root)

	_, err := Ensure()
	if err == nil || !strings.Contains(err.Error(), "linked path") {
		t.Fatalf("Ensure() after root replacement error = %v, want linked path refusal", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "skill-creator")); !os.IsNotExist(err) {
		t.Fatalf("outside skill stat err = %v, want not created", err)
	}
}

func symlinkDirOrSkip(t *testing.T, target string, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("creating directory symlink is unavailable: %v", err)
	}
}
