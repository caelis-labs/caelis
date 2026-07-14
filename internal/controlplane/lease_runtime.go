package controlplane

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const defaultSessionLeaseTTL = 30 * time.Second

var (
	_ agent.StreamProvider          = (*LeasedRuntime)(nil)
	_ agent.LiveRunAttacher         = (*LeasedRuntime)(nil)
	_ agent.ApprovalResolver        = (*LeasedRuntime)(nil)
	_ agent.ParticipantControlPlane = (*LeasedRuntime)(nil)
	_ PlacementExecutor             = (*LeasedRuntime)(nil)
)

// LeasedRuntimeConfig configures the Control-owned placement guard around one
// execution Runtime. The lease covers the asynchronous Runner lifetime.
type LeasedRuntimeConfig struct {
	Runtime           agent.Runtime
	Leases            session.SessionLeaseService
	OwnerID           string
	TTL               time.Duration
	HeartbeatInterval time.Duration
}

// LeasedRuntime acquires a store-level execution lease before a main or
// participant Turn and keeps it alive until the returned Runner completes or
// closes. Side ACP is therefore a valid owner of the same canonical execution
// envelope, not an unfenced writer.
type LeasedRuntime struct {
	runtimeFacade
	leases            session.SessionLeaseService
	ownerID           string
	ttl               time.Duration
	heartbeatInterval time.Duration
}

func NewLeasedRuntime(config LeasedRuntimeConfig) (*LeasedRuntime, error) {
	if config.Runtime == nil {
		return nil, fmt.Errorf("controlplane: leased runtime requires an execution runtime")
	}
	if config.Leases == nil {
		return nil, fmt.Errorf("controlplane: leased runtime requires a session lease service")
	}
	ownerID := strings.TrimSpace(config.OwnerID)
	if ownerID == "" {
		return nil, fmt.Errorf("controlplane: leased runtime requires owner_id")
	}
	ttl := config.TTL
	if ttl <= 0 {
		ttl = defaultSessionLeaseTTL
	}
	interval := config.HeartbeatInterval
	if interval <= 0 {
		interval = ttl / 3
	}
	if interval <= 0 || interval >= ttl {
		return nil, fmt.Errorf("controlplane: lease heartbeat interval must be positive and less than TTL")
	}
	return &LeasedRuntime{
		runtimeFacade:     newRuntimeFacade(config.Runtime),
		leases:            config.Leases,
		ownerID:           ownerID,
		ttl:               ttl,
		heartbeatInterval: interval,
	}, nil
}

// ExecutePlaced holds and heartbeats the session lease for the full synchronous
// callback. Lease loss cancels the callback context so work cannot continue
// under a stolen fence.
func (r *LeasedRuntime) ExecutePlaced(ctx context.Context, ref session.SessionRef, execute func(context.Context) error) error {
	return executeWithSessionLease(ctx, r.leases, r.ownerID, r.ttl, r.heartbeatInterval, ref, execute)
}

func (r *LeasedRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	return r.runWithLease(ctx, req.SessionRef, func(runCtx context.Context) (agent.RunResult, error) {
		return r.inner.Run(runCtx, req)
	})
}

func (r *LeasedRuntime) runWithLease(ctx context.Context, ref session.SessionRef, execute func(context.Context) (agent.RunResult, error)) (agent.RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ref = session.NormalizeSessionRef(ref)
	lease, err := r.acquireSessionLease(ctx, ref)
	if err != nil {
		return agent.RunResult{}, err
	}
	runCtx, cancel := context.WithCancel(session.ContextWithRuntimeLease(ctx, lease))
	guard := startSessionLeaseGuard(r.leases, lease, r.ttl, r.heartbeatInterval, cancel)
	result, err := execute(runCtx)
	if err != nil {
		cancel()
		return agent.RunResult{}, errors.Join(err, guard.finishAndErr())
	}
	if result.Handle == nil {
		cancel()
		return result, guard.finishAndErr()
	}
	return r.wrapLiveHandle(result, ref, func(inner agent.Runner, onFinish func()) agent.Runner {
		return newLeasedRunner(inner, guard, cancel, onFinish)
	}), nil
}

func (r *LeasedRuntime) acquireSessionLease(ctx context.Context, ref session.SessionRef) (session.SessionLease, error) {
	return acquireSessionLease(ctx, r.leases, r.ownerID, r.ttl, ref)
}

