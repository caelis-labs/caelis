package gatewayapp

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestDefaultSkillSandboxRootsIncludeGlobalAndWorkspaceSkillDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := filepath.Join(t.TempDir(), "workspace")

	got := defaultSkillSandboxRoots(workspace)
	want := []string{
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(workspace, ".agents", "skills"),
		filepath.Join(workspace, "skills"),
	}
	for _, root := range want {
		if !slices.Contains(got, root) {
			t.Fatalf("defaultSkillSandboxRoots() = %#v, want %q", got, root)
		}
	}
}

func TestSandboxPolicyRootMetadataIncludesConfiguredAndSkillWriteRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := filepath.Join(t.TempDir(), "workspace")

	got := withSandboxPolicyRootMetadata(map[string]any{
		"policy_extra_write_roots": []any{"/existing-write"},
	}, SandboxConfig{
		ReadableRoots: []string{"/configured-read"},
		WritableRoots: []string{"/configured-write"},
	}, workspace)

	readRoots, ok := got["policy_extra_read_roots"].([]string)
	if !ok {
		t.Fatalf("policy_extra_read_roots = %#v, want []string", got["policy_extra_read_roots"])
	}
	if !slices.Contains(readRoots, "/configured-read") {
		t.Fatalf("policy_extra_read_roots = %#v, want configured readable root", readRoots)
	}

	writeRoots, ok := got["policy_extra_write_roots"].([]string)
	if !ok {
		t.Fatalf("policy_extra_write_roots = %#v, want []string", got["policy_extra_write_roots"])
	}
	for _, root := range []string{
		"/existing-write",
		"/configured-write",
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(workspace, ".agents", "skills"),
		filepath.Join(workspace, "skills"),
	} {
		if !slices.Contains(writeRoots, root) {
			t.Fatalf("policy_extra_write_roots = %#v, want %q", writeRoots, root)
		}
	}
}

func TestEffectiveSandboxConfigAddsSkillWritableRootsWithoutMutatingStoredConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := filepath.Join(t.TempDir(), "workspace")
	stored := SandboxConfig{WritableRoots: []string{"/configured-write"}}

	got := effectiveSandboxConfig(stored, workspace)
	if len(stored.WritableRoots) != 1 || stored.WritableRoots[0] != "/configured-write" {
		t.Fatalf("stored WritableRoots mutated: %#v", stored.WritableRoots)
	}
	for _, root := range []string{
		"/configured-write",
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(workspace, ".agents", "skills"),
		filepath.Join(workspace, "skills"),
	} {
		if !slices.Contains(got.WritableRoots, root) {
			t.Fatalf("effective WritableRoots = %#v, want %q", got.WritableRoots, root)
		}
	}
}
