//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/acl"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/capability"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/win32"
)

const (
	manifestPhasePrepared    = "prepared"
	manifestPhaseActive      = "active"
	manifestPhaseDeleting    = "deleting"
	hostReceiptLedgerVersion = 1
)

var probeFileDACLWriteAccess = acl.ProbeFileDACLWriteAccess

type hostReceiptLedger struct {
	Version  int                 `json:"version"`
	Effects  []hostReceiptEffect `json:"effects,omitempty"`
	Runtimes []hostRuntimeLease  `json:"runtimes,omitempty"`
}

type hostReceiptEffect struct {
	Path       string         `json:"path"`
	Entry      acl.Entry      `json:"entry"`
	Receipt    acl.ACEReceipt `json:"receipt"`
	Applied    bool           `json:"applied"`
	References []string       `json:"references,omitempty"`
}

type hostRuntimeLease struct {
	ID              string                `json:"id"`
	Process         win32.ProcessIdentity `json:"process"`
	StateRoot       string                `json:"state_root"`
	WorkspaceRoot   string                `json:"workspace_root"`
	SandboxEnvRoot  string                `json:"sandbox_env_root"`
	ActiveUses      int                   `json:"active_uses,omitempty"`
	Closing         bool                  `json:"closing,omitempty"`
	LastUpdatedTime time.Time             `json:"last_updated_time"`
}

// activateReceiptPolicyLocked is the workspace-specific write-ahead journal.
// The caller holds stateCoordinator.aclMu. Every exact receipt is persisted
// before its DACL effect, and the replacement is fully verified before stale
// receipts may be retired.
func (r *runtime) activateReceiptPolicyLocked(ctx context.Context, policy workspacePolicy, canRetire bool) error {
	return r.withHostReceiptLedger(func(ledger *hostReceiptLedger) error {
		return r.activateReceiptPolicyTransactionLocked(ctx, policy, canRetire, ledger)
	})
}

func (r *runtime) activateReceiptPolicyTransactionLocked(ctx context.Context, policy workspacePolicy, canRetire bool, ledger *hostReceiptLedger) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	canRetire = canRetire && r.hostCanPruneACLs(*ledger)
	desired := receiptEffects(policy)
	manifest, err := r.readManifest()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	legacyPlan, legacyErr := r.prepareLegacyMigration(manifest, policy)
	if legacyErr != nil {
		return legacyErr
	}
	if legacyPlan.Required {
		if err := r.preflightLegacyMigrationElevation(legacyPlan, policy, desired); err != nil {
			return err
		}
	}
	if err == nil && manifest.Version == workspaceManifestVersion && pathutil.Key(manifest.WorkspaceRoot) != pathutil.Key(policy.WorkspaceRoot) {
		return fmt.Errorf("impl/sandbox/windows: workspace receipt manifest belongs to %s", manifest.WorkspaceRoot)
	}
	if err == nil && manifest.Version == workspaceManifestVersion && manifest.Phase == manifestPhaseDeleting {
		return fmt.Errorf("impl/sandbox/windows: sandbox environment deletion for %s must finish before ACL activation", manifest.DeletingEnvRoot)
	}
	if err == nil && manifest.Version == workspaceManifestVersion {
		if validateErr := validatePreparedReceiptsForEffects(manifest, desired); validateErr != nil {
			return validateErr
		}
	}
	if err == nil && manifest.Version == workspaceManifestVersion && manifest.Phase == manifestPhasePrepared {
		if resumeErr := r.finishPreparedReceiptManifest(ctx, &manifest, ledger); resumeErr != nil {
			return fmt.Errorf("impl/sandbox/windows: recover prepared workspace ACL receipts: %w", resumeErr)
		}
	}
	if err == nil && manifest.Version == workspaceManifestVersion && manifest.PolicyHash == policy.PolicyHash && !legacyPlan.Required {
		if resumeErr := r.finishPreparedReceiptManifest(ctx, &manifest, ledger); resumeErr == nil && receiptManifestCovers(manifest, desired) {
			if canRetire {
				return r.cleanupRetiringReceipts(&manifest, ledger)
			}
			return nil
		}
	}

	target := workspaceManifestForPolicy(policy)
	target.Phase = manifestPhasePrepared
	if err == nil {
		target.LegacyACEs = append(target.LegacyACEs, manifest.LegacyACEs...)
		if manifest.Version != workspaceManifestVersion {
			target.LegacyACEs = mergeManifestACEs(target.LegacyACEs, manifest.ACEs)
		} else {
			target.RetiringReceipts = append(target.RetiringReceipts, manifest.RetiringReceipts...)
			if !canRetire {
				target = mergeWorkspaceManifests(manifest, target)
				for _, managed := range manifest.ManagedReceipts {
					desired = append(desired, receiptEffect{Path: managed.Path, Entry: managed.Entry})
				}
			}
		}
	}
	if legacyPlan.Required {
		target.LegacyMigrationPrepared = true
		target.LegacyACEs = mergeManifestACEs(target.LegacyACEs, legacyPlan.ACEs)
		target.RetiringReceipts = dedupeManifestReceipts(append(target.RetiringReceipts, legacyPlan.Receipts...))
	}
	desired = dedupeReceiptEffects(desired)
	desiredByKey := make(map[string]receiptEffect, len(desired))
	for _, effect := range desired {
		desiredByKey[receiptEffectKey(effect.Path, effect.Entry)] = effect
	}
	if len(target.RetiringReceipts) > 0 {
		keptRetiring := target.RetiringReceipts[:0]
		for _, retiring := range target.RetiringReceipts {
			key := receiptEffectKey(retiring.Path, retiring.Entry)
			if _, wanted := desiredByKey[key]; wanted && retiring.Applied && acl.VerifyFileDACLReceipt(retiring.Path, retiring.Receipt) == nil {
				target.ManagedReceipts = appendReceipt(target.ManagedReceipts, retiring)
				continue
			}
			keptRetiring = append(keptRetiring, retiring)
		}
		target.RetiringReceipts = keptRetiring
	}
	if err == nil && manifest.Version == workspaceManifestVersion {
		for _, current := range manifest.ManagedReceipts {
			key := receiptEffectKey(current.Path, current.Entry)
			if _, wanted := desiredByKey[key]; wanted && current.Applied && acl.VerifyFileDACLReceipt(current.Path, current.Receipt) == nil {
				target.ManagedReceipts = append(target.ManagedReceipts, current)
				continue
			}
			target.RetiringReceipts = appendReceipt(target.RetiringReceipts, current)
		}
	}
	target.RetiringReceipts = dedupeManifestReceipts(target.RetiringReceipts)
	target.UpdatedAt = time.Now().UTC()
	if err := r.persistManifest(target); err != nil {
		return err
	}

	for _, effect := range desired {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := receiptEffectKey(effect.Path, effect.Entry)
		index := receiptIndex(target.ManagedReceipts, key)
		if index < 0 {
			receipt, applied, err := r.hostReceiptForEffect(ledger, effect)
			if err != nil {
				return fmt.Errorf("impl/sandbox/windows: prepare exact ACL receipt for %s: %w", effect.Path, err)
			}
			target.ManagedReceipts = append(target.ManagedReceipts, manifestReceipt{Path: effect.Path, Entry: effect.Entry, Receipt: receipt, Applied: applied})
			index = len(target.ManagedReceipts) - 1
			target.UpdatedAt = time.Now().UTC()
			if err := r.persistManifest(target); err != nil {
				return err
			}
		}
		changed, err := r.registerHostReceipt(ledger, &target.ManagedReceipts[index], hostReceiptReferenceForManifest(r.manifestPath()))
		if err != nil {
			return err
		}
		if changed {
			target.UpdatedAt = time.Now().UTC()
			if err := r.persistManifest(target); err != nil {
				return err
			}
		}
		managed := target.ManagedReceipts[index]
		if managed.Applied {
			if err := acl.VerifyFileDACLReceipt(managed.Path, managed.Receipt); err != nil {
				return err
			}
			continue
		}
		if err := acl.EnsureFileDACLReceipt(managed.Path, managed.Receipt); err != nil {
			return fmt.Errorf("impl/sandbox/windows: apply exact ACL receipt to %s: %w", managed.Path, err)
		}
		if err := acl.VerifyFileDACLReceipt(managed.Path, managed.Receipt); err != nil {
			return fmt.Errorf("impl/sandbox/windows: verify exact ACL receipt on %s: %w", managed.Path, err)
		}
		target.ManagedReceipts[index].Applied = true
		target.UpdatedAt = time.Now().UTC()
		if err := r.persistManifest(target); err != nil {
			return err
		}
		if _, err := r.registerHostReceipt(ledger, &target.ManagedReceipts[index], hostReceiptReferenceForManifest(r.manifestPath())); err != nil {
			return err
		}
	}
	target.Phase = manifestPhaseActive
	target.UpdatedAt = time.Now().UTC()
	if err := r.persistManifest(target); err != nil {
		return err
	}
	if canRetire {
		if err := r.cleanupRetiringReceipts(&target, ledger); err != nil {
			return err
		}
		return r.finalizeLegacyMigration(&target, legacyPlan)
	}
	return nil
}