func executeWithSessionLease(
	ctx context.Context,
	leases session.SessionLeaseService,
	ownerID string,
	ttl time.Duration,
	heartbeatInterval time.Duration,
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
	lease, err := acquireSessionLease(ctx, leases, ownerID, ttl, ref)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(session.ContextWithRuntimeLease(ctx, lease))
	defer cancel()
	guard := startSessionLeaseGuard(leases, lease, ttl, heartbeatInterval, cancel)
	execErr := execute(runCtx)
	return errors.Join(execErr, guard.finishAndErr())
}

func acquireSessionLease(
	ctx context.Context,
	leases session.SessionLeaseService,
	ownerID string,
	ttl time.Duration,
	ref session.SessionRef,
) (session.SessionLease, error) {
	acquire := session.AcquireSessionLeaseRequest{SessionRef: ref, OwnerID: strings.TrimSpace(ownerID), TTL: ttl}
	lease, err := leases.AcquireSessionLease(ctx, acquire)
	if session.IsCommitted(err) {
		committedErr := err
		if !matchesAcquiredSessionLease(acquire, lease) {
			if reader, ok := leases.(session.SessionLeaseReader); ok {
				confirmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), min(ttl, 5*time.Second))
				durable, readErr := reader.SessionLease(confirmCtx, ref)
				cancel()
				if readErr != nil {
					err = errors.Join(committedErr, readErr)
				} else {
					lease = durable
					err = committedErr
				}
			}
		}
		if matchesAcquiredSessionLease(acquire, lease) {
			err = nil
		}
	}
	if err != nil {
		return session.SessionLease{}, err
	}
	return lease, nil
}

func matchesAcquiredSessionLease(req session.AcquireSessionLeaseRequest, lease session.SessionLease) bool {
	return session.NormalizeSessionRef(lease.SessionRef) == session.NormalizeSessionRef(req.SessionRef) &&
		strings.TrimSpace(lease.LeaseID) != "" &&
		strings.TrimSpace(lease.OwnerID) == strings.TrimSpace(req.OwnerID) &&
		lease.Revision > 0 && lease.FencingToken > 0
}

func (r *LeasedRuntime) PromptParticipant(ctx context.Context, req agent.PromptParticipantRequest) (agent.RunResult, error) {
	participants, err := r.participants()
	if err != nil {
		return agent.RunResult{}, err
	}
	return r.runWithLease(ctx, req.SessionRef, func(runCtx context.Context) (agent.RunResult, error) {
		return participants.PromptParticipant(runCtx, req)
	})
}

func (r *LeasedRuntime) release(lease session.SessionLease) error {
	return releaseSessionLease(r.leases, lease, r.ttl)
}

// sessionLeaseGuard is the single heartbeat/release machine used by both
// synchronous placed operations and asynchronous leased runners.
type sessionLeaseGuard struct {
	leases          session.SessionLeaseService
	ttl             time.Duration
	interval        time.Duration
	onLoss          func()
	heartbeatCtx    context.Context
	heartbeatCancel context.CancelFunc

	mu                   sync.Mutex
	lease                session.SessionLease
	heartbeatErr         error
	stopping             bool
	heartbeatInterrupted bool
	stop                 chan struct{}
	finishOnce           sync.Once
	finishErr            error
	wg                   sync.WaitGroup
}

var errSessionLeaseRenewalDeadline = errors.New("controlplane: session lease renewal did not complete before the expiry safety deadline")

func startSessionLeaseGuard(
	leases session.SessionLeaseService,
	lease session.SessionLease,
	ttl, interval time.Duration,
	onLoss func(),
) *sessionLeaseGuard {
	heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())
	guard := &sessionLeaseGuard{
		leases: leases, lease: lease, ttl: ttl, interval: interval, onLoss: onLoss,
		heartbeatCtx: heartbeatCtx, heartbeatCancel: heartbeatCancel, stop: make(chan struct{}),
	}
	guard.wg.Add(1)
	go guard.heartbeat()
	return guard
}

