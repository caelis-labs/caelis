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
	rtStatus := exec.Status()
	if strings.TrimSpace(string(rtStatus.RequestedBackend)) != "" {
		status.RequestedBackend = string(rtStatus.RequestedBackend)
	}
	if strings.TrimSpace(string(rtStatus.ResolvedBackend)) != "" {
		status.ResolvedBackend = string(rtStatus.ResolvedBackend)
	}
	status.FallbackReason = strings.TrimSpace(rtStatus.FallbackReason)
	status.InstallHint = strings.TrimSpace(rtStatus.FallbackInstallHint)
	status.SetupRequired = rtStatus.SetupRequired
	status.SetupError = strings.TrimSpace(rtStatus.SetupError)
	status.SetupVersion = rtStatus.SetupVersion
	status.SetupMarkerCurrent = rtStatus.SetupMarkerCurrent
	status.SetupMarkerReason = strings.TrimSpace(rtStatus.SetupMarkerReason)
	status.SetupRunnerHash = strings.TrimSpace(rtStatus.SetupRunnerHash)
	status.SetupPolicyHash = strings.TrimSpace(rtStatus.SetupPolicyHash)
	status.SetupOfflineUser = strings.TrimSpace(rtStatus.SetupOfflineUser)
	status.SetupOnlineUser = strings.TrimSpace(rtStatus.SetupOnlineUser)
	status.SetupOwnerUser = strings.TrimSpace(rtStatus.SetupOwnerUser)
	status.SetupReadRoots = rtStatus.SetupReadRootCount
	status.SetupWriteRoots = rtStatus.SetupWriteRootCount
	status.SetupDenyRead = rtStatus.SetupDenyReadCount
	status.SetupDenyWrite = rtStatus.SetupDenyWriteCount
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
