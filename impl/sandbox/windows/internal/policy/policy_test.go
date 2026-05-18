package policy

import (
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
