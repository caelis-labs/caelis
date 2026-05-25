package sandboxpolicy

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/configstore"
)

func TestNormalizeBackendAcceptsWindowsElevatedAliases(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"windows", "windows-elevated", "windows_elevated", "windows elevated", "elevated"} {
		got, err := NormalizeBackend(input)
		if err != nil {
			t.Fatalf("NormalizeBackend(%q) error = %v", input, err)
		}
		if got != "windows-elevated" {
			t.Fatalf("NormalizeBackend(%q) = %q, want windows-elevated", input, got)
		}
	}
}

func TestNormalizeBackendAcceptsHost(t *testing.T) {
	t.Parallel()

	got, err := NormalizeBackend("host")
	if err != nil {
		t.Fatalf("NormalizeBackend(host) error = %v", err)
	}
	if got != "host" {
		t.Fatalf("NormalizeBackend(host) = %q, want host", got)
	}
}

func TestMergeConfigDefaultsSandboxNetworkEnabled(t *testing.T) {
	t.Parallel()

	got := MergeConfig(configstore.SandboxConfig{}, configstore.SandboxConfig{})
	if got.NetworkEnabled == nil || !*got.NetworkEnabled {
		t.Fatalf("NetworkEnabled = %#v, want true default", got.NetworkEnabled)
	}
}

func TestMergeConfigPreservesStoredSandboxNetworkDisabled(t *testing.T) {
	t.Parallel()

	disabled := false
	got := MergeConfig(configstore.SandboxConfig{NetworkEnabled: &disabled}, configstore.SandboxConfig{})
	if got.NetworkEnabled == nil || *got.NetworkEnabled {
		t.Fatalf("NetworkEnabled = %#v, want stored false", got.NetworkEnabled)
	}
}

func TestMergeConfigAllowsSandboxNetworkOverride(t *testing.T) {
	t.Parallel()

	disabled := false
	enabled := true
	got := MergeConfig(configstore.SandboxConfig{NetworkEnabled: &disabled}, configstore.SandboxConfig{NetworkEnabled: &enabled})
	if got.NetworkEnabled == nil || !*got.NetworkEnabled {
		t.Fatalf("NetworkEnabled = %#v, want override true", got.NetworkEnabled)
	}
}

func TestEffectiveConfigWindowsDefaultsAutoToHost(t *testing.T) {
	t.Parallel()

	got := EffectiveConfigForGOOS(configstore.SandboxConfig{RequestedType: "auto"}, t.TempDir(), "windows")
	if got.RequestedType != "host" {
		t.Fatalf("RequestedType = %q, want host", got.RequestedType)
	}
}

func TestEffectiveConfigWindowsPreservesExplicitElevatedSandbox(t *testing.T) {
	t.Parallel()

	got := EffectiveConfigForGOOS(configstore.SandboxConfig{RequestedType: "windows-elevated"}, t.TempDir(), "windows")
	if got.RequestedType != "windows-elevated" {
		t.Fatalf("RequestedType = %q, want windows-elevated", got.RequestedType)
	}
}

func TestEffectiveConfigNonWindowsKeepsAuto(t *testing.T) {
	t.Parallel()

	got := EffectiveConfigForGOOS(configstore.SandboxConfig{RequestedType: "auto"}, t.TempDir(), "linux")
	if got.RequestedType != "auto" {
		t.Fatalf("RequestedType = %q, want auto", got.RequestedType)
	}
}
