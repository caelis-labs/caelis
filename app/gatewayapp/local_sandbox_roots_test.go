package gatewayapp

import (
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/sandboxpolicy"
)

func TestSandboxPolicyMetadataAddsConfiguredWrites(t *testing.T) {
	got := sandboxpolicy.WithPolicyMetadata(map[string]any{
		"policy_extra_write_roots": []any{"/existing-write"},
	}, SandboxConfig{
		WritableRoots: []string{"/configured-write"},
	})

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
