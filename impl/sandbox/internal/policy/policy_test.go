package policy

import (
	"slices"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestDefaultMergesConstraintPathRules(t *testing.T) {
	t.Parallel()

	p := Default(sandbox.Config{
		CWD: "/sandbox-cwd",
	}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
		PathRules: []sandbox.PathRule{
			{Path: "/workspace", Access: sandbox.PathAccessReadWrite},
			{Path: "/read-only", Access: sandbox.PathAccessReadOnly},
		},
	})

	if !slices.Contains(p.WritableRoots, "/workspace") {
		t.Fatalf("WritableRoots = %#v, want /workspace from constraints", p.WritableRoots)
	}
	if !slices.Contains(p.ReadableRoots, "/read-only") {
		t.Fatalf("ReadableRoots = %#v, want /read-only from constraints", p.ReadableRoots)
	}
	if slices.Contains(p.HiddenRoots, "/hidden") {
		t.Fatalf("HiddenRoots = %#v, did not expect /hidden without hidden path rule", p.HiddenRoots)
	}
}

func TestDefaultKeepsGitReadOnlyUnlessExplicitGitPathIsWritable(t *testing.T) {
	t.Parallel()

	p := Default(sandbox.Config{
		CWD: "/workspace",
	}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
		PathRules: []sandbox.PathRule{
			{Path: "/workspace", Access: sandbox.PathAccessReadWrite},
		},
	})
	if !slices.Contains(p.ReadOnlySubpaths, ".git") {
		t.Fatalf("ReadOnlySubpaths = %#v, want default .git protection", p.ReadOnlySubpaths)
	}

	p = Default(sandbox.Config{
		CWD: "/workspace",
	}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
		PathRules: []sandbox.PathRule{
			{Path: "/workspace", Access: sandbox.PathAccessReadWrite},
			{Path: "/workspace/.git", Access: sandbox.PathAccessReadWrite},
		},
	})
	if slices.Contains(p.ReadOnlySubpaths, ".git") {
		t.Fatalf("ReadOnlySubpaths = %#v, did not expect .git after explicit .git write grant", p.ReadOnlySubpaths)
	}

	p = Default(sandbox.Config{
		CWD: "/workspace",
	}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
		PathRules: []sandbox.PathRule{
			{Path: "/workspace/.git/hooks", Access: sandbox.PathAccessReadWrite},
		},
	})
	if slices.Contains(p.ReadOnlySubpaths, ".git") {
		t.Fatalf("ReadOnlySubpaths = %#v, did not expect .git after explicit nested .git write grant", p.ReadOnlySubpaths)
	}
}

func TestDefaultMergesHiddenPathRules(t *testing.T) {
	t.Parallel()

	p := Default(sandbox.Config{
		CWD: "/sandbox-cwd",
	}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
		PathRules: []sandbox.PathRule{
			{Path: "/hidden", Access: sandbox.PathAccessHidden},
		},
	})

	if !slices.Contains(p.HiddenRoots, "/hidden") {
		t.Fatalf("HiddenRoots = %#v, want /hidden from constraints", p.HiddenRoots)
	}
}

func TestDefaultFullAccessIgnoresConstraintPathRules(t *testing.T) {
	t.Parallel()

	p := Default(sandbox.Config{
		CWD: "/sandbox-cwd",
	}, sandbox.Constraints{
		Permission: sandbox.PermissionFullAccess,
		PathRules: []sandbox.PathRule{
			{Path: "/workspace", Access: sandbox.PathAccessReadWrite},
		},
	})

	if len(p.WritableRoots) != 0 || len(p.ReadableRoots) != 0 {
		t.Fatalf("full access roots = readable %#v writable %#v, want unrestricted nil roots", p.ReadableRoots, p.WritableRoots)
	}
}
