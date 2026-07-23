//go:build linux

package bwrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
)

func TestBuildBwrapArgsPreservesHostReadsAndManagedMounts(t *testing.T) {
	workDir := t.TempDir()
	p := policy.Default(sandbox.Config{
		CWD:           workDir,
		WritableRoots: []string{workDir},
	}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
	})

	args, err := buildBwrapArgs(p, workDir)
	if err != nil {
		t.Fatalf("buildBwrapArgs() error = %v", err)
	}
	if !hasBwrapPair(args, "--ro-bind", "/", "/") {
		t.Fatalf("bwrap args = %#v, want host root mounted read-only", args)
	}
	if containsString(args, "--tmpfs") {
		t.Fatalf("bwrap args = %#v, did not want scoped temporary root", args)
	}
	assertBwrapManagedMountsNotReadOnly(t, args)
}

func TestBwrapWritableRootsSkipMissingRootWithoutCreatingIt(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "workspace")
	fakeHome := filepath.Join(root, "home")
	missingCache := filepath.Join(fakeHome, ".pnpm-store")
	for _, dir := range []string{workDir, fakeHome} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("Mkdir(%q) error = %v", dir, err)
		}
	}

	roots, err := bwrapWritableRoots(policy.Policy{
		Type:          policy.TypeWorkspaceWrite,
		WritableRoots: []string{workDir, missingCache},
	}, workDir)
	if err != nil {
		t.Fatalf("bwrapWritableRoots() error = %v", err)
	}

	if containsString(roots, fakeHome) {
		t.Fatalf("Writable roots = %#v, must not grant parent of missing root %q", roots, missingCache)
	}
	if !containsString(roots, workDir) {
		t.Fatalf("Writable roots = %#v, want existing workspace %q", roots, workDir)
	}
	if containsString(roots, missingCache) {
		t.Fatalf("Writable roots = %#v, did not want missing root %q", roots, missingCache)
	}
	if _, err := os.Stat(missingCache); !os.IsNotExist(err) {
		t.Fatalf("Stat(missingCache) error = %v, want not created", err)
	}
}

func assertBwrapManagedMountsNotReadOnly(t *testing.T, args []string) {
	t.Helper()
	if !hasBwrapPair(args, "--dev", "/dev", "") {
		t.Fatalf("bwrap args = %#v, want managed /dev mount", args)
	}
	if !hasBwrapPair(args, "--proc", "/proc", "") {
		t.Fatalf("bwrap args = %#v, want managed /proc mount", args)
	}
	for _, path := range []string{"/dev", "/dev/null", "/proc", "/proc/self"} {
		if hasBwrapPair(args, "--ro-bind", path, path) {
			t.Fatalf("bwrap args = %#v, did not expect read-only bind over managed mount %s", args, path)
		}
	}
}

func TestBwrapProbeFailureDetailDetectsAppArmorUserNSRestriction(t *testing.T) {
	detail := bwrapProbeFailureDetail(
		"/usr/bin/bwrap",
		"bwrap: Creating new namespace failed: Permission denied",
		func(string) (os.FileInfo, error) {
			return fakeFileInfo{mode: 0o755}, nil
		},
		func(name string) ([]byte, error) {
			switch name {
			case "/proc/sys/kernel/apparmor_restrict_unprivileged_userns":
				return []byte("1\n"), nil
			case "/sys/kernel/security/apparmor/profiles":
				return []byte("other-profile (enforce)\n"), nil
			default:
				return nil, os.ErrNotExist
			}
		},
	)

	for _, want := range []string{
		"kernel.apparmor_restrict_unprivileged_userns=1",
		"AppArmor bwrap profile not detected",
		"/etc/apparmor.d/bwrap",
		"sudo apparmor_parser -r /etc/apparmor.d/bwrap",
		"sandbox.requested_type=landlock",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("expected detail to contain %q, got %q", want, detail)
		}
	}
}

func TestBwrapProbeFailureDetailAcceptsLoadedAppArmorProfile(t *testing.T) {
	detail := bwrapProbeFailureDetail(
		"/usr/bin/bwrap",
		"bwrap: Creating new namespace failed: Permission denied",
		func(string) (os.FileInfo, error) {
			return fakeFileInfo{mode: 0o755}, nil
		},
		func(name string) ([]byte, error) {
			switch name {
			case "/proc/sys/kernel/apparmor_restrict_unprivileged_userns":
				return []byte("1\n"), nil
			case "/sys/kernel/security/apparmor/profiles":
				return []byte("bwrap (unconfined)\n"), nil
			default:
				return nil, os.ErrNotExist
			}
		},
	)

	if !strings.Contains(detail, "kernel.apparmor_restrict_unprivileged_userns=1") {
		t.Fatalf("expected AppArmor sysctl in detail, got %q", detail)
	}
	if strings.Contains(detail, "AppArmor bwrap profile not detected") {
		t.Fatalf("did not expect missing-profile hint when profile is loaded: %q", detail)
	}
}

type fakeFileInfo struct {
	mode os.FileMode
}

func (f fakeFileInfo) Name() string       { return "bwrap" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func hasBwrapPair(args []string, flag string, left string, right string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] != flag || i+1 >= len(args) || args[i+1] != left {
			continue
		}
		if right == "" {
			return true
		}
		if i+2 < len(args) && args[i+2] == right {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