func (r *runtime) finishPreparedReceiptManifest(ctx context.Context, manifest *workspaceManifest, ledger *hostReceiptLedger) error {
	if manifest == nil || manifest.Version != workspaceManifestVersion {
		return fmt.Errorf("impl/sandbox/windows: exact receipt manifest is unavailable")
	}
	for i := range manifest.ManagedReceipts {
		if err := ctx.Err(); err != nil {
			return err
		}
		managed := &manifest.ManagedReceipts[i]
		changed, err := r.registerHostReceipt(ledger, managed, hostReceiptReferenceForManifest(r.manifestPath()))
		if err != nil {
			return err
		}
		if changed {
			manifest.UpdatedAt = time.Now().UTC()
			if err := r.persistManifest(*manifest); err != nil {
				return err
			}
		}
		if managed.Applied {
			if err := acl.VerifyFileDACLReceipt(managed.Path, managed.Receipt); err != nil {
				return err
			}
			continue
		}
		if err := acl.EnsureFileDACLReceipt(managed.Path, managed.Receipt); err != nil {
			return err
		}
		managed.Applied = true
		manifest.UpdatedAt = time.Now().UTC()
		if err := r.persistManifest(*manifest); err != nil {
			return err
		}
		if _, err := r.registerHostReceipt(ledger, managed, hostReceiptReferenceForManifest(r.manifestPath())); err != nil {
			return err
		}
	}
	manifest.Phase = manifestPhaseActive
	manifest.UpdatedAt = time.Now().UTC()
	return r.persistManifest(*manifest)
}

func (r *runtime) cleanupRetiringReceipts(manifest *workspaceManifest, ledger *hostReceiptLedger) error {
	return r.cleanupRetiringReceiptsAt(r.manifestPath(), manifest, ledger)
}

func (r *runtime) cleanupRetiringReceiptsAt(manifestPath string, manifest *workspaceManifest, ledger *hostReceiptLedger) error {
	if manifest == nil {
		return nil
	}
	for len(manifest.RetiringReceipts) > 0 {
		retiring := manifest.RetiringReceipts[0]
		if receiptIndex(manifest.ManagedReceipts, receiptEffectKey(retiring.Path, retiring.Entry)) >= 0 {
			manifest.RetiringReceipts = manifest.RetiringReceipts[1:]
			manifest.UpdatedAt = time.Now().UTC()
			if err := persistWorkspaceManifest(manifestPath, *manifest); err != nil {
				return err
			}
			continue
		}
		if err := r.releaseHostReceipt(ledger, retiring, hostReceiptReferenceForManifest(manifestPath)); err != nil {
			return err
		}
		manifest.RetiringReceipts = manifest.RetiringReceipts[1:]
		manifest.UpdatedAt = time.Now().UTC()
		if err := persistWorkspaceManifest(manifestPath, *manifest); err != nil {
			return err
		}
	}
	return nil
}

