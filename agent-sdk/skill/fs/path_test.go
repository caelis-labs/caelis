package fs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootDirAcceptsSkillFileOrDirectory(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "example")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll(root) error = %v", err)
	}
	skillPath := filepath.Join(root, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("# Example\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(SKILL.md) error = %v", err)
	}

	for _, input := range []string{root, skillPath} {
		got, err := RootDir(input)
		if err != nil {
			t.Fatalf("RootDir(%q) error = %v", input, err)
		}
		if got != root {
			t.Fatalf("RootDir(%q) = %q, want %q", input, got, root)
		}
	}
}

func TestRootDirRejectsDirectoryWithoutSkillFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := RootDir(root); !os.IsNotExist(err) {
		t.Fatalf("RootDir(%q) error = %v, want missing SKILL.md", root, err)
	}
}
