package runtime

import (
	"context"
	"errors"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

const runnerSubmissionQueueCapacity = 64

var errRunnerSubmissionClosed = errorcode.New(errorcode.FailedPrecondition, "agent-sdk/runtime: runner submission handler is closed")

type runnerSubmissionRequest struct {
	ctx        context.Context
	submission agent.Submission
	result     chan error

	mu         sync.Mutex
	dispatched bool
	settled    bool
	cancelled  bool
}

type runnerSubmissionDispatcher struct {
	parent  context.Context
	handle  func(context.Context, agent.Submission) error
	queue   chan *runnerSubmissionRequest
	stopped chan struct{}
	done    chan struct{}

	stopOnce     sync.Once
	mu           sync.Mutex
	stopErr      error
	activeCancel context.CancelFunc
}

func newRunnerSubmissionDispatcher(
	parent context.Context,
	handler func(context.Context, agent.Submission) error,
) *runnerSubmissionDispatcher {
	if parent == nil {
		parent = context.Background()
	}
	dispatcher := &runnerSubmissionDispatcher{
		parent:  parent,
		handle:  handler,
		queue:   make(chan *runnerSubmissionRequest, runnerSubmissionQueueCapacity),
		stopped: make(chan struct{}),
		done:    make(chan struct{}),
	}
	go dispatcher.run()
	return dispatcher
}

func (d *runnerSubmissionDispatcher) submit(ctx context.Context, submission agent.Submission) error {
	if d == nil || d.handle == nil {
		return errors.New("agent-sdk/runtime: submission handler is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request := &runnerSubmissionRequest{
		ctx:        ctx,
		submission: agent.CloneSubmission(submission),
		result:     make(chan error, 1),
	}
	select {
	case <-ctx.Done():
		return submissionNotDispatchedError(ctx.Err())
	case <-d.stopped:
		return d.stoppedError()
	case d.queue <- request:
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		if request.cancelBeforeDispatch() {
			return submissionNotDispatchedError(ctx.Err())
		}
		select {
		case err := <-request.result:
			return err
		case <-d.done:
			select {
			case err := <-request.result:
				return err
			default:
				return d.stoppedError()
			}
		}
	case <-d.done:
		select {
		case err := <-request.result:
			return err
		default:
			return d.stoppedError()
		}
	}
}

func (d *runnerSubmissionDispatcher) run() {
	defer close(d.done)
	for {
		select {
		case <-d.parent.Done():
			d.close(d.parent.Err())
			d.rejectQueued()
			return
		case <-d.stopped:
			d.rejectQueued()
			return
		case request := <-d.queue:
			if err := request.ctx.Err(); err != nil {
				request.markSettled()
				request.result <- submissionNotDispatchedError(err)
				continue
			}
			if !request.beginDispatch() {
				request.markSettled()
				request.result <- submissionNotDispatchedError(request.ctx.Err())
				continue
			}
			callCtx, cancel := context.WithCancel(request.ctx)
			stopParent := context.AfterFunc(d.parent, cancel)
			d.mu.Lock()
			if d.stopErr != nil {
				err := d.stopErr
				d.mu.Unlock()
				stopParent()
				cancel()
				request.markSettled()
				request.result <- err
				continue
			}
			d.activeCancel = cancel
			d.mu.Unlock()
			err := d.handle(callCtx, request.submission)
			stopParent()
			cancel()
			d.mu.Lock()
			d.activeCancel = nil
			d.mu.Unlock()
			request.markSettled()
			request.result <- err
		}
	}
}

func (d *runnerSubmissionDispatcher) rejectQueued() {
	for {
		select {
		case request := <-d.queue:
			request.markSettled()
			request.result <- d.stoppedError()
		default:
			return
		}
	}
}

func (d *runnerSubmissionDispatcher) close(err error) {
	if d == nil {
		return
	}
	if err == nil {
		err = errRunnerSubmissionClosed
	}
	d.stopOnce.Do(func() {
		d.mu.Lock()
		d.stopErr = err
		cancel := d.activeCancel
		close(d.stopped)
		d.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
}

func (d *runnerSubmissionDispatcher) stoppedError() error {
	if d == nil {
		return errRunnerSubmissionClosed
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopErr != nil {
		return d.stopErr
	}
	return errRunnerSubmissionClosed
}

func submissionNotDispatchedError(err error) error {
	if err == nil {
		err = context.Canceled
	}
	return errorcode.Wrap(errorcode.FailedPrecondition, "agent-sdk/runtime: submission was cancelled before dispatch", err)
}

func (r *runnerSubmissionRequest) beginDispatch() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelled || r.settled {
		return false
	}
	r.dispatched = true
	return true
}

func (r *runnerSubmissionRequest) cancelBeforeDispatch() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatched || r.settled {
		return false
	}
	r.cancelled = true
	return true
}

func (r *runnerSubmissionRequest) markSettled() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.settled = true
	r.mu.Unlock()
}
