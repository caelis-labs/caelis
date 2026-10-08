//go:build darwin || linux || windows

package gatewayapp

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/internal/workspaceidentity"
)

// Native policy fixtures must sit outside ordinary /tmp and cache write grants.
func applicationNativePolicyRoot(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, ".caelis-native-policy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Windows manifest refresh can briefly retain a handle after Close,
		// so retry removal as testing.TempDir does for Windows file handles.
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := os.RemoveAll(root)
			if err == nil {
				return
			}
			if runtime.GOOS != "windows" || time.Now().After(deadline) {
				t.Error(err)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	canonical, err := workspaceidentity.CanonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
