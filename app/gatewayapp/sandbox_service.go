package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/sandboxpolicy"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func (s *Stack) SetSandboxBackend(_ context.Context, backend string) (SandboxStatus, error) {
	if s == nil {
		return SandboxStatus{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	if err := s.rejectReconfigureWhileActive("change sandbox backend"); err != nil {
		return SandboxStatus{}, err
	}
	normalized, err := normalizeSandboxBackend(backend)
	if err != nil {
		return SandboxStatus{}, err
	}
	s.mu.RLock()
	previous := s.sandbox
	s.mu.RUnlock()
	next := previous
	next.RequestedType = normalized
	if err := s.saveSandboxConfigValue(next); err != nil {
		return SandboxStatus{}, err
	}
	s.mu.Lock()
	s.sandbox = next
	s.mu.Unlock()
	if err := s.rebuildGateway(); err != nil {
		s.mu.Lock()
		s.sandbox = previous
		s.mu.Unlock()
		if rollbackErr := s.saveSandboxConfigValue(previous); rollbackErr != nil {
			return SandboxStatus{}, errors.Join(err, rollbackErr)
		}
		return SandboxStatus{}, err
	}
	return s.SandboxStatus(), nil
}

func (s *Stack) SandboxStatus() SandboxStatus {
	return s.sandboxStatus(false)
}

func (s *Stack) SandboxStartupStatus() SandboxStatus {
	return s.sandboxStatus(true)
}

func (s *Stack) sandboxStatus(startup bool) SandboxStatus {
	if s == nil {
		return SandboxStatus{}
	}
	s.mu.RLock()
	cfg := s.sandbox
	exec := s.exec
	s.mu.RUnlock()
	status := SandboxStatus{
		RequestedBackend: cfg.RequestedType,
		Route:            string(sandbox.RouteSandbox),
		SecuritySummary:  "sandbox",
	}
	if status.RequestedBackend == "" {
		status.RequestedBackend = "auto"
	}
	if exec == nil {
		status.ResolvedBackend = status.RequestedBackend
		return status
	}
	var rtStatus sandbox.Status
	if startup {
		rtStatus = sandbox.SelectionStatus(exec)
	} else {
		rtStatus = exec.Status()
	}
	if strings.TrimSpace(string(rtStatus.RequestedBackend)) != "" {
		status.RequestedBackend = string(rtStatus.RequestedBackend)
	}
	if strings.TrimSpace(string(rtStatus.ResolvedBackend)) != "" {
		status.ResolvedBackend = string(rtStatus.ResolvedBackend)
	}
	status.FallbackReason = strings.TrimSpace(rtStatus.FallbackReason)
	status.InstallHint = strings.TrimSpace(rtStatus.FallbackInstallHint)
	status.Setup = sandbox.CloneSetupStatus(rtStatus.Setup)
	applySandboxSetupProjection(&status, status.Setup)
	if rtStatus.FallbackToHost {
		status.Route = string(sandbox.RouteHost)
		status.SecuritySummary = "host fallback"
		if status.ResolvedBackend == "" {
			status.ResolvedBackend = string(sandbox.BackendHost)
		}
	} else if status.ResolvedBackend != "" {
		status.SecuritySummary = status.ResolvedBackend
	}
	if status.ResolvedBackend == "" {
		status.ResolvedBackend = status.RequestedBackend
	}
	return status
}

func applySandboxSetupProjection(status *SandboxStatus, setup sandbox.SetupStatus) {
	if status == nil {
		return
	}
	status.SetupRequired = setup.Required
	status.SetupError = strings.TrimSpace(setup.Error)
	if global, ok := setup.Check("global"); ok {
		status.SetupVersion = global.Version
		status.SetupMarkerCurrent = global.Current
		status.SetupMarkerReason = strings.TrimSpace(global.Reason)
		status.SetupRunnerHash = strings.TrimSpace(global.Details["runner_hash"])
		status.SetupPolicyHash = strings.TrimSpace(global.Details["policy_hash"])
		status.SetupOfflineUser = strings.TrimSpace(global.Details["offline_user"])
		status.SetupOnlineUser = strings.TrimSpace(global.Details["online_user"])
		status.SetupOwnerUser = strings.TrimSpace(global.Details["owner_user"])
		status.GlobalSetupCurrent = global.Current
		status.GlobalSetupRequired = global.Required
		status.GlobalSetupReason = strings.TrimSpace(global.Reason)
		if status.SetupError == "" {
			status.SetupError = strings.TrimSpace(global.Error)
		}
	}
	if workspace, ok := setup.Check("workspace"); ok {
		if status.SetupPolicyHash == "" {
			status.SetupPolicyHash = strings.TrimSpace(workspace.Details["policy_hash"])
		}
		status.SetupReadRoots = workspace.Counts["read_roots"]
		status.SetupWriteRoots = workspace.Counts["write_roots"]
		status.SetupDenyRead = workspace.Counts["deny_read"]
		status.SetupDenyWrite = workspace.Counts["deny_write"]
		status.WorkspaceSetupCurrent = workspace.Current
		status.WorkspaceSetupRequired = workspace.Required
		status.WorkspaceSetupReason = strings.TrimSpace(workspace.Reason)
		status.WorkspaceSetupRoot = strings.TrimSpace(workspace.Root)
		status.WorkspaceSetupWriteRoots = workspace.Counts["write_roots"]
		status.WorkspaceSetupPolicyHash = strings.TrimSpace(workspace.Details["policy_hash"])
		status.WorkspaceSetupUpdatedAt = workspace.UpdatedAt
		if status.SetupError == "" {
			status.SetupError = strings.TrimSpace(workspace.Error)
		}
	}
}

func (s *Stack) PrepareSandbox(ctx context.Context) (SandboxStatus, error) {
	if s == nil {
		return SandboxStatus{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	exec := s.exec
	s.mu.RUnlock()
	if exec == nil {
		return SandboxStatus{}, fmt.Errorf("gatewayapp: sandbox runtime is unavailable")
	}
	preparer, ok := exec.(sandbox.PreparableRuntime)
	if !ok {
		return s.SandboxStatus(), nil
	}
	err := preparer.Prepare(ctx)
	return s.SandboxStatus(), err
}

func (s *Stack) PreflightSandbox(ctx context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
	if s == nil {
		return SandboxStatus{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	exec := s.exec
	s.mu.RUnlock()
	if exec == nil {
		return SandboxStatus{}, fmt.Errorf("gatewayapp: sandbox runtime is unavailable")
	}
	preflight, ok := exec.(sandbox.PreflightRuntime)
	if !ok {
		return s.SandboxStatus(), nil
	}
	err := preflight.Preflight(ctx, sandbox.PreflightOptions{AllowNonElevatedRepair: allowNonElevatedRepair})
	return s.SandboxStatus(), err
}

func (s *Stack) ResetSandbox(ctx context.Context) (SandboxStatus, error) {
	if s == nil {
		return SandboxStatus{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	exec := s.exec
	s.mu.RUnlock()
	if exec == nil {
		return SandboxStatus{}, fmt.Errorf("gatewayapp: sandbox runtime is unavailable")
	}
	resetter, ok := exec.(sandbox.ResettableRuntime)
	if !ok {
		return s.SandboxStatus(), nil
	}
	err := resetter.Reset(ctx)
	return s.SandboxStatus(), err
}

func normalizeSandboxBackend(backend string) (string, error) {
	return sandboxpolicy.NormalizeBackend(backend)
}

func mergeSandboxConfig(stored SandboxConfig, override SandboxConfig) SandboxConfig {
	return sandboxpolicy.MergeConfig(stored, override)
}

func effectiveSandboxConfig(cfg SandboxConfig, workspaceDir string) SandboxConfig {
	return sandboxpolicy.EffectiveConfig(cfg, workspaceDir)
}

func withSandboxPolicyRootMetadata(metadata map[string]any, cfg SandboxConfig, workspaceDir string) map[string]any {
	return sandboxpolicy.WithPolicyRootMetadata(metadata, cfg, workspaceDir)
}

func defaultSkillSandboxRoots(workspaceDir string) []string {
	return sandboxpolicy.DefaultSkillRoots(workspaceDir)
}
