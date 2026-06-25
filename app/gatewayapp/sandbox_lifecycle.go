package gatewayapp

import (
	"context"
	"fmt"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

type sandboxLifecycleAction func(context.Context, sandbox.Runtime) error

type sandboxLifecycleRuntimeFactory func(SandboxConfig, string, string) (sandbox.Runtime, SandboxConfig, bool, error)

type sandboxLifecycleSnapshot struct {
	exec         sandbox.Runtime
	cfg          SandboxConfig
	workspaceCWD string
	storeDir     string
}

type sandboxLifecycleTarget struct {
	runtime sandbox.Runtime
	cfg     SandboxConfig
	current bool
	noOp    bool
}

func (s *Stack) runSandboxLifecycle(ctx context.Context, action sandboxLifecycleAction) (SandboxStatus, error) {
	target, err := s.selectSandboxLifecycleTarget()
	if err != nil {
		return SandboxStatus{}, err
	}
	if target.noOp {
		return s.SandboxStatus(), nil
	}
	defer target.close()
	err = action(ctx, target.runtime)
	return s.sandboxLifecycleStatus(target), err
}

func (s *Stack) selectSandboxLifecycleTarget() (sandboxLifecycleTarget, error) {
	snapshot, err := s.sandboxLifecycleSnapshot()
	if err != nil {
		return sandboxLifecycleTarget{}, err
	}
	if shouldUseStackRuntimeForWindowsLifecycle(snapshot.exec) {
		return sandboxLifecycleTarget{
			runtime: snapshot.exec,
			cfg:     snapshot.cfg,
			current: true,
		}, nil
	}
	if isHostSandboxBackend(snapshot.cfg.RequestedType) {
		return sandboxLifecycleTarget{noOp: true}, nil
	}
	runtime, runtimeCfg, ok, err := s.sandboxLifecycleRuntime(snapshot.cfg, snapshot.workspaceCWD, snapshot.storeDir)
	if err != nil {
		return sandboxLifecycleTarget{}, err
	}
	if !ok {
		return sandboxLifecycleTarget{noOp: true}, nil
	}
	if runtime == nil {
		return sandboxLifecycleTarget{}, fmt.Errorf("gatewayapp: sandbox lifecycle runtime factory returned nil runtime")
	}
	return sandboxLifecycleTarget{
		runtime: runtime,
		cfg:     runtimeCfg,
	}, nil
}

func (s *Stack) sandboxLifecycleSnapshot() (sandboxLifecycleSnapshot, error) {
	if s == nil {
		return sandboxLifecycleSnapshot{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.exec == nil {
		return sandboxLifecycleSnapshot{}, fmt.Errorf("gatewayapp: sandbox runtime is unavailable")
	}
	return sandboxLifecycleSnapshot{
		exec:         s.exec,
		cfg:          s.sandbox,
		workspaceCWD: s.Workspace.CWD,
		storeDir:     s.storeDir,
	}, nil
}

func (s *Stack) sandboxLifecycleStatus(target sandboxLifecycleTarget) SandboxStatus {
	if target.current {
		return s.SandboxStatus()
	}
	return sandboxStatusFromRuntime(target.cfg, target.runtime)
}

func (target sandboxLifecycleTarget) close() {
	if target.current {
		return
	}
	_ = target.runtime.Close()
}

func prepareSandboxRuntime(ctx context.Context, runtime sandbox.Runtime) error {
	preparer, ok := runtime.(sandbox.PreparableRuntime)
	if !ok {
		return nil
	}
	return preparer.Prepare(ctx)
}

func repairSandboxRuntime(ctx context.Context, runtime sandbox.Runtime) error {
	if repairer, ok := runtime.(sandbox.RepairableRuntime); ok {
		return repairer.Repair(ctx)
	}
	return prepareSandboxRuntime(ctx, runtime)
}

func resetSandboxRuntime(ctx context.Context, runtime sandbox.Runtime) error {
	resetter, ok := runtime.(sandbox.ResettableRuntime)
	if !ok {
		return nil
	}
	return resetter.Reset(ctx)
}

func shouldUseStackRuntimeForWindowsLifecycle(exec sandbox.Runtime) bool {
	if exec == nil {
		return false
	}
	status := sandbox.SelectionStatus(exec)
	return sandbox.CanonicalBackend(status.ResolvedBackend) == sandbox.BackendWindows ||
		sandbox.CanonicalBackend(status.RequestedBackend) == sandbox.BackendWindows
}

func isHostSandboxBackend(backend string) bool {
	return sandbox.CanonicalBackend(sandbox.Backend(backend)) == sandbox.BackendHost
}

func (s *Stack) sandboxLifecycleRuntime(cfg SandboxConfig, workspaceCWD string, storeDir string) (sandbox.Runtime, SandboxConfig, bool, error) {
	if s != nil && s.sandboxLifecycleFactory != nil {
		return s.sandboxLifecycleFactory(cfg, workspaceCWD, storeDir)
	}
	return windowsSandboxRuntime(cfg, workspaceCWD, storeDir)
}
