//go:build !windows

package winexec

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Options struct {
	Timeout        time.Duration
	Stdin          []byte
	MaxOutputBytes int
	TraceComponent string
	TraceName      string
	DisplayArgs    []string
}

type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	TimedOut bool
	Duration time.Duration
}

func (r Result) CombinedOutput() []byte {
	out := append([]byte(nil), r.Stdout...)
	out = append(out, r.Stderr...)
	return out
}

func Run(ctx context.Context, name string, args []string, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Result{}, fmt.Errorf("winexec: command name is required")
	}
	runCtx := ctx
	cancel := func() {}
	if opts.Timeout > 0 {
		if _, ok := ctx.Deadline(); !ok {
			runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		}
	}
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(runCtx, name, args...)
	if len(opts.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(opts.Stdin)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	result := Result{
		Stdout:   tailBytes(stdout.Bytes(), opts.MaxOutputBytes),
		Stderr:   tailBytes(stderr.Bytes(), opts.MaxOutputBytes),
		ExitCode: -1,
		TimedOut: runCtx.Err() == context.DeadlineExceeded,
		Duration: time.Since(started),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if runCtx.Err() != nil {
		return result, runCtx.Err()
	}
	return result, err
}

func tailBytes(data []byte, max int) []byte {
	if max <= 0 || len(data) <= max {
		return append([]byte(nil), data...)
	}
	return append([]byte(nil), data[len(data)-max:]...)
}
