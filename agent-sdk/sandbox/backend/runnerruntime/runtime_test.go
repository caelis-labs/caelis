package runnerruntime

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/cmdsession"
)

type waitResultTestRunner struct {
	session           *cmdsession.AsyncSession
	result            sandbox.CommandResult
	err               error
	waitSessionResult *sandbox.CommandResult
	waitSessionErr    error
	stdout            []byte
	stderr            []byte
	observation       *cmdsession.OutputObservation
}

func (r *waitResultTestRunner) Run(context.Context, Request) (sandbox.CommandResult, error) {
	return r.result, r.err
}

func (r *waitResultTestRunner) StartAsync(context.Context, Request) (string, error) {
	return r.session.ID, nil
}

func (r *waitResultTestRunner) WriteInput(string, []byte) error { return nil }

func (r *waitResultTestRunner) ReadOutput(string, int64, int64) ([]byte, []byte, int64, int64, error) {
	return r.stdout, r.stderr, int64(len(r.stdout)), int64(len(r.stderr)), nil
}

func (r *waitResultTestRunner) AwaitOutput(ctx context.Context, _ string, cursor sandbox.OutputCursor) (cmdsession.OutputObservation, error) {
	if r.observation != nil {
		return *r.observation, nil
	}
	return r.session.AwaitOutput(ctx, cursor)
}

func (r *waitResultTestRunner) GetSessionStatus(string) (cmdsession.SessionStatus, error) {
	return r.session.Status(), nil
}

func (r *waitResultTestRunner) WaitSession(ctx context.Context, _ string, timeout time.Duration) (sandbox.CommandResult, error) {
	if r.waitSessionResult != nil || r.waitSessionErr != nil {
		if r.session != nil {
			if timeout > 0 {
				if _, err := r.session.WaitWithTimeout(timeout); err != nil {
					return sandbox.CommandResult{}, err
				}
			} else if _, err := r.session.Wait(ctx); err != nil {
				return sandbox.CommandResult{}, err
			}
		}
		if r.waitSessionResult != nil {
			return *r.waitSessionResult, r.waitSessionErr
		}
		return sandbox.CommandResult{}, r.waitSessionErr
	}
	if timeout > 0 {
		if _, err := r.session.WaitWithTimeout(timeout); err != nil {
			return sandbox.CommandResult{}, err
		}
	} else if _, err := r.session.Wait(ctx); err != nil {
		return sandbox.CommandResult{}, err
	}
	return r.session.GetResult()
}

func (r *waitResultTestRunner) TerminateSession(string) error {
	return r.session.Terminate()
}

func (r *waitResultTestRunner) Close() error { return nil }

func TestSessionWaitTreatsPlainExitReasonAsCommandStatus(t *testing.T) {
	command := "exit 1"
	if runtime.GOOS == "windows" {
		command = "exit /b 1"
	}
	async := cmdsession.NewAsyncSession(cmdsession.AsyncSessionConfig{
		Command: command,
		BuildCommand: func(ctx context.Context, _ cmdsession.AsyncSessionConfig) (*exec.Cmd, error) {
			if runtime.GOOS == "windows" {
				return exec.CommandContext(ctx, "cmd.exe", "/d", "/c", "exit /b 1"), nil
			}
			return exec.CommandContext(ctx, "sh", "-c", "exit 1"), nil
		},
	})
	if err := async.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = async.Close() })

	waitResult := sandbox.CommandResult{ExitCode: 1}
	sess := &session{
		backend: sandbox.BackendHost,
		runner: &waitResultTestRunner{
			session:           async,
			waitSessionResult: &waitResult,
			waitSessionErr:    fmt.Errorf("process exited with code 1"),
		},
		sessionID: async.ID,
	}

	status, err := sess.Wait(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Running || status.ExitCode != 1 {
		t.Fatalf("Wait() status = %+v, want exited command with code 1", status)
	}
}

