//go:build !windows

package windows

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestLifecycleTargetForWindowsNoopsOnUnsupportedPlatform(t *testing.T) {
	target, err := sandbox.LifecycleTargetFor(sandbox.Config{RequestedBackend: sandbox.BackendWindows}, nil)
	if err != nil {
		t.Fatalf("LifecycleTargetFor(windows) error = %v", err)
	}
	if !target.NoOp || target.Runtime != nil {
		t.Fatalf("target = %#v, want unsupported platform no-op", target)
	}
}
