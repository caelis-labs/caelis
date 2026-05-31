package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/windows/internal/pathutil"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestBuildUsesOnlyWritableRootsAndDenyWriteCarveouts(t *testing.T) {
	workspace := pathutil.Normalize(t.TempDir())
	commandDir := filepath.Join(workspace, "cmd")
	extraWrite := pathutil.Normalize(filepath.Join(t.TempDir(), "write"))
	extraRead := pathutil.Normalize(filepath.Join(t.TempDir(), "read"))
	hidden := pathutil.Normalize(filepath.Join(workspace, "secret"))
	for _, dir := range []string{commandDir, extraWrite, extraRead, hidden, filepath.Join(workspace, ".git"), filepath.Join(workspace, "vendor")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}

	p := Build(Input{
		Config: sandbox.Config{
			CWD:              workspace,
			WritableRoots:    []string{extraWrite},
			ReadableRoots:    []string{extraRead},
			ReadOnlySubpaths: []string{"vendor"},
		},
		CommandDir: commandDir,
		Constraints: sandbox.Constraints{
			Network: sandbox.NetworkDisabled,
			PathRules: []sandbox.PathRule{
				{Path: extraRead, Access: sandbox.PathAccessReadOnly},
				{Path: extraWrite, Access: sandbox.PathAccessReadWrite},
				{Path: hidden, Access: sandbox.PathAccessHidden},
			},
		},
	})

	if p.Network != NetworkOnline {
		t.Fatalf("Network = %q, want online/non-enforced", p.Network)
	}
	if len(p.ReadRoots) != 0 || len(p.DenyReadPaths) != 0 {
		t.Fatalf("read policy = read %#v deny %#v, want no read boundary", p.ReadRoots, p.DenyReadPaths)
	}
	for _, want := range []string{workspace, commandDir, extraWrite} {
		if !containsPath(p.WriteRoots, want) {
			t.Fatalf("WriteRoots = %#v, want %q", p.WriteRoots, want)
		}
	}
	for _, unexpected := range []string{extraRead, hidden} {
		if containsPath(p.WriteRoots, unexpected) || containsPath(p.DenyWritePaths, unexpected) {
			t.Fatalf("policy unexpectedly consumed non-write path %q: %+v", unexpected, p)
		}
	}
	for _, want := range []string{filepath.Join(workspace, ".git"), filepath.Join(workspace, "vendor")} {
		if !containsPath(p.DenyWritePaths, want) {
			t.Fatalf("DenyWritePaths = %#v, want %q", p.DenyWritePaths, want)
		}
	}
}

func TestBuildFullAccessSkipsRoots(t *testing.T) {
	p := Build(Input{Constraints: sandbox.Constraints{
		Permission: sandbox.PermissionFullAccess,
		Network:    sandbox.NetworkDisabled,
	}})
	if !p.FullAccess {
		t.Fatal("FullAccess = false, want true")
	}
	if len(p.ReadRoots) != 0 || len(p.WriteRoots) != 0 || len(p.DenyWritePaths) != 0 {
		t.Fatalf("policy roots = %+v, want unrestricted nil roots", p)
	}
	if p.Network != NetworkOnline {
		t.Fatalf("Network = %q, want online/non-enforced", p.Network)
	}
}

func TestCommonGlobalPolicyDoesNotAddReadOrNetworkControls(t *testing.T) {
	commonRoot := filepath.Join(t.TempDir(), "cache")
	p := CommonGlobalPolicy([]string{commonRoot})

	if !containsPath(p.WriteRoots, commonRoot) {
		t.Fatalf("WriteRoots = %#v, want %q", p.WriteRoots, commonRoot)
	}
	if len(p.ReadRoots) != 0 || len(p.DenyReadPaths) != 0 || len(p.DenyWritePaths) != 0 {
		t.Fatalf("policy = %+v, want no read/deny controls", p)
	}
	if p.Network != NetworkOnline {
		t.Fatalf("Network = %q, want online/non-enforced", p.Network)
	}
}

func TestEffectiveWindowsNetworkFallsBackOnline(t *testing.T) {
	t.Parallel()

	for _, network := range []sandbox.Network{
		"",
		sandbox.NetworkInherit,
		sandbox.NetworkEnabled,
		sandbox.NetworkDisabled,
	} {
		if got := effectiveWindowsNetwork(network); got != NetworkOnline {
			t.Fatalf("effectiveWindowsNetwork(%q) = %q, want online", network, got)
		}
	}
}

func TestCommonGlobalPolicyCompactsCoveredWriteRoots(t *testing.T) {
	commonRoot := filepath.Join(t.TempDir(), "cache")
	childRoot := filepath.Join(commonRoot, "go-build")
	p := CommonGlobalPolicy([]string{childRoot, commonRoot})

	if !containsPath(p.WriteRoots, commonRoot) {
		t.Fatalf("WriteRoots = %#v, want parent %q", p.WriteRoots, commonRoot)
	}
	if containsPath(p.WriteRoots, childRoot) {
		t.Fatalf("WriteRoots = %#v, want covered child removed", p.WriteRoots)
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
