package sandboxpolicy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
)

func TestExternalSkillReadRootsUsesExistingOutsideWorkspaceDirectories(t *testing.T) {
	workspace := t.TempDir()
	external := filepath.Join(t.TempDir(), "external")
	local := filepath.Join(workspace, "skills", "local")
	for _, root := range []string{external, local} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", root, err)
		}
		if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("# Test\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", root, err)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	externalResolved, err := filepath.EvalSymlinks(external)
	if err != nil {
		t.Fatalf("EvalSymlinks(external) error = %v", err)
	}

	got := ExternalSkillReadRoots(workspace, []skill.Meta{
		{Name: "external", Path: filepath.Join(external, "SKILL.md")},
		{Name: "external-directory", Path: external},
		{Name: "local", Path: filepath.Join(local, "SKILL.md")},
		{Name: "missing", Path: filepath.Join(missing, "SKILL.md")},
		{Name: "empty-path"},
	})

	if want := []string{externalResolved}; !slices.Equal(got, want) {
		t.Fatalf("ExternalSkillReadRoots() = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing skill root stat error = %v, want not created", err)
	}
}

func TestExternalSkillReadRootsIncludesWorkspaceSymlinkTargetOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	external := filepath.Join(t.TempDir(), "external")
	if err := os.MkdirAll(external, 0o700); err != nil {
		t.Fatalf("MkdirAll(external) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(external, "SKILL.md"), []byte("# Linked\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(SKILL.md) error = %v", err)
	}
	externalResolved, err := filepath.EvalSymlinks(external)
	if err != nil {
		t.Fatalf("EvalSymlinks(external) error = %v", err)
	}
	link := filepath.Join(workspace, "linked-skill")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	got := ExternalSkillReadRoots(workspace, []skill.Meta{{
		Name: "linked",
		Path: filepath.Join(link, "SKILL.md"),
	}})

	if want := []string{externalResolved}; !slices.Equal(got, want) {
		t.Fatalf("ExternalSkillReadRoots() = %#v, want %#v", got, want)
	}
}
