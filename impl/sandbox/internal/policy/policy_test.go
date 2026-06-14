package policy

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestDefaultMergesConstraintPathRules(t *testing.T) {
	t.Parallel()

	workspace := testWorkspaceRoot()
	readOnly := testReadOnlyRoot()
	p := Default(sandbox.Config{
		CWD: "/sandbox-cwd",
	}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
		PathRules: []sandbox.PathRule{
			{Path: workspace, Access: sandbox.PathAccessReadWrite},
			{Path: readOnly, Access: sandbox.PathAccessReadOnly},
		},
	})

	if !slices.Contains(p.WritableRoots, workspace) {
		t.Fatalf("WritableRoots = %#v, want %s from constraints", p.WritableRoots, workspace)
	}
	if !slices.Contains(p.ReadableRoots, readOnly) {
		t.Fatalf("ReadableRoots = %#v, want %s from constraints", p.ReadableRoots, readOnly)
	}
	if slices.Contains(p.HiddenRoots, "/hidden") {
		t.Fatalf("HiddenRoots = %#v, did not expect /hidden without hidden path rule", p.HiddenRoots)
	}
}

func TestDefaultKeepsGitReadOnlyUnlessExplicitGitPathIsWritable(t *testing.T) {
	t.Parallel()

	workspace := testWorkspaceRoot()
	gitPath := filepath.Join(workspace, ".git")
	p := Default(sandbox.Config{
		CWD: workspace,
	}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
		PathRules: []sandbox.PathRule{
			{Path: workspace, Access: sandbox.PathAccessReadWrite},
		},
	})
	if !slices.Contains(p.ReadOnlySubpaths, ".git") {
		t.Fatalf("ReadOnlySubpaths = %#v, want default .git protection", p.ReadOnlySubpaths)
	}

	p = Default(sandbox.Config{
		CWD: workspace,
	}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
		PathRules: []sandbox.PathRule{
			{Path: workspace, Access: sandbox.PathAccessReadWrite},
			{Path: gitPath, Access: sandbox.PathAccessReadWrite},
		},
	})
	if slices.Contains(p.ReadOnlySubpaths, ".git") {
		t.Fatalf("ReadOnlySubpaths = %#v, did not expect .git after explicit .git write grant", p.ReadOnlySubpaths)
	}

	p = Default(sandbox.Config{
		CWD: workspace,
	}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
		PathRules: []sandbox.PathRule{
			{Path: filepath.Join(gitPath, "hooks"), Access: sandbox.PathAccessReadWrite},
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

	workspace := testWorkspaceRoot()
	p := Default(sandbox.Config{
		CWD: "/sandbox-cwd",
	}, sandbox.Constraints{
		Permission: sandbox.PermissionFullAccess,
		PathRules: []sandbox.PathRule{
			{Path: workspace, Access: sandbox.PathAccessReadWrite},
		},
	})

	if len(p.WritableRoots) != 0 || len(p.ReadableRoots) != 0 {
		t.Fatalf("full access roots = readable %#v writable %#v, want unrestricted nil roots", p.ReadableRoots, p.WritableRoots)
	}
}

func TestWritableRootPathDoesNotBroadenMissingRootToParent(t *testing.T) {
	t.Parallel()

	fakeHome := filepath.Join(t.TempDir(), "home")
	missingCache := filepath.Join(fakeHome, ".pnpm-store")

	if got := WritableRootPath(missingCache); got != missingCache {
		t.Fatalf("WritableRootPath(%q) = %q, want exact path", missingCache, got)
	}
	if got := WritableRootPath("  " + missingCache + "  "); got != missingCache {
		t.Fatalf("WritableRootPath(trimmed %q) = %q, want exact path", missingCache, got)
	}
	if got := WritableRootPath(" "); got != "" {
		t.Fatalf("WritableRootPath(blank) = %q, want empty", got)
	}
}

func testWorkspaceRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\workspace`
	}
	return "/workspace"
}

func TestEnsureExplicitWritableRootsCreatesMissingCacheDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	missingCache := filepath.Join(root, "home", ".pnpm-store")
	if err := EnsureExplicitWritableRoots([]string{missingCache}); err != nil {
		t.Fatalf("EnsureExplicitWritableRoots() error = %v", err)
	}
	if _, err := os.Stat(missingCache); err != nil {
		t.Fatalf("Stat(missingCache) error = %v, want created directory", err)
	}
	parent := filepath.Dir(missingCache)
	if _, err := os.Stat(parent); err != nil {
		t.Fatalf("Stat(parent) error = %v, want parent created for nested root", err)
	}
}

func testReadOnlyRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\read-only`
	}
	return "/read-only"
}
