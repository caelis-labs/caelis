package gatewayapp

import (
	"slices"
	"testing"

	policyapi "github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/sandboxpolicy"
)

func TestSandboxPolicyMetadataAddsConfiguredWrites(t *testing.T) {
	got := sandboxpolicy.WithPolicyMetadata(map[string]any{
		policyapi.MetadataWritableRoots: []any{"/existing-write"},
	}, SandboxConfig{
		WritableRoots: []string{"/configured-write"},
	})

	writeRoots, ok := got[policyapi.MetadataWritableRoots].([]string)
	if !ok {
		t.Fatalf("policy_writable_roots = %#v, want []string", got[policyapi.MetadataWritableRoots])
	}
	if want := []string{"/existing-write", "/configured-write"}; !slices.Equal(writeRoots, want) {
		t.Fatalf("policy_writable_roots = %#v, want %#v", writeRoots, want)
	}
}

func TestSandboxConfigToPortAddsSafeWorkspaceWithoutMutatingStoredRoots(t *testing.T) {
	workspace := t.TempDir()
	configured := t.TempDir()
	stored := SandboxConfig{WritableRoots: []string{configured}}

	got := sandboxConfigToPort(stored, workspace, t.TempDir())

	if want := []string{configured}; !slices.Equal(stored.WritableRoots, want) {
		t.Fatalf("stored WritableRoots mutated: %#v", stored.WritableRoots)
	}
	if want := []string{workspace, configured}; !slices.Equal(got.WritableRoots, want) {
		t.Fatalf("port WritableRoots = %#v, want %#v", got.WritableRoots, want)
	}
}

func TestSandboxConfigToPortInjectsProcessHostAuthority(t *testing.T) {
	authority := t.TempDir()
	got := sandboxConfigToPortWithAuthority(SandboxConfig{}, t.TempDir(), t.TempDir(), authority)
	if got.HostAuthorityDir != authority {
		t.Fatalf("HostAuthorityDir = %q, want %q", got.HostAuthorityDir, authority)
	}
}
