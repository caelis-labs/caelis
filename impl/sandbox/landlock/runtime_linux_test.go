//go:build linux

package landlock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/policy"
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
