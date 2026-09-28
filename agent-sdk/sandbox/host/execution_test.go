//go:build !windows

package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestExecutionConfigSameEnvironmentAcrossRunStartAndTTY(t *testing.T) {
	inherit := false
	cfg := Config{CWD: t.TempDir(), BaseEnv: []string{"SECRET=inherited", "EMPTY="}, Execution: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{Inherit: &inherit, Set: map[string]string{"CONFIG": "value", "DROP": "config"}}}}
	rt, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := sandbox.CommandRequest{Command: `printf 'SECRET:%s EMPTY:%s CONFIG:%s DROP:%s REQUEST:%s' "${SECRET-absent}" "${EMPTY-absent}" "${CONFIG-absent}" "${DROP-absent}" "${REQUEST-absent}"`, UnsetEnv: []string{"DROP"}, Env: map[string]string{"REQUEST": "value", "CONFIG": "request"}}
	want := "SECRET:absent EMPTY:absent CONFIG:request DROP:absent REQUEST:value"
	result, err := rt.Run(context.Background(), req)
	if err != nil || result.Stdout != want {
		t.Fatalf("Run = %+v, %v; want %q", result, err, want)
	}
	for _, tty := range []bool{false, true} {
		start := req
		start.TTY = tty
		session, err := rt.Start(context.Background(), start)
		if err != nil {
			t.Fatalf("Start(TTY=%v): %v", tty, err)
		}
		if _, err = session.Wait(context.Background(), 3*time.Second); err != nil {
			t.Fatal(err)
		}
		result, err = session.Result(context.Background())
		if err != nil || strings.TrimSpace(result.Stdout) != want {
			t.Fatalf("Start(TTY=%v) = %+v, %v; want %q", tty, result, err, want)
		}
	}
}

func TestExecutionConfigSnapshotsHostAndKeepsEmptyEnv(t *testing.T) {
	t.Setenv("SANDBOX_SNAPSHOT_TEST", "before")
	rt, err := New(Config{CWD: t.TempDir(), BaseEnv: []string{"SANDBOX_SNAPSHOT_TEST=initial"}})
	if err != nil {
		t.Fatal(err)
	}
	os.Setenv("SANDBOX_SNAPSHOT_TEST", "after")
	result, err := rt.Run(context.Background(), sandbox.CommandRequest{Command: `printf %s "$SANDBOX_SNAPSHOT_TEST"`})
	if err != nil || result.Stdout != "initial" {
		t.Fatalf("snapshot = %+v, %v", result, err)
	}
	inherit := false
	empty, err := New(Config{CWD: t.TempDir(), BaseEnv: []string{}, Execution: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{Inherit: &inherit}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err = empty.Run(context.Background(), sandbox.CommandRequest{Command: `env | grep '^SANDBOX_SNAPSHOT_TEST=' || true`})
	if err != nil || result.Stdout != "" {
		t.Fatalf("empty env = %+v, %v", result, err)
	}
	session, err := empty.Start(context.Background(), sandbox.CommandRequest{Command: `env | grep '^SANDBOX_SNAPSHOT_TEST=' || true`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Wait(context.Background(), 3*time.Second); err != nil {
		t.Fatal(err)
	}
	result, err = session.Result(context.Background())
	if err != nil || result.Stdout != "" {
		t.Fatalf("empty async env = %+v, %v", result, err)
	}
}

func TestExecutionCWDDefaultsAndRelativeDir(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ dir, want string }{{"", realWorkspace}, {"child", filepath.Join(realWorkspace, "child")}} {
		req := sandbox.CommandRequest{Command: "pwd -P", Dir: tc.dir}
		result, err := rt.Run(context.Background(), req)
		if err != nil || strings.TrimSpace(result.Stdout) != tc.want {
			t.Fatalf("Run(%q) = %+v, %v; want %s", tc.dir, result, err, tc.want)
		}
		session, err := rt.Start(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = session.Wait(context.Background(), 3*time.Second); err != nil {
			t.Fatal(err)
		}
		result, err = session.Result(context.Background())
		if err != nil || strings.TrimSpace(result.Stdout) != tc.want {
			t.Fatalf("Start(%q) = %+v, %v; want %s", tc.dir, result, err, tc.want)
		}
	}
}

func TestExecutionConfigRejectsInvalidRequestBeforeStart(t *testing.T) {
	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	req := sandbox.CommandRequest{Command: "echo unsafe", Env: map[string]string{"BAD=KEY": "value"}}
	if _, err := rt.Run(context.Background(), req); err == nil {
		t.Fatal("Run accepted invalid env")
	}
	if _, err := rt.Start(context.Background(), req); err == nil {
		t.Fatal("Start accepted invalid env")
	}
}

func TestExecutionConfigCustomShellAndLoginOptIn(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	if !strings.HasPrefix(zsh, "/") {
		t.Skip("zsh path is not absolute")
	}
	zdotdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(zdotdir, ".zshenv"), []byte(`export PATH="$PATH:/zshenv-marker"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zdotdir, ".zprofile"), []byte(`export PATH="/login-marker:$PATH"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, login := range []bool{false, true} {
		rt, err := New(Config{CWD: t.TempDir(), BaseEnv: []string{}, Execution: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{Set: map[string]string{"ZDOTDIR": zdotdir, "PATH": "/base-marker"}}, Shell: sandbox.ShellConfig{Path: zsh, Login: login}}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := rt.Run(context.Background(), sandbox.CommandRequest{Command: `printf '%s|%s' "$PATH" "$ZDOTDIR"`})
		if err != nil {
			t.Fatalf("zsh login=%v: %+v %v", login, result, err)
		}
		if !strings.HasSuffix(result.Stdout, "|"+zdotdir) || !strings.Contains(result.Stdout, "/zshenv-marker") {
			t.Fatalf("zsh config not loaded, login=%v: %q", login, result.Stdout)
		}
		if login && !strings.HasPrefix(result.Stdout, "/login-marker:") {
			t.Fatalf("login PATH missing profile prefix: %q", result.Stdout)
		}
		if !login && result.Stdout != "/base-marker:/zshenv-marker|"+zdotdir {
			t.Fatalf("non-login PATH loaded profile: %q", result.Stdout)
		}
	}
}
