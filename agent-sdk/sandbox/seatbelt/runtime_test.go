//go:build darwin

package seatbelt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/cmdsession"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/runnerruntime"
)

func TestSeatbeltWritableRootsDoNotBroadenMissingRootToParent(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	fakeHome := filepath.Join(root, "home")
	missingCache := filepath.Join(fakeHome, ".pnpm-store")

	roots, err := seatbeltWritableRoots(policy.Policy{
		Type:          policy.TypeWorkspaceWrite,
		WritableRoots: []string{workspace, missingCache},
	}, workspace)
	if err != nil {
		t.Fatalf("seatbeltWritableRoots() error = %v", err)
	}

	if containsString(roots, fakeHome) {
		t.Fatalf("Writable roots = %#v, must not grant parent of missing root %q", roots, missingCache)
	}
	if !containsString(roots, missingCache) {
		t.Fatalf("Writable roots = %#v, want exact missing root %q retained", roots, missingCache)
	}
	if _, err := os.Stat(missingCache); err != nil {
		t.Fatalf("Stat(missingCache) error = %v, want pre-created writable root", err)
	}
}

func TestStartAsyncWorkspaceWriteDeniesHomeWrite(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatalf("Mkdir(%s) error = %v", workspace, err)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Fatalf("UserHomeDir() = %q, %v", home, err)
	}
	target := filepath.Join(home, ".caelis-seatbelt-deny-"+time.Now().Format("20060102150405.000000000")+".txt")
	t.Cleanup(func() { _ = os.Remove(target) })

	runner := &seatbeltRunner{
		execCommand: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if name != "sandbox-exec" {
				t.Errorf("exec command = %q, want sandbox-exec", name)
			}
			profile := ""
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "-p" {
					profile = args[i+1]
					break
				}
			}
			if profile == "" {
				t.Errorf("sandbox-exec args = %#v, want inline profile", args)
			}
			if !strings.Contains(profile, workspace) {
				t.Errorf("seatbelt profile does not include workspace writable root %q", workspace)
			}
			if strings.Contains(profile, target) {
				t.Errorf("seatbelt profile unexpectedly grants home target; profile=%s", profile)
			}
			return exec.CommandContext(ctx, "sh", "-c", "printf 'Operation not permitted' >&2; exit 1")
		},
		goos:           runtime.GOOS,
		cfg:            sandbox.NormalizeConfig(sandbox.Config{CWD: workspace}),
		sessionManager: cmdsession.NewSessionManager(cmdsession.DefaultSessionManagerConfig()),
	}
	t.Cleanup(func() { _ = runner.Close() })

	sessionID, err := runner.StartAsync(context.Background(), runnerruntime.Request{
		Command: `printf denied > ` + shellQuote(target),
		Dir:     workspace,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendSeatbelt,
			Permission: sandbox.PermissionWorkspaceWrite,
			PathRules: []sandbox.PathRule{{
				Path:   workspace,
				Access: sandbox.PathAccessReadWrite,
			}},
		},
	})
	if err != nil {
		t.Fatalf("StartAsync() error = %v", err)
	}

	result, err := runner.WaitSession(context.Background(), sessionID, 5*time.Second)
	if err == nil && result.ExitCode == 0 {
		t.Fatalf("Result() succeeded, want sandbox denial; result=%#v", result)
	}
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	if !sandbox.IsSandboxPermissionDeniedText(result.Stderr + result.Stdout + errText) {
		t.Fatalf("Result() error/stdout/stderr = %v / %q / %q, want sandbox permission denial", err, result.Stdout, result.Stderr)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside target stat error = %v, want not exist", statErr)
	}
}

func TestWaitSessionTimeoutDoesNotConsumeExitForLaterResultWait(t *testing.T) {
	dir := t.TempDir()
	runner := &seatbeltRunner{
		execCommand: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
			command := ""
			if len(args) > 0 {
				command = args[len(args)-1]
			}
			return exec.CommandContext(ctx, "sh", "-c", command)
		},
		goos:           runtime.GOOS,
		cfg:            sandbox.NormalizeConfig(sandbox.Config{CWD: dir}),
		sessionManager: cmdsession.NewSessionManager(cmdsession.DefaultSessionManagerConfig()),
	}
	t.Cleanup(func() { _ = runner.Close() })

	sessionID, err := runner.StartAsync(context.Background(), runnerruntime.Request{
		Command: "printf ok",
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartAsync() error = %v", err)
	}

	result, err := runner.WaitSession(context.Background(), sessionID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitSession(timeout) error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "ok" {
		t.Fatalf("stdout = %q, want ok", result.Stdout)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err = runner.WaitSession(waitCtx, sessionID, 0)
	if err != nil {
		t.Fatalf("WaitSession(result) error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "ok" {
		t.Fatalf("second stdout = %q, want ok", result.Stdout)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