func (g *sessionLeaseGuard) heartbeat() {
	defer g.wg.Done()
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-ticker.C:
			if err := g.heartbeatOnce(); err != nil {
				g.mu.Lock()
				if g.stopping && errors.Is(err, context.Canceled) {
					// Finish cancelled a heartbeat that was waiting on durable
					// storage. Its commit outcome is reconciled before release;
					// an intentional shutdown is not lease loss.
					g.heartbeatInterrupted = true
					g.mu.Unlock()
					return
				}
				g.heartbeatErr = err
				onLoss := g.onLoss
				g.mu.Unlock()
				if onLoss != nil {
					onLoss()
				}
				return
			}
		}
	}
}

func (g *sessionLeaseGuard) setOnLoss(onLoss func()) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.onLoss = onLoss
	alreadyLost := g.heartbeatErr != nil
	g.mu.Unlock()
	if alreadyLost && onLoss != nil {
		onLoss()
	}
}

func (g *sessionLeaseGuard) heartbeatOnce() error {
	g.mu.Lock()
	lease := g.lease
	heartbeatCtx := g.heartbeatCtx
	g.mu.Unlock()
	if heartbeatCtx == nil {
		heartbeatCtx = context.Background()
	}
	deadline, err := sessionLeaseRenewalDeadline(lease, g.interval, time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(heartbeatCtx, deadline)
	next, err := g.leases.HeartbeatSessionLease(ctx, session.HeartbeatSessionLeaseRequest{
		SessionRef: lease.SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID,
		ExpectedLeaseRevision: lease.Revision, TTL: g.ttl,
	})
	cancel()
	if session.IsCommitted(err) {
		if next.LeaseID == lease.LeaseID && next.Revision > lease.Revision {
			err = nil
		} else if reader, ok := g.leases.(session.SessionLeaseReader); ok {
			ctx, cancel = context.WithDeadline(heartbeatCtx, deadline)
			next, err = reader.SessionLease(ctx, lease.SessionRef)
			cancel()
		}
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return errSessionLeaseRenewalDeadline
		}
		if errors.Is(err, session.ErrLeaseConflict) {
			return errors.New("controlplane: session lease ownership was lost during heartbeat")
		}
		return fmt.Errorf("controlplane: session lease heartbeat failed: %w", err)
	}
	if next.LeaseID != lease.LeaseID || next.OwnerID != lease.OwnerID || next.FencingToken != lease.FencingToken {
		return fmt.Errorf("controlplane: session lease identity changed during heartbeat")
	}
	if next.Revision <= lease.Revision {
		return fmt.Errorf("controlplane: session lease revision did not advance during heartbeat")
	}
	if !next.ExpiresAt.After(time.Now()) {
		return errSessionLeaseRenewalDeadline
	}
	g.mu.Lock()
	g.lease = next
	g.mu.Unlock()
	return nil
}

// sessionLeaseRenewalDeadline leaves one heartbeat interval (or half of a
// shorter remaining lifetime) for producer cancellation before the durable
// fence expires. A heartbeat must never wait for a fresh full TTL: that would
// let Runtime keep writing after the currently committed lease has expired.
func sessionLeaseRenewalDeadline(
	lease session.SessionLease,
	interval time.Duration,
	now time.Time,
) (time.Time, error) {
	expiresAt := lease.ExpiresAt
	if expiresAt.IsZero() {
		return time.Time{}, errSessionLeaseRenewalDeadline
	}
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return time.Time{}, errSessionLeaseRenewalDeadline
	}
	margin := interval
	if margin <= 0 {
		margin = remaining / 2
	}
	if margin >= remaining {
		margin = remaining / 2
	}
	deadline := expiresAt.Add(-margin)
	if !deadline.After(now) {
		return time.Time{}, errSessionLeaseRenewalDeadline
	}
	return deadline, nil
}

func (g *sessionLeaseGuard) err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.heartbeatErr
}

func (g *sessionLeaseGuard) finish() error {
	g.finishOnce.Do(func() {
		g.mu.Lock()
		g.stopping = true
		cancelHeartbeat := g.heartbeatCancel
		g.mu.Unlock()
		close(g.stop)
		if cancelHeartbeat != nil {
			cancelHeartbeat()
		}
		g.wg.Wait()
		g.mu.Lock()
		lease := g.lease
		interrupted := g.heartbeatInterrupted
		heartbeatErr := g.heartbeatErr
		g.mu.Unlock()
		if interrupted || heartbeatErr != nil {
			var active bool
			lease, active, g.finishErr = reconcileSessionLeaseForRelease(g.leases, lease, g.ttl)
			if g.finishErr != nil || !active {
				return
			}
		}
		g.finishErr = releaseSessionLease(g.leases, lease, g.ttl)
	})
	return g.finishErr
}

