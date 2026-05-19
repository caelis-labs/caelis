//go:build windows

package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/acl"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/netpolicy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/pathutil"
	winpolicy "github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/policy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setupstate"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
	"golang.org/x/sys/windows/registry"
)

func Execute(payload Payload) error {
	return ExecuteWithProgress(payload, nil)
}

func ExecuteWithProgress(payload Payload, progress ProgressFunc) error {
	payload = payload.Normalize()
	if strings.TrimSpace(payload.StateRoot) == "" {
		return fmt.Errorf("windows setup: state root is required")
	}
	if payload.Version != PayloadVersion {
		return fmt.Errorf("windows setup: unsupported payload version %d", payload.Version)
	}
	switch payload.Kind {
	case SetupKindReset:
		return executeReset(payload, progress)
	case SetupKindRuntimeRefresh:
		return executeRuntimeRefresh(payload, progress)
	case SetupKindWorkspaceOnly:
		return executeWorkspaceOnly(payload, progress)
	case SetupKindFull:
	default:
		return fmt.Errorf("windows setup: unsupported setup kind %q", payload.Kind)
	}
	elevated, err := win32.IsElevated()
	if err != nil {
		return fmt.Errorf("windows setup: check elevation: %w", err)
	}
	if !elevated {
		return fmt.Errorf("windows setup: administrator elevation is required")
	}

	dirs := setupstate.NewDirs(payload.StateRoot)
	reportProgress(payload, progress, Progress{Phase: "state", Message: "preparing sandbox state directories", Step: 1, Total: 11})
	if err := setupstate.EnsureDirs(dirs); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "ensuring sandbox local group", Step: 2, Total: 11})
	if err := ensureLocalGroup(GroupName); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "ensuring offline sandbox user", Step: 3, Total: 11})
	offlinePassword, err := ensureLocalUser(payload.OfflineUsername)
	if err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "ensuring online sandbox user", Step: 4, Total: 11})
	onlinePassword, err := ensureLocalUser(payload.OnlineUsername)
	if err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "updating sandbox group membership", Step: 5, Total: 11})
	if err := addUserToGroup(payload.OfflineUsername, GroupName); err != nil {
		return err
	}
	if err := addUserToGroup(payload.OnlineUsername, GroupName); err != nil {
		return err
	}
	if err := removeUserFromGroup(payload.OfflineUsername, "Administrators"); err != nil {
		return err
	}
	if err := removeUserFromGroup(payload.OnlineUsername, "Administrators"); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "hiding sandbox users from Windows sign-in", Step: 6, Total: 12})
	hideSandboxUsers(payload.OfflineUsername, payload.OnlineUsername)
	reportProgress(payload, progress, Progress{Phase: "state", Message: "protecting sandbox state directories", Step: 7, Total: 12})
	if err := protectStateDirectories(dirs, payload.OwnerUsername); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "acl", Message: "refreshing current workspace ACL policy", Step: 8, Total: 12})
	if err := ApplyMissingPolicyACLs(payload.Policy, payload.OfflineUsername, payload.OnlineUsername); err != nil {
		return err
	}
	if err := writeWorkspaceState(payload); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "firewall", Message: "refreshing Windows Firewall rules; this can take a while", Step: 9, Total: 12})
	if err := netpolicy.Refresh(netpolicy.Config{
		OfflineUsername: payload.OfflineUsername,
		OnlineUsername:  payload.OnlineUsername,
	}); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "secrets", Message: "writing sandbox credentials", Step: 10, Total: 12})
	if err := writeUsersFile(dirs.UsersPath, payload.OfflineUsername, offlinePassword, payload.OnlineUsername, onlinePassword); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "marker", Message: "writing setup marker", Step: 11, Total: 12})
	if err := setupstate.WriteMarker(dirs.MarkerPath, setupstate.Marker{
		Version:         payload.Version,
		RunnerHash:      payload.RunnerHash,
		PolicyHash:      payload.GlobalPolicyHash,
		OfflineUsername: payload.OfflineUsername,
		OnlineUsername:  payload.OnlineUsername,
		OwnerUsername:   payload.OwnerUsername,
	}); err != nil {
		return err
	}
	if err := setupstate.ClearError(dirs.ErrorPath); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "Windows sandbox setup is ready", Step: 12, Total: 12, Done: true})
	return nil
}

