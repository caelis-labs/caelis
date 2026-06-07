//go:build linux

package bwrap

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

func (b *Backend) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	if err := req.Validate(); err != nil {
		return sandbox.CommandResult{}, err
	}
	if err := sandbox.CheckCommandConstraints(req.Dir, req.Constraints); err != nil {
		return sandbox.CommandResult{}, err
	}
	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
		defer cancel()
	}
	args := buildArgs(req.Constraints)
	args = append(args, "--", "bash", "-lc", req.Command)
	cmd := exec.CommandContext(runCtx, "bwrap", args...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	if req.Env != nil {
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := sandbox.CommandResult{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, fmt.Errorf("bwrap run: %w", err)
		}
		return sandbox.CommandResult{}, fmt.Errorf("bwrap run: %w", err)
	}
	return result, nil
}

func (b *Backend) Status(context.Context) (sandbox.Status, error) {
	_, bwrapErr := exec.LookPath("bwrap")
	_, bashErr := exec.LookPath("bash")
	return sandbox.Status{Running: bwrapErr == nil && bashErr == nil}, nil
}

func buildArgs(c sandbox.Constraints) []string {
	args := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--new-session",
		"--die-with-parent",
		"--unshare-user",
		"--unshare-pid",
	}
	if !c.Network {
		args = append(args, "--unshare-net")
	}
	return args
}
