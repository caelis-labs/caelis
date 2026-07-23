package gatewayapp

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/sandboxpolicy"
)

func TestSandboxPolicyRootMetadataUsesOnlyDiscoveredExternalSkillsAsReadRoots(t *testing.T) {
	workspace := t.TempDir()
	externalRoot := filepath.Join(t.TempDir(), "skills", "external")
	workspaceRoot := filepath.Join(workspace, ".agents", "skills", "workspace")
	for _, root := range []string{externalRoot, workspaceRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", root, err)
		}
		if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("# Test\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", root, err)
		}
	}
	missingRoot := filepath.Join(t.TempDir(), "skills", "missing")
	externalResolved, err := filepath.EvalSymlinks(externalRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(externalRoot) error = %v", err)
	}
	catalog := skill.NewCatalog([]skill.Meta{
		{Name: "external", Path: filepath.Join(externalRoot, "SKILL.md")},
		{Name: "workspace", Path: filepath.Join(workspaceRoot, "SKILL.md")},
		{Name: "missing", Path: filepath.Join(missingRoot, "SKILL.md")},
	})

	got := sandboxpolicy.WithPolicyRootMetadata(map[string]any{
		"policy_extra_read_roots":  []any{"/existing-read"},
		"policy_extra_write_roots": []any{"/existing-write"},
	}, SandboxConfig{
		ReadableRoots: []string{"/configured-read"},
		WritableRoots: []string{"/configured-write"},
	}, workspace, catalog.Metas())

	readRoots, ok := got["policy_extra_read_roots"].([]string)
	if !ok {
		t.Fatalf("policy_extra_read_roots = %#v, want []string", got["policy_extra_read_roots"])
	}
	for _, root := range []string{"/existing-read", "/configured-read", externalResolved} {
		if !slices.Contains(readRoots, root) {
			t.Fatalf("policy_extra_read_roots = %#v, want %q", readRoots, root)
		}
	}
	for _, root := range []string{workspaceRoot, missingRoot, filepath.Join(workspace, "skills")} {
		if slices.Contains(readRoots, root) {
			t.Fatalf("policy_extra_read_roots = %#v, did not want %q", readRoots, root)
		}
	}

	writeRoots, ok := got["policy_extra_write_roots"].([]string)
	if !ok {
		t.Fatalf("policy_extra_write_roots = %#v, want []string", got["policy_extra_write_roots"])
	}
	if want := []string{"/existing-write", "/configured-write"}; !slices.Equal(writeRoots, want) {
		t.Fatalf("policy_extra_write_roots = %#v, want %#v", writeRoots, want)
	}
}

func TestSandboxConfigToPortPreservesOnlyConfiguredWritableRoots(t *testing.T) {
	workspace := t.TempDir()
	stored := SandboxConfig{WritableRoots: []string{"/configured-write"}}

	got := sandboxConfigToPort(stored, workspace, t.TempDir())

	if want := []string{"/configured-write"}; !slices.Equal(stored.WritableRoots, want) {
		t.Fatalf("stored WritableRoots mutated: %#v", stored.WritableRoots)
	}
	if want := []string{"/configured-write"}; !slices.Equal(got.WritableRoots, want) {
		t.Fatalf("port WritableRoots = %#v, want %#v", got.WritableRoots, want)
	}
}