func executeReset(payload Payload, progress ProgressFunc) error {
	elevated, err := win32.IsElevated()
	if err != nil {
		return fmt.Errorf("windows setup reset: check elevation: %w", err)
	}
	if !elevated {
		return fmt.Errorf("windows setup reset: administrator elevation is required")
	}
	dirs := setupstate.NewDirs(payload.StateRoot)
	reportProgress(payload, progress, Progress{Phase: "reset", Message: "collecting sandbox state for cleanup", Step: 1, Total: 5})
	users := sandboxUsersForCleanup(payload, dirs)
	if record, err := setupstate.ReadWorkspace(dirs.WorkspacePath); err == nil {
		cleanupRecordedWorkspaceACLs(record, users)
	}
	reportProgress(payload, progress, Progress{Phase: "firewall", Message: "removing Windows sandbox firewall rules", Step: 2, Total: 5})
	if err := netpolicy.Clear(); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "removing sandbox local users and group", Step: 3, Total: 5})
	deleteSandboxUserListValues(users...)
	for _, username := range users {
		if err := deleteLocalUser(username); err != nil {
			return err
		}
	}
	if err := deleteLocalGroup(GroupName); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "state", Message: "removing sandbox state directories", Step: 4, Total: 5})
	for _, dir := range []string{dirs.Sandbox, dirs.Bin, dirs.Secrets} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove sandbox state directory %s: %w", dir, err)
		}
	}
	payload.ProgressPath = ""
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "Windows sandbox state reset complete", Step: 5, Total: 5, Done: true})
	return nil
}

func executeRuntimeRefresh(payload Payload, progress ProgressFunc) error {
	dirs := setupstate.NewDirs(payload.StateRoot)
	reportProgress(payload, progress, Progress{Phase: "refresh", Message: "validating existing sandbox setup", Step: 1, Total: 3})
	if err := validateGlobalSetup(payload, dirs); err != nil {
		return err
	}
	if err := setupstate.EnsureDirs(dirs); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "acl", Message: "refreshing request ACL policy", Step: 2, Total: 3})
	if err := ApplyMissingPolicyACLs(payload.Policy, payload.OfflineUsername, payload.OnlineUsername); err != nil {
		return err
	}
	if err := setupstate.ClearError(dirs.ErrorPath); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "request ACL policy is ready", Step: 3, Total: 3, Done: true})
	return nil
}

func executeWorkspaceOnly(payload Payload, progress ProgressFunc) error {
	dirs := setupstate.NewDirs(payload.StateRoot)
	reportProgress(payload, progress, Progress{Phase: "workspace", Message: "validating existing Windows sandbox setup", Step: 1, Total: 3})
	if err := validateGlobalSetup(payload, dirs); err != nil {
		return err
	}
	if err := setupstate.EnsureDirs(dirs); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "acl", Message: "refreshing current workspace ACL policy", Step: 2, Total: 3})
	if err := ApplyMissingPolicyACLs(payload.Policy, payload.OfflineUsername, payload.OnlineUsername); err != nil {
		return err
	}
	if err := writeWorkspaceState(payload); err != nil {
		return err
	}
	if err := setupstate.ClearError(dirs.ErrorPath); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "current workspace ACL policy is ready", Step: 3, Total: 3, Done: true})
	return nil
}

func validateGlobalSetup(payload Payload, dirs setupstate.Dirs) error {
	marker, err := setupstate.ReadMarker(dirs.MarkerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("windows setup: full setup is required before ACL refresh")
		}
		return fmt.Errorf("windows setup: read setup marker before ACL refresh: %w", err)
	}
	if marker.Version != payload.Version {
		return fmt.Errorf("windows setup: setup version changed; run full setup")
	}
	if _, err := os.Stat(dirs.UsersPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("windows setup: sandbox users file missing; run full setup")
		}
		return fmt.Errorf("windows setup: inspect sandbox users file: %w", err)
	}
	return nil
}

