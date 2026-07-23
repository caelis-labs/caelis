package gatewayapp

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
)

type sandboxLifecycleAction = sandbox.LifecycleAction

type sandboxLifecycleRuntimeFactory func(sandbox.Config, sandbox.Runtime) (sandbox.LifecycleTarget, error)

type sandboxLifecycleSnapshot struct {
	exec         sandbox.Runtime
	cfg          SandboxConfig
	workspaceCWD string
	storeDir     string
}

func (s *Stack) runSandboxLifecycle(ctx context.Context, action sandboxLifecycleAction) (SandboxStatus, error) {
	target, err := s.selectSandboxLifecycleTarget()
	if err != nil {
		return SandboxStatus{}, err
	}
	if target.NoOp {
		return s.SandboxStatus(), nil
	}
	defer func() {
		_ = target.Close()
	}()
	err = action(ctx, target.Runtime)
	return s.sandboxLifecycleStatus(target), err
}

func (s *Stack) selectSandboxLifecycleTarget() (sandbox.LifecycleTarget, error) {
	snapshot, err := s.sandboxLifecycleSnapshot()
	if err != nil {
		return sandbox.LifecycleTarget{}, err
	}
	if isHostSandboxBackend(snapshot.cfg.RequestedType) {
		cfg := sandboxConfigToPort(snapshot.cfg, snapshot.workspaceCWD, snapshot.storeDir)
		return sandbox.LifecycleTarget{Config: cfg, NoOp: true}, nil
	}
	target, err := s.sandboxLifecycleRuntime(snapshot.cfg, snapshot.exec, snapshot.workspaceCWD, snapshot.storeDir)
	if err != nil {
		return sandbox.LifecycleTarget{}, err
	}
	if target.NoOp {
		return target, nil
	}
	if target.Runtime == nil {
		return sandbox.LifecycleTarget{}, fmt.Errorf("gatewayapp: sandbox lifecycle runtime factory returned nil runtime")
	}
	return target, nil
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

func (s *Stack) sandboxLifecycleStatus(target sandbox.LifecycleTarget) SandboxStatus {
	if target.Current {
		return s.SandboxStatus()
	}
	return sandboxStatusFromRuntime(sandboxConfigFromPort(target.Config), target.Runtime)
}

func prepareSandboxRuntime(ctx context.Context, runtime sandbox.Runtime) error {
	return sandbox.PrepareRuntime(ctx, runtime)
}

func repairSandboxRuntime(ctx context.Context, runtime sandbox.Runtime) error {
	return sandbox.RepairRuntime(ctx, runtime)
}

func resetSandboxRuntime(ctx context.Context, runtime sandbox.Runtime) error {
	return sandbox.ResetRuntime(ctx, runtime)
}

func isHostSandboxBackend(backend string) bool {
	return sandbox.CanonicalBackend(sandbox.Backend(backend)) == sandbox.BackendHost
}

func (s *Stack) sandboxLifecycleRuntime(cfg SandboxConfig, current sandbox.Runtime, workspaceCWD string, storeDir string) (sandbox.LifecycleTarget, error) {
	portCfg := sandboxConfigToPort(cfg, workspaceCWD, storeDir)
	if s != nil && s.sandboxLifecycleFactory != nil {
		return s.sandboxLifecycleFactory(portCfg, current)
	}
	return sandbox.LifecycleTargetFor(portCfg, current)
}

func sandboxConfigToPort(cfg SandboxConfig, workspaceCWD string, storeDir string) sandbox.Config {
	cfg = configstore.DefaultSandboxConfig(cfg)
	return sandbox.Config{
		CWD:              workspaceCWD,
		RequestedBackend: sandbox.Backend(cfg.RequestedType),
		HelperPath:       cfg.HelperPath,
		StateDir:         storeDir,
		WritableRoots:    append([]string(nil), cfg.WritableRoots...),
		ReadOnlySubpaths: append([]string(nil), cfg.ReadOnlySubpaths...),
	}
}

func sandboxConfigFromPort(cfg sandbox.Config) SandboxConfig {
	return SandboxConfig{
		RequestedType:    string(cfg.RequestedBackend),
		HelperPath:       cfg.HelperPath,
		WritableRoots:    append([]string(nil), cfg.WritableRoots...),
		ReadOnlySubpaths: append([]string(nil), cfg.ReadOnlySubpaths...),
	}
}
