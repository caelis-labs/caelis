package policy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestGitMetadataPathsProtectsWritableRootAndDirectChildOnly(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	directGit := filepath.Join(root, ".git")
	childGit := filepath.Join(root, "child", ".git")
	grandchildGit := filepath.Join(root, "child", "grandchild", ".git")
	for _, path := range []string{directGit, childGit, grandchildGit} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}

	got, err := GitMetadataPaths(root, []string{root}, nil)
	if err != nil {
		t.Fatalf("GitMetadataPaths() error = %v", err)
	}
	for _, want := range []string{directGit, childGit} {
		if !slices.Contains(got, want) {
			t.Fatalf("GitMetadataPaths() = %#v, want %q", got, want)
		}
	}
	if slices.Contains(got, grandchildGit) {
		t.Fatalf("GitMetadataPaths() = %#v, did not want depth-two %q", got, grandchildGit)
	}
}

func TestGitMetadataPathsResolvesWorktreeGitDirAndCommonDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	gitDir := filepath.Join(root, "admin", "worktrees", "worktree")
	commonDir := filepath.Join(root, "admin")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(gitDir) error = %v", err)
	}
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatalf("MkdirAll(worktree) error = %v", err)
	}
	gitFile := filepath.Join(worktree, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: ../admin/worktrees/worktree\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.git) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(commondir) error = %v", err)
	}

	got, err := GitMetadataPaths(root, []string{root}, nil)
	if err != nil {
		t.Fatalf("GitMetadataPaths() error = %v", err)
	}
	for _, want := range []string{gitFile, gitDir, commonDir} {
		if !slices.Contains(got, want) {
			t.Fatalf("GitMetadataPaths() = %#v, want %q", got, want)
		}
	}
}

func TestReadOnlyPathsScopesGitWriteOverrideToMatchingRepository(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	firstGit := filepath.Join(root, "first", ".git")
	secondGit := filepath.Join(root, "second", ".git")
	for _, path := range []string{firstGit, secondGit} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}
	got, err := ReadOnlyPaths(Policy{
		WritableRoots:      []string{root},
		GitProtectionRoots: []string{root},
		WriteOverrides:     []string{filepath.Join(firstGit, "index")},
		ReadOnlySubpaths:   []string{".git"},
	}, root)
	if err != nil {
		t.Fatalf("ReadOnlyPaths() error = %v", err)
	}
	if slices.Contains(got, firstGit) {
		t.Fatalf("ReadOnlyPaths() = %#v, did not want overridden %q", got, firstGit)
	}
	if !slices.Contains(got, secondGit) {
		t.Fatalf("ReadOnlyPaths() = %#v, want unaffected %q", got, secondGit)
	}
}
