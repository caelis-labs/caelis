//go:build windows

package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/impl/sandbox/windows/internal/capability"
	"github.com/caelis-labs/caelis/impl/sandbox/windows/internal/pathutil"
	"github.com/caelis-labs/caelis/ports/sandbox"
)

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
	ACLPaths        []string
	Principals      []string
	LegacyPaths     []string
	LegacyProtected []string
}

func (r *runtime) cleanupPlan() cleanupPlan {
	var plan cleanupPlan
	if manifest, err := r.readManifest(); err == nil {
		for _, ace := range manifest.ACEs {
			plan.ACLPaths = append(plan.ACLPaths, ace.Path)
			plan.Principals = append(plan.Principals, ace.Principal)
		}
	}
	legacyRoots, legacyPrincipals := r.legacyACLArtifacts()
	plan.ACLPaths = append(plan.ACLPaths, legacyRoots...)
	plan.Principals = append(plan.Principals, legacyPrincipals...)
	plan.ACLPaths = pathutil.Dedupe(plan.ACLPaths)
	plan.Principals = dedupeStrings(plan.Principals)
	plan.LegacyPaths = pathutil.Dedupe([]string{
		r.manifestPath(),
		r.capabilityStorePath(),
		r.sandboxEnvBase(),
		r.legacyWorkspaceSandboxEnvRoot(),
		filepath.Join(r.sandboxStateDir(), "workspace_setup.json"),
		filepath.Join(r.sandboxStateDir(), "setup_marker.json"),
		filepath.Join(r.sandboxStateDir(), "setup_error.json"),
		filepath.Join(r.sandboxStateDir(), "setup_progress.json"),
		filepath.Join(r.stateRoot, ".sandbox-bin"),
		filepath.Join(r.stateRoot, ".sandbox-secrets"),
		filepath.Join(r.stateRoot, ".sandbox-reset"),
	})
	hash := stateRootHash(r.stateRoot)
	plan.LegacyProtected = dedupeStrings(
		[]string{
			"local user CaelisSbxOff" + hash,
			"local user CaelisSbxOn" + hash,
			"local group CaelisSandboxUsers",
			"Windows Firewall rules CaelisSandbox-*",
		},
	)
	return plan
}

func (r *runtime) legacyACLArtifacts() ([]string, []string) {
	var roots []string
	var principals []string
	type oldWorkspace struct {
		WriteRoots              []string          `json:"write_roots"`
		DenyWritePaths          []string          `json:"deny_write_paths"`
		CapabilitySIDs          []string          `json:"capability_sids"`
		WriteRootCapabilitySIDs map[string]string `json:"write_root_capability_sids"`
		OfflineUsername         string            `json:"offline_username"`
		OnlineUsername          string            `json:"online_username"`
	}
	if data, err := os.ReadFile(filepath.Join(r.sandboxStateDir(), "workspace_setup.json")); err == nil {
		var record oldWorkspace
		if json.Unmarshal(data, &record) == nil {
			roots = append(roots, record.WriteRoots...)
			roots = append(roots, record.DenyWritePaths...)
			principals = append(principals, record.CapabilitySIDs...)
			for _, sid := range record.WriteRootCapabilitySIDs {
				principals = append(principals, sid)
			}
			principals = append(principals, record.OfflineUsername, record.OnlineUsername, "CaelisSandboxUsers")
		}
	}
	if data, err := os.ReadFile(r.capabilityStorePath()); err == nil {
		var store capability.Store
		if json.Unmarshal(data, &store) == nil {
			for root, sid := range store.WorkspaceByCWD {
				roots = append(roots, root)
				principals = append(principals, sid)
			}
			for root, sid := range store.WritableRootByPath {
				roots = append(roots, root)
				principals = append(principals, sid)
			}
		}
	}
	return pathutil.Dedupe(roots), dedupeStrings(principals)
}

func (r *runtime) sandboxStateDir() string {
	return filepath.Join(r.stateRoot, ".sandbox")
}

func (r *runtime) capabilityStorePath() string {
	return filepath.Join(r.sandboxStateDir(), "cap_sids.json")
}

func (r *runtime) manifestPath() string {
	return filepath.Join(r.sandboxStateDir(), "workspace_write_manifest.json")
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
	sum := sha256.Sum256([]byte(pathutil.Key(workspace)))
	return filepath.Join(r.sandboxEnvBase(), hex.EncodeToString(sum[:])[:16])
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
	activeKey := pathutil.Key(activeEnvRoot)
	now := time.Now().UTC()
	var errs []error
	removed := map[string]struct{}{}
	for _, entry := range entries {
		if activeKey != "" && pathutil.Key(entry.path) == activeKey {
			continue
		}
		if now.Sub(entry.modTime) <= windowsCacheMaxAge {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if err := os.RemoveAll(entry.path); err != nil {
			errs = append(errs, fmt.Errorf("impl/sandbox/windows: clean sandbox cache %s: %w", entry.path, err))
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
			if activeKey != "" && key == activeKey {
				continue
			}
			if _, ok := removed[key]; ok {
				continue
			}
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			if err := os.RemoveAll(entry.path); err != nil {
				errs = append(errs, fmt.Errorf("impl/sandbox/windows: clean sandbox cache %s: %w", entry.path, err))
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