func reportProgress(payload Payload, progress ProgressFunc, update Progress) {
	update.Phase = strings.TrimSpace(update.Phase)
	update.Message = strings.TrimSpace(update.Message)
	if strings.TrimSpace(payload.ProgressPath) != "" {
		_ = setupstate.WriteProgress(payload.ProgressPath, setupstate.ProgressReport{
			Phase:   update.Phase,
			Message: update.Message,
			Step:    update.Step,
			Total:   update.Total,
			Done:    update.Done,
		})
	}
	if progress != nil {
		progress(update)
	}
}

func ensureLocalGroup(group string) error {
	if err := runNet("localgroup", group); err == nil {
		return nil
	}
	return runNet("localgroup", group, "/add")
}

func ensureLocalUser(username string) (string, error) {
	password, err := randomPassword()
	if err != nil {
		return "", err
	}
	if err := runNet("user", username); err == nil {
		return password, runNetWithInput("Y\r\n", "user", username, password, "/active:yes", "/expires:never", "/Y")
	}
	if err := runNetWithInput("Y\r\n", "user", username, password, "/add", "/active:yes", "/expires:never", "/passwordchg:no", "/Y"); err != nil {
		return "", err
	}
	return password, nil
}

func addUserToGroup(username string, group string) error {
	if userInGroup(username, group) {
		return nil
	}
	return runNet("localgroup", group, username, "/add")
}

func removeUserFromGroup(username string, group string) error {
	if !userInGroup(username, group) {
		return nil
	}
	return runNet("localgroup", group, username, "/delete")
}

func hideSandboxUsers(usernames ...string) {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon\SpecialAccounts\UserList`, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer key.Close()
	for _, username := range usernames {
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}
		_ = key.SetDWordValue(username, 0)
	}
}

func deleteSandboxUserListValues(usernames ...string) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon\SpecialAccounts\UserList`, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer key.Close()
	for _, username := range usernames {
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}
		_ = key.DeleteValue(username)
	}
}