func (r *runtime) retireEnvironmentReceiptsTransaction(ctx context.Context, manifestPath string, manifest *workspaceManifest, envRoot string, ledger *hostReceiptLedger) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if manifest == nil || manifest.Version != workspaceManifestVersion {
		return fmt.Errorf("impl/sandbox/windows: exact receipt manifest is required before cache deletion")
	}
	kept := manifest.ManagedReceipts[:0]
	for _, managed := range manifest.ManagedReceipts {
		if pathutil.IsUnder(managed.Path, envRoot) {
			manifest.RetiringReceipts = appendReceipt(manifest.RetiringReceipts, managed)
			continue
		}
		kept = append(kept, managed)
	}
	manifest.ManagedReceipts = kept
	manifest.Phase = manifestPhaseDeleting
	manifest.DeletingEnvRoot = envRoot
	manifest.UpdatedAt = time.Now().UTC()
	if err := persistWorkspaceManifest(manifestPath, *manifest); err != nil {
		return err
	}
	return r.cleanupRetiringReceiptsAt(manifestPath, manifest, ledger)
}

func finalizeEnvironmentDeletion(manifestPath string, manifest *workspaceManifest) error {
	if manifest == nil {
		return nil
	}
	manifest.SandboxEnvRoot = ""
	manifest.PolicyHash = ""
	manifest.DeletingEnvRoot = ""
	manifest.Phase = manifestPhaseActive
	manifest.UpdatedAt = time.Now().UTC()
	return persistWorkspaceManifest(manifestPath, *manifest)
}

func (r *runtime) resetWorkspaceReceipts(ctx context.Context) error {
	return r.withHostReceiptLedger(func(ledger *hostReceiptLedger) error {
		return r.resetWorkspaceReceiptsTransaction(ctx, ledger)
	})
}

func (r *runtime) resetWorkspaceReceiptsTransaction(ctx context.Context, ledger *hostReceiptLedger) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	manifest, err := r.readManifest()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if manifest.Version != workspaceManifestVersion {
		return fmt.Errorf("impl/sandbox/windows: legacy workspace ACLs require explicit repair; refusing principal-wide reset")
	}
	if len(manifest.LegacyACEs) > 0 {
		return fmt.Errorf("impl/sandbox/windows: legacy workspace ACL residue requires explicit repair; refusing principal-wide reset")
	}
	manifest.RetiringReceipts = dedupeManifestReceipts(append(manifest.RetiringReceipts, manifest.ManagedReceipts...))
	manifest.ManagedReceipts = nil
	manifest.Phase = manifestPhaseActive
	if err := r.persistManifest(manifest); err != nil {
		return err
	}
	return r.cleanupRetiringReceipts(&manifest, ledger)
}

type legacyMigrationPlan struct {
	Required bool
	ACEs     []manifestACE
	Receipts []manifestReceipt
	Consumed capability.LegacyV1Store
}

func (r *runtime) preflightLegacyMigrationElevation(plan legacyMigrationPlan, policy workspacePolicy, desired []receiptEffect) error {
	expected := legacyV1ReceiptEffects(policy, r.capabilityStorePath())
	expectedKeys := make(map[string]struct{}, len(expected))
	for _, effect := range expected {
		expectedKeys[receiptEffectKey(effect.Path, effect.Entry)] = struct{}{}
	}
	independentlyDerivable := len(plan.Receipts) > 0
	for _, retiring := range plan.Receipts {
		if _, ok := expectedKeys[receiptEffectKey(retiring.Path, retiring.Entry)]; !ok {
			independentlyDerivable = false
			break
		}
	}
	if independentlyDerivable {
		return nil
	}
	// A non-derivable legacy SID may still be migrated by the normal Host user
	// because its exact Store+manifest provenance was already adopted. Before
	// installing any replacement ACE, prove that the same token can finish all
	// replacement and retirement DACL writes without elevation. Otherwise fail
	// closed so an effect-only helper never leaves the machine with two SIDs.
	paths := make([]string, 0, len(desired)+len(plan.Receipts))
	for _, effect := range desired {
		paths = append(paths, effect.Path)
	}
	for _, retiring := range plan.Receipts {
		paths = append(paths, retiring.Path)
	}
	for _, path := range pathutil.Dedupe(paths) {
		if err := probeFileDACLWriteAccess(path); err != nil {
			return fmt.Errorf("impl/sandbox/windows: legacy ACL on %s uses a SID that elevated repair cannot independently derive; refusing replacement before any ACL effect: %w", path, err)
		}
	}
	return nil
}

