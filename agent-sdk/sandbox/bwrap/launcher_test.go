//go:build linux

package bwrap

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/cmdsession"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/runnerruntime"
)

func TestBwrapLauncherKeepsCommandEnvironmentAfterBoundary(t *testing.T) {
	workspace := t.TempDir()
	var captured *exec.Cmd
	var args []string
	runner := &bwrapRunner{
		cfg: sandbox.NormalizeConfig(sandbox.Config{CWD: workspace}),
		env: sandbox.NewEnvironmentSnapshot(nil, []string{"BASE=host"}),
		execCommand: func(ctx context.Context, _ string, argv ...string) *exec.Cmd {
			args = slices.Clone(argv)
			captured = exec.CommandContext(ctx, "/usr/bin/true")
			return captured
		},
		sessionManager: cmdsession.NewSessionManager(cmdsession.DefaultSessionManagerConfig()),
	}
	request := runnerruntime.Request{Dir: workspace, Command: "echo ok", EnvOverrides: map[string]string{"LD_PRELOAD": "/evil", "EMPTY": ""}}
	if _, err := runner.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	checkBwrapLauncher(t, captured, args)
	if _, err := runner.StartAsync(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	checkBwrapLauncher(t, captured, args)
	_ = runner.Close()
}

func TestBwrapLauncherClosesPayloadOnStartFailure(t *testing.T) {
	workspace := t.TempDir()
	var captured *exec.Cmd
	runner := &bwrapRunner{
		cfg: sandbox.NormalizeConfig(sandbox.Config{CWD: workspace}),
		env: sandbox.NewEnvironmentSnapshot(nil, []string{}),
		execCommand: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			captured = exec.CommandContext(ctx, "/nonexistent-bwrap-launcher")
			return captured
		},
		sessionManager: cmdsession.NewSessionManager(cmdsession.DefaultSessionManagerConfig()),
	}
	req := runnerruntime.Request{Dir: workspace, Command: "echo ok"}
	if _, err := runner.Run(context.Background(), req); err == nil {
		t.Fatal("Run unexpectedly started")
	}
	checkClosedPayload(t, captured)
	if _, err := runner.StartAsync(context.Background(), req); err == nil {
		t.Fatal("StartAsync unexpectedly started")
	}
	checkClosedPayload(t, captured)
	_ = runner.Close()
}

func checkClosedPayload(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if cmd == nil || len(cmd.ExtraFiles) != 1 {
		t.Fatalf("payload missing: %+v", cmd)
	}
	if _, err := cmd.ExtraFiles[0].Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("payload descriptor open after start error: %v", err)
	}
}

func TestBwrapTargetEnvOnlyOnPrivateFD(t *testing.T) {
	runner := &bwrapRunner{execCommand: exec.CommandContext}
	options := appendBwrapEnvironment(nil, []string{"LD_PRELOAD=/private.so", "EMPTY=", "VALUE=--\nwith spaces"})
	cmd, payload, err := runner.commandWithArgs(context.Background(), options, "/chosen/shell", []string{"-lc", "echo ok"})
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()
	data, err := io.ReadAll(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "/private.so") {
		t.Fatalf("launcher argv leaks target environment: %q", cmd.Args)
	}
	wantArgs := []string{"bwrap", "--args", "3", "--", "/chosen/shell", "-lc", "echo ok"}
	if !slices.Equal(cmd.Args, wantArgs) {
		t.Fatalf("launcher argv = %q, want %q", cmd.Args, wantArgs)
	}
	wantOptions := "--clearenv\x00--setenv\x00LD_PRELOAD\x00/private.so\x00--setenv\x00EMPTY\x00\x00--setenv\x00VALUE\x00--\nwith spaces\x00"
	if string(data) != wantOptions {
		t.Fatalf("private options = %q, want %q", data, wantOptions)
	}
}

func checkBwrapLauncher(t *testing.T, cmd *exec.Cmd, args []string) {
	t.Helper()
	if cmd.Env == nil || len(cmd.Env) != 0 {
		t.Fatalf("launcher Env = %#v", cmd.Env)
	}
	if !slices.Equal(args, []string{"--args", "3", "--", "/bin/bash", "-c", "echo ok"}) {
		t.Fatalf("launcher argv missing target command or leaked options: %q", args)
	}
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("launcher private fds = %d", len(cmd.ExtraFiles))
	}
	if _, err := cmd.ExtraFiles[0].Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("payload file not closed after start: %v", err)
	}
}
