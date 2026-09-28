//go:build darwin

package seatbelt

import (
	"context"
	"encoding/json"
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

func TestSeatbeltHelperMustBeWiredAndInsideReadCeiling(t *testing.T) {
	workspace := t.TempDir()
	if _, err := New(sandbox.Config{CWD: workspace, HelperPath: "/usr/bin/true"}); err == nil || !strings.Contains(err.Error(), "call seatbelt.MaybeRunInternalHelper") {
		t.Fatalf("unwired helper = %v", err)
	}
	if _, err := New(sandbox.Config{CWD: workspace, HelperPath: "/usr/bin/true", ResourceLimits: &sandbox.ResourceLimits{ReadPaths: []string{workspace}}}); err == nil || !strings.Contains(err.Error(), "read ceiling") {
		t.Fatalf("outside helper = %v", err)
	}
}

func TestSeatbeltLauncherKeepsCommandEnvironmentAfterBoundary(t *testing.T) {
	workspace := t.TempDir()
	var captured *exec.Cmd
	var args []string
	runner := &seatbeltRunner{
		cfg:        sandbox.NormalizeConfig(sandbox.Config{CWD: workspace}),
		helperPath: "/usr/bin/true",
		env:        sandbox.NewEnvironmentSnapshot(nil, []string{"BASE=host"}),
		execCommand: func(ctx context.Context, _ string, argv ...string) *exec.Cmd {
			args = slices.Clone(argv)
			captured = exec.CommandContext(ctx, "/usr/bin/true")
			return captured
		},
		sessionManager: cmdsession.NewSessionManager(cmdsession.DefaultSessionManagerConfig()),
	}
	request := runnerruntime.Request{Dir: workspace, Command: "echo ok", EnvOverrides: map[string]string{"DYLD_INSERT_LIBRARIES": "/evil", "EMPTY": ""}}
	if _, err := runner.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	checkSeatbeltLauncher(t, captured, args)
	if _, err := runner.StartAsync(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	checkSeatbeltLauncher(t, captured, args)
	_ = runner.Close()
}

func TestSeatbeltLauncherClosesPayloadOnStartFailure(t *testing.T) {
	workspace := t.TempDir()
	var captured *exec.Cmd
	runner := &seatbeltRunner{
		cfg: sandbox.NormalizeConfig(sandbox.Config{CWD: workspace}), helperPath: "/usr/bin/true",
		env: sandbox.NewEnvironmentSnapshot(nil, []string{}),
		execCommand: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			captured = exec.CommandContext(ctx, "/nonexistent-seatbelt-launcher")
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

func TestSeatbeltHelperPayloadPrivate(t *testing.T) {
	runner := &seatbeltRunner{helperPath: "/usr/bin/true", execCommand: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/usr/bin/true")
	}}
	cmd, payload, err := runner.seatbeltCommand(context.Background(), "(version 1) (allow default)", "/bin/bash", []string{"-c", "echo ok"}, []string{"DYLD_INSERT_LIBRARIES=/private.so"})
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()
	data, err := io.ReadAll(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"DYLD_INSERT_LIBRARIES=/private.so"}) || strings.Contains(strings.Join(cmd.Args, " "), "/private.so") {
		t.Fatalf("payload/argv = %q / %q", got, cmd.Args)
	}
}

func checkSeatbeltLauncher(t *testing.T, cmd *exec.Cmd, args []string) {
	t.Helper()
	if cmd.Env == nil || len(cmd.Env) != 0 {
		t.Fatalf("launcher Env = %#v, want explicitly empty", cmd.Env)
	}
	joined := strings.Join(args, " ")
	if !slices.Contains(args, seatbeltHelperCommand) || !slices.Contains(args, "--env-fd") || strings.Contains(joined, "/evil") {
		t.Fatalf("launcher argv leaked target env: %q", args)
	}
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("private files = %d", len(cmd.ExtraFiles))
	}
	if _, err := cmd.ExtraFiles[0].Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("payload descriptor open: %v", err)
	}
}
