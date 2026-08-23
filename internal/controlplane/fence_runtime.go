package controlplane

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

var (
	_ agent.StreamProvider          = (*FencedRuntime)(nil)
	_ agent.LiveRunAttacher         = (*FencedRuntime)(nil)
	_ agent.ApprovalResolver        = (*FencedRuntime)(nil)
	_ agent.ParticipantControlPlane = (*FencedRuntime)(nil)
	_ PlacementExecutor             = (*FencedRuntime)(nil)
)

// FencedRuntimeConfig configures the Control-owned placement guard around one
// execution Runtime. The fence covers the asynchronous Runner lifetime.
type FencedRuntimeConfig struct {
	Runtime          agent.Runtime
	Fences           session.SessionFenceService
	OwnerID          string
	LifecycleContext context.Context
	Diagnostics      *slog.Logger
}

// FencedRuntime acquires a store-level execution fence before a main or
// participant Turn and holds it until the returned Runner completes or closes.
// Side ACP is therefore a valid owner of the same canonical execution envelope,
// not an unfenced writer.
type FencedRuntime struct {
	runtimeFacade
	fences       session.SessionFenceService
	ownerID      string
	lifecycleCtx context.Context
	diagnostics  *fencePhaseDiagnostics
	executionsMu sync.Mutex
	executions   map[fencedExecutionKey]*activeFencedExecution
}

func NewFencedRuntime(config FencedRuntimeConfig) (*FencedRuntime, error) {
	if config.Runtime == nil {
		return nil, fmt.Errorf("controlplane: fenced runtime requires an execution runtime")
	}
	if _, ok := config.Runtime.(agent.RunnerCompletionRuntime); !ok {
		return nil, fmt.Errorf("controlplane: fenced runtime requires guaranteed runner completion waiters")
	}
	if config.Fences == nil {
		return nil, fmt.Errorf("controlplane: fenced runtime requires a session fence service")
	}
	ownerID := strings.TrimSpace(config.OwnerID)
	if ownerID == "" {
		return nil, fmt.Errorf("controlplane: fenced runtime requires owner_id")
	}
	lifecycleCtx := config.LifecycleContext
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}
	return &FencedRuntime{
		runtimeFacade: newRuntimeFacade(config.Runtime),
		fences:        config.Fences,
		ownerID:       ownerID,
		lifecycleCtx:  lifecycleCtx,
		diagnostics:   newFencePhaseDiagnostics(config.Diagnostics),
		executions:    make(map[fencedExecutionKey]*activeFencedExecution),
	}, nil
}

// ExecutePlaced holds the session execution fence for the full synchronous
// callback. Fence loss cancels the callback context so work cannot continue
// under a replaced fence.
func (r *FencedRuntime) ExecutePlaced(ctx context.Context, ref session.SessionRef, execute func(context.Context) error) error {
	return executeWithSessionFence(ctx, r.fences, r.ownerID, r.lifecycleCtx, r.diagnostics, ref, execute)
}

func (r *FencedRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	return r.runWithFence(ctx, req.SessionRef, func(runCtx context.Context) (agent.RunResult, error) {
		return r.inner.Run(runCtx, req)
	})
}

func (r *FencedRuntime) runWithFence(ctx context.Context, ref session.SessionRef, execute func(context.Context) (agent.RunResult, error)) (agent.RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ref = session.NormalizeSessionRef(ref)
	started := r.diagnostics.started()
	fence, err := r.acquireSessionFence(ctx, ref)
	r.diagnostics.observe(ctx, "acquire", started, err)
	if err != nil {
		return agent.RunResult{}, err
	}
	runCtx, cancel := context.WithCancel(session.ContextWithRuntimeFence(ctx, fence))
	guard := startSessionFenceGuard(r.fences, fence, r.lifecycleCtx, cancel, r.diagnostics)
	execution := r.registerFencedExecution(ref, fence.FenceID, guard)
	guard.setOnReleased(func() { r.releaseFencedExecution(execution) })
	started = r.diagnostics.started()
	result, err := execute(runCtx)
	r.diagnostics.observe(runCtx, "start_producer", started, err)
	if err != nil {
		cancel()
		finishErr := guard.finishAndErr()
		r.completeFencedExecution(execution)
		return agent.RunResult{}, errors.Join(err, finishErr)
	}
	if result.Handle == nil {
		cancel()
		finishErr := guard.finishAndErr()
		r.completeFencedExecution(execution)
		return result, finishErr
	}
	if _, ok := result.Handle.(agent.RunnerCompletionWaiter); !ok {
		cancel()
		closeErr := result.Handle.Close()
		return agent.RunResult{}, errors.Join(
			fmt.Errorf("controlplane: fenced runtime runner requires a completion waiter; execution fence retained"),
			closeErr,
		)
	}
	return r.wrapLiveHandle(result, ref, func(inner agent.Runner, onFinish func()) agent.Runner {
		wrapped := newFencedRunner(inner, guard, cancel, r.diagnostics, func() {
			onFinish()
			r.completeFencedExecution(execution)
		})
		base := baseFencedRunner(wrapped)
		execution.setRunner(base)
		go func() { _ = base.finishAfterProducer() }()
		return wrapped
	}), nil
}