func (g *sessionLeaseGuard) finishAndErr() error {
	finishErr := g.finish()
	return errors.Join(g.err(), finishErr)
}

func releaseSessionLease(leases session.SessionLeaseService, lease session.SessionLease, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), min(ttl, 5*time.Second))
	defer cancel()
	err := leases.ReleaseSessionLease(ctx, session.ReleaseSessionLeaseRequest{
		SessionRef: lease.SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, ExpectedLeaseRevision: lease.Revision,
	})
	if session.IsCommitted(err) {
		return nil
	}
	return err
}

func reconcileSessionLeaseForRelease(
	leases session.SessionLeaseService,
	lease session.SessionLease,
	ttl time.Duration,
) (session.SessionLease, bool, error) {
	reader, ok := leases.(session.SessionLeaseReader)
	if !ok {
		// A release with the last confirmed revision is safe even if an
		// interrupted heartbeat committed: the store's revision CAS will reject
		// that stale release rather than clear a newer fence.
		return lease, true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), min(ttl, 5*time.Second))
	defer cancel()
	durable, err := reader.SessionLease(ctx, lease.SessionRef)
	if err != nil {
		return session.SessionLease{}, false, fmt.Errorf("controlplane: reconcile interrupted session lease heartbeat: %w", err)
	}
	if strings.TrimSpace(durable.LeaseID) == "" {
		return session.SessionLease{}, false, nil
	}
	if durable.LeaseID != lease.LeaseID || durable.OwnerID != lease.OwnerID || durable.FencingToken != lease.FencingToken {
		return session.SessionLease{}, false, fmt.Errorf("controlplane: session lease identity changed before release")
	}
	if durable.Revision < lease.Revision {
		return session.SessionLease{}, false, fmt.Errorf(
			"controlplane: session lease revision moved backwards before release: durable=%d confirmed=%d",
			durable.Revision,
			lease.Revision,
		)
	}
	return durable, true, nil
}

type leasedRunner struct {
	inner      agent.Runner
	guard      *sessionLeaseGuard
	cancel     context.CancelFunc
	onFinish   func()
	finishOnce sync.Once
	finishErr  error
}

func newLeasedRunner(inner agent.Runner, guard *sessionLeaseGuard, cancel context.CancelFunc, onFinish func()) agent.Runner {
	runner := &leasedRunner{inner: inner, guard: guard, cancel: cancel, onFinish: onFinish}
	guard.setOnLoss(func() {
		cancel()
		inner.Cancel()
	})
	if source, ok := inner.(agent.SourceHandle); ok {
		return &leasedSourceRunner{leasedRunner: runner, source: source}
	}
	return runner
}

func (r *leasedRunner) RunID() string { return r.inner.RunID() }

func (r *leasedRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		completed := true
		for event, err := range r.inner.Events() {
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

type leasedSourceRunner struct {
	*leasedRunner
	source agent.SourceHandle
}

func (r *leasedSourceRunner) SourceEvents() iter.Seq2[agent.SourceEvent, error] {
	return func(yield func(agent.SourceEvent, error) bool) {
		completed := true
		for event, err := range r.source.SourceEvents() {
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

func (r *leasedRunner) Submit(submission agent.Submission) error { return r.inner.Submit(submission) }

func (r *leasedRunner) Cancel() agent.CancelResult { return r.inner.Cancel() }

func (r *leasedRunner) Close() error {
	innerErr := r.inner.Close()
	finishErr := r.finishAfterProducer()
	return errors.Join(innerErr, finishErr, r.guard.err())
}

func (r *leasedRunner) finishAfterProducer() error {
	var waitErr error
	if waiter, ok := r.inner.(agent.RunnerCompletionWaiter); ok {
		waitErr = waiter.WaitCompletion(context.Background())
	}
	return errors.Join(waitErr, r.finish())
}

func (r *leasedRunner) finish() error {
	r.finishOnce.Do(func() {
		if r.cancel != nil {
			defer r.cancel()
		}
		if r.onFinish != nil {
			defer r.onFinish()
		}
		r.finishErr = r.guard.finishAndErr()
	})
	return r.finishErr
}
