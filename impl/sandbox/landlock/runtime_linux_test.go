//go:build linux

package landlock

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/impl/sandbox/internal/policy"
)

func TestLandlockWritableRootsDoNotBroadenMissingRootToParent(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "workspace")
	fakeHome := filepath.Join(root, "home")
	missingCache := filepath.Join(fakeHome, ".pnpm-store")
	for _, dir := range []string{workDir, fakeHome} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("Mkdir(%q) error = %v", dir, err)
		}
	}

	roots := landlockWritableRoots(policy.Policy{
		Type:          policy.TypeWorkspaceWrite,
		WritableRoots: []string{workDir, missingCache},
	}, workDir)

	if containsString(roots, fakeHome) {
		t.Fatalf("Writable roots = %#v, must not grant parent of missing root %q", roots, missingCache)
	}
	if containsString(roots, missingCache) {
		t.Fatalf("Writable roots = %#v, did not expect missing root %q to be added", roots, missingCache)
	}
}

func TestProbeRuntimeHonorsContextTimeout(t *testing.T) {
	runner := &landlockRunner{
		execCommand: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sleep", "30")
		},
		helperPath: "/bin/sh",
		probe:      func() error { return nil },
		goos:       "linux",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := runner.probeRuntime(ctx)
	if err == nil {
		t.Fatal("probeRuntime() error = nil, want timeout failure")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("probeRuntime() elapsed = %s, want prompt context cancellation", elapsed)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
