//go:build darwin || linux || windows

package gatewayapp

import (
	"os"
	"testing"

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
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	canonical, err := workspaceidentity.CanonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
