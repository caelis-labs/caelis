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

// LeasedRuntime acquires a store-level session lease before dispatch and keeps
// it alive until the returned Runner completes or closes.
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
	if execute == nil {
		return fmt.Errorf("controlplane: placed operation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ref = session.NormalizeSessionRef(ref)
	lease, err := r.acquireSessionLease(ctx, ref)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(session.ContextWithRuntimeLease(ctx, lease))
	defer cancel()
	guard := startSessionLeaseGuard(r.leases, lease, r.ttl, r.heartbeatInterval, cancel)
	execErr := execute(runCtx)
	return errors.Join(execErr, guard.err(), guard.finish())
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
	ctx = session.ContextWithRuntimeLease(ctx, lease)
	result, err := execute(ctx)
	if err != nil {
		return agent.RunResult{}, errors.Join(err, r.release(lease))
	}
	if result.Handle == nil {
		return result, r.release(lease)
	}
	return r.wrapLiveHandle(result, func(inner agent.Runner, runID string) agent.Runner {
		return newLeasedRunner(inner, r.leases, lease, r.ttl, r.heartbeatInterval, func() { r.forgetRun(runID) })
	}), nil
}

func (r *LeasedRuntime) acquireSessionLease(ctx context.Context, ref session.SessionRef) (session.SessionLease, error) {
	acquire := session.AcquireSessionLeaseRequest{SessionRef: ref, OwnerID: r.ownerID, TTL: r.ttl}
	lease, err := r.leases.AcquireSessionLease(ctx, acquire)
	if session.IsCommitted(err) {
		committedErr := err
		if !matchesAcquiredSessionLease(acquire, lease) {
			if reader, ok := r.leases.(session.SessionLeaseReader); ok {
				confirmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), min(r.ttl, 5*time.Second))
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
	leases   session.SessionLeaseService
	ttl      time.Duration
	interval time.Duration
	onLoss   func()

	mu           sync.Mutex
	lease        session.SessionLease
	heartbeatErr error
	stop         chan struct{}
	finishOnce   sync.Once
	finishErr    error
	wg           sync.WaitGroup
}

func startSessionLeaseGuard(
	leases session.SessionLeaseService,
	lease session.SessionLease,
	ttl, interval time.Duration,
	onLoss func(),
) *sessionLeaseGuard {
	guard := &sessionLeaseGuard{
		leases: leases, lease: lease, ttl: ttl, interval: interval, onLoss: onLoss, stop: make(chan struct{}),
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
				g.heartbeatErr = err
				g.mu.Unlock()
				if g.onLoss != nil {
					g.onLoss()
				}
				return
			}
		}
	}
}

func (g *sessionLeaseGuard) heartbeatOnce() error {
	g.mu.Lock()
	lease := g.lease
	g.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), g.interval)
	next, err := g.leases.HeartbeatSessionLease(ctx, session.HeartbeatSessionLeaseRequest{
		SessionRef: lease.SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID,
		ExpectedLeaseRevision: lease.Revision, TTL: g.ttl,
	})
	cancel()
	if session.IsCommitted(err) {
		if next.LeaseID == lease.LeaseID && next.Revision > lease.Revision {
			err = nil
		} else if reader, ok := g.leases.(session.SessionLeaseReader); ok {
			ctx, cancel = context.WithTimeout(context.Background(), g.interval)
			next, err = reader.SessionLease(ctx, lease.SessionRef)
			cancel()
		}
	}
	if err != nil {
		return fmt.Errorf("controlplane: session lease heartbeat failed: %w", err)
	}
	if next.LeaseID != lease.LeaseID {
		return fmt.Errorf("controlplane: session lease identity changed during heartbeat")
	}
	g.mu.Lock()
	g.lease = next
	g.mu.Unlock()
	return nil
}

func (g *sessionLeaseGuard) err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.heartbeatErr
}

func (g *sessionLeaseGuard) finish() error {
	g.finishOnce.Do(func() {
		close(g.stop)
		g.wg.Wait()
		g.mu.Lock()
		lease := g.lease
		g.mu.Unlock()
		g.finishErr = releaseSessionLease(g.leases, lease, g.ttl)
	})
	return g.finishErr
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

type leasedRunner struct {
	inner      agent.Runner
	guard      *sessionLeaseGuard
	onFinish   func()
	finishOnce sync.Once
	finishErr  error
}

func newLeasedRunner(inner agent.Runner, leases session.SessionLeaseService, lease session.SessionLease, ttl, interval time.Duration, onFinish func()) agent.Runner {
	runner := &leasedRunner{inner: inner, onFinish: onFinish}
	runner.guard = startSessionLeaseGuard(leases, lease, ttl, interval, func() {
		runner.inner.Cancel()
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
		}
		if err := errors.Join(r.guard.err(), r.finish()); err != nil {
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
		}
		if err := errors.Join(r.guard.err(), r.finish()); err != nil {
			yield(agent.SourceEvent{}, err)
		}
	}
}

func (r *leasedRunner) Submit(submission agent.Submission) error { return r.inner.Submit(submission) }

func (r *leasedRunner) Cancel() agent.CancelResult { return r.inner.Cancel() }

func (r *leasedRunner) Close() error { return errors.Join(r.inner.Close(), r.finish()) }

func (r *leasedRunner) finish() error {
	r.finishOnce.Do(func() {
		if r.onFinish != nil {
			defer r.onFinish()
		}
		r.finishErr = r.guard.finish()
	})
	return r.finishErr
}
