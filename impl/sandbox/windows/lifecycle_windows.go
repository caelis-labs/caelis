//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/acl"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func (r *runtime) Status() sandbox.Status {
	check := r.workspaceSetupCheck()
	status := sandbox.Status{
		RequestedBackend: sandbox.BackendWindows,
		ResolvedBackend:  sandbox.BackendWindows,
		Setup: sandbox.SetupStatus{
			Required: check.Required,
			Error:    strings.TrimSpace(check.Error),
			Checks:   []sandbox.SetupCheck{check},
		},
	}
	status.Setup.Details = map[string]string{
		"backend": "windows-restricted-token",
	}
	return sandbox.CloneStatus(status)
}

func (r *runtime) SelectionStatus() sandbox.Status {
	return sandbox.Status{
		RequestedBackend: sandbox.BackendWindows,
		ResolvedBackend:  sandbox.BackendWindows,
	}
}

func (r *runtime) Prepare(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "acl",
		Message: "preparing current workspace ACL policy",
		Step:    1,
		Total:   2,
	})
	_, err := r.ensureForRequestMode(ctx, sandbox.CommandRequest{
		Dir: r.cfg.CWD,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	}, ensureModeBackgroundRefresh)
	if err != nil {
		return err
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "complete",
		Message: "Windows workspace-write sandbox ACL policy is ready",
		Step:    2,
		Total:   2,
		Done:    true,
	})
	return nil
}

func (r *runtime) Repair(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.Prepare(ctx); err == nil {
		return nil
	} else if !requiresElevatedACLRepair(err) {
		return err
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "elevate",
		Message: "requesting explicit Windows sandbox ACL repair",
		Step:    1,
		Total:   2,
	})
	if err := r.runElevatedRepair(ctx); err != nil {
		r.recordWorkspaceSetupError(err)
		return err
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "verify",
		Message: "verifying Windows sandbox ACL repair",
		Step:    2,
		Total:   2,
	})
	if err := r.Prepare(ctx); err != nil {
		r.recordWorkspaceSetupError(err)
		return err
	}
	return nil
}

func requiresElevatedACLRepair(err error) bool {
	if err == nil {
		return false
	}
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "acl: write") && strings.Contains(lower, "dacl")
}

func (r *runtime) Preflight(ctx context.Context, opts sandbox.PreflightOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !opts.AllowNonElevatedRepair {
		return nil
	}
	refreshCtx, cancel := context.WithTimeout(ctx, windowsPreflightTimeout)
	defer cancel()
	return r.Refresh(refreshCtx)
}

func (r *runtime) Refresh(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !r.beginRefresh() {
		return nil
	}
	err := r.refreshForRequest(ctx, sandbox.CommandRequest{
		Dir: r.cfg.CWD,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	})
	r.finishRefresh(err)
	return err
}

func (r *runtime) startBackgroundRefresh(ctx context.Context, req sandbox.CommandRequest) {
	if r == nil || !r.beginRefresh() {
		return
	}
	go func() {
		refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		r.finishRefresh(r.refreshForRequest(refreshCtx, req))
	}()
}

func (r *runtime) beginRefresh() bool {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if r.refreshRunning {
		return false
	}
	r.refreshRunning = true
	return true
}

func (r *runtime) finishRefresh(err error) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	r.refreshRunning = false
	r.lastRefreshAt = time.Now().UTC()
	if err == nil || errors.Is(err, context.Canceled) {
		r.lastRefreshError = ""
		return
	}
	r.lastRefreshError = strings.TrimSpace(err.Error())
}

func (r *runtime) refreshSnapshot() (running bool, lastErr string, lastAt time.Time, lastCacheCleanup time.Time, lastCacheBytes int64) {
	r.refreshMu.RLock()
	defer r.refreshMu.RUnlock()
	return r.refreshRunning, strings.TrimSpace(r.lastRefreshError), r.lastRefreshAt, r.lastCacheCleanupAt, r.lastCacheBytes
}

func (r *runtime) refreshForRequest(ctx context.Context, req sandbox.CommandRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	policy, err := r.policyForRequest(req)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.sandboxStateDir(), 0o700); err != nil {
		return err
	}
	manifest, manifestErr := r.readManifest()
	if manifestErr == nil && manifestCoversPolicy(manifest, policy, true) {
		missing, err := r.missingACLEntries(policy)
		if err == nil && len(missing) == 0 {
			return r.cleanupSandboxCaches(ctx, policy.SandboxEnvRoot)
		}
	}
	if manifestErr == nil {
		if !r.tryCleanupStaleManifestACLs(ctx, manifest, policy) {
			return r.cleanupSandboxCaches(ctx, policy.SandboxEnvRoot)
		}
	}
	appliedPolicy, complete, aclErr := r.applyPolicyACLsInterruptible(ctx, policy)
	if complete && aclErr == nil {
		if !r.tryWriteManifest(ctx, appliedPolicy) {
			return r.cleanupSandboxCaches(ctx, policy.SandboxEnvRoot)
		}
	}
	cacheErr := r.cleanupSandboxCaches(ctx, policy.SandboxEnvRoot)
	return errors.Join(aclErr, cacheErr)
}

func (r *runtime) tryCleanupStaleManifestACLs(ctx context.Context, manifest workspaceManifest, policy workspacePolicy) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	if !r.ensureMu.TryLock() {
		return false
	}
	defer r.ensureMu.Unlock()
	r.cleanupStaleManifestACLs(manifest, policy)
	return true
}

func (r *runtime) tryWriteManifest(ctx context.Context, policy workspacePolicy) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	if !r.ensureMu.TryLock() {
		return false
	}
	defer r.ensureMu.Unlock()
	return r.writeManifest(policy) == nil
}

func (r *runtime) Reset(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	plan := r.cleanupPlan()
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "clean",
		Message: fmt.Sprintf("cleaning Windows sandbox state: %d ACL paths, %d legacy paths", len(plan.ACLPaths), len(plan.LegacyPaths)),
		Step:    1,
		Total:   3,
	})
	var errs []error
	for _, path := range plan.ACLPaths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := os.Stat(path); err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("inspect ACL cleanup path %s: %w", path, err))
			}
			continue
		}
		if err := acl.RemoveFileDACLPrincipals(path, plan.Principals...); err != nil {
			errs = append(errs, fmt.Errorf("remove sandbox ACL principals from %s: %w", path, err))
		}
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "state",
		Message: "removing Windows sandbox state files",
		Step:    2,
		Total:   3,
	})
	for _, path := range plan.LegacyPaths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("remove sandbox state path %s: %w", path, err))
		}
	}
	for _, leftover := range plan.LegacyProtected {
		sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
			Phase:   "legacy",
			Message: "legacy Windows sandbox artifact may require elevated cleanup: " + leftover,
			Step:    2,
			Total:   3,
		})
	}
	if len(errs) > 0 {
		joined := errors.Join(errs...)
		sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
			Phase:   "error",
			Message: "Windows sandbox cleanup finished with errors: " + joined.Error(),
			Step:    3,
			Total:   3,
		})
		return fmt.Errorf("impl/sandbox/windows: clean sandbox state: %w", joined)
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "complete",
		Message: "Windows sandbox cleanup complete",
		Step:    3,
		Total:   3,
		Done:    true,
	})
	return nil
}

func (r *runtime) Close() error {
	r.mu.RLock()
	sessions := make([]*windowsSession, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.RUnlock()
	for _, session := range sessions {
		_ = session.Terminate(context.Background())
	}
	return nil
}
