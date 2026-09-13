//go:build windows

package windows

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
)

func TestWindowsResourceLimitsConstrainCommandAndFileSystem(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	limits := &sandbox.ResourceLimits{WritePaths: []string{root}, Network: sandbox.NetworkDisabled}
	rt, err := sandbox.New(sandbox.Config{
		RequestedBackend: sandbox.BackendWindows,
		CWD:              outside, StateDir: t.TempDir(), HostAuthorityDir: t.TempDir(),
		WritableRoots: []string{outside}, ResourceLimits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rt.Close(); err != nil {
			t.Error(err)
		}
	})
	// Mutating the caller's config and requesting full access must not change
	// either execution path's embedding-owned ceiling.
	limits.WritePaths[0] = outside
	constraints := sandbox.Constraints{
		Route: sandbox.RouteHost, Permission: sandbox.PermissionFullAccess,
		PathRules: []sandbox.PathRule{{Path: outside, Access: sandbox.PathAccessReadWrite}},
		Network:   sandbox.NetworkDisabled,
	}
	fs := rt.FileSystemFor(constraints)
	if err := fs.WriteFile(filepath.Join(root, "allowed.txt"), []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(outside, "denied.txt")
	if err := fs.WriteFile(denied, []byte("escape"), 0o600); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("outside filesystem write=%v", err)
	}
	command := "$ErrorActionPreference='Stop'; Set-Content -LiteralPath '" + escapePowerShellSingleQuote(denied) + "' -Value escape"
	result, err := rt.Run(t.Context(), sandbox.CommandRequest{Command: command, Dir: outside, Timeout: 10 * time.Second, Constraints: constraints})
	if !sandbox.IsCommandExit(err) || result.ExitCode == 0 || result.Route != sandbox.RouteSandbox {
		t.Fatalf("outside command write=%+v error=%v", result, err)
	}
	if _, err := os.Stat(denied); !os.IsNotExist(err) {
		t.Fatalf("outside target created: %v", err)
	}
	result, err = rt.Run(t.Context(), sandbox.CommandRequest{
		Command: "$ErrorActionPreference='Stop'; Set-Content -LiteralPath \"$env:TEMP/allowed.txt\" -Value cache; Write-Output $env:TEMP",
		Dir:     outside, Timeout: 10 * time.Second, Constraints: constraints,
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("temporary command write=%+v error=%v", result, err)
	}
	if !pathutil.IsUnder(result.Stdout, root) {
		t.Fatalf("process TEMP escaped resource limits: %q", result.Stdout)
	}
	if limits.Network != sandbox.NetworkDisabled || limits.WritePaths[0] != outside {
		t.Fatal("caller config was mutated")
	}
}

func TestWindowsResourceLimitsRejectEnvironmentJunctionEscape(t *testing.T) {
	for _, nested := range []string{"", "python-site"} {
		t.Run(nested, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			envRoot := filepath.Join(root, ".sandbox-env")
			link := envRoot
			if nested != "" {
				if err := os.Mkdir(envRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(envRoot, nested)
			}
			if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", filepath.FromSlash(link), filepath.FromSlash(outside)).CombinedOutput(); err != nil {
				t.Fatalf("create temporary junction: %v (%s)", err, output)
			}
			if reparse, err := isReparsePoint(link); err != nil || !reparse {
				t.Fatalf("temporary junction is not a reparse point: %v %v", reparse, err)
			}
			rt := &runtime{cfg: Config{CWD: root, ResourceLimits: &sandbox.ResourceLimits{WritePaths: []string{root}}}}
			if _, err := rt.prepareSandboxEnvRoot(root, true); err == nil || (!strings.Contains(err.Error(), "escapes explicit writable roots") && !strings.Contains(err.Error(), "reparse point")) {
				t.Fatalf("environment junction was accepted: %v", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("environment preparation wrote outside its roots: %v %v", entries, err)
			}
		})
	}
}
