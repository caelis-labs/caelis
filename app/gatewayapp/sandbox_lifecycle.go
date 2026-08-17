package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
)

type sandboxLifecycleAction = sandbox.LifecycleAction

type sandboxLifecycleRuntimeFactory func(sandbox.Config, sandbox.Runtime) (sandbox.LifecycleTarget, error)

type sandboxLifecycleSnapshot struct {
	exec         sandbox.Runtime
	workspaceCWD string
	storeDir     string
}

type sandboxLifecycleCommand struct {
	action  sandboxLifecycleAction
	refresh bool
}

// runSandboxLifecycleCommand fixes the canonical revision before selecting a
// disposable lifecycle target. It never holds the configuration write boundary
// or mutates an activated Session Runtime across the external setup effect.
// The bool reports whether an effect was invoked (or cleanup became uncertain).
func (s *Stack) runSandboxLifecycleCommand(
	ctx context.Context,
	command sandboxLifecycleCommand,
	expected *uint64,
) (SandboxStatus, uint64, bool, error) {
	if s == nil {
		return SandboxStatus{}, 0, false, errorcode.New(errorcode.Unavailable, "gatewayapp: stack is unavailable")
	}
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return SandboxStatus{}, 0, false, err
	}
	doc, err := s.loadSandboxConfigDocument(ctx, expected)
	if err != nil {
		return SandboxStatus{}, doc.ConfigurationRevision, false, err
	}
	target, err := s.selectSandboxLifecycleTarget(doc.Sandbox)
	if err != nil {
		return SandboxStatus{}, doc.ConfigurationRevision, false, err
	}
	if command.refresh {
		if target.NoOp {
			return s.sandboxLifecycleStatus(target), doc.ConfigurationRevision, false, nil
		}
		refresher, ok := target.Runtime.(sandbox.RefreshableRuntime)
		if !ok {
			status := s.sandboxLifecycleStatus(target)
			return status, doc.ConfigurationRevision, false, target.Close()
		}
		if err := ctx.Err(); err != nil {
			return SandboxStatus{}, doc.ConfigurationRevision, false, errors.Join(err, target.Close())
		}
		effectErr := refresher.Refresh(ctx)
		status := s.sandboxLifecycleStatus(target)
		return status, doc.ConfigurationRevision, true, errors.Join(effectErr, target.Close())
	}
	if target.NoOp {
		return s.runtimeProjection().SandboxStatus(), doc.ConfigurationRevision, false, nil
	}
	if err := ctx.Err(); err != nil {
		closeErr := target.Close()
		return SandboxStatus{}, doc.ConfigurationRevision, closeErr != nil, errors.Join(err, closeErr)
	}
	effectErr := command.action(ctx, target.Runtime)
	status := s.sandboxLifecycleStatus(target)
	closeErr := target.Close()
	return status, doc.ConfigurationRevision, true, errors.Join(effectErr, closeErr)
}

func (s *Stack) selectSandboxLifecycleTarget(config SandboxConfig) (sandbox.LifecycleTarget, error) {
	snapshot, err := s.sandboxLifecycleSnapshot()
	if err != nil {
		return sandbox.LifecycleTarget{}, err
	}
	s.composition.mu.RLock()
	override := cloneSandboxConfig(s.composition.sandboxOverride)
	s.composition.mu.RUnlock()
	config = mergeSandboxConfig(config, override)
	if isHostSandboxBackend(config.RequestedType) {
		cfg := sandboxConfigToPort(config, snapshot.workspaceCWD, snapshot.storeDir)
		return sandbox.LifecycleTarget{Config: cfg, NoOp: true}, nil
	}
	target, err := s.sandboxLifecycleRuntime(config, snapshot.exec, snapshot.workspaceCWD, snapshot.storeDir)
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
	s.composition.mu.RLock()
	defer s.composition.mu.RUnlock()
	if s.composition.exec == nil {
		return sandboxLifecycleSnapshot{}, fmt.Errorf("gatewayapp: sandbox runtime is unavailable")
	}
	return sandboxLifecycleSnapshot{
		exec:         s.composition.exec,
		workspaceCWD: s.composition.workspace.CWD,
		storeDir:     s.composition.authorities.storeDir,
	}, nil
}

func (s *Stack) sandboxLifecycleStatus(target sandbox.LifecycleTarget) SandboxStatus {
	if target.Current {
		return s.runtimeProjection().SandboxStatus()
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