func (r *FencedRuntime) acquireSessionFence(ctx context.Context, ref session.SessionRef) (session.SessionFence, error) {
	return acquireSessionFence(ctx, r.fences, r.ownerID, ref)
}

func executeWithSessionFence(
	ctx context.Context,
	fences session.SessionFenceService,
	ownerID string,
	lifecycleCtx context.Context,
	diagnostics *fencePhaseDiagnostics,
	ref session.SessionRef,
	execute func(context.Context) error,
) error {
	if execute == nil {
		return fmt.Errorf("controlplane: placed operation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ref = session.NormalizeSessionRef(ref)
	started := diagnostics.started()
	fence, err := acquireSessionFence(ctx, fences, ownerID, ref)
	diagnostics.observe(ctx, "acquire", started, err)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(session.ContextWithRuntimeFence(ctx, fence))
	defer cancel()
	guard := startSessionFenceGuard(fences, fence, lifecycleCtx, cancel, diagnostics)
	started = diagnostics.started()
	execErr := execute(runCtx)
	diagnostics.observe(runCtx, "producer", started, execErr)
	return errors.Join(execErr, guard.finishAndErr())
}

func acquireSessionFence(
	ctx context.Context,
	fences session.SessionFenceService,
	ownerID string,
	ref session.SessionRef,
) (session.SessionFence, error) {
	acquire := session.AcquireSessionFenceRequest{
		SessionRef: ref,
		OwnerID:    strings.TrimSpace(ownerID),
	}
	fence, err := fences.AcquireSessionFence(ctx, acquire)
	if session.IsCommitted(err) {
		if matchesAcquiredSessionFence(acquire, fence) {
			err = nil
		}
	}
	if err != nil {
		return session.SessionFence{}, err
	}
	return fence, nil
}

func matchesAcquiredSessionFence(req session.AcquireSessionFenceRequest, fence session.SessionFence) bool {
	return session.NormalizeSessionRef(fence.SessionRef) == session.NormalizeSessionRef(req.SessionRef) &&
		strings.TrimSpace(fence.FenceID) != "" &&
		strings.TrimSpace(fence.OwnerID) == strings.TrimSpace(req.OwnerID) &&
		fence.FencingToken > 0 && session.SessionFenceHasClaim(fence)
}

func (r *FencedRuntime) PromptParticipant(ctx context.Context, req agent.PromptParticipantRequest) (agent.RunResult, error) {
	participants, err := r.participants()
	if err != nil {
		return agent.RunResult{}, err
	}
	return r.runWithFence(ctx, req.SessionRef, func(runCtx context.Context) (agent.RunResult, error) {
		return participants.PromptParticipant(runCtx, req)
	})
}

// sessionFenceGuard is the single loss/release machine used by both synchronous
// placed operations and asynchronous fenced runners. Host liveness owns the
// fence lifetime; there is no renewal timer or steady-state storage traffic.
// Only a failed exact release starts bounded Host-lifecycle retry traffic.
type sessionFenceGuard struct {
	fences      session.SessionFenceService
	onLoss      func()
	releaseCtx  context.Context
	diagnostics *fencePhaseDiagnostics

	mu           sync.Mutex
	fence        session.SessionFence
	lossErr      error
	lossNotified bool

	releaseOnce      sync.Once
	releaseFirstDone chan struct{}
	releaseMu        sync.Mutex
	releaseFirstErr  error
	releaseCompleted bool
	onReleased       func()
	releaseNotified  bool
}

var errSessionFenceOwnershipLost = fmt.Errorf("controlplane: session fence ownership was lost: %w", session.ErrFenceConflict)

func startSessionFenceGuard(
	fences session.SessionFenceService,
	fence session.SessionFence,
	releaseCtx context.Context,
	onLoss func(),
	diagnostics *fencePhaseDiagnostics,
) *sessionFenceGuard {
	if releaseCtx == nil {
		releaseCtx = context.Background()
	}
	return &sessionFenceGuard{
		fences: fences, fence: fence, onLoss: onLoss, releaseCtx: releaseCtx, diagnostics: diagnostics,
		releaseFirstDone: make(chan struct{}),
	}
}

func (g *sessionFenceGuard) setOnReleased(onReleased func()) {
	if g == nil {
		return
	}
	g.releaseMu.Lock()
	g.onReleased = onReleased
	notify := g.releaseCompleted && onReleased != nil && !g.releaseNotified
	if notify {
		g.releaseNotified = true
	}
	g.releaseMu.Unlock()
	if notify {
		onReleased()
	}
}

func (g *sessionFenceGuard) setOnLoss(onLoss func()) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.onLoss = onLoss
	g.lossNotified = false
	alreadyLost := g.lossErr != nil && onLoss != nil
	if alreadyLost {
		g.lossNotified = true
	}
	g.mu.Unlock()
	if alreadyLost && onLoss != nil {
		onLoss()
	}
}

func (g *sessionFenceGuard) recordLoss(err error) {
	if g == nil || err == nil {
		return
	}
	g.mu.Lock()
	if g.lossErr == nil {
		g.lossErr = err
	}
	onLoss := g.onLoss
	notify := onLoss != nil && !g.lossNotified
	if notify {
		g.lossNotified = true
	}
	g.mu.Unlock()
	if notify {
		onLoss()
	}
}

func (g *sessionFenceGuard) err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lossErr
}