func (r *runtime) prepareLegacyMigration(manifest workspaceManifest, policy workspacePolicy) (legacyMigrationPlan, error) {
	legacyACEs := append([]manifestACE(nil), manifest.LegacyACEs...)
	if manifest.Version != 0 && manifest.Version != workspaceManifestVersion {
		legacyACEs = mergeManifestACEs(legacyACEs, manifest.ACEs)
	}
	if len(legacyACEs) == 0 {
		return legacyMigrationPlan{}, nil
	}
	plan := legacyMigrationPlan{Required: true, ACEs: legacyACEs, Consumed: capability.LegacyV1Store{
		WorkspaceByCWD:     map[string]string{},
		WritableRootByPath: map[string]string{},
	}}
	alreadyPrepared := manifest.LegacyMigrationPrepared
	legacy, err := capability.LegacyV1Snapshot(r.capabilityStorePath())
	if err != nil {
		return legacyMigrationPlan{}, fmt.Errorf("impl/sandbox/windows: read trusted legacy SID provenance: %w", err)
	}
	if legacy == nil && alreadyPrepared {
		return plan, nil
	}
	if legacy == nil {
		return legacyMigrationPlan{}, fmt.Errorf("impl/sandbox/windows: legacy ACL manifest has no trusted SID Store provenance; refusing v2 activation")
	}
	rootSIDs := map[string]string{}
	workspaceKey := pathutil.Key(policy.WorkspaceRoot)
	if sid := strings.TrimSpace(legacy.WorkspaceByCWD[workspaceKey]); sid != "" {
		rootSIDs[pathutil.Normalize(policy.WorkspaceRoot)] = sid
		plan.Consumed.WorkspaceByCWD[workspaceKey] = sid
	}
	for _, root := range append(append([]string(nil), manifest.WriteRoots...), manifest.SandboxEnvRoot) {
		key := pathutil.Key(root)
		if sid := strings.TrimSpace(legacy.WritableRootByPath[key]); sid != "" {
			rootSIDs[pathutil.Normalize(root)] = sid
			plan.Consumed.WritableRootByPath[key] = sid
		}
	}
	if len(rootSIDs) == 0 {
		return legacyMigrationPlan{}, fmt.Errorf("impl/sandbox/windows: legacy ACL manifest has no matching trusted SID roots; refusing v2 activation")
	}
	if alreadyPrepared {
		return plan, nil
	}
	for _, legacyACE := range legacyACEs {
		entry, err := trustedLegacyEntry(legacyACE, rootSIDs)
		if err != nil {
			return legacyMigrationPlan{}, err
		}
		count, err := acl.ExactFileDACLEntryCount(legacyACE.Path, entry)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return legacyMigrationPlan{}, err
		}
		switch count {
		case 0:
			continue
		case 1:
			receipt, err := acl.AdoptExistingExactFileDACLEntry(legacyACE.Path, entry)
			if err != nil {
				return legacyMigrationPlan{}, err
			}
			plan.Receipts = appendReceipt(plan.Receipts, manifestReceipt{Path: legacyACE.Path, Entry: entry, Receipt: receipt, Applied: true})
		default:
			return legacyMigrationPlan{}, fmt.Errorf("impl/sandbox/windows: legacy exact ACE count on %s = %d; refusing ambiguous migration", legacyACE.Path, count)
		}
	}
	return plan, nil
}

func trustedLegacyEntry(legacy manifestACE, rootSIDs map[string]string) (acl.Entry, error) {
	path := pathutil.Normalize(legacy.Path)
	principal := strings.TrimSpace(legacy.Principal)
	var matchedRoot bool
	var matchedPrincipal bool
	for root, sid := range rootSIDs {
		if strings.EqualFold(sid, principal) {
			matchedPrincipal = true
		}
		if pathutil.IsUnder(path, root) {
			matchedRoot = true
			if strings.EqualFold(sid, principal) && strings.EqualFold(legacy.Mode, string(acl.Grant)) && strings.EqualFold(legacy.Rights, string(acl.Modify)) {
				return acl.Entry{Principal: principal, Mode: acl.Grant, Rights: acl.Modify, Inherit: legacy.Inherit}, nil
			}
		}
	}
	if matchedRoot && matchedPrincipal && strings.EqualFold(legacy.Mode, string(acl.Deny)) && strings.EqualFold(legacy.Rights, string(acl.Write)) {
		return acl.Entry{Principal: principal, Mode: acl.Deny, Rights: acl.Write, Inherit: legacy.Inherit}, nil
	}
	return acl.Entry{}, fmt.Errorf("impl/sandbox/windows: legacy ACE on %s is not proven by the current workspace Store", path)
}

func (r *runtime) finalizeLegacyMigration(manifest *workspaceManifest, plan legacyMigrationPlan) error {
	if manifest == nil || !manifest.LegacyMigrationPrepared || len(manifest.RetiringReceipts) > 0 {
		return nil
	}
	if len(plan.Consumed.WorkspaceByCWD) > 0 || len(plan.Consumed.WritableRootByPath) > 0 {
		if err := capability.ConsumeLegacyV1(r.capabilityStorePath(), plan.Consumed); err != nil {
			return err
		}
	}
	manifest.LegacyACEs = nil
	manifest.LegacyMigrationPrepared = false
	manifest.UpdatedAt = time.Now().UTC()
	return r.persistManifest(*manifest)
}

func (r *runtime) hostReceiptLedgerPath() string {
	return filepath.Join(r.hostReceiptAuthorityRoot, "host_acl_receipts.json")
}

func (r *runtime) withHostReceiptLedger(fn func(*hostReceiptLedger) error) error {
	if err := r.ensureHostReceiptAuthority(); err != nil {
		return err
	}
	return capability.WithStoreLock(r.hostReceiptLedgerPath(), func() error {
		ledger, err := r.readHostReceiptLedger()
		if err != nil {
			return err
		}
		changed, err := pruneDeadHostRuntimes(&ledger)
		if err != nil {
			return err
		}
		if changed {
			if err := r.persistHostReceiptLedger(ledger); err != nil {
				return err
			}
		}
		return fn(&ledger)
	})
}

func (r *runtime) ensureHostReceiptAuthority() error {
	r.hostAuthorityMu.Lock()
	defer r.hostAuthorityMu.Unlock()
	if r.hostAuthorityValidated {
		return nil
	}
	if err := os.MkdirAll(r.hostReceiptAuthorityRoot, 0o700); err != nil {
		return fmt.Errorf("impl/sandbox/windows: create Host ACL receipt authority %s: %w", r.hostReceiptAuthorityRoot, err)
	}
	if err := validateNoReparseAncestors(r.hostReceiptAuthorityRoot); err != nil {
		return err
	}
	info, err := acl.InspectFileDACL(r.hostReceiptAuthorityRoot)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(info.OwnerSID), r.hostUserSID) {
		return fmt.Errorf("impl/sandbox/windows: Host ACL receipt authority owner %s does not match Host user %s", info.OwnerSID, r.hostUserSID)
	}
	if err := protectHostAuthorityObject(r.hostReceiptAuthorityRoot, r.hostUserSID, true); err != nil {
		return err
	}
	for _, path := range []string{r.hostReceiptLedgerPath(), r.hostReceiptLedgerPath() + ".lock"} {
		childInfo, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("impl/sandbox/windows: inspect Host authority object %s: %w", path, err)
		}
		if childInfo.Mode()&os.ModeSymlink != 0 || !childInfo.Mode().IsRegular() {
			return fmt.Errorf("impl/sandbox/windows: Host authority object %s is not a regular file", path)
		}
		if reparse, err := isReparsePoint(path); err != nil {
			return fmt.Errorf("impl/sandbox/windows: inspect Host authority object %s: %w", path, err)
		} else if reparse {
			return fmt.Errorf("impl/sandbox/windows: Host authority object %s is a reparse point", path)
		}
		childACL, err := acl.InspectFileDACL(path)
		if err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(childACL.OwnerSID), r.hostUserSID) {
			return fmt.Errorf("impl/sandbox/windows: Host authority object owner %s does not match Host user %s", childACL.OwnerSID, r.hostUserSID)
		}
		if err := protectHostAuthorityObject(path, r.hostUserSID, false); err != nil {
			return err
		}
	}
	if err := verifyDirectoryFileWrite(r.hostReceiptAuthorityRoot); err != nil {
		return fmt.Errorf("impl/sandbox/windows: Host ACL receipt authority is not writable: %w", err)
	}
	r.hostAuthorityValidated = true
	return nil
}

