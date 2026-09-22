//go:build linux

package bwrap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/procutil"
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

func TestBuildBwrapArgsReadCeilingRejectsAmbientRoot(t *testing.T) {
	workDir := t.TempDir()
	for _, roots := range [][]string{{}, {workDir, "/usr", "/bin"}} {
		p := policy.Default(sandbox.Config{ResourceLimits: &sandbox.ResourceLimits{ReadPaths: roots, WritePaths: []string{workDir}, Network: sandbox.NetworkDisabled}}, sandbox.Constraints{Permission: sandbox.PermissionFullAccess, Network: sandbox.NetworkEnabled})
		args, err := buildBwrapArgs(p, workDir)
		if err != nil {
			t.Fatal(err)
		}
		if hasBwrapPair(args, "--ro-bind", "/", "/") || !containsString(args, "--unshare-net") {
			t.Fatalf("mandatory ceiling widened: %v", args)
		}
		for _, root := range roots {
			if !hasBwrapPair(args, "--ro-bind", root, root) {
				t.Fatalf("missing explicit system/work mount: %s", root)
			}
		}
	}
	if _, err := buildBwrapArgs(policy.Policy{ResourceLimits: &sandbox.ResourceLimits{ReadPaths: []string{"/"}}}, workDir); err == nil {
		t.Fatal("ambient root read grant accepted")
	}
}

func TestBuildBwrapArgsDoesNotTreatCWDAsImplicitWritableRoot(t *testing.T) {
	workDir := t.TempDir()
	p := policy.Default(sandbox.Config{CWD: workDir}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
	})
	args, err := buildBwrapArgs(p, workDir)
	if err != nil {
		t.Fatalf("buildBwrapArgs() error = %v", err)
	}
	if hasBwrapPair(args, "--bind", workDir, workDir) {
		t.Fatalf("bwrap args = %#v, did not want implicit CWD write bind", args)
	}
}

func TestBuildBwrapArgsProtectsGitMetadataAtDepthOneOnly(t *testing.T) {
	workDir := t.TempDir()
	childGit := filepath.Join(workDir, "child", ".git")
	deepGit := filepath.Join(workDir, "container", "child", ".git")
	for _, path := range []string{childGit, deepGit} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}
	p := policy.Default(sandbox.Config{CWD: workDir, WritableRoots: []string{workDir}}, sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
	})
	args, err := buildBwrapArgs(p, workDir)
	if err != nil {
		t.Fatalf("buildBwrapArgs() error = %v", err)
	}
	if !hasBwrapPair(args, "--ro-bind", childGit, childGit) {
		t.Fatalf("bwrap args = %#v, want direct child Git metadata read-only", args)
	}
	if hasBwrapPair(args, "--ro-bind", deepGit, deepGit) {
		t.Fatalf("bwrap args = %#v, did not want depth-two Git metadata", args)
	}
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
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("expected detail to contain %q, got %q", want, detail)
		}
	}
	if strings.Contains(detail, "landlock") {
		t.Fatalf("detail recommends retired product landlock backend: %q", detail)
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

func TestBwrapProbeUsesFixedExecutableWithoutLoginShell(t *testing.T) {
	var (
		gotArgs     []string
		gotDeadline bool
		started     *exec.Cmd
	)
	runner := &bwrapRunner{
		execCommand: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
			gotArgs = append([]string(nil), args...)
			_, gotDeadline = ctx.Deadline()
			cmd := exec.CommandContext(ctx, bwrapProbeExecutable)
			started = cmd
			return cmd
		},
		lookPath: func(name string) (string, error) {
			switch name {
			case "bwrap", "bash":
				return "/usr/bin/" + name, nil
			default:
				return "", os.ErrNotExist
			}
		},
		goos: "linux",
		cfg:  sandbox.NormalizeConfig(sandbox.Config{}),
	}
	if err := runner.probe(context.Background()); err != nil {
		t.Fatalf("probe() error = %v", err)
	}
	if !gotDeadline {
		t.Fatal("probe context has no deadline")
	}
	if len(gotArgs) < 2 || gotArgs[len(gotArgs)-2] != "--" || gotArgs[len(gotArgs)-1] != bwrapProbeExecutable {
		t.Fatalf("probe args = %#v, want -- %s", gotArgs, bwrapProbeExecutable)
	}
	for _, arg := range gotArgs {
		switch arg {
		case "-lc", "-l", "bash", "bwrap-probe":
			t.Fatalf("probe args = %#v, did not want login shell or profile execution", gotArgs)
		}
	}
	if started == nil {
		t.Fatal("probe did not start a command")
	}
	if started.WaitDelay != bwrapProbeWaitDelay {
		t.Fatalf("WaitDelay = %v, want %v", started.WaitDelay, bwrapProbeWaitDelay)
	}
	if _, ok := started.Stderr.(*procutil.BoundedWriter); !ok {
		t.Fatalf("Stderr type = %T, want bounded writer", started.Stderr)
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