func (g *sessionFenceGuard) finish() error {
	if g == nil {
		return nil
	}
	g.releaseOnce.Do(func() { go g.releaseLoop() })
	<-g.releaseFirstDone
	g.releaseMu.Lock()
	defer g.releaseMu.Unlock()
	if g.releaseCompleted {
		return nil
	}
	return g.releaseFirstErr
}

func (g *sessionFenceGuard) releaseLoop() {
	delay := 100 * time.Millisecond
	first := true
	for {
		g.mu.Lock()
		fence := g.fence
		g.mu.Unlock()
		started := g.diagnostics.started()
		err := releaseSessionFence(g.fences, fence)

		g.releaseMu.Lock()
		if first {
			g.releaseFirstErr = err
			close(g.releaseFirstDone)
			first = false
		}
		if err == nil {
			g.releaseCompleted = true
			onReleased := g.onReleased
			notify := onReleased != nil && !g.releaseNotified
			if notify {
				g.releaseNotified = true
			}
			g.releaseMu.Unlock()
			if notify {
				onReleased()
			}
			// Publish release before best-effort diagnostics so filesystem log
			// I/O cannot extend the durable execution-fence lifetime.
			g.diagnostics.observe(g.releaseCtx, "release_reconcile", started, nil)
			return
		}
		g.releaseMu.Unlock()
		g.diagnostics.observe(g.releaseCtx, "release_reconcile", started, err)

		timer := time.NewTimer(delay)
		select {
		case <-g.releaseCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		if delay < 5*time.Second {
			delay *= 2
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
		}
	}
}

func (g *sessionFenceGuard) released() bool {
	if g == nil {
		return true
	}
	g.releaseMu.Lock()
	defer g.releaseMu.Unlock()
	return g.releaseCompleted
}

func (g *sessionFenceGuard) finishAndErr() error {
	finishErr := g.finish()
	return errors.Join(g.err(), finishErr)
}

func releaseSessionFence(fences session.SessionFenceService, fence session.SessionFence) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := fences.ReleaseSessionFence(ctx, session.SessionFenceReleaseRequest(fence))
	if session.IsCommitted(err) {
		return nil
	}
	if err == nil {
		return nil
	}
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	durable, readErr := fences.SessionFence(readCtx, fence.SessionRef)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	if strings.TrimSpace(durable.FenceID) == "" ||
		durable.FenceID != fence.FenceID || durable.OwnerID != fence.OwnerID || durable.FencingToken != fence.FencingToken {
		// The release committed without a reliable response, or a later Host
		// epoch already replaced it. Stale cleanup must not touch the new fence.
		return nil
	}
	return err
}