func protectHostAuthorityObject(path, hostUserSID string, inherit bool) error {
	if err := acl.ReplaceFileDACL(path, true, acl.Entry{
		Principal: hostUserSID,
		Rights:    acl.FullControl,
		Mode:      acl.Set,
		Inherit:   inherit,
	}); err != nil {
		return fmt.Errorf("impl/sandbox/windows: protect Host authority object %s: %w", path, err)
	}
	info, err := acl.InspectFileDACL(path)
	if err != nil {
		return err
	}
	if !info.Protected || !info.HasDACL || info.HasInheritedACE {
		return fmt.Errorf("impl/sandbox/windows: Host authority object %s does not have a protected explicit DACL", path)
	}
	return nil
}

func validateNoReparseAncestors(path string) error {
	path = pathutil.Normalize(path)
	volume := filepath.VolumeName(path)
	if path == "" || volume == "" {
		return fmt.Errorf("impl/sandbox/windows: canonical local authority path is required")
	}
	relative := strings.TrimPrefix(path, volume+string(filepath.Separator))
	current := volume + string(filepath.Separator)
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		reparse, err := isReparsePoint(current)
		if err != nil {
			return fmt.Errorf("impl/sandbox/windows: inspect Host authority ancestor %s: %w", current, err)
		}
		if reparse {
			return fmt.Errorf("impl/sandbox/windows: Host ACL receipt authority traverses reparse point %s", current)
		}
	}
	return nil
}

func (r *runtime) registerHostRuntime() error {
	return r.withHostReceiptLedger(func(ledger *hostReceiptLedger) error {
		if hostRuntimeIndex(*ledger, r.runtimeID) >= 0 {
			return fmt.Errorf("impl/sandbox/windows: duplicate Host runtime ID %s", r.runtimeID)
		}
		ledger.Runtimes = append(ledger.Runtimes, hostRuntimeLease{
			ID:              r.runtimeID,
			Process:         r.hostProcessIdentity,
			StateRoot:       r.stateRoot,
			WorkspaceRoot:   pathutil.Normalize(r.cfg.CWD),
			SandboxEnvRoot:  r.registeredEnvRoot,
			LastUpdatedTime: time.Now().UTC(),
		})
		return r.persistHostReceiptLedger(*ledger)
	})
}

func (r *runtime) ensureHostRuntimeRegistered() error {
	r.hostRegistrationMu.Lock()
	defer r.hostRegistrationMu.Unlock()
	return r.ensureHostRuntimeRegisteredLocked()
}

func (r *runtime) ensureHostRuntimeRegisteredLocked() error {
	if r.hostRegistered {
		return nil
	}
	if err := r.registerHostRuntime(); err != nil {
		return err
	}
	r.hostRegistered = true
	return nil
}

func (r *runtime) beginRuntimeUse() (func(), error) {
	if err := r.recoverDeletingEnvironmentBeforeUse(context.Background()); err != nil {
		return nil, err
	}
	releaseLocal, err := r.stateCoordinator.beginUse(r.registeredEnvRoot)
	if err != nil {
		return nil, err
	}
	if err := r.ensureHostRuntimeRegistered(); err != nil {
		releaseLocal()
		return nil, err
	}
	r.hostRegistrationMu.Lock()
	if err := r.withHostReceiptLedger(func(ledger *hostReceiptLedger) error {
		index := hostRuntimeIndex(*ledger, r.runtimeID)
		if index < 0 || ledger.Runtimes[index].Closing {
			return errSandboxStateBusy
		}
		applyPendingHostUseReleases(&ledger.Runtimes[index], r.pendingHostUseReleases)
		ledger.Runtimes[index].ActiveUses++
		ledger.Runtimes[index].LastUpdatedTime = time.Now().UTC()
		return r.persistHostReceiptLedger(*ledger)
	}); err != nil {
		r.hostRegistrationMu.Unlock()
		releaseLocal()
		return nil, err
	}
	r.pendingHostUseReleases = 0
	r.hostRegistrationMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			if err := r.releaseHostRuntimeUse(); err != nil {
				r.recordWorkspaceSetupError(fmt.Errorf("release Host runtime use: %w", err))
			}
			releaseLocal()
		})
	}, nil
}

func (r *runtime) recoverDeletingEnvironmentBeforeUse(ctx context.Context) error {
	manifest, err := r.readManifest()
	if os.IsNotExist(err) || (err == nil && manifest.Phase != manifestPhaseDeleting) {
		return nil
	}
	if err != nil {
		return err
	}
	target := pathutil.Normalize(manifest.DeletingEnvRoot)
	if target == "" || pathutil.Key(target) != pathutil.Key(r.registeredEnvRoot) {
		return fmt.Errorf("impl/sandbox/windows: invalid pending sandbox environment deletion %s", target)
	}
	if !r.stateCoordinator.canPruneACLs() {
		return errSandboxStateBusy
	}
	return r.withHostReceiptLedger(func(ledger *hostReceiptLedger) error {
		if hostEnvironmentBusyExcept(*ledger, target, r.runtimeID) {
			return errSandboxStateBusy
		}
		if err := r.retireEnvironmentReceiptsTransaction(ctx, r.manifestPath(), &manifest, target, ledger); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		return finalizeEnvironmentDeletion(r.manifestPath(), &manifest)
	})
}

