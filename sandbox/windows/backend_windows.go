//go:build windows

package windows

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
	cmd := exec.CommandContext(runCtx, "cmd", "/C", req.Command)
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
			return result, fmt.Errorf("windows run: %w", err)
		}
		return sandbox.CommandResult{}, fmt.Errorf("windows run: %w", err)
	}
	return result, nil
}

func (b *Backend) Status(context.Context) (sandbox.Status, error) {
	return sandbox.Status{Running: true}, nil
}