type fencedRunner struct {
	inner       agent.Runner
	guard       *sessionFenceGuard
	cancel      context.CancelFunc
	diagnostics *fencePhaseDiagnostics
	onFinish    func()
	quiesceOnce sync.Once
	quiesceDone chan struct{}
	quiesceErr  error
	finishOnce  sync.Once
}

func newFencedRunner(inner agent.Runner, guard *sessionFenceGuard, cancel context.CancelFunc, diagnostics *fencePhaseDiagnostics, onFinish func()) agent.Runner {
	runner := &fencedRunner{
		inner: inner, guard: guard, cancel: cancel, diagnostics: diagnostics, onFinish: onFinish,
		quiesceDone: make(chan struct{}),
	}
	guard.setOnLoss(func() {
		cancel()
		inner.Cancel()
	})
	if source, ok := inner.(agent.SourceHandle); ok {
		return &fencedSourceRunner{fencedRunner: runner, source: source}
	}
	return runner
}

func (r *fencedRunner) RunID() string { return r.inner.RunID() }

func (r *fencedRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		completed := true
		for event, err := range r.inner.Events() {
			if errors.Is(err, session.ErrFenceConflict) {
				r.guard.recordLoss(errSessionFenceOwnershipLost)
				break
			}
			if !yield(event, err) {
				completed = false
				break
			}
		}
		if !completed {
			_ = r.inner.Close()
			_ = r.finishAfterProducer()
			return
		}
		if err := r.finishAfterProducer(); err != nil {
			yield(nil, err)
		}
	}
}

type fencedSourceRunner struct {
	*fencedRunner
	source agent.SourceHandle
}

func (r *fencedSourceRunner) SourceEvents() iter.Seq2[agent.SourceEvent, error] {
	return func(yield func(agent.SourceEvent, error) bool) {
		completed := true
		for event, err := range r.source.SourceEvents() {
			if errors.Is(err, session.ErrFenceConflict) {
				r.guard.recordLoss(errSessionFenceOwnershipLost)
				break
			}
			if !yield(event, err) {
				completed = false
				break
			}
		}
		if !completed {
			_ = r.inner.Close()
			_ = r.finishAfterProducer()
			return
		}
		if err := r.finishAfterProducer(); err != nil {
			yield(agent.SourceEvent{}, err)
		}
	}
}

func (r *fencedRunner) Submit(submission agent.Submission) error { return r.inner.Submit(submission) }

func (r *fencedRunner) SubmitContext(ctx context.Context, submission agent.Submission) error {
	if contextual, ok := r.inner.(agent.ContextSubmissionRunner); ok {
		return contextual.SubmitContext(ctx, submission)
	}
	return r.inner.Submit(submission)
}

func (r *fencedRunner) Cancel() agent.CancelResult { return r.inner.Cancel() }

func (r *fencedRunner) Close() error {
	innerErr := r.inner.Close()
	finishErr := r.finishAfterProducer()
	return errors.Join(innerErr, finishErr, r.guard.err())
}

func (r *fencedRunner) finishAfterProducer() error {
	waitErr := r.waitForProducer(context.Background())
	if waitErr != nil {
		// A waiter error does not prove producer quiescence. Retain the fence so
		// an unobserved stale producer cannot commit after a new Turn starts.
		return errors.Join(waitErr, r.guard.err())
	}
	return r.finish()
}

func (r *fencedRunner) waitForProducer(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.quiesceOnce.Do(func() {
		started := r.diagnostics.started()
		go func() {
			waiter := r.inner.(agent.RunnerCompletionWaiter)
			r.quiesceErr = waiter.WaitCompletion(context.Background())
			// A normal Turn lifetime is not a slow wait. Record this phase only
			// when completion proof fails; otherwise every model Turn would emit
			// a warning merely for doing useful work.
			if r.quiesceErr != nil {
				r.diagnostics.observe(context.Background(), "wait_producer", started, r.quiesceErr)
			}
			close(r.quiesceDone)
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.quiesceDone:
		return r.quiesceErr
	}
}

func (r *fencedRunner) finish() error {
	r.finishOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.onFinish != nil {
			r.onFinish()
		}
	})
	return r.guard.finishAndErr()
}
