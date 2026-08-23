//go:build windows

package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/acl"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/capability"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/win32"
	"golang.org/x/sys/windows"
)

const elevatedRepairRequestVersion = 2

type elevatedRepairRequest struct {
	Version                  int                `json:"version"`
	Config                   sandbox.Config     `json:"config"`
	HostUserSID              string             `json:"host_user_sid"`
	HostReceiptAuthorityRoot string             `json:"host_receipt_authority_root"`
	AuthorityIdentity        win32.FileIdentity `json:"authority_identity"`
	RequestName              string             `json:"request_name"`
	RequestIdentity          win32.FileIdentity `json:"request_identity"`
	ResultName               string             `json:"result_name"`
	ResultIdentity           win32.FileIdentity `json:"result_identity"`
	PolicyHash               string             `json:"policy_hash"`
	Receipts                 []manifestReceipt  `json:"receipts"`
	RetireReceipts           []manifestReceipt  `json:"retire_receipts,omitempty"`
	Nonce                    string             `json:"nonce"`
}

type elevatedRepairResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Nonce string `json:"nonce"`
}

type elevatedRepairProcessLauncher func(context.Context, string, []string, string) (uint32, error)

var launchElevatedRepairProcess elevatedRepairProcessLauncher = launchElevatedRepairProcessDefault

func (r *runtime) runElevatedRepair(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.verifyRepairRootsWritable(ctx); err != nil {
		return err
	}
	if err := r.ensureHostReceiptAuthority(); err != nil {
		return err
	}
	policy, err := r.derivePolicyForRequest(sandbox.CommandRequest{
		Dir:         r.cfg.CWD,
		Constraints: sandbox.Constraints{Route: sandbox.RouteSandbox, Backend: sandbox.BackendWindows, Permission: sandbox.PermissionWorkspaceWrite, Network: sandbox.NetworkEnabled},
	})
	if err != nil {
		return err
	}
	manifest, err := r.readManifest()
	if err != nil {
		return fmt.Errorf("impl/sandbox/windows: read prepared repair receipts: %w", err)
	}
	receipts, err := exactRepairReceipts(manifest, receiptEffects(policy))
	if err != nil {
		return err
	}
	retireReceipts, err := exactElevatedLegacyRetiringReceipts(manifest, policy, r.capabilityStorePath())
	if err != nil {
		return err
	}
	authority, err := win32.OpenStableDirectory(r.hostReceiptAuthorityRoot, r.hostUserSID)
	if err != nil {
		return err
	}
	defer authority.Close()
	repairID, err := newID("repair")
	if err != nil {
		return err
	}
	nonce, err := newID("nonce")
	if err != nil {
		return err
	}
	requestName := repairID + ".request.json"
	resultName := repairID + ".result.json"
	requestFile, requestIdentity, err := authority.CreateNewFile(requestName)
	if err != nil {
		return fmt.Errorf("impl/sandbox/windows: create repair request: %w", err)
	}
	resultFile, resultIdentity, err := authority.CreateNewFile(resultName)
	if err != nil {
		_ = requestFile.Close()
		_ = os.Remove(filepath.Join(authority.Path(), requestName))
		return fmt.Errorf("impl/sandbox/windows: create repair result: %w", err)
	}
	defer func() {
		_ = requestFile.Close()
		_ = resultFile.Close()
		_ = os.Remove(filepath.Join(authority.Path(), requestName))
		_ = os.Remove(filepath.Join(authority.Path(), resultName))
	}()

	cfg := sandbox.NormalizeConfig(r.cfg)
	cfg.RequestedBackend = sandbox.BackendWindows
	request := elevatedRepairRequest{
		Version:                  elevatedRepairRequestVersion,
		Config:                   cfg,
		HostUserSID:              r.hostUserSID,
		HostReceiptAuthorityRoot: r.hostReceiptAuthorityRoot,
		AuthorityIdentity:        authority.Identity(),
		RequestName:              requestName,
		RequestIdentity:          requestIdentity,
		ResultName:               resultName,
		ResultIdentity:           resultIdentity,
		PolicyHash:               policy.PolicyHash,
		Receipts:                 receipts,
		RetireReceipts:           retireReceipts,
		Nonce:                    nonce,
	}
	data, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return err
	}
	if err := win32.WriteStableFile(requestFile, data); err != nil {
		return fmt.Errorf("impl/sandbox/windows: write elevated repair request: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("impl/sandbox/windows: resolve current executable: %w", err)
	}
	exitCode, launchErr := launchElevatedRepairProcess(ctx, exe, []string{
		internalRepairHelperCommand,
		"-authority-root", authority.Path(),
		"-authority-identity", encodeFileIdentity(authority.Identity()),
		"-request-name", requestName,
		"-request-identity", encodeFileIdentity(requestIdentity),
		"-result-name", resultName,
		"-result-identity", encodeFileIdentity(resultIdentity),
	}, cfg.CWD)
	result, resultErr := readElevatedRepairResultFile(resultFile)
	if launchErr != nil {
		if resultErr == nil && strings.TrimSpace(result.Error) != "" {
			return fmt.Errorf("impl/sandbox/windows: elevated sandbox repair failed: %s", result.Error)
		}
		return fmt.Errorf("impl/sandbox/windows: launch elevated sandbox repair: %w", launchErr)
	}
	if resultErr != nil {
		return fmt.Errorf("impl/sandbox/windows: read elevated sandbox repair result: %w", resultErr)
	}
	if strings.TrimSpace(result.Error) != "" {
		return fmt.Errorf("impl/sandbox/windows: elevated sandbox repair failed: %s", result.Error)
	}
	if exitCode != 0 || !result.OK {
		return fmt.Errorf("impl/sandbox/windows: elevated sandbox repair exited with code %d", exitCode)
	}
	if result.Nonce != nonce {
		return fmt.Errorf("impl/sandbox/windows: elevated sandbox repair result nonce mismatch")
	}
	return nil
}

