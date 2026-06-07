package shell

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

// executeHostCommand runs a command directly on the host using os/exec.
// This is the fallback when no sandbox backend is available.
func executeHostCommand(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	// Check command constraints before execution.
	if err := sandbox.CheckCommandConstraints(req.Dir, req.Constraints); err != nil {
		return sandbox.CommandResult{}, err
	}

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", req.Command)
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
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: 0,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return sandbox.CommandResult{}, err
		}
	}
	return result, nil
}
