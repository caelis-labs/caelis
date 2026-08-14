package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCloneGitRepoImmutableKeepsExistingPublishedRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "marker.txt")
	runGit(t, repo, "commit", "-m", "v1")
	firstSHA := gitHEAD(t, repo)

	parent := filepath.Join(tmp, "cache")
	firstRoot, err := cloneLocalGitRepoImmutable(context.Background(), repo, "", parent, "")
	if err != nil {
		t.Fatalf("first clone: %v", err)
	}
	if filepath.Base(firstRoot) != firstSHA {
		t.Fatalf("first root = %q, want content-addressed %s", firstRoot, firstSHA)
	}
	if got := readFile(t, filepath.Join(firstRoot, "marker.txt")); got != "v1\n" {
		t.Fatalf("first marker = %q", got)
	}

	// Mutate source and clone again. Existing published root must remain.
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "marker.txt")
	runGit(t, repo, "commit", "-m", "v2")
	secondSHA := gitHEAD(t, repo)
	if secondSHA == firstSHA {
		t.Fatal("expected distinct commits")
	}
	secondRoot, err := cloneLocalGitRepoImmutable(context.Background(), repo, "", parent, "")
	if err != nil {
		t.Fatalf("second clone: %v", err)
	}
	if secondRoot == firstRoot {
		t.Fatal("second clone overwrote the first published root")
	}
	if got := readFile(t, filepath.Join(firstRoot, "marker.txt")); got != "v1\n" {
		t.Fatalf("old published root mutated: %q", got)
	}
	if got := readFile(t, filepath.Join(secondRoot, "marker.txt")); got != "v2\n" {
		t.Fatalf("new root marker = %q", got)
	}
}

func TestCloneGitRepoImmutableConcurrentSameParentPublishesOnce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "marker.txt")
	runGit(t, repo, "commit", "-m", "shared")
	wantSHA := gitHEAD(t, repo)
	parent := filepath.Join(tmp, "cache")

	const workers = 8
	roots := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			root, err := cloneLocalGitRepoImmutable(context.Background(), repo, "", parent, "")
			roots[idx] = root
			errs[idx] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	for i, root := range roots {
		if filepath.Base(root) != wantSHA {
			t.Fatalf("worker %d root = %q, want %s", i, root, wantSHA)
		}
		if root != roots[0] {
			t.Fatalf("worker %d published distinct root %q, want shared %q", i, root, roots[0])
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != ".staging" {
			published++
		}
	}
	if published != 1 {
		t.Fatalf("published roots = %d, want 1 under %s", published, parent)
	}
}

func TestValidateGitCloneURLProductionAllowlist(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"file:///etc/passwd",
		"/tmp/evil.git",
		"ftp://example.com/repo.git",
		"javascript:alert(1)",
		"http://example.com/repo.git",
		"git://example.com/repo.git",
		"repo",
		"./relative.git",
		"../escape.git",
		"-e",
		"--upload-pack=evil",
		"ssh://example.com/repo.git", // no authenticated user
		"https://user:pass@example.com/repo.git",
		"https://:pass@example.com/repo.git",
		"https://example.com/repo.git?token=secret",
		"ssh://git:pass@example.com/acme/repo.git",
	} {
		if _, err := validateGitCloneURL(raw); err == nil {
			t.Fatalf("validateGitCloneURL(%q) error = nil, want rejection", raw)
		}
	}
	for _, raw := range []string{
		"https://github.com/acme/repo.git",
		"git@github.com:acme/repo.git",
		"ssh://git@github.com/acme/repo.git",
	} {
		if got, err := validateGitCloneURL(raw); err != nil || got != raw {
			t.Fatalf("validateGitCloneURL(%q) = (%q, %v), want accepted", raw, got, err)
		}
	}
}

func TestGitSourceErrorsDoNotEchoCredentials(t *testing.T) {
	t.Parallel()

	source := "ssh://git:super-secret@example.com/acme/repo.git"
	if _, err := validateGitCloneURL(source); err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("validateGitCloneURL() error = %v, want credential-free rejection", err)
	}
	output := redactGitSourceOutput("fatal: could not read from "+source, source)
	if strings.Contains(output, "super-secret") || !strings.Contains(output, "<redacted git source>") {
		t.Fatalf("redacted output = %q", output)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func gitHEAD(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
