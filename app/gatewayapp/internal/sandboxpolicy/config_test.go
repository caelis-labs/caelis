package sandboxpolicy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
)

func TestEffectiveConfigSeparatesBroadWorkspaceFromWritableRoots(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home")
	project := filepath.Join(home, "project")
	configured := filepath.Join(home, "configured")
	cfg := effectiveConfig(home, home, configstore.SandboxConfig{
		WritableRoots: []string{filepath.Dir(home), home, configured},
	})
	if want := []string{configured}; !slices.Equal(cfg.WritableRoots, want) {
		t.Fatalf("WritableRoots = %#v, want %#v", cfg.WritableRoots, want)
	}

	cfg = effectiveConfig(project, home, configstore.SandboxConfig{})
	if want := []string{project}; !slices.Equal(cfg.WritableRoots, want) {
		t.Fatalf("project WritableRoots = %#v, want %#v", cfg.WritableRoots, want)
	}
}

func TestEffectiveConfigRejectsVolumeRootAndResolvesRelativeRoots(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home")
	workspace := filepath.Join(home, "workspace")
	cfg := effectiveConfig(workspace, home, configstore.SandboxConfig{
		WritableRoots: []string{"cache", filepath.VolumeName(workspace) + string(filepath.Separator)},
	})
	want := []string{workspace, filepath.Join(workspace, "cache")}
	if !slices.Equal(cfg.WritableRoots, want) {
		t.Fatalf("WritableRoots = %#v, want %#v", cfg.WritableRoots, want)
	}
}

func TestEffectiveConfigDoesNotGrantHomeThroughSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	alias := filepath.Join(root, "home-alias")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatalf("Mkdir(home) error = %v", err)
	}
	if err := os.Symlink(home, alias); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	cfg := effectiveConfig(alias, home, configstore.SandboxConfig{})
	if len(cfg.WritableRoots) != 0 {
		t.Fatalf("WritableRoots = %#v, did not want symlinked home grant", cfg.WritableRoots)
	}
}

func TestNormalizeBackendAcceptsWindowsAliases(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"windows", "windows-restricted-token", "windows-elevated", "windows_elevated", "windows elevated", "elevated"} {
		got, err := NormalizeBackend(input)
		if err != nil {
			t.Fatalf("NormalizeBackend(%q) error = %v", input, err)
		}
		if got != "windows" {
			t.Fatalf("NormalizeBackend(%q) = %q, want windows", input, got)
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

func TestNormalizeBackendRejectsRetiredProductLandlockBackend(t *testing.T) {
	t.Parallel()

	_, err := NormalizeBackend("landlock")
	if err == nil {
		t.Fatal("NormalizeBackend(landlock) error = nil, want retired product backend rejection")
	}
	if message := err.Error(); !strings.Contains(message, "retired") || !strings.Contains(message, "bwrap") {
		t.Fatalf("NormalizeBackend(landlock) error = %q, want actionable retirement guidance", message)
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
