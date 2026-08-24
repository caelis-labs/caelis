//go:build windows

package windows

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAtomicJobStdinFailureBoundsFailedTerminationRootWait(t *testing.T) {
	writeErr := errors.New("stdin write failed")
	terminateErr := errors.New("terminate failed")
	process := newBlockingAtomicJobProcess(&failingAtomicJobInput{writeErr: writeErr}, terminateErr)
	process.drainFailures.Store(1)
	released := make(chan struct{})

	started := time.Now()
	exitCode, err, keepUse := runStartedAtomicJobProcess(
		context.Background(), process, []byte("input"), io.Discard, io.Discard,
		func() { close(released) }, 10*time.Millisecond,
	)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stdin failure returned after %s, want bounded root wait", elapsed)
	}
	if exitCode != -1 || !keepUse {
		t.Fatalf("stdin failure result = exit %d, keep use %t; want -1/true", exitCode, keepUse)
	}
	if !errors.Is(err, writeErr) || !errors.Is(err, terminateErr) || !strings.Contains(err.Error(), "root process did not exit") {
		t.Fatalf("stdin failure error = %v, want write, terminate, and bounded-wait errors", err)
	}
	awaitAtomicJobRelease(t, released)
	if process.drainCalls.Load() != 2 {
		t.Fatalf("DrainAndClose calls = %d, want retry after initial failure", process.drainCalls.Load())
	}
	if process.processHandleClosed.Load() {
		t.Fatal("process handle closed while WaitRoot remained pending")
	}
	process.completeRootWait()
	process.awaitRootWait(t)
}

func TestAtomicJobCancellationBoundsDelayedRootSignal(t *testing.T) {
	process := newBlockingAtomicJobProcess(nil, nil)
	released := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	exitCode, err, keepUse := runStartedAtomicJobProcess(
		ctx, process, nil, io.Discard, io.Discard,
		func() { close(released) }, 10*time.Millisecond,
	)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation returned after %s, want bounded root wait", elapsed)
	}
	if exitCode != -1 || !keepUse {
		t.Fatalf("cancellation result = exit %d, keep use %t; want -1/true", exitCode, keepUse)
	}
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "root process did not exit") {
		t.Fatalf("cancellation error = %v, want context and bounded-wait errors", err)
	}
	awaitAtomicJobRelease(t, released)
	if process.terminateCalls.Load() != 1 {
		t.Fatalf("Terminate calls = %d, want 1", process.terminateCalls.Load())
	}
	if process.processHandleClosed.Load() {
		t.Fatal("process handle closed before delayed root signal")
	}
	process.completeRootWait()
	process.awaitRootWait(t)
}

type failingAtomicJobInput struct {
	writeErr error
	closeErr error
}

func (w *failingAtomicJobInput) Write([]byte) (int, error) { return 0, w.writeErr }
func (w *failingAtomicJobInput) Close() error              { return w.closeErr }

type blockingAtomicJobProcess struct {
	input io.WriteCloser

	terminateErr error
	waitRelease  chan struct{}
	waitDone     chan struct{}
	waitDoneOnce sync.Once

	terminateCalls      atomic.Int32
	drainCalls          atomic.Int32
	drainFailures       atomic.Int32
	drainDone           atomic.Bool
	processHandleClosed atomic.Bool
}

func newBlockingAtomicJobProcess(input io.WriteCloser, terminateErr error) *blockingAtomicJobProcess {
	return &blockingAtomicJobProcess{
		input:        input,
		terminateErr: terminateErr,
		waitRelease:  make(chan struct{}),
		waitDone:     make(chan struct{}),
	}
}

func (p *blockingAtomicJobProcess) Input() io.WriteCloser { return p.input }
func (p *blockingAtomicJobProcess) Stdout() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (p *blockingAtomicJobProcess) Stderr() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }

func (p *blockingAtomicJobProcess) WaitRoot() (int, error) {
	<-p.waitRelease
	p.processHandleClosed.Store(true)
	p.waitDoneOnce.Do(func() { close(p.waitDone) })
	return 1, errors.New("process exited with code 1")
}

func (p *blockingAtomicJobProcess) Terminate() error {
	p.terminateCalls.Add(1)
	return p.terminateErr
}

func (p *blockingAtomicJobProcess) DrainAndClose(context.Context) error {
	p.drainCalls.Add(1)
	if p.drainFailures.Add(-1) >= 0 {
		return errors.New("drain failed")
	}
	p.drainDone.Store(true)
	return nil
}

func (p *blockingAtomicJobProcess) NeedsDrainRetry() bool { return !p.drainDone.Load() }
func (*blockingAtomicJobProcess) ClosePipes()             {}

func (p *blockingAtomicJobProcess) completeRootWait() {
	close(p.waitRelease)
}

func (p *blockingAtomicJobProcess) awaitRootWait(t *testing.T) {
	t.Helper()
	select {
	case <-p.waitDone:
	case <-time.After(time.Second):
		t.Fatal("WaitRoot did not finish after root signal")
	}
}

func awaitAtomicJobRelease(t *testing.T, released <-chan struct{}) {
	t.Helper()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("background Job drain did not release Runtime use")
	}
}
