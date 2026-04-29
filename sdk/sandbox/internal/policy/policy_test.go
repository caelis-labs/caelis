package policy

import (
	"slices"
	"testing"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
)

func TestDefaultMergesConstraintPathRules(t *testing.T) {
	t.Parallel()

	p := Default(sdksandbox.Config{
		CWD: "/sandbox-cwd",
	}, sdksandbox.Constraints{
		Permission: sdksandbox.PermissionWorkspaceWrite,
		PathRules: []sdksandbox.PathRule{
			{Path: "/workspace", Access: sdksandbox.PathAccessReadWrite},
			{Path: "/read-only", Access: sdksandbox.PathAccessReadOnly},
		},
	})

	if !slices.Contains(p.WritableRoots, "/workspace") {
		t.Fatalf("WritableRoots = %#v, want /workspace from constraints", p.WritableRoots)
	}
	if !slices.Contains(p.ReadableRoots, "/read-only") {
		t.Fatalf("ReadableRoots = %#v, want /read-only from constraints", p.ReadableRoots)
	}
}

func TestDefaultFullAccessIgnoresConstraintPathRules(t *testing.T) {
	t.Parallel()

	p := Default(sdksandbox.Config{
		CWD: "/sandbox-cwd",
	}, sdksandbox.Constraints{
		Permission: sdksandbox.PermissionFullAccess,
		PathRules: []sdksandbox.PathRule{
			{Path: "/workspace", Access: sdksandbox.PathAccessReadWrite},
		},
	})

	if len(p.WritableRoots) != 0 || len(p.ReadableRoots) != 0 {
		t.Fatalf("full access roots = readable %#v writable %#v, want unrestricted nil roots", p.ReadableRoots, p.WritableRoots)
	}
}
