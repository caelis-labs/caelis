//go:build windows

package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
)

var errSandboxCacheProvenance = errors.New("sandbox cache has no exact ACL receipt provenance")

func (r *runtime) workspaceSetupCheck() (check sandbox.SetupCheck) {
	check = sandbox.SetupCheck{
		Name:     "workspace",
		Scope:    sandbox.SetupScopeWorkspace,
		Required: false,
	}
	lastErr := r.workspaceSetupError()
	defer func() {
		if lastErr == "" {
			return
		}
		check.Current = false
		check.Required = true
		check.Error = lastErr
		check.Reason = "workspace ACL repair failed; explicit user repair is required"
		if check.Details == nil {
			check.Details = map[string]string{}
		}
		check.Details["manual_fix_hint"] = "run `/doctor` in TUI or `caelis sandbox fix`"
	}()
	policy, err := r.inspectPolicyForRequest(sandbox.CommandRequest{Dir: r.cfg.CWD, Constraints: r.Describe().DefaultConstraints})
	if err != nil {
		check.Reason = err.Error()
		return check
	}
	check.Root = policy.WorkspaceRoot
	check.Details = map[string]string{"policy_hash": policy.PolicyHash}
	refreshRunning, refreshErr, refreshAt, cacheCleanupAt, cacheBytes := r.refreshSnapshot()
	check.Details["refresh_state"] = "idle"
	if refreshRunning {
		check.Details["refresh_state"] = "running"
	}
	if refreshErr != "" {
		check.Details["refresh_error"] = refreshErr
	}
	if !refreshAt.IsZero() {
		check.Details["last_refresh_at"] = refreshAt.Format(time.RFC3339)
	}
	if !cacheCleanupAt.IsZero() {
		check.Details["last_cache_cleanup_at"] = cacheCleanupAt.Format(time.RFC3339)
	}
	if cacheBytes > 0 {
		check.Details["sandbox_cache_bytes"] = fmt.Sprint(cacheBytes)
	}
	check.Counts = map[string]int{
		"write_roots": len(policy.WriteRoots),
		"deny_write":  len(policy.DenyWritePaths),
	}
	manifest, err := r.readManifest()
	if err != nil {
		check.Reason = "workspace ACL manifest will be prepared lazily"
		return check
	}
	check.Current = manifestFresh(manifest, policy)
	check.UpdatedAt = manifest.UpdatedAt
	if !check.Current {
		check.Reason = "workspace ACL manifest is stale and will be repaired lazily"
	}
	return check
}

func (r *runtime) recordWorkspaceSetupError(err error) {
	if r == nil || err == nil {
		return
	}
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	r.lastWorkspaceSetupError = strings.TrimSpace(err.Error())
}

func (r *runtime) clearWorkspaceSetupError() {
	if r == nil {
		return
	}
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	r.lastWorkspaceSetupError = ""
}

func (r *runtime) workspaceSetupError() string {
	if r == nil {
		return ""
	}
	r.setupMu.RLock()
	defer r.setupMu.RUnlock()
	return strings.TrimSpace(r.lastWorkspaceSetupError)
}

type cleanupPlan struct {
	LegacyPaths     []string
	LegacyProtected []string
}

func (r *runtime) cleanupPlan() cleanupPlan {
	var plan cleanupPlan
	plan.LegacyPaths = pathutil.Dedupe([]string{
		filepath.Dir(r.manifestPath()),
		r.sandboxEnvRoot(r.cfg.CWD),
		r.legacyWorkspaceSandboxEnvRoot(),
	})
	hash := stateRootHash(r.stateRoot)
	plan.LegacyProtected = dedupeStrings(
		[]string{
			"StateDir-wide capability ledger and other workspace manifests/caches",
			"legacy random-SID ACLs require explicit repair and are never removed by principal scan",
			"local user CaelisSbxOff" + hash,
			"local user CaelisSbxOn" + hash,
			"local group CaelisSandboxUsers",
			"Windows Firewall rules CaelisSandbox-*",
		},
	)
	return plan
}

func (r *runtime) sandboxStateDir() string {
	return filepath.Join(r.stateRoot, ".sandbox")
}

func (r *runtime) capabilityStorePath() string {
	return filepath.Join(r.sandboxStateDir(), "cap_sids.json")
}

func (r *runtime) manifestPath() string {
	return r.manifestPathForWorkspace(r.cfg.CWD)
}

func (r *runtime) manifestPathForWorkspace(workspaceRoot string) string {
	return filepath.Join(r.workspaceManifestBase(), workspaceStateID(workspaceRoot), "workspace_write_manifest.json")
}

func (r *runtime) workspaceManifestBase() string {
	return filepath.Join(r.sandboxStateDir(), "workspaces")
}

func (r *runtime) legacyManifestPath() string {
	return filepath.Join(r.sandboxStateDir(), "workspace_write_manifest.json")
}

func workspaceStateID(workspaceRoot string) string {
	workspace := pathutil.Normalize(workspaceRoot)
	if workspace == "" {
		return "unknown"
	}
	sum := sha256.Sum256([]byte(pathutil.Key(workspace)))
	return hex.EncodeToString(sum[:])[:16]
}

func (r *runtime) sandboxEnvBase() string {
	return filepath.Join(r.sandboxStateDir(), "env")
}

func (r *runtime) sandboxEnvRoot(workspaceRoot string) string {
	workspace := pathutil.Normalize(workspaceRoot)
	if workspace == "" {
		workspace = pathutil.Normalize(r.cfg.CWD)
	}
	if workspace == "" {
		return ""
	}
	return filepath.Join(r.sandboxEnvBase(), workspaceStateID(workspace))
}

