package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureSystemSkillsMaterializesEmbeddedSkills(t *testing.T) {
	home := t.TempDir()
	if err := ensureSystemSkills(home); err != nil {
		t.Fatalf("ensureSystemSkills() error = %v", err)
	}
	root := systemSkillRoot(home)
	for _, path := range []string{
		filepath.Join(root, "skill-creator", "SKILL.md"),
		filepath.Join(root, "skill-creator", "scripts", "init_skill.py"),
		filepath.Join(root, "skill-installer", "SKILL.md"),
		filepath.Join(root, "skill-installer", "scripts", "install-skill-from-github.py"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("materialized skill asset %s: %v", path, err)
		}
	}
}

func TestEnsureSystemSkillsRejectsSystemRootSymlink(t *testing.T) {
	home := t.TempDir()
	root := systemSkillRoot(home)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatalf("mkdir root parent: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	symlinkDirOrSkip(t, outside, root)

	err := ensureSystemSkills(home)
	if err == nil || !strings.Contains(err.Error(), "linked path") {
		t.Fatalf("ensureSystemSkills() error = %v, want linked path refusal", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "skill-creator")); !os.IsNotExist(err) {
		t.Fatalf("outside skill stat err = %v, want not created", err)
	}
}

func TestEnsureSystemSkillsRejectsSystemSkillSymlink(t *testing.T) {
	home := t.TempDir()
	root := systemSkillRoot(home)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	symlinkDirOrSkip(t, outside, filepath.Join(root, "skill-creator"))

	err := ensureSystemSkills(home)
	if err == nil || !strings.Contains(err.Error(), "linked path") {
		t.Fatalf("ensureSystemSkills() error = %v, want linked path refusal", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("outside SKILL.md stat err = %v, want not created", err)
	}
}

func symlinkDirOrSkip(t *testing.T, target string, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("creating directory symlink is unavailable: %v", err)
	}
}
