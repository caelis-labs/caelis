//go:build darwin

package seatbelt

import (
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
	"strings"
	"testing"
)

func TestExplicitWriteRootsNeverAddCacheOrWorkspaceAndPreserveNetwork(t *testing.T) {
	for _, network := range []sandbox.Network{sandbox.NetworkEnabled, sandbox.NetworkDisabled} {
		cfg := sandbox.Config{WritableRoots: []string{"/workspace"}, ResourceLimits: &sandbox.ResourceLimits{WritePaths: []string{"/private/tmp/guardian"}, Network: network}}
		p := policy.Default(cfg, sandbox.Constraints{Permission: sandbox.PermissionFullAccess, Network: sandbox.NetworkEnabled, PathRules: []sandbox.PathRule{{Path: "/escape", Access: sandbox.PathAccessReadWrite}}})
		roots, err := seatbeltWritableRoots(p, "/workspace")
		if err != nil || len(roots) != 1 || roots[0] != "/private/tmp/guardian" {
			t.Fatalf("roots=%v err=%v", roots, err)
		}
		profile, err := buildSeatbeltProfile(p, "/workspace")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(profile, "/workspace") || strings.Contains(profile, "/escape") || strings.Contains(profile, ".cache") {
			t.Fatal("implicit write grant")
		}
		if strings.Contains(profile, "(allow network*)") != (network == sandbox.NetworkEnabled) {
			t.Fatal("network changed")
		}
	}
}