type sandboxEnvCacheEntry struct {
	path    string
	modTime time.Time
	size    int64
}

func (r *runtime) cleanupSandboxCaches(ctx context.Context, activeEnvRoot string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base := r.sandboxEnvBase()
	entries, total, err := sandboxEnvCacheEntries(ctx, base)
	if err != nil {
		if os.IsNotExist(err) {
			r.recordCacheCleanup(time.Now().UTC(), 0)
			return nil
		}
		return err
	}
	now := time.Now().UTC()
	var errs []error
	removed := map[string]struct{}{}
	for _, entry := range entries {
		if pathutil.Key(entry.path) == pathutil.Key(activeEnvRoot) || r.stateCoordinator.protectsEnvRoot(entry.path) {
			continue
		}
		if now.Sub(entry.modTime) <= windowsCacheMaxAge {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		removedNow, err := r.stateCoordinator.withUnusedEnvRoot(entry.path, func() error {
			return r.retireAndRemoveSandboxEnv(ctx, entry.path)
		})
		if err != nil {
			if errors.Is(err, errSandboxCacheProvenance) || errors.Is(err, errSandboxStateBusy) {
				continue
			}
			errs = append(errs, fmt.Errorf("impl/sandbox/windows: clean sandbox cache %s: %w", entry.path, err))
			continue
		}
		if !removedNow {
			continue
		}
		total -= entry.size
		removed[pathutil.Key(entry.path)] = struct{}{}
	}
	if total > windowsCacheMaxBytes {
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].modTime.Before(entries[j].modTime)
		})
		for _, entry := range entries {
			if total <= windowsCacheMaxBytes {
				break
			}
			key := pathutil.Key(entry.path)
			if key == "" {
				continue
			}
			if key == pathutil.Key(activeEnvRoot) || r.stateCoordinator.protectsEnvRoot(entry.path) {
				continue
			}
			if _, ok := removed[key]; ok {
				continue
			}
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			removedNow, err := r.stateCoordinator.withUnusedEnvRoot(entry.path, func() error {
				return r.retireAndRemoveSandboxEnv(ctx, entry.path)
			})
			if err != nil {
				if errors.Is(err, errSandboxCacheProvenance) || errors.Is(err, errSandboxStateBusy) {
					continue
				}
				errs = append(errs, fmt.Errorf("impl/sandbox/windows: clean sandbox cache %s: %w", entry.path, err))
				continue
			}
			if !removedNow {
				continue
			}
			total -= entry.size
			removed[key] = struct{}{}
		}
	}
	if total < 0 {
		total = 0
	}
	r.recordCacheCleanup(now, total)
	return errors.Join(errs...)
}

func (r *runtime) retireAndRemoveSandboxEnv(ctx context.Context, envRoot string) error {
	envRoot = pathutil.Normalize(envRoot)
	base := pathutil.Normalize(r.sandboxEnvBase())
	if envRoot == "" || pathutil.Key(filepath.Dir(envRoot)) != pathutil.Key(base) {
		return fmt.Errorf("impl/sandbox/windows: refuse unscoped sandbox cache removal %s", envRoot)
	}
	return r.withHostReceiptLedger(func(ledger *hostReceiptLedger) error {
		if hostEnvironmentBusy(*ledger, envRoot) {
			return errSandboxStateBusy
		}
		manifestPath := filepath.Join(r.workspaceManifestBase(), filepath.Base(envRoot), "workspace_write_manifest.json")
		manifest, err := readWorkspaceManifest(manifestPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%w: %s", errSandboxCacheProvenance, envRoot)
			}
			return fmt.Errorf("impl/sandbox/windows: read sandbox cache receipt manifest: %w", err)
		}
		if pathutil.Key(manifest.SandboxEnvRoot) != pathutil.Key(envRoot) {
			return fmt.Errorf("impl/sandbox/windows: sandbox cache receipt manifest does not own %s", envRoot)
		}
		if err := r.retireEnvironmentReceiptsTransaction(ctx, manifestPath, &manifest, envRoot, ledger); err != nil {
			return err
		}
		if err := os.RemoveAll(envRoot); err != nil {
			return err
		}
		return finalizeEnvironmentDeletion(manifestPath, &manifest)
	})
}

func sandboxEnvCacheEntries(ctx context.Context, base string) ([]sandboxEnvCacheEntry, int64, error) {
	base = pathutil.Normalize(base)
	if base == "" {
		return nil, 0, nil
	}
	items, err := os.ReadDir(base)
	if err != nil {
		return nil, 0, err
	}
	entries := make([]sandboxEnvCacheEntry, 0, len(items))
	var total int64
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, total, err
		}
		if !item.IsDir() {
			continue
		}
		path := filepath.Join(base, item.Name())
		info, err := item.Info()
		if err != nil {
			return nil, total, err
		}
		size, err := directorySize(ctx, path)
		if err != nil {
			return nil, total, err
		}
		entries = append(entries, sandboxEnvCacheEntry{path: path, modTime: info.ModTime(), size: size})
		total += size
	}
	return entries, total, nil
}

func directorySize(ctx context.Context, root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func (r *runtime) recordCacheCleanup(at time.Time, bytes int64) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	r.lastCacheCleanupAt = at
	r.lastCacheBytes = bytes
}

func (r *runtime) legacyWorkspaceSandboxEnvRoot() string {
	workspace := pathutil.Normalize(r.cfg.CWD)
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, ".caelis-sandbox")
}
