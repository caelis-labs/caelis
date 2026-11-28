//go:build windows

package winproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
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
	closeJob()
}

type atomicJobWaitResult struct {
	code int
	err  error
}

// RunStartedJobProcess captures a process created in its Job and owns the
// pipes, root wait and tree drain. A non-nil releaseUse fences sandbox ACLs
// if drain proof must continue after the request returns.
func RunStartedJobProcess(
	ctx context.Context,
	process atomicJobProcess,
	stdin []byte,
	stdout io.Writer,
	stderr io.Writer,
	releaseUse func(),
	rootWaitTimeout, drainTimeout time.Duration,
	strictOutput bool,
) (int, error, bool) {
	var closePipesOnce sync.Once
	closePipes := func() { closePipesOnce.Do(process.ClosePipes) }
	defer closePipes()

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	copyOutput := func(dst io.Writer, src io.Reader) {
		_, err := io.Copy(dst, src)
		if strictOutput && err != nil && !errors.Is(err, io.ErrClosedPipe) {
			cancel(err)
		}
	}
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() { defer copyWG.Done(); copyOutput(stdout, process.Stdout()) }()
	go func() { defer copyWG.Done(); copyOutput(stderr, process.Stderr()) }()
	// WaitRoot exclusively owns the process handle until Windows completes the
	// pending wait. A caller timeout may close pipes and drain the Job, but it
	// must leave this waiter to close the process handle after it is signaled.
	waitCh := make(chan atomicJobWaitResult, 1)
	go func() {
		code, waitErr := process.WaitRoot()
		waitCh <- atomicJobWaitResult{code: code, err: waitErr}
	}()
	handOffDrain := func() {
		if releaseUse == nil {
			process.closeJob()
		} else {
			go retryAtomicJobProcessDrain(process, releaseUse, drainTimeout)
		}
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
			drainErr, keepUse := drainAtomicJobProcess(process, releaseUse, drainTimeout)
			if drainErr != nil {
				closePipes()
			}
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
		contextErr = context.Cause(ctx)
		terminateErr = process.Terminate()
		var rootWaitErr error
		var timedOut bool
		waited, rootWaitErr, timedOut = waitForAtomicJobRoot(waitCh, rootWaitTimeout)
		if timedOut {
			handOffDrain()
			return waited.code, errors.Join(contextErr, terminateErr, rootWaitErr), true
		}
	}
	drainErr, keepUse := drainAtomicJobProcess(process, releaseUse, drainTimeout)
	if drainErr != nil {
		closePipes()
	}
	copyWG.Wait()
	if contextErr == nil {
		contextErr = context.Cause(ctx)
	}
	if contextErr == nil && terminateErr == nil && drainErr == nil && waited.code > 0 {
		return waited.code, sandbox.MarkCommandExit(waited.err), keepUse
	}
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

func drainAtomicJobProcess(process atomicJobProcess, releaseUse func(), drainTimeout time.Duration) (error, bool) {
	drainCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	drainErr := process.DrainAndClose(drainCtx)
	cancel()
	if drainErr == nil || !process.NeedsDrainRetry() {
		return drainErr, false
	}
	if releaseUse == nil {
		process.closeJob()
		return drainErr, false
	}
	go retryAtomicJobProcessDrain(process, releaseUse, drainTimeout)
	return drainErr, true
}

func retryAtomicJobProcessDrain(process atomicJobProcess, releaseUse func(), drainTimeout time.Duration) {
	for {
		drainCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
		err := process.DrainAndClose(drainCtx)
		cancel()
		if err == nil || !process.NeedsDrainRetry() {
			releaseUse()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
