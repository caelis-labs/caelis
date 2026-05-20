//go:build windows

package winexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/job"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/runnertrace"
)

const (
	defaultMaxOutputBytes = 64 * 1024
	readerDrainTimeout    = 2 * time.Second
	processKillWait       = 5 * time.Second
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
	if len(r.Stderr) == 0 {
		return append([]byte(nil), r.Stdout...)
	}
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
	maxOutput := opts.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutputBytes
	}
	traceComponent := strings.TrimSpace(opts.TraceComponent)
	if traceComponent == "" {
		traceComponent = "windows-command"
	}
	traceName := strings.TrimSpace(opts.TraceName)
	if traceName == "" {
		traceName = "command"
	}
	displayArgs := append([]string(nil), opts.DisplayArgs...)
	if len(displayArgs) == 0 {
		displayArgs = append([]string(nil), args...)
	}

	runCtx := ctx
	cancel := func() {}
	if opts.Timeout > 0 {
		if _, ok := ctx.Deadline(); !ok {
			runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		}
	}
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return Result{ExitCode: -1, TimedOut: errors.Is(err, context.DeadlineExceeded)}, err
	}

	started := time.Now()
	runnertrace.Printf(traceComponent, "%s start name=%q args=%q timeout=%s", traceName, name, strings.Join(displayArgs, " "), opts.Timeout)
	var result Result
	var runErr error
	defer func() {
		result.Duration = time.Since(started)
		runnertrace.Printf(traceComponent, "%s done name=%q args=%q duration=%s exit=%d timed_out=%t err=%v stdout_bytes=%d stderr_bytes=%d",
			traceName,
			name,
			strings.Join(displayArgs, " "),
			result.Duration.Round(time.Millisecond),
			result.ExitCode,
			result.TimedOut,
			runErr,
			len(result.Stdout),
			len(result.Stderr),
		)
	}()

	cmd := exec.Command(name, args...)
	if len(opts.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(opts.Stdin)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		runErr = err
		return result, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		runErr = err
		return result, err
	}
	stdout := newTailBuffer(maxOutput)
	stderr := newTailBuffer(maxOutput)
	var readers sync.WaitGroup
	readPipe := func(dst *tailBuffer, pipe io.ReadCloser) {
		defer readers.Done()
		defer pipe.Close()
		_, _ = io.Copy(dst, pipe)
	}
	readers.Add(2)
	go readPipe(stdout, stdoutPipe)
	go readPipe(stderr, stderrPipe)

	if err := cmd.Start(); err != nil {
		closePipe(stdoutPipe)
		closePipe(stderrPipe)
		waitReaders(&readers, readerDrainTimeout)
		runErr = err
		return result, err
	}

	jobObject, jobErr := job.New()
	if jobErr == nil {
		if err := jobObject.AssignPID(cmd.Process.Pid); err != nil {
			runnertrace.Printf(traceComponent, "%s assign_job pid=%d err=%v", traceName, cmd.Process.Pid, err)
		}
	} else {
		runnertrace.Printf(traceComponent, "%s create_job err=%v", traceName, jobErr)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		if jobObject != nil {
			_ = jobObject.Close()
		}
		if !waitReaders(&readers, readerDrainTimeout) {
			closePipe(stdoutPipe)
			closePipe(stderrPipe)
			waitReaders(&readers, time.Second)
		}
		result.Stdout = stdout.Bytes()
		result.Stderr = stderr.Bytes()
		result.ExitCode = exitCode(cmd)
		runErr = err
		return result, err
	case <-runCtx.Done():
		result.TimedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		if jobObject != nil {
			_ = jobObject.Terminate(1)
			_ = jobObject.Close()
		}
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-waitCh:
		case <-time.After(processKillWait):
			runnertrace.Printf(traceComponent, "%s wait_after_kill timed_out pid=%d", traceName, cmd.Process.Pid)
		}
		closePipe(stdoutPipe)
		closePipe(stderrPipe)
		waitReaders(&readers, time.Second)
		result.Stdout = stdout.Bytes()
		result.Stderr = stderr.Bytes()
		result.ExitCode = exitCode(cmd)
		runErr = runCtx.Err()
		return result, runCtx.Err()
	}
}

func closePipe(pipe io.Closer) {
	if pipe != nil {
		_ = pipe.Close()
	}
}

func waitReaders(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	if timeout <= 0 {
		<-done
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func exitCode(cmd *exec.Cmd) int {
	if cmd == nil || cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer {
	if max <= 0 {
		max = defaultMaxOutputBytes
	}
	return &tailBuffer{max: max}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return len(p), nil
	}
	keep := b.max - len(p)
	if len(b.buf) > keep {
		copy(b.buf, b.buf[len(b.buf)-keep:])
		b.buf = b.buf[:keep]
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *tailBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}