func (r *runtime) verifyRepairRootsWritable(ctx context.Context) error {
	policy, err := r.policyForRequest(sandbox.CommandRequest{
		Dir: r.cfg.CWD,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	})
	if err != nil {
		return err
	}
	for _, root := range policy.WriteRoots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := verifyDirectoryFileWrite(root); err != nil {
			return fmt.Errorf("impl/sandbox/windows: elevated repair refuses root without current-user file write access %s: %w", root, err)
		}
	}
	return nil
}

func verifyDirectoryFileWrite(root string) error {
	root = pathutil.Normalize(root)
	if root == "" {
		return fmt.Errorf("path is required")
	}
	file, err := os.CreateTemp(root, ".caelis-repair-probe-*")
	if err != nil {
		return err
	}
	name := file.Name()
	closeErr := file.Close()
	removeErr := os.Remove(name)
	return errors.Join(closeErr, removeErr)
}

func runInternalRepairHelper(args []string) error {
	fs := flag.NewFlagSet(internalRepairHelperCommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var authorityRoot, authorityIdentityValue, requestName, requestIdentityValue, resultName, resultIdentityValue string
	fs.StringVar(&authorityRoot, "authority-root", "", "repair authority root")
	fs.StringVar(&authorityIdentityValue, "authority-identity", "", "repair authority identity")
	fs.StringVar(&requestName, "request-name", "", "repair request basename")
	fs.StringVar(&requestIdentityValue, "request-identity", "", "repair request identity")
	fs.StringVar(&resultName, "result-name", "", "repair result basename")
	fs.StringVar(&resultIdentityValue, "result-identity", "", "repair result identity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	authorityIdentity, err := decodeFileIdentity(authorityIdentityValue)
	if err != nil {
		return err
	}
	requestIdentity, err := decodeFileIdentity(requestIdentityValue)
	if err != nil {
		return err
	}
	resultIdentity, err := decodeFileIdentity(resultIdentityValue)
	if err != nil {
		return err
	}
	authority, err := win32.OpenStableDirectory(authorityRoot, "")
	if err != nil {
		return err
	}
	defer authority.Close()
	if authority.Identity() != authorityIdentity {
		return fmt.Errorf("impl/sandbox/windows: repair authority identity changed")
	}
	requestFile, err := authority.OpenExpectedFile(requestName, requestIdentity, "", false)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(requestFile)
	closeErr := requestFile.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	request, err := readValidatedElevatedRepairRequest(data)
	if err != nil {
		return err
	}
	if authority.Identity() != request.AuthorityIdentity || request.RequestName != requestName || request.RequestIdentity != requestIdentity || request.ResultName != resultName || request.ResultIdentity != resultIdentity {
		return fmt.Errorf("impl/sandbox/windows: repair IPC identity binding mismatch")
	}
	if durablePathKey(authority.Path()) != durablePathKey(request.HostReceiptAuthorityRoot) {
		return fmt.Errorf("impl/sandbox/windows: repair authority path binding mismatch")
	}
	if !strings.EqualFold(authority.OwnerSID(), request.HostUserSID) {
		return fmt.Errorf("impl/sandbox/windows: repair authority owner %s does not match Host user %s", authority.OwnerSID(), request.HostUserSID)
	}
	requestOwnerCheck, err := authority.OpenExpectedFile(requestName, requestIdentity, request.HostUserSID, false)
	if err != nil {
		return fmt.Errorf("impl/sandbox/windows: verify repair request owner: %w", err)
	}
	if err := requestOwnerCheck.Close(); err != nil {
		return err
	}
	if err := validateElevatedRepairAuthority(authority.Path(), request.HostUserSID); err != nil {
		return err
	}
	resultFile, err := authority.OpenExpectedFile(resultName, resultIdentity, request.HostUserSID, true)
	if err != nil {
		return err
	}
	defer resultFile.Close()
	opErr := runInternalRepairRequest(request)
	resultErr := writeElevatedRepairResultFile(resultFile, request.Nonce, opErr)
	if opErr != nil {
		return errors.Join(opErr, resultErr)
	}
	return resultErr
}

func readValidatedElevatedRepairRequest(data []byte) (elevatedRepairRequest, error) {
	var request elevatedRepairRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return elevatedRepairRequest{}, fmt.Errorf("decode repair request: %w", err)
	}
	if request.Version != elevatedRepairRequestVersion {
		return elevatedRepairRequest{}, fmt.Errorf("unsupported repair request version %d", request.Version)
	}
	normalizedHostSID, err := win32.NormalizeSID(request.HostUserSID)
	if err != nil {
		return elevatedRepairRequest{}, err
	}
	request.HostUserSID = normalizedHostSID
	request.HostReceiptAuthorityRoot = pathutil.Normalize(request.HostReceiptAuthorityRoot)
	cfg := sandbox.NormalizeConfig(request.Config)
	if err := validateElevatedRepairConfig(cfg, request.HostUserSID); err != nil {
		return elevatedRepairRequest{}, err
	}
	request.Config = cfg
	return request, nil
}

func runInternalRepairRequest(request elevatedRepairRequest) error {
	stateRoot, err := resolveStateRoot(request.Config.StateDir)
	if err != nil {
		return err
	}
	windowsRuntime := &runtime{
		cfg:                      request.Config,
		stateRoot:                stateRoot,
		hostUserSID:              request.HostUserSID,
		hostReceiptAuthorityRoot: request.HostReceiptAuthorityRoot,
	}
	policy, err := windowsRuntime.derivePolicyForRequest(sandbox.CommandRequest{
		Dir:         request.Config.CWD,
		Constraints: sandbox.Constraints{Route: sandbox.RouteSandbox, Backend: sandbox.BackendWindows, Permission: sandbox.PermissionWorkspaceWrite, Network: sandbox.NetworkEnabled},
	})
	if err != nil {
		return err
	}
	if policy.PolicyHash != request.PolicyHash {
		return fmt.Errorf("impl/sandbox/windows: elevated repair policy hash changed")
	}
	if err := validateRepairReceipts(request.Receipts, receiptEffects(policy)); err != nil {
		return err
	}
	if err := validateRepairReceiptsOptional(request.RetireReceipts, legacyV1ReceiptEffects(policy, windowsRuntime.capabilityStorePath())); err != nil {
		return fmt.Errorf("impl/sandbox/windows: validate elevated legacy retirement: %w", err)
	}
	if err := validateRepairReceiptOwners(request.HostUserSID, request.Receipts, request.RetireReceipts); err != nil {
		return err
	}
	for _, managed := range request.Receipts {
		if err := acl.EnsureFileDACLReceipt(managed.Path, managed.Receipt); err != nil {
			return fmt.Errorf("impl/sandbox/windows: elevated exact ACL repair for %s: %w", managed.Path, err)
		}
	}
	for _, managed := range request.Receipts {
		if err := acl.VerifyFileDACLReceipt(managed.Path, managed.Receipt); err != nil {
			return fmt.Errorf("impl/sandbox/windows: verify elevated replacement ACL on %s: %w", managed.Path, err)
		}
	}
	for _, retiring := range request.RetireReceipts {
		if _, err := acl.RemoveFileDACLReceipt(retiring.Path, retiring.Receipt); err != nil {
			return fmt.Errorf("impl/sandbox/windows: retire elevated exact legacy ACL on %s: %w", retiring.Path, err)
		}
	}
	return nil
}

func validateRepairReceiptOwners(hostUserSID string, receiptSets ...[]manifestReceipt) error {
	normalizedHostSID, err := win32.NormalizeSID(hostUserSID)
	if err != nil {
		return err
	}
	for _, receipts := range receiptSets {
		for _, managed := range receipts {
			ownerSID, err := win32.NormalizeSID(managed.Receipt.OwnerSID)
			if err != nil {
				return fmt.Errorf("impl/sandbox/windows: elevated repair receipt owner on %s: %w", managed.Path, err)
			}
			authorized, err := win32.CurrentProcessAuthorizesOwner(normalizedHostSID, ownerSID)
			if err != nil {
				return fmt.Errorf("impl/sandbox/windows: authorize elevated repair receipt owner on %s: %w", managed.Path, err)
			}
			if !authorized {
				return fmt.Errorf("impl/sandbox/windows: elevated repair refuses receipt on %s not owned by the Host user", managed.Path)
			}
		}
	}
	return nil
}

func validateElevatedRepairAuthority(authorityRoot, hostUserSID string) error {
	authorityRoot = pathutil.Normalize(authorityRoot)
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(hostUserSID))))
	if !strings.EqualFold(filepath.Base(authorityRoot), hex.EncodeToString(sum[:])[:16]) {
		return fmt.Errorf("impl/sandbox/windows: Host receipt authority is not bound to Host user SID")
	}
	return nil
}