func TestSessionWaitDoesNotConsumeExitForResult(t *testing.T) {
	command := "printf 'ok\\n'"
	if runtime.GOOS == "windows" {
		command = "Write-Output ok"
	}
	async := cmdsession.NewAsyncSession(cmdsession.AsyncSessionConfig{
		Command: command,
		BuildCommand: func(ctx context.Context, _ cmdsession.AsyncSessionConfig) (*exec.Cmd, error) {
			if runtime.GOOS == "windows" {
				return exec.CommandContext(ctx, "cmd.exe", "/d", "/c", "echo ok"), nil
			}
			return exec.CommandContext(ctx, "sh", "-c", "printf 'ok\\n'"), nil
		},
	})
	if err := async.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = async.Close() })

	sess := &session{
		backend:   sandbox.BackendHost,
		runner:    &waitResultTestRunner{session: async},
		sessionID: async.ID,
	}

	status, err := sess.Wait(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Running {
		t.Fatalf("Wait() status = %+v, want exited session", status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := sess.Result(ctx)
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "ok" {
		t.Fatalf("Result().Stdout = %q, want ok", result.Stdout)
	}
}

func TestRuntimeRunPreservesSandboxCommandPermissionDiagnostics(t *testing.T) {
	t.Parallel()

	deniedPath := "/sandbox-denied-home/.gitconfig"
	raw := "错误：无法锁定配置文件 " + deniedPath + ": 只读文件系统"
	rt := New(Config{
		Backend: sandbox.BackendBwrap,
		Runner: &waitResultTestRunner{
			result: sandbox.CommandResult{Stderr: raw, ExitCode: 1},
			err:    fmt.Errorf("tool: bwrap sandbox command failed: stderr=%s", raw),
		},
	})
	result, err := rt.Run(context.Background(), sandbox.CommandRequest{Command: "git config --global user.name test"})
	if err == nil {
		t.Fatal("Run() error = nil, want command error")
	}
	if !strings.Contains(result.Stderr, deniedPath) || !strings.Contains(err.Error(), deniedPath) {
		t.Fatalf("Run() lost command diagnostics: stderr=%q err=%q", result.Stderr, err.Error())
	}
	if result.Stderr != raw {
		t.Fatalf("Run().Stderr = %q, want raw command stderr %q", result.Stderr, raw)
	}
}

func TestSessionReadOutputPreservesSandboxCommandPermissionDiagnostics(t *testing.T) {
	t.Parallel()

	deniedPath := "/sandbox-denied-home/.gitconfig"
	raw := "fatal: cannot lock config file " + deniedPath + ": Read-only file system"
	sess := &session{
		backend: sandbox.BackendBwrap,
		runner:  &waitResultTestRunner{stderr: []byte(raw)},
	}
	_, stderr, _, _, err := sess.ReadOutput(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("ReadOutput() error = %v", err)
	}
	if !strings.Contains(string(stderr), deniedPath) {
		t.Fatalf("ReadOutput() lost command diagnostics: %q", string(stderr))
	}
	if strings.TrimSpace(string(stderr)) != raw {
		t.Fatalf("ReadOutput() stderr = %q, want raw command stderr %q", string(stderr), raw)
	}
}

func TestSessionAwaitOutputTranslatesBackendStatus(t *testing.T) {
	t.Parallel()

	now := time.Now()
	runner := &waitResultTestRunner{
		observation: &cmdsession.OutputObservation{
			Cursor: sandbox.OutputCursor{Stdout: 7, Stderr: 3},
			Status: cmdsession.SessionStatus{
				State:        cmdsession.SessionStateRunning,
				StartTime:    now,
				LastActivity: now,
			},
		},
	}
	sess := &session{
		backend:   sandbox.BackendBwrap,
		runner:    runner,
		sessionID: "session-1",
	}
	observation, err := sess.AwaitOutput(context.Background(), sandbox.OutputCursor{})
	if err != nil {
		t.Fatalf("AwaitOutput() error = %v", err)
	}
	if observation.Cursor != runner.observation.Cursor {
		t.Fatalf("Cursor = %+v, want %+v", observation.Cursor, runner.observation.Cursor)
	}
	if observation.Status.Backend != sandbox.BackendBwrap ||
		observation.Status.SessionID != "session-1" ||
		observation.Status.Terminal.TerminalID != "session-1" ||
		!observation.Status.Running {
		t.Fatalf("Status = %+v, want translated bwrap session", observation.Status)
	}
}
