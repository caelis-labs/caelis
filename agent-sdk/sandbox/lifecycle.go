package sandbox

import (
	"context"
	"fmt"
	"sync"
)

type LifecycleAction func(context.Context, Runtime) error

type LifecycleTarget struct {
	Runtime Runtime
	Config  Config
	Current bool
	NoOp    bool
}

func (t LifecycleTarget) Close() error {
	if t.Current || t.NoOp || t.Runtime == nil {
		return nil
	}
	return t.Runtime.Close()
}

type LifecycleFactory interface {
	Backend() Backend
	BuildLifecycle(Config) (Runtime, error)
}

var (
	lifecycleFactoriesMu sync.RWMutex
	lifecycleFactories   = map[Backend]LifecycleFactory{}
)

func RegisterLifecycleFactory(factory LifecycleFactory) error {
	if factory == nil {
		return fmt.Errorf("ports/sandbox: lifecycle factory is required")
	}
	backend := CanonicalBackend(factory.Backend())
	if backend == "" {
		return fmt.Errorf("ports/sandbox: lifecycle factory must declare a backend")
	}
	lifecycleFactoriesMu.Lock()
	defer lifecycleFactoriesMu.Unlock()
	if _, exists := lifecycleFactories[backend]; exists {
		return fmt.Errorf("ports/sandbox: duplicated lifecycle factory %q", backend)
	}
	lifecycleFactories[backend] = factory
	return nil
}

func RegisterBuiltInLifecycleFactory(factory LifecycleFactory) {
	if err := RegisterLifecycleFactory(factory); err != nil {
		backendFactoriesMu.Lock()
		backendRegistrationErrs = append(backendRegistrationErrs, err)
		backendFactoriesMu.Unlock()
	}
}

func LifecycleTargetFor(cfg Config, current Runtime) (LifecycleTarget, error) {
	cfg = NormalizeConfig(cfg)
	requested := CanonicalBackend(cfg.RequestedBackend)
	if current != nil && runtimeMatchesLifecycleBackend(current, requested) && runtimeHasLifecycle(current) {
		return LifecycleTarget{
			Runtime: current,
			Config:  cfg,
			Current: true,
		}, nil
	}
	if requested == BackendHost {
		return LifecycleTarget{Config: cfg, NoOp: true}, nil
	}
	if requested == "" {
		return LifecycleTarget{Config: cfg, NoOp: true}, nil
	}
	lifecycleFactoriesMu.RLock()
	factory, ok := lifecycleFactories[requested]
	lifecycleFactoriesMu.RUnlock()
	if !ok {
		return LifecycleTarget{Config: cfg, NoOp: true}, nil
	}
	cfg.RequestedBackend = requested
	runtime, err := factory.BuildLifecycle(cfg)
	if err != nil {
		return LifecycleTarget{}, err
	}
	if runtime == nil {
		return LifecycleTarget{}, fmt.Errorf("ports/sandbox: lifecycle factory %q returned a nil runtime", requested)
	}
	return LifecycleTarget{
		Runtime: runtime,
		Config:  cfg,
	}, nil
}

func runtimeMatchesLifecycleBackend(runtime Runtime, requested Backend) bool {
	if runtime == nil {
		return false
	}
	status := SelectionStatus(runtime)
	resolved := CanonicalBackend(status.ResolvedBackend)
	req := CanonicalBackend(status.RequestedBackend)
	if requested == "" {
		return resolved != "" && resolved != BackendHost
	}
	return resolved == requested || req == requested
}

func runtimeHasLifecycle(runtime Runtime) bool {
	if runtime == nil {
		return false
	}
	if _, ok := runtime.(PreparableRuntime); ok {
		return true
	}
	if _, ok := runtime.(RepairableRuntime); ok {
		return true
	}
	if _, ok := runtime.(ResettableRuntime); ok {
		return true
	}
	if _, ok := runtime.(PreflightRuntime); ok {
		return true
	}
	if _, ok := runtime.(RefreshableRuntime); ok {
		return true
	}
	return false
}

func PrepareRuntime(ctx context.Context, runtime Runtime) error {
	preparer, ok := runtime.(PreparableRuntime)
	if !ok {
		return nil
	}
	return preparer.Prepare(ctx)
}

func RepairRuntime(ctx context.Context, runtime Runtime) error {
	if repairer, ok := runtime.(RepairableRuntime); ok {
		return repairer.Repair(ctx)
	}
	return PrepareRuntime(ctx, runtime)
}

func ResetRuntime(ctx context.Context, runtime Runtime) error {
	resetter, ok := runtime.(ResettableRuntime)
	if !ok {
		return nil
	}
	return resetter.Reset(ctx)
}
