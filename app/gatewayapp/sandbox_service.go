package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/sandboxpolicy"
)

func (s *Stack) setSandboxBackendAtRevision(ctx context.Context, backend string, expected *uint64) (SandboxStatus, uint64, error) {
	if s == nil {
		return SandboxStatus{}, 0, configurationRejectedError(errorcode.New(errorcode.Unavailable, "gatewayapp: stack is unavailable"))
	}
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return SandboxStatus{}, 0, configurationRejectedError(err)
	}
	doc, err := s.loadSandboxConfigDocument(ctx, expected)
	if err != nil {
		return SandboxStatus{}, doc.ConfigurationRevision, classifyConfigurationPreEffectError(err)
	}
	currentRevision := doc.ConfigurationRevision
	normalized, err := normalizeSandboxBackend(backend)
	if err != nil {
		return SandboxStatus{}, currentRevision, configurationRejectedError(errorcode.Wrap(errorcode.InvalidArgument, err.Error(), err))
	}
	process := s.composition.runtimeProcessSnapshot()
	securityPosture := resolveProcessSecurityPosture(process.runtime)
	runtimeOverride := process.sandboxOverride
	if err := securityPosture.validateSandboxBackend(sandbox.Backend(normalized)); err != nil {
		return SandboxStatus{}, currentRevision, configurationRejectedError(errorcode.Wrap(errorcode.FailedPrecondition, err.Error(), err))
	}
	if securityPosture.RequiredSandboxBackend != "" {
		return s.runtimeProjection().SandboxStatus(), currentRevision, nil
	}
	nextCanonical := cloneSandboxConfig(doc.Sandbox)
	nextCanonical.RequestedType = normalized
	doc.Sandbox = nextCanonical
	saved, persistErr := s.persistSandboxConfigDocument(ctx, doc)
	if persistErr != nil && !configstore.WriteCommitted(persistErr) {
		return SandboxStatus{}, currentRevision, classifyConfigurationPreEffectError(persistErr)
	}

	observed := saved
	observedErr := error(nil)
	observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if current, loadErr := s.loadSandboxConfigDocument(observeCtx, nil); loadErr != nil {
		observedErr = loadErr
	} else if current.ConfigurationRevision >= saved.ConfigurationRevision {
		observed = current
	}
	s.composition.mu.Lock()
	s.composition.sandboxPersisted = cloneSandboxConfig(observed.Sandbox)
	s.composition.sandboxRevision = observed.ConfigurationRevision
	s.composition.mu.Unlock()
	nextLive := mergeSandboxConfig(observed.Sandbox, runtimeOverride)
	status := sandboxStatusFromRuntime(nextLive, nil)
	return securityPosture.applySandboxStatus(status), observed.ConfigurationRevision, errors.Join(persistErr, observedErr)
}

func (s *runtimeComposition) SandboxStatus() SandboxStatus {
	return s.sandboxStatus(true)
}

// SandboxStatusForWorkspace projects only Runtime-specific setup data that
// belongs to the explicitly addressed workspace. A persistent Host must not
// reuse its startup Runtime's workspace roots for another client directory.
func (s *runtimeComposition) SandboxStatusForWorkspace(workspace session.WorkspaceRef) SandboxStatus {
	if s == nil {
		return SandboxStatus{}
	}
	includeRuntime := sameWorkspaceCWD(workspace.CWD, s.workspace.CWD)
	return s.sandboxStatus(includeRuntime)
}

func sameWorkspaceCWD(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (s *runtimeComposition) sandboxStatus(includeRuntime bool) SandboxStatus {
	if s == nil {
		return SandboxStatus{}
	}
	s.mu.RLock()
	liveCfg := cloneSandboxConfig(s.sandbox)
	activationPinned := s.sandboxActivationPinned
	persistedCfg := cloneSandboxConfig(s.sandboxPersisted)
	exec := s.exec
	s.mu.RUnlock()
	process := s.runtimeProcessSnapshot()
	override := process.sandboxOverride
	securityPosture := resolveProcessSecurityPosture(process.runtime)
	if !includeRuntime {
		exec = nil
	}
	if activationPinned {
		return securityPosture.applySandboxStatus(sandboxStatusFromRuntime(liveCfg, exec))
	}
	cfg := mergeSandboxConfig(persistedCfg, override)
	if reflect.DeepEqual(configstore.NormalizeSandboxConfig(liveCfg), configstore.NormalizeSandboxConfig(cfg)) {
		cfg = liveCfg
	} else {
		// The configured backend belongs to later Session activations. The root
		// Runtime remains on its startup snapshot and must not be presented as the
		// resolved instance for the new policy.
		exec = nil
	}
	status := sandboxStatusFromRuntime(cfg, exec)
	return securityPosture.applySandboxStatus(status)
}

func sandboxStatusFromRuntime(cfg SandboxConfig, exec sandbox.Runtime) SandboxStatus {
	status := SandboxStatus{
		RequestedBackend: cfg.RequestedType,
		Route:            string(sandbox.RouteSandbox),
		SecuritySummary:  "sandbox",
	}
	if status.RequestedBackend == "" {
		status.RequestedBackend = "auto"
	}
	if strings.EqualFold(status.RequestedBackend, string(sandbox.BackendHost)) {
		status.Route = string(sandbox.RouteHost)
		status.SecuritySummary = "host"
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
	status.Setup = sandbox.CloneSetupStatus(rtStatus.Setup)
	applySandboxSetupProjection(&status, status.Setup)
	resolvedHost := strings.EqualFold(status.ResolvedBackend, string(sandbox.BackendHost))
	if rtStatus.FallbackToHost {
		status.Route = string(sandbox.RouteHost)
		status.SecuritySummary = "host fallback"
		if status.ResolvedBackend == "" {
			status.ResolvedBackend = string(sandbox.BackendHost)
		}
	} else if resolvedHost {
		status.Route = string(sandbox.RouteHost)
		status.SecuritySummary = "host"
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

func (s *Stack) PreflightSandbox(ctx context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
	if s == nil {
		return SandboxStatus{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	snapshot, err := s.sandboxLifecycleSnapshot()
	if err != nil {
		return SandboxStatus{}, err
	}
	preflight, ok := snapshot.exec.(sandbox.PreflightRuntime)
	if !ok {
		return s.runtimeProjection().SandboxStatus(), nil
	}
	err = preflight.Preflight(ctx, sandbox.PreflightOptions{AllowNonElevatedRepair: allowNonElevatedRepair})
	return s.runtimeProjection().SandboxStatus(), err
}

func normalizeSandboxBackend(backend string) (string, error) {
	return sandboxpolicy.NormalizeBackend(backend)
}

func mergeSandboxConfig(stored SandboxConfig, override SandboxConfig) SandboxConfig {
	return sandboxpolicy.MergeConfig(stored, override)
}