func userInGroup(username string, group string) bool {
	cmd := exec.Command("net.exe", "localgroup", group)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	username = strings.TrimSpace(username)
	for _, line := range strings.Split(string(output), "\n") {
		member := strings.TrimSpace(strings.Trim(line, "\r"))
		if strings.EqualFold(member, username) {
			return true
		}
		if _, short, ok := strings.Cut(member, `\`); ok && strings.EqualFold(short, username) {
			return true
		}
	}
	return false
}

func localUserExists(username string) bool {
	username = strings.TrimSpace(username)
	if username == "" {
		return false
	}
	return runNet("user", username) == nil
}

func deleteLocalUser(username string) error {
	username = strings.TrimSpace(username)
	if username == "" || !localUserExists(username) {
		return nil
	}
	return runNet("user", username, "/delete")
}

func deleteLocalGroup(group string) error {
	group = strings.TrimSpace(group)
	if group == "" {
		return nil
	}
	if err := runNet("localgroup", group); err != nil {
		return nil
	}
	return runNet("localgroup", group, "/delete")
}

func protectStateDirectories(dirs setupstate.Dirs, ownerUser string) error {
	for _, dir := range []string{dirs.Sandbox, dirs.Bin, dirs.Secrets} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	ownerUser = strings.TrimSpace(ownerUser)
	grants := []acl.Entry{
		{Principal: "S-1-5-32-544", Rights: acl.FullControl, Mode: acl.Grant, Inherit: true},
		{Principal: "S-1-5-18", Rights: acl.FullControl, Mode: acl.Grant, Inherit: true},
	}
	if ownerUser != "" {
		grants = append([]acl.Entry{{Principal: ownerUser, Rights: acl.FullControl, Mode: acl.Grant, Inherit: true}}, grants...)
	}
	if err := acl.ReplaceFileDACL(dirs.Sandbox, true, grants...); err != nil {
		return err
	}
	binGrants := append([]acl.Entry{}, grants...)
	binGrants = append(binGrants, acl.Entry{Principal: GroupName, Rights: acl.ReadExecute, Mode: acl.Grant, Inherit: true})
	if err := acl.ReplaceFileDACL(dirs.Bin, true, binGrants...); err != nil {
		return err
	}
	if err := acl.ReplaceFileDACL(dirs.Secrets, true, grants...); err != nil {
		return err
	}
	return nil
}

func writeWorkspaceState(payload Payload) error {
	if payload.Kind == SetupKindRuntimeRefresh {
		return nil
	}
	dirs := setupstate.NewDirs(payload.StateRoot)
	path := strings.TrimSpace(payload.WorkspaceStatePath)
	if path == "" {
		path = dirs.WorkspacePath
	}
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return setupstate.WriteWorkspace(path, setupstate.WorkspaceRecord{
		Version:                 1,
		WorkspaceRoot:           pathutil.Normalize(payload.WorkspaceRoot),
		ReadRoots:               pathutil.Dedupe(payload.Policy.ReadRoots),
		WriteRoots:              pathutil.Dedupe(payload.Policy.WriteRoots),
		TraverseRoots:           policyTraverseRoots(payload.Policy),
		DenyReadPaths:           pathutil.Dedupe(payload.Policy.DenyReadPaths),
		DenyWritePaths:          pathutil.Dedupe(payload.Policy.DenyWritePaths),
		PolicyHash:              strings.TrimSpace(payload.WorkspacePolicyHash),
		CapabilitySIDs:          append([]string(nil), payload.Policy.CapabilitySIDs...),
		WriteRootCapabilitySIDs: cloneStringMap(payload.Policy.WriteRootCapabilitySIDs),
		OfflineUsername:         strings.TrimSpace(payload.OfflineUsername),
		OnlineUsername:          strings.TrimSpace(payload.OnlineUsername),
		OwnerUsername:           strings.TrimSpace(payload.OwnerUsername),
		SetupVersion:            payload.Version,
	})
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	return out
}

func sandboxUsersForCleanup(payload Payload, dirs setupstate.Dirs) []string {
	values := []string{
		payload.OfflineUsername,
		payload.OnlineUsername,
		OfflineUser,
		OnlineUser,
	}
	if marker, err := setupstate.ReadMarker(dirs.MarkerPath); err == nil {
		values = append(values, marker.OfflineUsername, marker.OnlineUsername)
	}
	if data, err := os.ReadFile(dirs.UsersPath); err == nil {
		var users UsersFile
		if json.Unmarshal(data, &users) == nil {
			values = append(values, users.Offline.Username, users.Online.Username)
		}
	}
	values = append(values, listCaelisSandboxUsers()...)
	return dedupeStrings(values...)
}

func listCaelisSandboxUsers() []string {
	cmd := exec.Command("net.exe", "user")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	var out []string
	for _, token := range strings.Fields(string(output)) {
		token = strings.TrimSpace(token)
		if strings.HasPrefix(token, "CaelisSbxOff") || strings.HasPrefix(token, "CaelisSbxOn") || strings.EqualFold(token, OfflineUser) || strings.EqualFold(token, OnlineUser) {
			out = append(out, token)
		}
	}
	return out
}

func cleanupRecordedWorkspaceACLs(record setupstate.WorkspaceRecord, users []string) {
	roots := append([]string{record.WorkspaceRoot}, record.ReadRoots...)
	roots = append(roots, record.WriteRoots...)
	roots = append(roots, record.TraverseRoots...)
	for _, root := range append(append([]string{}, record.ReadRoots...), record.WriteRoots...) {
		roots = append(roots, ancestorPaths(root)...)
	}
	roots = append(roots, record.DenyReadPaths...)
	roots = append(roots, record.DenyWritePaths...)
	targets := append([]string{GroupName, record.OfflineUsername, record.OnlineUsername}, users...)
	targets = append(targets, record.CapabilitySIDs...)
	for _, sid := range record.WriteRootCapabilitySIDs {
		targets = append(targets, sid)
	}
	targets = dedupeStrings(targets...)
	for _, root := range pathutil.Dedupe(roots) {
		if !pathExists(root) {
			continue
		}
		for _, target := range targets {
			removeACLPrincipal(root, target)
		}
	}
}

func removeACLPrincipal(path string, principal string) {
	principal = icaclsPrincipal(principal)
	if strings.TrimSpace(path) == "" || principal == "" {
		return
	}
	runICACLSRemove(path, "/remove:g", principal)
	runICACLSRemove(path, "/remove:d", principal)
}

func runICACLSRemove(path string, mode string, principal string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "icacls.exe", path, mode, principal).Run()
}

func icaclsPrincipal(principal string) string {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToUpper(principal), "S-1-") {
		return "*" + principal
	}
	return principal
}

func dedupeStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

type RequiredACE struct {
	Path      string
	Principal string
	Rights    string
	Mode      string
}

type ACLCheckResult struct {
	Path          string
	Current       bool
	Missing       []RequiredACE
	NeedsWriteDAC bool
	Reason        string
}

func CheckPolicyACLs(policy winpolicy.Policy, users ...string) ([]ACLCheckResult, error) {
	var out []ACLCheckResult
	for _, target := range requiredPolicyACLTargets(policy, users...) {
		missing, err := acl.MissingFileDACLEntries(target.Path, target.Entries...)
		result := ACLCheckResult{Path: target.Path}
		if err != nil {
			result.Current = false
			result.NeedsWriteDAC = true
			result.Reason = err.Error()
			out = append(out, result)
			continue
		}
		for _, entry := range missing {
			result.Missing = append(result.Missing, RequiredACE{
				Path:      target.Path,
				Principal: entry.Principal,
				Rights:    string(entry.Rights),
				Mode:      string(entry.Mode),
			})
		}
		result.Current = len(result.Missing) == 0
		if !result.Current {
			result.NeedsWriteDAC = true
			result.Reason = fmt.Sprintf("%d ACL entries missing", len(result.Missing))
		}
		out = append(out, result)
	}
	return out, nil
}

func ApplyMissingPolicyACLs(policy winpolicy.Policy, users ...string) error {
	principals := appendPrincipal(append([]string{GroupName}, users...)...)
	capabilities := appendPrincipal(policy.CapabilitySIDs...)
	writeRootKeys := map[string]struct{}{}
	for _, root := range policy.WriteRoots {
		key := pathutil.Key(root)
		if key != "" {
			writeRootKeys[key] = struct{}{}
		}
	}
	materializeDenyWriteKeys := map[string]struct{}{}
	for _, root := range policy.MaterializeDenyWritePaths {
		key := pathutil.Key(root)
		if key != "" {
			materializeDenyWriteKeys[key] = struct{}{}
		}
	}
	for _, root := range policy.ReadRoots {
		if isDefaultReadRoot(root) {
			continue
		}
		if _, writable := writeRootKeys[pathutil.Key(root)]; writable {
			continue
		}
		targets := append([]string{}, principals...)
		targets = append(targets, capabilities...)
		if err := grantAncestorTraverse(root, targets); err != nil {
			return err
		}
		if err := grantPath(root, targets, "RX"); err != nil {
			return err
		}
	}
	for _, root := range policy.WriteRoots {
		sid := writeRootCapabilitySID(policy, root)
		targets := append([]string{}, principals...)
		if sid != "" {
			targets = append(targets, sid)
		}
		if err := grantAncestorTraverse(root, targets); err != nil {
			return err
		}
		if err := grantPath(root, targets, "M"); err != nil {
			return err
		}
	}
	for _, root := range policy.DenyWritePaths {
		targets := append([]string{}, principals...)
		for _, sid := range policy.CapabilitySIDs {
			if strings.TrimSpace(sid) != "" {
				targets = append(targets, sid)
			}
		}
		if _, shouldMaterialize := materializeDenyWriteKeys[pathutil.Key(root)]; shouldMaterialize {
			if err := os.MkdirAll(root, 0o700); err != nil {
				return fmt.Errorf("materialize deny-write path %s: %w", root, err)
			}
		}
		if err := denyPath(root, targets, "W"); err != nil {
			return err
		}
	}
	for _, root := range policy.DenyReadPaths {
		if err := denyPath(root, principals, "RX"); err != nil {
			return err
		}
	}
	return nil
}

func requiredPolicyACLTargets(policy winpolicy.Policy, users ...string) []policyACLTarget {
	principals := appendPrincipal(append([]string{GroupName}, users...)...)
	capabilities := appendPrincipal(policy.CapabilitySIDs...)
	writeRootKeys := map[string]struct{}{}
	for _, root := range policy.WriteRoots {
		key := pathutil.Key(root)
		if key != "" {
			writeRootKeys[key] = struct{}{}
		}
	}
	var out []policyACLTarget
	for _, root := range policy.ReadRoots {
		if isDefaultReadRoot(root) {
			continue
		}
		if _, writable := writeRootKeys[pathutil.Key(root)]; writable {
			continue
		}
		if !pathExists(root) {
			continue
		}
		targets := append([]string{}, principals...)
		targets = append(targets, capabilities...)
		out = appendAncestorACLTargets(out, root, targets)
		out = append(out, policyACLTarget{Path: root, Entries: aclEntries(targets, "RX", acl.Grant)})
	}
	for _, root := range policy.WriteRoots {
		if !pathExists(root) {
			continue
		}
		sid := writeRootCapabilitySID(policy, root)
		targets := append([]string{}, principals...)
		if sid != "" {
			targets = append(targets, sid)
		}
		out = appendAncestorACLTargets(out, root, targets)
		out = append(out, policyACLTarget{Path: root, Entries: aclEntries(targets, "M", acl.Grant)})
	}
	for _, root := range policy.DenyWritePaths {
		if !pathExists(root) {
			continue
		}
		targets := append([]string{}, principals...)
		for _, sid := range policy.CapabilitySIDs {
			if strings.TrimSpace(sid) != "" {
				targets = append(targets, sid)
			}
		}
		out = append(out, policyACLTarget{Path: root, Entries: aclEntries(targets, "W", acl.Deny)})
	}
	for _, root := range policy.DenyReadPaths {
		if !pathExists(root) {
			continue
		}
		out = append(out, policyACLTarget{Path: root, Entries: aclEntries(principals, "RX", acl.Deny)})
	}
	return out
}

type policyACLTarget struct {
	Path    string
	Entries []acl.Entry
}

func policyTraverseRoots(policy winpolicy.Policy) []string {
	var roots []string
	for _, root := range policy.ReadRoots {
		if isDefaultReadRoot(root) {
			continue
		}
		roots = append(roots, ancestorPaths(root)...)
	}
	for _, root := range policy.WriteRoots {
		roots = append(roots, ancestorPaths(root)...)
	}
	return pathutil.Dedupe(roots)
}

func appendAncestorACLTargets(out []policyACLTarget, root string, principals []string) []policyACLTarget {
	for _, ancestor := range ancestorPaths(root) {
		if !pathExists(ancestor) {
			continue
		}
		out = append(out, policyACLTarget{Path: ancestor, Entries: aclEntries(principals, "X", acl.Grant, false)})
	}
	return out
}

func aclEntries(principals []string, rights string, mode acl.Mode, inheritOverride ...bool) []acl.Entry {
	inherit := true
	if len(inheritOverride) > 0 {
		inherit = inheritOverride[0]
	}
	entries := make([]acl.Entry, 0, len(principals))
	for _, principal := range principals {
		principal = strings.TrimSpace(principal)
		if principal == "" {
			continue
		}
		entries = append(entries, acl.Entry{
			Principal: principal,
			Rights:    aclRights(rights),
			Mode:      mode,
			Inherit:   inherit,
		})
	}
	return entries
}

func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func appendPrincipal(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func writeRootCapabilitySID(policy winpolicy.Policy, root string) string {
	if len(policy.WriteRootCapabilitySIDs) == 0 {
		return ""
	}
	if sid := strings.TrimSpace(policy.WriteRootCapabilitySIDs[pathutil.Normalize(root)]); sid != "" {
		return sid
	}
	rootKey := pathutil.Key(root)
	for candidate, sid := range policy.WriteRootCapabilitySIDs {
		if pathutil.Key(candidate) == rootKey {
			return strings.TrimSpace(sid)
		}
	}
	return ""
}

func isDefaultReadRoot(path string) bool {
	normalized := strings.TrimRight(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(path), "/", `\`)), `\`)
	switch normalized {
	case `c:\windows`, `c:\program files`, `c:\program files (x86)`, `c:\programdata`:
		return true
	default:
		return false
	}
}

func grantAncestorTraverse(path string, principals []string) error {
	for _, ancestor := range ancestorPaths(path) {
		if err := grantPathWithInherit(ancestor, principals, "X", false); err != nil {
			return err
		}
	}
	return nil
}

func ancestorPaths(path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	parent := filepath.Dir(clean)
	if parent == "" || strings.EqualFold(parent, clean) {
		return nil
	}
	var ancestors []string
	for {
		if parent == "" {
			break
		}
		if isVolumeRoot(parent) {
			break
		}
		ancestors = append(ancestors, parent)
		next := filepath.Dir(parent)
		if next == "" || strings.EqualFold(next, parent) {
			break
		}
		parent = next
	}
	return pathutil.Dedupe(ancestors)
}

func isVolumeRoot(path string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	volume := filepath.VolumeName(path)
	if volume == "" {
		return path == string(filepath.Separator)
	}
	rest := strings.Trim(path[len(volume):], `\/`)
	return rest == ""
}

func grantPath(path string, principals []string, rights string) error {
	return grantPathWithInherit(path, principals, rights, true)
}

func grantPathWithInherit(path string, principals []string, rights string, inherit bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	entries := make([]acl.Entry, 0, len(principals))
	for _, principal := range principals {
		principal = strings.TrimSpace(principal)
		if principal == "" {
			continue
		}
		entries = append(entries, acl.Entry{
			Principal: principal,
			Rights:    aclRights(rights),
			Mode:      acl.Grant,
			Inherit:   inherit,
		})
	}
	return acl.ModifyFileDACL(path, entries...)
}

func denyPath(path string, principals []string, rights string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	entries := make([]acl.Entry, 0, len(principals))
	for _, principal := range principals {
		principal = strings.TrimSpace(principal)
		if principal == "" {
			continue
		}
		entries = append(entries, acl.Entry{
			Principal: principal,
			Rights:    aclRights(rights),
			Mode:      acl.Deny,
			Inherit:   true,
		})
	}
	return acl.ModifyFileDACL(path, entries...)
}

func aclRights(rights string) acl.Rights {
	switch strings.ToUpper(strings.TrimSpace(rights)) {
	case "M":
		return acl.Modify
	case "W":
		return acl.Write
	case "X":
		return acl.Traverse
	case "RX":
		fallthrough
	default:
		return acl.ReadExecute
	}
}

func writeUsersFile(path string, offlineUser string, offlinePassword string, onlineUser string, onlinePassword string) error {
	offlineProtected, err := win32.ProtectMachineString(offlinePassword, "caelis sandbox offline password")
	if err != nil {
		return err
	}
	onlineProtected, err := win32.ProtectMachineString(onlinePassword, "caelis sandbox online password")
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(UsersFile{
		Offline: UserSecret{Username: offlineUser, PasswordProtected: offlineProtected},
		Online:  UserSecret{Username: onlineUser, PasswordProtected: onlineProtected},
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func randomPassword() (string, error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "Cae!1" + hex.EncodeToString(buf[:]), nil
}

func runNet(args ...string) error {
	return runCommand("net.exe", args...)
}

func runNetWithInput(input string, args ...string) error {
	return runCommandWithInput("net.exe", input, args...)
}

func runCommand(name string, args ...string) error {
	return runCommandWithInput(name, "", args...)
}

func runCommandWithInput(name string, input string, args ...string) error {
	cmd := exec.Command(name, args...)
	if strings.TrimSpace(input) != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
