package local

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type runner struct {
	runID       string
	cancelFn    context.CancelFunc
	eventsCh    chan runnerEvent
	closeOnce   sync.Once
	mu          sync.Mutex
	cancelled   bool
	closed      bool
	submissions []agent.Submission
	cancelHook  func() error
}

type runnerEvent struct {
	event *session.Event
	err   error
}

func newRunner(runID string, cancel context.CancelFunc) *runner {
	return &runner{
		runID:    runID,
		cancelFn: cancel,
		eventsCh: make(chan runnerEvent, 64),
	}
}

func (r *runner) RunID() string { return r.runID }

func (r *runner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for item := range r.eventsCh {
			if !yield(session.CloneEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (r *runner) Submit(sub agent.Submission) error {
	if sub.Kind != agent.SubmissionKindConversation {
		return fmt.Errorf("impl/agent/local: unsupported submission kind %q", sub.Kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("impl/agent/local: runner is closed")
	}
	r.submissions = append(r.submissions, agent.CloneSubmission(sub))
	return nil
}

func (r *runner) drainSubmissions() []agent.Submission {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := agent.CloneSubmissions(r.submissions)
	r.submissions = nil
	return out
}

func (r *runner) markClosed() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func (r *runner) Cancel() agent.CancelResult {
	r.mu.Lock()
	if r.cancelled {
		r.mu.Unlock()
		return agent.CancelResult{Status: agent.CancelStatusAlreadyCancelled}
	}
	r.cancelled = true
	cancelFn := r.cancelFn
	cancelHook := r.cancelHook
	r.mu.Unlock()

	if cancelFn != nil {
		cancelFn()
	}
	result := agent.CancelResult{Status: agent.CancelStatusCancelled}
	if cancelHook != nil {
		if err := cancelHook(); err != nil {
			result.Err = err
		}
	}
	return result
}

func (r *runner) setCancelHook(fn func() error) {
	r.mu.Lock()
	cancelled := r.cancelled
	r.cancelHook = fn
	r.mu.Unlock()
	if cancelled && fn != nil {
		_ = fn()
	}
}

func (r *runner) Close() error {
	r.markClosed()
	return nil
}

func (r *runner) publishEvent(event *session.Event) {
	if r == nil || event == nil {
		return
	}
	r.publish(runnerEvent{event: session.CloneEvent(event)})
}

func (r *runner) publishError(err error) {
	if r == nil || err == nil {
		return
	}
	r.publish(runnerEvent{err: err})
}

func (r *runner) publish(item runnerEvent) {
	if r == nil {
		return
	}
	select {
	case r.eventsCh <- item:
	default:
		r.eventsCh <- item
	}
}

func (r *runner) finish() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.markClosed()
		close(r.eventsCh)
	})
}

func interruptedOrFailedStatus(ctx context.Context, err error) agent.RunLifecycleStatus {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return agent.RunLifecycleStatusInterrupted
	}
	return agent.RunLifecycleStatusFailed
}
