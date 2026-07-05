package runtime

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type runner struct {
	runID       string
	cancelFn    context.CancelFunc
	events      *eventQueue[runnerEvent]
	closeOnce   sync.Once
	mu          sync.Mutex
	cancelled   bool
	closed      bool
	submissions []agent.Submission
	cancelHook  func() error
}

type runnerEvent struct {
	event agent.SourceEvent
	err   error
}

func newRunner(runID string, cancel context.CancelFunc) *runner {
	return &runner{
		runID:    runID,
		cancelFn: cancel,
		events:   newEventQueue[runnerEvent](),
	}
}

func (r *runner) RunID() string { return r.runID }

func (r *runner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		if r == nil {
			return
		}
		for {
			item, ok := r.events.Pop()
			if !ok {
				return
			}
			if item.err != nil {
				if !yield(nil, item.err) {
					return
				}
				continue
			}
			if item.event.Canonical == nil {
				continue
			}
			if !yield(session.CloneEvent(item.event.Canonical), nil) {
				return
			}
		}
	}
}

func (r *runner) SourceEvents() iter.Seq2[agent.SourceEvent, error] {
	return func(yield func(agent.SourceEvent, error) bool) {
		if r == nil {
			return
		}
		for {
			item, ok := r.events.Pop()
			if !ok {
				return
			}
			if !yield(agent.CloneSourceEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (r *runner) Submit(sub agent.Submission) error {
	if sub.Kind != agent.SubmissionKindConversation {
		return fmt.Errorf("agent-sdk/runtime: unsupported submission kind %q", sub.Kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("agent-sdk/runtime: runner is closed")
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

func (r *runner) PublishEvent(event *session.Event) {
	r.publishEvent(event)
}

func (r *runner) PublishSourceEvent(event agent.SourceEvent) {
	r.publishSourceEvent(event)
}

func (r *runner) publishEvent(event *session.Event) {
	if r == nil || event == nil {
		return
	}
	r.publishSourceEvent(agent.SourceEvent{Canonical: session.CloneEvent(event)})
}

func (r *runner) publishSourceEvent(event agent.SourceEvent) {
	if r == nil || (event.Canonical == nil && event.Native == nil) {
		return
	}
	r.publish(runnerEvent{event: agent.CloneSourceEvent(event)})
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
	r.events.Push(item)
}

func (r *runner) finish() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.markClosed()
		r.events.Close()
	})
}

func interruptedOrFailedStatus(ctx context.Context, err error) agent.RunLifecycleStatus {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return agent.RunLifecycleStatusInterrupted
	}
	return agent.RunLifecycleStatusFailed
}