func readElevatedRepairResultFile(file *os.File) (elevatedRepairResult, error) {
	if file == nil {
		return elevatedRepairResult{}, fmt.Errorf("result file is required")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return elevatedRepairResult{}, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return elevatedRepairResult{}, err
	}
	var result elevatedRepairResult
	if err := json.Unmarshal(data, &result); err != nil {
		return elevatedRepairResult{}, err
	}
	return result, nil
}

func writeElevatedRepairResultFile(file *os.File, nonce string, opErr error) error {
	result := elevatedRepairResult{OK: opErr == nil, Nonce: nonce}
	if opErr != nil {
		result.Error = opErr.Error()
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return win32.WriteStableFile(file, data)
}

func exactRepairReceipts(manifest workspaceManifest, effects []receiptEffect) ([]manifestReceipt, error) {
	expected := make(map[string]struct{}, len(effects))
	for _, effect := range effects {
		expected[receiptEffectKey(effect.Path, effect.Entry)] = struct{}{}
	}
	out := make([]manifestReceipt, 0, len(manifest.ManagedReceipts))
	for _, managed := range manifest.ManagedReceipts {
		if managed.Applied {
			continue
		}
		if _, ok := expected[receiptEffectKey(managed.Path, managed.Entry)]; ok {
			out = append(out, managed)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("impl/sandbox/windows: no prepared exact ACL repair receipt is available")
	}
	if err := validateRepairReceipts(out, effects); err != nil {
		return nil, err
	}
	return out, nil
}

func validateRepairReceipts(receipts []manifestReceipt, effects []receiptEffect) error {
	if len(receipts) == 0 || len(receipts) > len(effects) {
		return fmt.Errorf("impl/sandbox/windows: elevated repair receipt set size %d is outside expected effect set %d", len(receipts), len(effects))
	}
	expected := make(map[string]struct{}, len(effects))
	for _, effect := range effects {
		expected[receiptEffectKey(effect.Path, effect.Entry)] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, managed := range receipts {
		if durablePathKey(managed.Path) != durablePathKey(managed.Receipt.Path) {
			return fmt.Errorf("impl/sandbox/windows: elevated repair receipt path mismatch for %s", managed.Path)
		}
		if err := acl.ValidateACEReceiptEntry(managed.Receipt, managed.Entry); err != nil {
			return err
		}
		key := receiptEffectKey(managed.Path, managed.Entry)
		if _, ok := expected[key]; !ok {
			return fmt.Errorf("impl/sandbox/windows: elevated repair receipt for %s is outside the expected policy", managed.Path)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("impl/sandbox/windows: duplicate elevated repair receipt for %s", managed.Path)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateRepairReceiptsOptional(receipts []manifestReceipt, effects []receiptEffect) error {
	if len(receipts) == 0 {
		return nil
	}
	return validateRepairReceipts(receipts, effects)
}

func exactElevatedLegacyRetiringReceipts(manifest workspaceManifest, policy workspacePolicy, storePath string) ([]manifestReceipt, error) {
	if len(manifest.LegacyACEs) == 0 && !manifest.LegacyMigrationPrepared {
		return nil, nil
	}
	expected := legacyV1ReceiptEffects(policy, storePath)
	expectedKeys := make(map[string]struct{}, len(expected))
	for _, effect := range expected {
		expectedKeys[receiptEffectKey(effect.Path, effect.Entry)] = struct{}{}
	}
	legacyKeys := make(map[string]struct{}, len(manifest.LegacyACEs))
	for _, legacy := range manifest.LegacyACEs {
		entry := acl.Entry{
			Principal: legacy.Principal,
			Mode:      acl.Mode(legacy.Mode),
			Rights:    acl.Rights(legacy.Rights),
			Inherit:   legacy.Inherit,
		}
		legacyKeys[receiptEffectKey(legacy.Path, entry)] = struct{}{}
	}
	var out []manifestReceipt
	for _, retiring := range manifest.RetiringReceipts {
		key := receiptEffectKey(retiring.Path, retiring.Entry)
		if _, legacy := legacyKeys[key]; !legacy {
			continue
		}
		if _, independentlyDerived := expectedKeys[key]; !independentlyDerived {
			return nil, fmt.Errorf("impl/sandbox/windows: legacy ACL on %s uses a SID that elevated repair cannot independently derive; no replacement ACL was changed", retiring.Path)
		}
		out = append(out, retiring)
	}
	if len(out) != len(legacyKeys) {
		return nil, fmt.Errorf("impl/sandbox/windows: legacy ACL retirement provenance is incomplete; no replacement ACL was changed")
	}
	if err := validateRepairReceiptsOptional(out, expected); err != nil {
		return nil, err
	}
	return out, nil
}

func legacyV1ReceiptEffects(policy workspacePolicy, storePath string) []receiptEffect {
	rootSIDs := make(map[string]string, len(policy.WriteRoots))
	var effects []receiptEffect
	for _, root := range policy.WriteRoots {
		sid := capability.DeriveLegacyV1SID(storePath, root)
		rootSIDs[pathutil.Normalize(root)] = sid
		effects = append(effects, receiptEffect{Path: root, Entry: acl.Entry{Principal: sid, Rights: acl.Modify, Mode: acl.Grant, Inherit: true}})
	}
	if envRoot := pathutil.Normalize(policy.SandboxEnvRoot); envRoot != "" {
		sid := capability.DeriveLegacyV1SID(storePath, envRoot)
		rootSIDs[envRoot] = sid
		for _, path := range sandboxEnvDirs(envRoot) {
			if !pathListContains(policy.WriteRoots, path) {
				effects = append(effects, receiptEffect{Path: path, Entry: acl.Entry{Principal: sid, Rights: acl.Modify, Mode: acl.Grant, Inherit: true}})
			}
		}
	}
	for _, path := range policy.DenyWritePaths {
		for root, sid := range rootSIDs {
			if pathutil.IsUnder(path, root) {
				effects = append(effects, receiptEffect{Path: path, Entry: acl.Entry{Principal: sid, Rights: acl.Write, Mode: acl.Deny, Inherit: true}})
			}
		}
	}
	return dedupeReceiptEffects(effects)
}

func encodeFileIdentity(identity win32.FileIdentity) string {
	data, _ := json.Marshal(identity)
	return hex.EncodeToString(data)
}

func decodeFileIdentity(value string) (win32.FileIdentity, error) {
	data, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return win32.FileIdentity{}, err
	}
	var identity win32.FileIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return win32.FileIdentity{}, err
	}
	if identity.VolumeSerialNumber == 0 || identity.FileID == ([16]byte{}) {
		return win32.FileIdentity{}, fmt.Errorf("impl/sandbox/windows: repair IPC file identity is incomplete")
	}
	return identity, nil
}

func validateElevatedRepairConfig(cfg sandbox.Config, hostUserSID string) error {
	if sandbox.CanonicalBackend(cfg.RequestedBackend) != sandbox.BackendWindows {
		return fmt.Errorf("impl/sandbox/windows: elevated repair only supports the Windows sandbox backend")
	}
	workspaceRoot, err := pathutil.NormalizeWithBase("", cfg.CWD)
	if err != nil {
		return err
	}
	if workspaceRoot == "" {
		return fmt.Errorf("impl/sandbox/windows: workspace cwd is required for elevated repair")
	}
	if err := validateRepairDirectory("workspace", workspaceRoot, hostUserSID); err != nil {
		return err
	}
	stateRoot, err := resolveStateRoot(cfg.StateDir)
	if err != nil {
		return err
	}
	if err := validateRepairDirectory("state", stateRoot, hostUserSID); err != nil {
		return err
	}
	for _, root := range cfg.WritableRoots {
		normalized, err := pathutil.NormalizeWithBase(workspaceRoot, root)
		if err != nil {
			return err
		}
		if normalized == "" {
			continue
		}
		existing, err := existingWritableRoots([]string{normalized})
		if err != nil {
			return err
		}
		for _, path := range existing {
			if err := validateRepairDirectory("writable root", path, hostUserSID); err != nil {
				return err
			}
		}
	}
	for _, subpath := range cfg.ReadOnlySubpaths {
		if !safeRelativeSubpath(subpath) {
			return fmt.Errorf("impl/sandbox/windows: elevated repair refuses unsafe read-only subpath: %s", subpath)
		}
	}
	return nil
}

func validateRepairDirectory(label string, path string, hostUserSID string) error {
	path = pathutil.Normalize(path)
	if path == "" {
		return fmt.Errorf("impl/sandbox/windows: %s path is required", label)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("impl/sandbox/windows: inspect %s path %s: %w", label, path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("impl/sandbox/windows: %s path %s is not a directory", label, path)
	}
	if isUNCPath(path) || isVolumeRoot(path) || isKnownSystemPath(path) {
		return fmt.Errorf("impl/sandbox/windows: elevated repair refuses unsafe %s path: %s", label, path)
	}
	reparse, err := isReparsePoint(path)
	if err != nil {
		return fmt.Errorf("impl/sandbox/windows: inspect %s reparse point %s: %w", label, path, err)
	}
	if reparse {
		return fmt.Errorf("impl/sandbox/windows: elevated repair refuses reparse-point %s path: %s", label, path)
	}
	daclInfo, err := acl.InspectFileDACL(path)
	if err != nil {
		return fmt.Errorf("impl/sandbox/windows: inspect %s owner %s: %w", label, path, err)
	}
	authorized, err := win32.CurrentProcessAuthorizesOwner(hostUserSID, daclInfo.OwnerSID)
	if err != nil {
		return fmt.Errorf("impl/sandbox/windows: authorize %s owner %s: %w", label, path, err)
	}
	if !authorized {
		return fmt.Errorf("impl/sandbox/windows: elevated repair refuses %s path not owned by Host user %s: %s", label, hostUserSID, path)
	}
	return nil
}

func safeRelativeSubpath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return true
	}
	if filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func isUNCPath(path string) bool {
	return strings.HasPrefix(filepath.Clean(path), `\\`)
}

func isVolumeRoot(path string) bool {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return clean == string(filepath.Separator)
	}
	root := filepath.Clean(volume + string(filepath.Separator))
	return strings.EqualFold(clean, root)
}

func isKnownSystemPath(path string) bool {
	for _, root := range knownSystemRoots() {
		if root != "" && pathutil.IsUnder(path, root) {
			return true
		}
	}
	return false
}

func knownSystemRoots() []string {
	var roots []string
	for _, key := range []string{"WINDIR", "SystemRoot", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			roots = append(roots, pathutil.Normalize(value))
		}
	}
	return pathutil.Dedupe(roots)
}

func isReparsePoint(path string) (bool, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return false, err
	}
	return attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoConsole      = 0x00008000
	swHide                = 0
	waitTimeout           = 258
)

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

func launchElevatedRepairProcessDefault(ctx context.Context, exe string, args []string, cwd string) (uint32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(exe) == "" {
		return 0, fmt.Errorf("executable path is required")
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return 0, err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	params, err := windows.UTF16PtrFromString(composeWindowsParameters(args))
	if err != nil {
		return 0, err
	}
	var dir *uint16
	if strings.TrimSpace(cwd) != "" {
		dir, err = windows.UTF16PtrFromString(cwd)
		if err != nil {
			return 0, err
		}
	}
	info := shellExecuteInfo{
		cbSize:       uint32(unsafe.Sizeof(shellExecuteInfo{})),
		fMask:        seeMaskNoCloseProcess | seeMaskNoConsole,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		lpDirectory:  dir,
		nShow:        swHide,
	}
	r1, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		if !errors.Is(callErr, syscall.Errno(0)) {
			if errors.Is(callErr, windows.ERROR_CANCELLED) {
				return 0, fmt.Errorf("UAC prompt was cancelled")
			}
			return 0, callErr
		}
		return 0, syscall.EINVAL
	}
	if info.hProcess == 0 {
		return 0, nil
	}
	defer func() {
		_ = windows.CloseHandle(info.hProcess)
	}()
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		event, err := windows.WaitForSingleObject(info.hProcess, uint32((200*time.Millisecond)/time.Millisecond))
		if err != nil {
			return 0, err
		}
		switch event {
		case windows.WAIT_OBJECT_0:
			var exitCode uint32
			if err := windows.GetExitCodeProcess(info.hProcess, &exitCode); err != nil {
				return 0, err
			}
			return exitCode, nil
		case waitTimeout:
			continue
		default:
			return 0, fmt.Errorf("wait for elevated repair process returned %d", event)
		}
	}
}

func composeWindowsParameters(args []string) string {
	if len(args) == 0 {
		return ""
	}
	escaped := make([]string, 0, len(args))
	for _, arg := range args {
		escaped = append(escaped, windows.EscapeArg(arg))
	}
	return strings.Join(escaped, " ")
}
