package local

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
)

type liveSandboxRuntime struct {
	mu      sync.RWMutex
	current sandbox.Runtime
	retired []sandbox.Runtime
	closed  bool
}

func newLiveSandboxRuntime(current sandbox.Runtime) (*liveSandboxRuntime, error) {
	if current == nil {
		return nil, errors.New("app/local: live sandbox requires an initial runtime")
	}
	return &liveSandboxRuntime{current: current}, nil
}

func (r *liveSandboxRuntime) replace(next sandbox.Runtime) error {
	if next == nil {
		return errors.New("app/local: replacement sandbox runtime is required")
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = next.Close()
		return errors.New("app/local: live sandbox is closed")
	}
	previous := r.current
	r.current = next
	if previous != nil && previous != next {
		r.retired = append(r.retired, previous)
	}
	r.mu.Unlock()
	return nil
}

func (r *liveSandboxRuntime) runtime() (sandbox.Runtime, error) {
	if r == nil {
		return nil, errors.New("app/local: live sandbox is nil")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, errors.New("app/local: live sandbox is closed")
	}
	if r.current == nil {
		return nil, errors.New("app/local: sandbox runtime is unavailable")
	}
	return r.current, nil
}

func (r *liveSandboxRuntime) CurrentSandboxRuntime() (sandbox.Runtime, error) {
	return r.runtime()
}

func (r *liveSandboxRuntime) runtimes() ([]sandbox.Runtime, error) {
	if r == nil {
		return nil, errors.New("app/local: live sandbox is nil")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, errors.New("app/local: live sandbox is closed")
	}
	out := make([]sandbox.Runtime, 0, 1+len(r.retired))
	if r.current != nil {
		out = append(out, r.current)
	}
	out = append(out, r.retired...)
	if len(out) == 0 {
		return nil, errors.New("app/local: sandbox runtime is unavailable")
	}
	return out, nil
}

func (r *liveSandboxRuntime) Descriptor() sandbox.Descriptor {
	current, err := r.runtime()
	if err != nil {
		return sandbox.Descriptor{}
	}
	return current.Descriptor()
}

func (r *liveSandboxRuntime) Status() sandbox.Status {
	current, err := r.runtime()
	if err != nil {
		return sandbox.Status{}
	}
	return current.Status()
}

func (r *liveSandboxRuntime) FileSystem() sandbox.FileSystem {
	current, err := r.runtime()
	if err != nil {
		return nil
	}
	return current.FileSystem()
}

func (r *liveSandboxRuntime) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	current, err := r.runtime()
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	return current.Run(ctx, req)
}

func (r *liveSandboxRuntime) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	current, err := r.runtime()
	if err != nil {
		return nil, err
	}
	return current.Start(ctx, req)
}

func (r *liveSandboxRuntime) Open(ctx context.Context, ref sandbox.SessionRef) (sandbox.Session, error) {
	runtimes, err := r.runtimes()
	if err != nil {
		return nil, err
	}
	var firstErr error
	for _, runtime := range runtimes {
		if runtime == nil {
			continue
		}
		if ref.Backend != "" && runtime.Descriptor().Backend != "" && ref.Backend != runtime.Descriptor().Backend {
			continue
		}
		session, err := runtime.Open(ctx, ref)
		if err == nil {
			return session, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("app/local: sandbox session backend %q is unavailable", ref.Backend)
}

func (r *liveSandboxRuntime) ListSessions(ctx context.Context, query sandbox.SessionListQuery) ([]sandbox.SessionSnapshot, error) {
	runtimes, err := r.runtimes()
	if err != nil {
		return nil, err
	}
	var out []sandbox.SessionSnapshot
	seen := map[string]struct{}{}
	foundLister := false
	for _, runtime := range runtimes {
		lister, ok := runtime.(sandbox.SessionLister)
		if !ok {
			continue
		}
		foundLister = true
		listed, err := lister.ListSessions(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, snapshot := range listed {
			key := string(snapshot.Ref.Backend) + ":" + snapshot.Ref.ID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, snapshot)
		}
	}
	if !foundLister {
		return nil, fmt.Errorf("app/local: sandbox backend %q does not support session listing", r.Descriptor().Backend)
	}
	if query.Limit > 0 && len(out) > query.Limit {
		return out[:query.Limit], nil
	}
	return out, nil
}

func (r *liveSandboxRuntime) Prepare(ctx context.Context) error {
	current, err := r.runtime()
	if err != nil {
		return err
	}
	preparer, ok := current.(sandbox.PreparableRuntime)
	if !ok {
		return nil
	}
	return preparer.Prepare(ctx)
}

func (r *liveSandboxRuntime) Repair(ctx context.Context) error {
	current, err := r.runtime()
	if err != nil {
		return err
	}
	if repairer, ok := current.(sandbox.RepairableRuntime); ok {
		return repairer.Repair(ctx)
	}
	if preparer, ok := current.(sandbox.PreparableRuntime); ok {
		return preparer.Prepare(ctx)
	}
	return nil
}

func (r *liveSandboxRuntime) Preflight(ctx context.Context, opts sandbox.PreflightOptions) error {
	current, err := r.runtime()
	if err != nil {
		return err
	}
	preflight, ok := current.(sandbox.PreflightRuntime)
	if !ok {
		return nil
	}
	return preflight.Preflight(ctx, opts)
}

func (r *liveSandboxRuntime) Reset(ctx context.Context) error {
	current, err := r.runtime()
	if err != nil {
		return err
	}
	resetter, ok := current.(sandbox.ResettableRuntime)
	if !ok {
		return nil
	}
	return resetter.Reset(ctx)
}

func (r *liveSandboxRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	current := r.current
	retired := append([]sandbox.Runtime(nil), r.retired...)
	r.current = nil
	r.retired = nil
	r.mu.Unlock()
	var err error
	if current != nil {
		err = current.Close()
	}
	for _, runtime := range retired {
		if runtime == nil || runtime == current {
			continue
		}
		err = errors.Join(err, runtime.Close())
	}
	return err
}

var _ sandbox.Runtime = (*liveSandboxRuntime)(nil)
var _ sandbox.SessionLister = (*liveSandboxRuntime)(nil)
var _ sandbox.PreparableRuntime = (*liveSandboxRuntime)(nil)
var _ sandbox.RepairableRuntime = (*liveSandboxRuntime)(nil)
var _ sandbox.PreflightRuntime = (*liveSandboxRuntime)(nil)
var _ sandbox.ResettableRuntime = (*liveSandboxRuntime)(nil)