func (r *runtime) releaseHostRuntimeUse() error {
	r.hostRegistrationMu.Lock()
	defer r.hostRegistrationMu.Unlock()
	err := r.withHostReceiptLedger(func(ledger *hostReceiptLedger) error {
		index := hostRuntimeIndex(*ledger, r.runtimeID)
		if index < 0 {
			return nil
		}
		lease := &ledger.Runtimes[index]
		applyPendingHostUseReleases(lease, r.pendingHostUseReleases+1)
		if lease.Closing && lease.ActiveUses == 0 {
			ledger.Runtimes = append(ledger.Runtimes[:index], ledger.Runtimes[index+1:]...)
		} else {
			lease.LastUpdatedTime = time.Now().UTC()
		}
		return r.persistHostReceiptLedger(*ledger)
	})
	if err != nil {
		r.pendingHostUseReleases++
		return err
	}
	r.pendingHostUseReleases = 0
	return nil
}

func (r *runtime) closeHostRuntime() error {
	r.hostRegistrationMu.Lock()
	defer r.hostRegistrationMu.Unlock()
	if !r.hostRegistered {
		return nil
	}
	return r.withHostReceiptLedger(func(ledger *hostReceiptLedger) error {
		index := hostRuntimeIndex(*ledger, r.runtimeID)
		if index < 0 {
			r.pendingHostUseReleases = 0
			r.hostRegistered = false
			return nil
		}
		applyPendingHostUseReleases(&ledger.Runtimes[index], r.pendingHostUseReleases)
		if ledger.Runtimes[index].ActiveUses == 0 {
			ledger.Runtimes = append(ledger.Runtimes[:index], ledger.Runtimes[index+1:]...)
		} else {
			ledger.Runtimes[index].Closing = true
			ledger.Runtimes[index].LastUpdatedTime = time.Now().UTC()
		}
		if err := r.persistHostReceiptLedger(*ledger); err != nil {
			return err
		}
		r.pendingHostUseReleases = 0
		r.hostRegistered = false
		return nil
	})
}

func applyPendingHostUseReleases(lease *hostRuntimeLease, count int) {
	if lease == nil || count <= 0 {
		return
	}
	if count >= lease.ActiveUses {
		lease.ActiveUses = 0
		return
	}
	lease.ActiveUses -= count
}

func (r *runtime) hostCanPruneACLs(ledger hostReceiptLedger) bool {
	for _, lease := range ledger.Runtimes {
		if durablePathKey(lease.StateRoot) != durablePathKey(r.stateRoot) {
			continue
		}
		if lease.ID != r.runtimeID || lease.ActiveUses > 1 || lease.Closing {
			return false
		}
	}
	return true
}

func (r *runtime) hostResetBusy(ledger hostReceiptLedger) bool {
	for _, lease := range ledger.Runtimes {
		if durablePathKey(lease.StateRoot) != durablePathKey(r.stateRoot) {
			continue
		}
		if lease.ID != r.runtimeID || lease.ActiveUses > 0 || lease.Closing {
			return true
		}
	}
	return false
}

func hostEnvironmentBusy(ledger hostReceiptLedger, envRoot string) bool {
	return hostEnvironmentBusyExcept(ledger, envRoot, "")
}

func hostEnvironmentBusyExcept(ledger hostReceiptLedger, envRoot, ignoredRuntimeID string) bool {
	for _, lease := range ledger.Runtimes {
		if lease.ID == ignoredRuntimeID && lease.ActiveUses == 0 && !lease.Closing {
			continue
		}
		if durablePathKey(lease.SandboxEnvRoot) == durablePathKey(envRoot) {
			return true
		}
	}
	return false
}

func hostRuntimeIndex(ledger hostReceiptLedger, runtimeID string) int {
	for i := range ledger.Runtimes {
		if ledger.Runtimes[i].ID == runtimeID {
			return i
		}
	}
	return -1
}

func pruneDeadHostRuntimes(ledger *hostReceiptLedger) (bool, error) {
	if ledger == nil {
		return false, nil
	}
	out := ledger.Runtimes[:0]
	changed := false
	for _, lease := range ledger.Runtimes {
		alive, err := win32.ProcessIdentityAlive(lease.Process)
		if err != nil {
			return false, fmt.Errorf("impl/sandbox/windows: verify Host runtime %s liveness: %w", lease.ID, err)
		}
		if !alive {
			changed = true
			continue
		}
		out = append(out, lease)
	}
	ledger.Runtimes = out
	return changed, nil
}

