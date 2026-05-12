package stdio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Config struct {
	Command         string
	Args            []string
	Env             map[string]string
	WorkDir         string
	ShutdownTimeout time.Duration
}

type Process struct {
	Cmd     *exec.Cmd
	Stdin   io.WriteCloser
	Stdout  io.ReadCloser
	Stderr  io.ReadCloser
	timeout time.Duration
}

func Start(ctx context.Context, cfg Config) (*Process, error) {
	if ctx == nil {
		return nil, fmt.Errorf("acp/transport/stdio: context is required")
	}
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		return nil, fmt.Errorf("acp/transport/stdio: command is required")
	}
	workDir := strings.TrimSpace(cfg.WorkDir)
	if workDir != "" && !filepath.IsAbs(workDir) {
		abs, err := filepath.Abs(workDir)
		if err != nil {
			return nil, err
		}
		workDir = abs
	}
	cmd := exec.CommandContext(ctx, command, cfg.Args...)
	cmd.Dir = workDir
	cmd.Env = mergedEnv(cfg.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	timeout := cfg.ShutdownTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Process{
		Cmd:     cmd,
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  stderr,
		timeout: timeout,
	}, nil
}

func (p *Process) Close(ctx context.Context) error {
	if p == nil || p.Cmd == nil {
		return nil
	}
	if p.Stdin != nil {
		_ = p.Stdin.Close()
	}
	done := make(chan error, 1)
	go func() {
		done <- p.Cmd.Wait()
	}()
	timeout := p.timeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	select {
	case err := <-done:
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	case <-ctx.Done():
		_ = killProcess(p.Cmd.Process)
		<-done
		return ctx.Err()
	case <-time.After(timeout):
		_ = killProcess(p.Cmd.Process)
		<-done
		return nil
	}
}

func mergedEnv(overrides map[string]string) []string {
	base := os.Environ()
	if len(overrides) == 0 {
		return base
	}
	values := map[string]string{}
	for _, item := range base {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			values[name] = value
		}
	}
	for name, value := range overrides {
		values[name] = value
	}
	out := make([]string, 0, len(values))
	for name, value := range values {
		out = append(out, name+"="+value)
	}
	return out
}

func killProcess(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		return proc.Kill()
	}
	return proc.Kill()
}
