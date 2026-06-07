//go:build darwin

package seatbelt

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
	profile := buildProfile(req.Constraints)
	cmd := exec.CommandContext(runCtx, "sandbox-exec", "-p", profile, "/bin/sh", "-c", req.Command)
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
			return result, fmt.Errorf("seatbelt run: %w", err)
		}
		return sandbox.CommandResult{}, fmt.Errorf("seatbelt run: %w", err)
	}
	return result, nil
}

func (b *Backend) Status(context.Context) (sandbox.Status, error) {
	_, err := exec.LookPath("sandbox-exec")
	return sandbox.Status{Running: err == nil}, nil
}

func buildProfile(c sandbox.Constraints) string {
	networkRule := "(allow network*)"
	if !c.Network {
		networkRule = "(deny network*)"
	}
	return "(version 1) (allow default) " + networkRule
}