func (r *runtime) readHostReceiptLedger() (hostReceiptLedger, error) {
	data, err := os.ReadFile(r.hostReceiptLedgerPath())
	if os.IsNotExist(err) {
		return hostReceiptLedger{Version: hostReceiptLedgerVersion}, nil
	}
	if err != nil {
		return hostReceiptLedger{}, err
	}
	var ledger hostReceiptLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return hostReceiptLedger{}, fmt.Errorf("impl/sandbox/windows: decode Host ACL receipt ledger: %w", err)
	}
	if ledger.Version != hostReceiptLedgerVersion {
		return hostReceiptLedger{}, fmt.Errorf("impl/sandbox/windows: unsupported Host ACL receipt ledger version %d", ledger.Version)
	}
	seen := map[string]struct{}{}
	for i := range ledger.Effects {
		effect := &ledger.Effects[i]
		effect.Path = durableCleanPath(effect.Path)
		effect.Entry.Principal = strings.TrimSpace(effect.Entry.Principal)
		effect.References = dedupeSortedStrings(effect.References)
		if effect.Path == "" || durablePathKey(effect.Path) != durablePathKey(effect.Receipt.Path) {
			return hostReceiptLedger{}, fmt.Errorf("impl/sandbox/windows: Host ACL receipt ledger effect has mismatched path")
		}
		if err := acl.ValidateACEReceiptEntry(effect.Receipt, effect.Entry); err != nil {
			return hostReceiptLedger{}, fmt.Errorf("impl/sandbox/windows: Host ACL receipt ledger effect for %s is invalid: %w", effect.Path, err)
		}
		key := receiptEffectKey(effect.Path, effect.Entry)
		if _, ok := seen[key]; ok {
			return hostReceiptLedger{}, fmt.Errorf("impl/sandbox/windows: Host ACL receipt ledger has duplicate effect %s", effect.Path)
		}
		seen[key] = struct{}{}
	}
	runtimeIDs := map[string]struct{}{}
	for i := range ledger.Runtimes {
		lease := &ledger.Runtimes[i]
		lease.ID = strings.TrimSpace(lease.ID)
		lease.StateRoot = durableCleanPath(lease.StateRoot)
		lease.WorkspaceRoot = durableCleanPath(lease.WorkspaceRoot)
		lease.SandboxEnvRoot = durableCleanPath(lease.SandboxEnvRoot)
		if lease.ID == "" || lease.Process.PID == 0 || lease.Process.CreationTime == 0 || lease.StateRoot == "" || lease.WorkspaceRoot == "" {
			return hostReceiptLedger{}, fmt.Errorf("impl/sandbox/windows: Host runtime lease is incomplete")
		}
		if lease.ActiveUses < 0 {
			return hostReceiptLedger{}, fmt.Errorf("impl/sandbox/windows: Host runtime lease %s has negative active use count", lease.ID)
		}
		if _, ok := runtimeIDs[lease.ID]; ok {
			return hostReceiptLedger{}, fmt.Errorf("impl/sandbox/windows: duplicate Host runtime lease %s", lease.ID)
		}
		runtimeIDs[lease.ID] = struct{}{}
	}
	return ledger, nil
}

func validatePreparedReceiptsForEffects(manifest workspaceManifest, desired []receiptEffect) error {
	desiredKeys := make(map[string]struct{}, len(desired))
	for _, effect := range desired {
		desiredKeys[receiptEffectKey(effect.Path, effect.Entry)] = struct{}{}
	}
	for _, managed := range manifest.ManagedReceipts {
		if managed.Applied {
			continue
		}
		if _, ok := desiredKeys[receiptEffectKey(managed.Path, managed.Entry)]; !ok {
			return fmt.Errorf("impl/sandbox/windows: prepared receipt for %s is outside the current validated policy", managed.Path)
		}
	}
	return nil
}

func (r *runtime) persistHostReceiptLedger(ledger hostReceiptLedger) error {
	ledger.Version = hostReceiptLedgerVersion
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	path := r.hostReceiptLedgerPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".host_acl_receipts.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *runtime) hostReceiptForEffect(ledger *hostReceiptLedger, effect receiptEffect) (acl.ACEReceipt, bool, error) {
	if ledger == nil {
		return acl.ACEReceipt{}, false, fmt.Errorf("impl/sandbox/windows: Host ACL receipt ledger is required")
	}
	if index := hostReceiptIndex(*ledger, effect.Path, effect.Entry); index >= 0 {
		record := ledger.Effects[index]
		if len(record.References) > 0 {
			return record.Receipt, record.Applied, nil
		}
		if err := settleUnreferencedHostReceipt(record); err != nil {
			return acl.ACEReceipt{}, false, err
		}
		ledger.Effects = append(ledger.Effects[:index], ledger.Effects[index+1:]...)
		if err := r.persistHostReceiptLedger(*ledger); err != nil {
			return acl.ACEReceipt{}, false, err
		}
	}
	receipt, err := acl.PrepareExactFileDACLEntry(effect.Path, effect.Entry)
	return receipt, false, err
}

// registerHostReceipt writes the workspace reference before the ACL effect.
// It returns true when the manifest receipt was replaced by the Host-owned
// canonical receipt and therefore must be persisted again before Ensure.
func (r *runtime) registerHostReceipt(ledger *hostReceiptLedger, managed *manifestReceipt, reference string) (bool, error) {
	if ledger == nil || managed == nil || reference == "" {
		return false, fmt.Errorf("impl/sandbox/windows: Host ACL receipt registration is incomplete")
	}
	index := hostReceiptIndex(*ledger, managed.Path, managed.Entry)
	manifestChanged := false
	ledgerChanged := false
	if index < 0 {
		ledger.Effects = append(ledger.Effects, hostReceiptEffect{
			Path:    managed.Path,
			Entry:   managed.Entry,
			Receipt: managed.Receipt,
			Applied: managed.Applied,
		})
		index = len(ledger.Effects) - 1
		ledgerChanged = true
	} else if !sameACEReceipt(managed.Receipt, ledger.Effects[index].Receipt) {
		managed.Receipt = ledger.Effects[index].Receipt
		manifestChanged = true
	}
	if ledger.Effects[index].Applied && !managed.Applied {
		managed.Applied = true
		manifestChanged = true
	}
	if managed.Applied && !ledger.Effects[index].Applied {
		ledger.Effects[index].Applied = true
		ledgerChanged = true
	}
	if !containsExactString(ledger.Effects[index].References, reference) {
		ledger.Effects[index].References = append(ledger.Effects[index].References, reference)
		ledger.Effects[index].References = dedupeSortedStrings(ledger.Effects[index].References)
		ledgerChanged = true
	}
	if ledgerChanged {
		if err := r.persistHostReceiptLedger(*ledger); err != nil {
			return false, err
		}
	}
	return manifestChanged, nil
}

