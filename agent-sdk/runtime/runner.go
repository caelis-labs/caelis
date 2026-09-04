package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/runtimeinput"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type runner struct {
	runID         string
	ctx           context.Context
	cancelFn      context.CancelFunc
	observer      agent.SourceEventObserver
	observerMu    sync.Mutex
	ownership     liveContentOwnership
	finishOnce    sync.Once
	done          chan struct{}
	mu            sync.Mutex
	cancelled     bool
	closed        bool
	finished      bool
	completionErr error
	submissions   []agent.Submission
	cancelHook    func() error
	dispatcher    *runnerSubmissionDispatcher
}

func newRunner(ctx context.Context, runID string, cancel context.CancelFunc, observer agent.SourceEventObserver) *runner {
	if ctx == nil {
		ctx = context.Background()
	}
	return &runner{
		runID:    runID,
		ctx:      ctx,
		cancelFn: cancel,
		observer: observer,
		done:     make(chan struct{}),
	}
}

func (r *runner) RunID() string { return r.runID }

func (r *runner) Submit(sub agent.Submission) error {
	return r.SubmitContext(context.Background(), sub)
}

func (r *runner) SubmitContext(ctx context.Context, sub agent.Submission) error {
	if sub.Kind != agent.SubmissionKindConversation && sub.Kind != agent.SubmissionKindAgentCommunication {
		return fmt.Errorf("agent-sdk/runtime: unsupported submission kind %q", sub.Kind)
	}
	if sub.Kind == agent.SubmissionKindAgentCommunication {
		if err := session.ValidateAgentCommunicationActor(sub.Actor); err != nil {
			return fmt.Errorf("agent-sdk/runtime: %w", err)
		}
	}
	return r.submitContext(ctx, sub)
}

// submitRuntimeModelContext is intentionally available only to Runtime
// orchestration. The public Runner API rejects this kind so SDK consumers
// cannot create hidden canonical model input.
func (r *runner) submitRuntimeModelContext(ctx context.Context, sub agent.Submission) error {
	if sub.Kind != runtimeinput.ModelContext || !session.ActorRefHasIdentity(sub.Actor) {
		return errors.New("agent-sdk/runtime: invalid runtime model-context submission")
	}
	return r.submitContext(ctx, sub)
}

func (r *runner) submitContext(ctx context.Context, sub agent.Submission) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errRunnerSubmissionClosed
	}
	dispatcher := r.dispatcher
	if dispatcher != nil {
		r.mu.Unlock()
		return dispatcher.submit(ctx, sub)
	}
	r.submissions = append(r.submissions, agent.CloneSubmission(sub))
	r.mu.Unlock()
	return nil
}

func (r *runner) setSubmissionHandler(ctx context.Context, handler func(context.Context, agent.Submission) error) error {
	if r == nil || handler == nil {
		return errors.New("agent-sdk/runtime: submission handler is required")
	}
	dispatcher := newRunnerSubmissionDispatcher(ctx, handler)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		dispatcher.close(errRunnerSubmissionClosed)
		return errRunnerSubmissionClosed
	}
	if r.dispatcher != nil {
		r.mu.Unlock()
		dispatcher.close(errors.New("agent-sdk/runtime: submission handler is already installed"))
		return errors.New("agent-sdk/runtime: submission handler is already installed")
	}
	if len(r.submissions) != 0 {
		r.mu.Unlock()
		dispatcher.close(errors.New("agent-sdk/runtime: submission handler cannot replace queued submissions"))
		return errors.New("agent-sdk/runtime: submission handler cannot replace queued submissions")
	}
	r.dispatcher = dispatcher
	r.mu.Unlock()
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
	if r.cancelled || r.finished {
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
	if r == nil {
		return nil
	}
	r.mu.Lock()
	finished := r.finished
	r.mu.Unlock()
	var cancelErr error
	if !finished {
		cancelErr = r.Cancel().Err
	}
	r.markClosed()
	return cancelErr
}

func (r *runner) WaitCompletion(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-r.done:
		r.mu.Lock()
		err := r.completionErr
		r.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
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
	r.publishSourceEvent(agent.SourceEvent{
		Canonical: session.CloneEvent(event),
	})
}

func (r *runner) publishSourceEvent(event agent.SourceEvent) {
	if r == nil || (event.Canonical == nil && event.Native == nil && event.Err == nil) {
		return
	}
	r.publish(event)
}

func (r *runner) publishError(err error) {
	if r == nil || err == nil {
		return
	}
	r.mu.Lock()
	if r.completionErr == nil {
		r.completionErr = err
	}
	r.mu.Unlock()
	r.publish(agent.SourceEvent{Err: err})
}

func (r *runner) publish(event agent.SourceEvent) {
	if r == nil || r.observer == nil {
		return
	}
	r.observerMu.Lock()
	event = agent.CloneSourceEvent(event)
	event.CanonicalContentAlreadyPublished |= r.ownership.observe(event.Canonical)
	_ = r.observer.ObserveSourceEvent(r.ctx, event)
	r.observerMu.Unlock()
}

func (r *runner) finish() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.finished = true
	r.closed = true
	dispatcher := r.dispatcher
	r.mu.Unlock()
	if dispatcher != nil {
		dispatcher.close(errRunnerSubmissionClosed)
	}
	r.finishOnce.Do(func() {
		close(r.done)
	})
}

func interruptedOrFailedStatus(ctx context.Context, err error) agent.RunLifecycleStatus {
	if isInterruptedError(ctx, err) {
		return agent.RunLifecycleStatusInterrupted
	}
	return agent.RunLifecycleStatusFailed
}

func isInterruptedError(ctx context.Context, err error) bool {
	return errorcode.Is(err, errorcode.Interrupted) || isCancellationError(ctx, err)
}

func isCancellationError(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) ||
		(ctx != nil && errors.Is(ctx.Err(), context.Canceled)) ||
		errorcode.Is(err, errorcode.Cancelled)
}

func executionJournalStatus(ctx context.Context, err error) session.ExecutionStatus {
	switch {
	case errorcode.Is(err, errorcode.UnknownOutcome):
		return session.ExecutionUnknownOutcome
	case errorcode.Is(err, errorcode.Interrupted):
		return session.ExecutionInterrupted
	case isCancellationError(ctx, err):
		return session.ExecutionCancelled
	default:
		return session.ExecutionFailed
	}
}
