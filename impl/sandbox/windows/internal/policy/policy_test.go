package policy

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/pathutil"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestBuildMapsPathRulesAndReadOnlySubpaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows policy roots")
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	readonly := filepath.Join(workspace, "vendor")
	hidden := filepath.Join(workspace, "secret")
	extraRead := filepath.Join(t.TempDir(), "read")
	extraWrite := filepath.Join(t.TempDir(), "write")

	p := Build(Input{
		Config: sandbox.Config{
			CWD:              workspace,
			ReadOnlySubpaths: []string{"vendor"},
		},
		Constraints: sandbox.Constraints{
			Network: sandbox.NetworkDisabled,
			PathRules: []sandbox.PathRule{
				{Path: extraRead, Access: sandbox.PathAccessReadOnly},
				{Path: extraWrite, Access: sandbox.PathAccessReadWrite},
				{Path: hidden, Access: sandbox.PathAccessHidden},
			},
		},
	})

	if p.Network != NetworkOffline {
		t.Fatalf("Network = %q, want offline", p.Network)
	}
	if len(p.CapabilitySIDs) != 0 {
		t.Fatalf("CapabilitySIDs = %#v, want no capabilities before runtime binding", p.CapabilitySIDs)
	}
	if len(p.WriteRootCapabilitySIDs) != 0 {
		t.Fatalf("WriteRootCapabilitySIDs = %#v, want no capabilities before runtime binding", p.WriteRootCapabilitySIDs)
	}
	if !containsPath(p.ReadRoots, extraRead) || !containsPath(p.ReadRoots, extraWrite) {
		t.Fatalf("ReadRoots = %#v, want rule roots", p.ReadRoots)
	}
	if !containsPath(p.WriteRoots, workspace) || !containsPath(p.WriteRoots, extraWrite) {
		t.Fatalf("WriteRoots = %#v, want workspace and write rule", p.WriteRoots)
	}
	if !containsPath(p.DenyReadPaths, hidden) {
		t.Fatalf("DenyReadPaths = %#v, want hidden path", p.DenyReadPaths)
	}
	if !containsPath(p.DenyWritePaths, hidden) || !containsPath(p.DenyWritePaths, readonly) {
		t.Fatalf("DenyWritePaths = %#v, want hidden and read-only subpath", p.DenyWritePaths)
	}
	if !containsPath(p.MaterializeDenyWritePaths, hidden) || !containsPath(p.MaterializeDenyWritePaths, readonly) {
		t.Fatalf("MaterializeDenyWritePaths = %#v, want hidden and read-only subpath", p.MaterializeDenyWritePaths)
	}
}

func TestBuildMaterializesExplicitWritableCarveouts(t *testing.T) {
	workspace := pathutil.Normalize(t.TempDir())
	readonly := filepath.Join(workspace, "vendor")
	hidden := filepath.Join(workspace, "secret")

	p := Build(Input{
		Config: sandbox.Config{
			CWD:              workspace,
			ReadOnlySubpaths: []string{"vendor"},
		},
		Constraints: sandbox.Constraints{
			PathRules: []sandbox.PathRule{
				{Path: hidden, Access: sandbox.PathAccessHidden},
			},
		},
	})

	if !containsPath(p.DenyWritePaths, hidden) || !containsPath(p.DenyWritePaths, readonly) {
		t.Fatalf("DenyWritePaths = %#v, want hidden and read-only carveouts", p.DenyWritePaths)
	}
	if !containsPath(p.MaterializeDenyWritePaths, hidden) || !containsPath(p.MaterializeDenyWritePaths, readonly) {
		t.Fatalf("MaterializeDenyWritePaths = %#v, want hidden and read-only carveouts", p.MaterializeDenyWritePaths)
	}
}

func TestBuildProtectsExistingControlDirsUnderWritableRoots(t *testing.T) {
	workspace := pathutil.Normalize(t.TempDir())
	gitDir := filepath.Join(workspace, ".git")
	codexDir := filepath.Join(workspace, ".codex")
	agentsDir := filepath.Join(workspace, ".agents")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(.git) error = %v", err)
	}
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(.codex) error = %v", err)
	}

	p := Build(Input{Config: sandbox.Config{CWD: workspace}})

	if !containsPath(p.DenyWritePaths, gitDir) || !containsPath(p.DenyWritePaths, codexDir) {
		t.Fatalf("DenyWritePaths = %#v, want existing control dirs", p.DenyWritePaths)
	}
	if containsPath(p.DenyWritePaths, agentsDir) {
		t.Fatalf("DenyWritePaths = %#v, want missing .agents skipped", p.DenyWritePaths)
	}
	if containsPath(p.MaterializeDenyWritePaths, gitDir) || containsPath(p.MaterializeDenyWritePaths, codexDir) {
		t.Fatalf("MaterializeDenyWritePaths = %#v, want control dirs not materialized", p.MaterializeDenyWritePaths)
	}
}

func TestBuildFullAccessSkipsRoots(t *testing.T) {
	p := Build(Input{Constraints: sandbox.Constraints{
		Permission: sandbox.PermissionFullAccess,
		Network:    sandbox.NetworkEnabled,
	}})
	if !p.FullAccess {
		t.Fatal("FullAccess = false, want true")
	}
	if len(p.ReadRoots) != 0 || len(p.WriteRoots) != 0 {
		t.Fatalf("roots = read %#v write %#v, want unrestricted nil roots", p.ReadRoots, p.WriteRoots)
	}
	if p.Network != NetworkOnline {
		t.Fatalf("Network = %q, want online", p.Network)
	}
}

func TestBuildNetworkModes(t *testing.T) {
	disabled := Build(Input{Config: sandbox.Config{CWD: t.TempDir()}, Constraints: sandbox.Constraints{
		Network: sandbox.NetworkDisabled,
	}})
	if disabled.Network != NetworkOffline {
		t.Fatalf("disabled Network = %q, want offline", disabled.Network)
	}

	enabled := Build(Input{Config: sandbox.Config{CWD: t.TempDir()}, Constraints: sandbox.Constraints{
		Network: sandbox.NetworkEnabled,
	}})
	if enabled.Network != NetworkOnline {
		t.Fatalf("enabled Network = %q, want online", enabled.Network)
	}
}

func containsPath(paths []string, want string) bool {
	wantKey := pathutil.Key(want)
	for _, path := range paths {
		if pathutil.Key(path) == wantKey {
			return true
		}
	}
	return false
}