func (r *runtime) releaseHostReceipt(ledger *hostReceiptLedger, managed manifestReceipt, reference string) error {
	if ledger == nil || reference == "" {
		return fmt.Errorf("impl/sandbox/windows: Host ACL receipt release is incomplete")
	}
	index := hostReceiptIndex(*ledger, managed.Path, managed.Entry)
	if index < 0 {
		// Adopt pre-ledger exact provenance before changing its reference count.
		ledger.Effects = append(ledger.Effects, hostReceiptEffect{
			Path:       managed.Path,
			Entry:      managed.Entry,
			Receipt:    managed.Receipt,
			Applied:    managed.Applied,
			References: []string{reference},
		})
		index = len(ledger.Effects) - 1
		if err := r.persistHostReceiptLedger(*ledger); err != nil {
			return err
		}
	}
	record := &ledger.Effects[index]
	if containsExactString(record.References, reference) {
		refs := record.References[:0]
		for _, candidate := range record.References {
			if candidate != reference {
				refs = append(refs, candidate)
			}
		}
		record.References = refs
		if err := r.persistHostReceiptLedger(*ledger); err != nil {
			return err
		}
	}
	if len(record.References) > 0 {
		return nil
	}
	if err := settleUnreferencedHostReceipt(*record); err != nil {
		return fmt.Errorf("impl/sandbox/windows: retire exact ACL receipt from %s: %w", record.Path, err)
	}
	ledger.Effects = append(ledger.Effects[:index], ledger.Effects[index+1:]...)
	return r.persistHostReceiptLedger(*ledger)
}

func settleUnreferencedHostReceipt(record hostReceiptEffect) error {
	if _, err := os.Lstat(record.Path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !record.Applied {
		// Ensure closes both write-ahead crash windows: a prepared receipt may
		// have been interrupted immediately before or immediately after its effect.
		if err := acl.EnsureFileDACLReceipt(record.Path, record.Receipt); err != nil {
			return err
		}
	}
	_, err := acl.RemoveFileDACLReceipt(record.Path, record.Receipt)
	return err
}

func hostReceiptIndex(ledger hostReceiptLedger, path string, entry acl.Entry) int {
	key := receiptEffectKey(path, entry)
	for i := range ledger.Effects {
		if receiptEffectKey(ledger.Effects[i].Path, ledger.Effects[i].Entry) == key {
			return i
		}
	}
	return -1
}

func hostReceiptReferenceForManifest(manifestPath string) string {
	return workspaceStateID(manifestPath)
}

func containsExactString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func dedupeSortedStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func sameACEReceipt(a, b acl.ACEReceipt) bool {
	aJSON, aErr := json.Marshal(a)
	bJSON, bErr := json.Marshal(b)
	return aErr == nil && bErr == nil && string(aJSON) == string(bJSON)
}

type receiptEffect struct {
	Path  string
	Entry acl.Entry
}

func receiptEffects(policy workspacePolicy) []receiptEffect {
	var effects []receiptEffect
	for _, root := range policy.WriteRoots {
		if sid := policy.sidForWriteRoot(root); sid != "" {
			effects = append(effects, receiptEffect{Path: root, Entry: acl.Entry{Principal: sid, Rights: acl.Modify, Mode: acl.Grant, Inherit: true}})
		}
	}
	if sid := policy.sidForWriteRoot(policy.SandboxEnvRoot); sid != "" {
		for _, path := range sandboxEnvDirs(policy.SandboxEnvRoot) {
			if !pathListContains(policy.WriteRoots, path) {
				effects = append(effects, receiptEffect{Path: path, Entry: acl.Entry{Principal: sid, Rights: acl.Modify, Mode: acl.Grant, Inherit: true}})
			}
		}
	}
	for _, path := range policy.DenyWritePaths {
		if sid := policy.sidForCoveredPath(path); sid != "" {
			effects = append(effects, receiptEffect{Path: path, Entry: acl.Entry{Principal: sid, Rights: acl.Write, Mode: acl.Deny, Inherit: true}})
		}
	}
	return effects
}

func (p workspacePolicy) sidForCoveredPath(path string) string {
	path = pathutil.Normalize(path)
	bestRoot := ""
	bestSID := ""
	for root, sid := range p.WriteRootCapabilitySIDs {
		root = pathutil.Normalize(root)
		if root != "" && pathutil.IsUnder(path, root) && len(root) > len(bestRoot) {
			bestRoot, bestSID = root, strings.TrimSpace(sid)
		}
	}
	return bestSID
}

func receiptEffectKey(path string, entry acl.Entry) string {
	return strings.Join([]string{durablePathKey(path), strings.ToUpper(strings.TrimSpace(entry.Principal)), string(entry.Mode), string(entry.Rights), fmt.Sprint(entry.Inherit)}, "\x00")
}

// durablePathKey compares paths that have already crossed a normalization
// boundary before they were persisted. Re-running EvalSymlinks for every
// historical Host-ledger entry makes each ACL transaction progressively more
// expensive and is unnecessary: exact receipt effects re-open the object with
// no-reparse/final-path/FileID validation before touching its DACL.
func durablePathKey(path string) string {
	path = durableCleanPath(path)
	if path == "" {
		return ""
	}
	return strings.ToLower(path)
}

func durableCleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return ""
	}
	return path
}

func receiptIndex(receipts []manifestReceipt, key string) int {
	for i, receipt := range receipts {
		if receiptEffectKey(receipt.Path, receipt.Entry) == key {
			return i
		}
	}
	return -1
}

func receiptManifestCovers(manifest workspaceManifest, effects []receiptEffect) bool {
	if manifest.Phase != manifestPhaseActive {
		return false
	}
	for _, effect := range effects {
		index := receiptIndex(manifest.ManagedReceipts, receiptEffectKey(effect.Path, effect.Entry))
		if index < 0 || !manifest.ManagedReceipts[index].Applied {
			return false
		}
	}
	return len(manifest.ManagedReceipts) == len(effects)
}

func appendReceipt(receipts []manifestReceipt, receipt manifestReceipt) []manifestReceipt {
	if receiptIndex(receipts, receiptEffectKey(receipt.Path, receipt.Entry)) >= 0 {
		return receipts
	}
	return append(receipts, receipt)
}

func dedupeManifestReceipts(receipts []manifestReceipt) []manifestReceipt {
	out := make([]manifestReceipt, 0, len(receipts))
	for _, receipt := range receipts {
		out = appendReceipt(out, receipt)
	}
	return out
}

func dedupeReceiptEffects(effects []receiptEffect) []receiptEffect {
	out := make([]receiptEffect, 0, len(effects))
	seen := map[string]struct{}{}
	for _, effect := range effects {
		key := receiptEffectKey(effect.Path, effect.Entry)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, effect)
	}
	return out
}
