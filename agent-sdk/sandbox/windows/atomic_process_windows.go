//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/jobprocess"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/win32"
)

type atomicJobProcess interface {
	Input() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	WaitRoot() (int, error)
	Terminate() error
	DrainAndClose(context.Context) error
	NeedsDrainRetry() bool
	ClosePipes()
}

type atomicJobWaitResult struct {
	code int
	err  error
}

func runAtomicJobProcess(ctx context.Context, cmd *exec.Cmd, token win32.Token, stdin []byte, stdout, stderr io.Writer, releaseUse func()) (int, error, bool) {
	process, err := jobprocess.Start(jobprocess.Config{Token: uintptr(token), Application: cmd.Path, Args: append([]string(nil), cmd.Args[1:]...), Dir: cmd.Dir, Env: append([]string(nil), cmd.Env...), Stdin: len(stdin) > 0})
	_ = token.Close()
	if err != nil {
		return -1, err, false
	}
	return runStartedAtomicJobProcess(ctx, process, stdin, stdout, stderr, releaseUse, windowsTerminateDrain)
}

func runStartedAtomicJobProcess(
	ctx context.Context,
	process atomicJobProcess,
	stdin []byte,
	stdout io.Writer,
	stderr io.Writer,
	releaseUse func(),
	rootWaitTimeout time.Duration,
) (int, error, bool) {
	var closePipesOnce sync.Once
	closePipes := func() { closePipesOnce.Do(process.ClosePipes) }
	defer closePipes()

	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() { defer copyWG.Done(); _, _ = io.Copy(stdout, process.Stdout()) }()
	go func() { defer copyWG.Done(); _, _ = io.Copy(stderr, process.Stderr()) }()
	// WaitRoot exclusively owns the process handle until Windows completes the
	// pending wait. A caller timeout may close pipes and drain the Job, but it
	// must leave this waiter to close the process handle after it is signaled.
	waitCh := make(chan atomicJobWaitResult, 1)
	go func() {
		code, waitErr := process.WaitRoot()
		waitCh <- atomicJobWaitResult{code: code, err: waitErr}
	}()
	handOffDrain := func() {
		go retryAtomicJobProcessDrain(process, releaseUse)
		closePipes()
		copyWG.Wait()
	}

	if input := process.Input(); input != nil {
		_, writeErr := input.Write(stdin)
		closeErr := input.Close()
		if writeErr != nil || closeErr != nil {
			terminateErr := process.Terminate()
			waited, rootWaitErr, timedOut := waitForAtomicJobRoot(waitCh, rootWaitTimeout)
			if timedOut {
				handOffDrain()
				return waited.code, errors.Join(writeErr, closeErr, terminateErr, rootWaitErr), true
			}
			drainErr, keepUse := drainAtomicJobProcess(process, releaseUse)
			copyWG.Wait()
			return waited.code, errors.Join(writeErr, closeErr, terminateErr, waited.err, drainErr), keepUse
		}
	}

	var waited atomicJobWaitResult
	var contextErr error
	var terminateErr error
	select {
	case waited = <-waitCh:
	case <-ctx.Done():
		contextErr = ctx.Err()
		terminateErr = process.Terminate()
		var rootWaitErr error
		var timedOut bool
		waited, rootWaitErr, timedOut = waitForAtomicJobRoot(waitCh, rootWaitTimeout)
		if timedOut {
			handOffDrain()
			return waited.code, errors.Join(contextErr, terminateErr, rootWaitErr), true
		}
	}
	drainErr, keepUse := drainAtomicJobProcess(process, releaseUse)
	copyWG.Wait()
	return waited.code, errors.Join(contextErr, terminateErr, waited.err, drainErr), keepUse
}

func waitForAtomicJobRoot(waitCh <-chan atomicJobWaitResult, timeout time.Duration) (atomicJobWaitResult, error, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case waited := <-waitCh:
		return waited, nil, false
	case <-timer.C:
		return atomicJobWaitResult{code: -1}, fmt.Errorf("impl/sandbox/windows: root process did not exit within %s after termination", timeout), true
	}
}

func drainAtomicJobProcess(process atomicJobProcess, releaseUse func()) (error, bool) {
	drainCtx, cancel := context.WithTimeout(context.Background(), windowsPreflightTimeout)
	drainErr := process.DrainAndClose(drainCtx)
	cancel()
	if drainErr == nil || !process.NeedsDrainRetry() {
		return drainErr, false
	}
	go retryAtomicJobProcessDrain(process, releaseUse)
	return drainErr, true
}

func retryAtomicJobProcessDrain(process atomicJobProcess, releaseUse func()) {
	for {
		drainCtx, cancel := context.WithTimeout(context.Background(), windowsPreflightTimeout)
		err := process.DrainAndClose(drainCtx)
		cancel()
		if err == nil || !process.NeedsDrainRetry() {
			releaseUse()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
