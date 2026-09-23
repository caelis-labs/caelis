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

func TestExplicitReadCeilingNeverBecomesAmbientReadAccess(t *testing.T) {
	for _, roots := range [][]string{{}, {"/private/tmp/work", "/usr/bin"}} {
		cfg := sandbox.Config{ResourceLimits: &sandbox.ResourceLimits{ReadPaths: roots, WritePaths: []string{"/private/tmp/work"}, Network: sandbox.NetworkDisabled}}
		p := policy.Default(cfg, sandbox.Constraints{Permission: sandbox.PermissionFullAccess, Network: sandbox.NetworkEnabled})
		profile, err := buildSeatbeltProfile(p, "/private/tmp/work")
		if err != nil {
			t.Fatal(err)
		}
		for _, ambient := range []string{"(allow file-read*)\n", "(allow network*)", "(allow process*)", "(allow sysctl-read)", "system.sb", "com.apple.app-sandbox", "/Applications", "/Library/Preferences", "/opt/homebrew/lib", "/usr/local/lib", "/private/var/run/syslog", "/cores"} {
			if strings.Contains(profile, ambient) {
				t.Fatalf("mandatory read/network ceiling widened by %q: %s", ambient, profile)
			}
		}
		if !strings.Contains(profile, "(allow sysctl-read (sysctl-name \"hw.pagesize_compat\"))") {
			t.Fatal("explicit ceiling lacks the bounded Go allocator bootstrap query")
		}
		for _, root := range roots {
			if !strings.Contains(profile, "(allow file-read* file-map-executable (subpath "+sbplString(root)+"))") {
				t.Fatalf("explicit executable/read root missing: %s", root)
			}
		}
	}
	if _, err := buildSeatbeltProfile(policy.Policy{ResourceLimits: &sandbox.ResourceLimits{ReadPaths: []string{"/"}}}, "/work"); err == nil {
		t.Fatal("root read grant accepted")
	}
	ordinary, err := buildSeatbeltProfile(policy.Default(sandbox.Config{}, sandbox.Constraints{}), "/work")
	if err != nil {
		t.Fatal(err)
	}
	for _, retained := range []string{"(allow process*)", "(allow sysctl-read)", "system.sb"} {
		if !strings.Contains(ordinary, retained) {
			t.Fatalf("ordinary nil read ceiling lost backend default %q", retained)
		}
	}
}
