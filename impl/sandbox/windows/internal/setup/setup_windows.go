//go:build windows

package setup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/acl"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/netpolicy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/pathutil"
	winpolicy "github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/policy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/runnertrace"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setupstate"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/winexec"
	"golang.org/x/sys/windows/registry"
)

func Execute(payload Payload) error {
	return ExecuteWithProgress(payload, nil)
}

const setupMaintenanceLockTimeout = 30 * time.Second

func ExecuteWithProgress(payload Payload, progress ProgressFunc) error {
	payload = payload.Normalize()
	if strings.TrimSpace(payload.StateRoot) == "" {
		return fmt.Errorf("windows setup: state root is required")
	}
	if payload.Version != PayloadVersion {
		return fmt.Errorf("windows setup: unsupported payload version %d", payload.Version)
	}
	if !payload.ExpiresAt.IsZero() && time.Now().After(payload.ExpiresAt) {
		return fmt.Errorf("windows setup: operation %s expired before execution", strings.TrimSpace(payload.OperationID))
	}
	return win32.WithNamedMutex(context.Background(), setupMaintenanceMutexName(payload.StateRoot), setupMaintenanceLockTimeout, func() error {
		if !payload.ExpiresAt.IsZero() && time.Now().After(payload.ExpiresAt) {
			return fmt.Errorf("windows setup: operation %s expired before execution", strings.TrimSpace(payload.OperationID))
		}
		return executeWithProgressLocked(payload, progress)
	})
}

func executeWithProgressLocked(payload Payload, progress ProgressFunc) error {
	switch payload.Kind {
	case SetupKindReset:
		err := executeReset(payload, progress)
		if err != nil {
			writeResetError(payload, err)
		}
		return err
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

	const totalSteps = 12
	dirs := setupstate.NewDirs(payload.StateRoot)
	reportProgress(payload, progress, Progress{Phase: "state", Message: "preparing sandbox state directories", Step: 1, Total: totalSteps})
	if err := setupstate.EnsureDirs(dirs); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "ensuring sandbox local group", Step: 2, Total: totalSteps})
	if err := ensureLocalGroup(GroupName); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "ensuring offline sandbox user", Step: 3, Total: totalSteps})
	offlinePassword, err := ensureLocalUser(payload.OfflineUsername)
	if err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "updating sandbox group membership", Step: 4, Total: totalSteps})
	if err := addUserToGroup(payload.OfflineUsername, GroupName); err != nil {
		return err
	}
	if err := removeUserFromGroup(payload.OfflineUsername, "Administrators"); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "hiding sandbox users from Windows sign-in", Step: 5, Total: totalSteps})
	hideSandboxUsers(payload.OfflineUsername)
	reportProgress(payload, progress, Progress{Phase: "state", Message: "protecting sandbox state directories", Step: 6, Total: totalSteps})
	if err := protectStateDirectories(dirs, payload.OwnerUsername); err != nil {
		return err
	}
	if policyHasACLTargets(payload.GlobalPolicy) {
		reportProgress(payload, progress, Progress{Phase: "acl", Message: "refreshing global sandbox ACL policy", Step: 7, Total: totalSteps})
		if err := ApplyMissingPolicyACLsWithOptions(payload.GlobalPolicy, ApplyPolicyACLOptions{
			StateRoot:                         payload.StateRoot,
			Users:                             []string{payload.OfflineUsername},
			CleanupLegacyAncestorCapabilities: true,
		}); err != nil {
			return err
		}
	}
	reportProgress(payload, progress, Progress{Phase: "acl", Message: "refreshing current workspace ACL policy", Step: 8, Total: totalSteps})
	if err := ApplyMissingPolicyACLsWithOptions(payload.Policy, ApplyPolicyACLOptions{
		StateRoot:                         payload.StateRoot,
		Users:                             []string{payload.OfflineUsername},
		CleanupLegacyAncestorCapabilities: true,
	}); err != nil {
		return err
	}
	if err := prepareRunnerEnvironmentDirs(dirs, []string{payload.OfflineUsername}, runnerEnvironmentCapabilitySIDs(payload.Policy)); err != nil {
		return err
	}
	if err := writeWorkspaceState(payload); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "firewall", Message: "refreshing Windows Firewall rules", Step: 9, Total: totalSteps})
	if err := netpolicy.RefreshWithOptions(context.Background(), netpolicy.Config{
		OfflineUsername: payload.OfflineUsername,
	}, netpolicy.ClearOptions{
		Debugf: func(format string, args ...any) {
			reportDebugProgress(payload, progress, fmt.Sprintf(format, args...))
		},
	}); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "secrets", Message: "writing sandbox credentials", Step: 10, Total: totalSteps})
	if err := writeUsersFile(dirs.UsersPath, payload.OfflineUsername, offlinePassword); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "marker", Message: "writing setup marker", Step: 11, Total: totalSteps})
	if err := setupstate.WriteMarker(dirs.MarkerPath, setupstate.Marker{
		Version:         payload.Version,
		RunnerHash:      payload.RunnerHash,
		PolicyHash:      payload.GlobalPolicyHash,
		OfflineUsername: payload.OfflineUsername,
		OwnerUsername:   payload.OwnerUsername,
	}); err != nil {
		return err
	}
	if err := setupstate.ClearError(dirs.ErrorPath); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "Windows sandbox setup is ready", Step: 12, Total: totalSteps, Done: true})
	return nil
}

func setupMaintenanceMutexName(stateRoot string) string {
	normalized := strings.ToLower(filepath.Clean(strings.TrimSpace(stateRoot)))
	hash, err := setupstate.HashJSON(normalized)
	if err != nil || len(hash) < 16 {
		return `Local\CaelisSandboxSetup`
	}
	return `Local\CaelisSandboxSetup-` + hash[:16]
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
	reportProgress(payload, progress, Progress{Phase: "reset", Message: "collecting sandbox state for cleanup", Step: 1, Total: 6})
	plan := resetCleanupPlanFromState(payload, dirs)
	reportDebugProgress(payload, progress, fmt.Sprintf("cleanup plan users=%d acl_roots=%d acl_principals=%d state_root=%s", len(plan.Users), len(plan.ACLRoots), len(plan.ACLPrincipals), payload.StateRoot))
	reportProgress(payload, progress, Progress{Phase: "acl", Message: "removing recorded sandbox ACL grants", Step: 2, Total: 6})
	cleanupResetPlanACLsWithProgress(payload, progress, plan)
	reportProgress(payload, progress, Progress{Phase: "firewall", Message: "removing Windows sandbox firewall rules", Step: 3, Total: 6})
	if err := netpolicy.ClearContextWithOptions(context.Background(), netpolicy.ClearOptions{
		Debugf: func(format string, args ...any) {
			reportDebugProgress(payload, progress, fmt.Sprintf(format, args...))
		},
	}); err != nil {
		reportProgress(payload, progress, Progress{
			Phase:   "firewall",
			Message: "Windows sandbox firewall cleanup failed; continuing reset: " + err.Error(),
			Step:    3,
			Total:   6,
		})
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "removing sandbox local users and group", Step: 4, Total: 6})
	reportDebugProgress(payload, progress, fmt.Sprintf("removing sandbox account artifacts users=%d group=%s", len(plan.Users), plan.GroupName))
	deleteSandboxUserListValues(plan.Users...)
	for _, username := range plan.Users {
		if err := deleteLocalUser(username); err != nil {
			return err
		}
		cleanupSandboxUserProfiles(username, func(message string) {
			reportDebugProgress(payload, progress, message)
		})
	}
	if err := deleteLocalGroup(plan.GroupName); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "state", Message: "removing sandbox state directories", Step: 5, Total: 6})
	for _, dir := range plan.StateDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		reportDebugProgress(payload, progress, "removing sandbox state directory "+dir)
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove sandbox state directory %s: %w", dir, err)
		}
	}
	_ = setupstate.ClearError(dirs.ResetErrorPath)
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "Windows sandbox state reset complete", Step: 6, Total: 6, Done: true})
	return nil
}

type resetCleanupPlan struct {
	Version       int               `json:"version"`
	OperationID   string            `json:"operation_id,omitempty"`
	StateRoot     string            `json:"state_root,omitempty"`
	GroupName     string            `json:"group_name,omitempty"`
	Users         []string          `json:"users,omitempty"`
	ACLRoots      []string          `json:"acl_roots,omitempty"`
	ACLPrincipals []string          `json:"acl_principals,omitempty"`
	StateDirs     []string          `json:"state_dirs,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

func resetCleanupPlanFromState(payload Payload, dirs setupstate.Dirs) resetCleanupPlan {
	plan := resetCleanupPlan{
		Version:     1,
		OperationID: strings.TrimSpace(payload.OperationID),
		StateRoot:   strings.TrimSpace(payload.StateRoot),
		GroupName:   GroupName,
	}
	plan.Users = append(plan.Users, sandboxUsersForCleanup(payload, dirs)...)
	plan.StateDirs = append(plan.StateDirs, dirs.Sandbox, dirs.Bin, dirs.Secrets)
	if record, err := setupstate.ReadWorkspace(dirs.WorkspacePath); err == nil {
		plan.ACLRoots = append(plan.ACLRoots, recordedWorkspaceACLCleanupRoots(record)...)
		targets := append([]string{GroupName, record.OfflineUsername, record.OnlineUsername}, plan.Users...)
		targets = append(targets, record.CapabilitySIDs...)
		for _, sid := range record.WriteRootCapabilitySIDs {
			targets = append(targets, sid)
		}
		plan.ACLPrincipals = append(plan.ACLPrincipals, targets...)
	}
	if capStore, err := readCapabilityStore(dirs.CapPath); err == nil {
		capRoots, capSIDs := capabilityStoreRootsAndSIDs(capStore)
		plan.ACLRoots = append(plan.ACLRoots, capRoots...)
		for _, root := range capRoots {
			plan.ACLRoots = append(plan.ACLRoots, ancestorPaths(root)...)
		}
		plan.ACLPrincipals = append(plan.ACLPrincipals, capSIDs...)
	}
	plan.Users = dedupeStrings(plan.Users...)
	plan.ACLRoots = pathutil.Dedupe(plan.ACLRoots)
	plan.ACLPrincipals = dedupeStrings(plan.ACLPrincipals...)
	plan.StateDirs = pathutil.Dedupe(plan.StateDirs)
	return plan
}

func cleanupResetPlanACLs(plan resetCleanupPlan) {
	cleanupResetPlanACLsWithProgress(Payload{}, nil, plan)
}

func cleanupResetPlanACLsWithProgress(payload Payload, progress ProgressFunc, plan resetCleanupPlan) {
	roots := pathutil.Dedupe(plan.ACLRoots)
	principals := dedupeStrings(plan.ACLPrincipals...)
	runnertrace.Printf("windows-setup", "cleanup_reset_plan_acls roots=%d targets=%d", len(roots), len(principals))
	if len(roots) == 0 || len(principals) == 0 {
		return
	}
	existing := make([]string, 0, len(roots))
	for _, root := range roots {
		if pathExists(root) {
			existing = append(existing, root)
		}
	}
	if len(existing) == 0 {
		return
	}
	type task struct {
		index int
		root  string
	}
	workers := 4
	if len(existing) < workers {
		workers = len(existing)
	}
	tasks := make(chan task)
	var progressMu sync.Mutex
	report := func(index int, root string) {
		progressMu.Lock()
		defer progressMu.Unlock()
		reportProgress(payload, progress, Progress{
			Phase:   "acl",
			Message: fmt.Sprintf("removing recorded sandbox ACL grants (%d/%d): %s", index, len(existing), root),
			Step:    2,
			Total:   6,
		})
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range tasks {
				report(task.index, task.root)
				removeACLPrincipals(task.root, principals)
			}
		}()
	}
	for i, root := range existing {
		tasks <- task{index: i + 1, root: root}
	}
	close(tasks)
	wg.Wait()
}

func writeResetError(payload Payload, err error) {
	if err == nil {
		return
	}
	dirs := setupstate.NewDirs(payload.StateRoot)
	_ = setupstate.WriteError(dirs.ResetErrorPath, setupstate.ErrorReport{
		Phase:   "reset",
		Code:    "reset_failed",
		Message: err.Error(),
	})
}

func executeRuntimeRefresh(payload Payload, progress ProgressFunc) error {
	done := runnertrace.Span("windows-setup", "runtime_refresh")
	defer done()
	dirs := setupstate.NewDirs(payload.StateRoot)
	reportProgress(payload, progress, Progress{Phase: "refresh", Message: "validating existing sandbox setup", Step: 1, Total: 3})
	validateDone := runnertrace.Span("windows-setup", "runtime_refresh.validate_global_setup")
	if err := validateGlobalSetup(payload, dirs); err != nil {
		validateDone()
		return err
	}
	validateDone()
	dirsDone := runnertrace.Span("windows-setup", "runtime_refresh.ensure_dirs")
	if err := setupstate.EnsureDirs(dirs); err != nil {
		dirsDone()
		return err
	}
	dirsDone()
	reportProgress(payload, progress, Progress{Phase: "acl", Message: "refreshing request ACL policy", Step: 2, Total: 3})
	if err := ApplyRuntimePolicyACLsWithOptions(payload.Policy, ApplyPolicyACLOptions{
		StateRoot: payload.StateRoot,
		Users:     []string{payload.OfflineUsername},
	}); err != nil {
		return err
	}
	if err := prepareRunnerEnvironmentDirs(dirs, []string{payload.OfflineUsername}, runnerEnvironmentCapabilitySIDs(payload.Policy)); err != nil {
		return err
	}
	clearDone := runnertrace.Span("windows-setup", "runtime_refresh.clear_error")
	if err := setupstate.ClearError(dirs.ErrorPath); err != nil {
		clearDone()
		return err
	}
	clearDone()
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
	if err := ApplyMissingPolicyACLsWithOptions(payload.Policy, ApplyPolicyACLOptions{
		StateRoot:                         payload.StateRoot,
		Users:                             []string{payload.OfflineUsername},
		CleanupLegacyAncestorCapabilities: true,
	}); err != nil {
		return err
	}
	if err := prepareRunnerEnvironmentDirs(dirs, []string{payload.OfflineUsername}, runnerEnvironmentCapabilitySIDs(payload.Policy)); err != nil {
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
			Debug:   update.Debug,
		})
	}
	if progress != nil {
		progress(update)
	}
}

func reportDebugProgress(payload Payload, progress ProgressFunc, message string) {
	if !payload.Debug {
		return
	}
	reportProgress(payload, progress, Progress{
		Phase:   "debug",
		Message: strings.TrimSpace(message),
		Debug:   true,
	})
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
	inGroup, err := userInGroup(username, group)
	if err != nil {
		return err
	}
	if inGroup {
		return nil
	}
	return runNet("localgroup", group, username, "/add")
}

func removeUserFromGroup(username string, group string) error {
	inGroup, err := userInGroup(username, group)
	if err != nil {
		return err
	}
	if !inGroup {
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

func userInGroup(username string, group string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := winexec.Run(ctx, "net.exe", []string{"localgroup", group}, winexec.Options{
		Timeout:        5 * time.Second,
		TraceComponent: "windows-setup",
		TraceName:      "setup_command",
	})
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false, fmt.Errorf("net.exe localgroup %s: %w", group, err)
	}
	if err != nil {
		return false, nil
	}
	username = strings.TrimSpace(username)
	for _, line := range strings.Split(string(result.CombinedOutput()), "\n") {
		member := strings.TrimSpace(strings.Trim(line, "\r"))
		if strings.EqualFold(member, username) {
			return true, nil
		}
		if _, short, ok := strings.Cut(member, `\`); ok && strings.EqualFold(short, username) {
			return true, nil
		}
	}
	return false, nil
}

func localUserExists(username string) bool {
	exists, _ := localUserExistsWithError(username)
	return exists
}

func localUserExistsWithError(username string) (bool, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return false, nil
	}
	err := runNet("user", username)
	if err == nil {
		return true, nil
	}
	if isCommandContextError(err) {
		return false, err
	}
	return false, nil
}

func deleteLocalUser(username string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil
	}
	exists, err := localUserExistsWithError(username)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return runNet("user", username, "/delete")
}

func cleanupSandboxUserProfiles(username string, debugf func(string)) {
	username = strings.TrimSpace(username)
	if !isSandboxUsername(username) {
		return
	}
	for _, dir := range sandboxUserProfileDirs(username) {
		if debugf != nil {
			debugf("removing sandbox user profile " + dir)
		}
		if err := os.RemoveAll(dir); err != nil && debugf != nil {
			debugf(fmt.Sprintf("sandbox user profile cleanup failed for %s: %v", dir, err))
		}
	}
}

func sandboxUserProfileDirs(username string) []string {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil
	}
	systemDrive := strings.TrimRight(strings.TrimSpace(os.Getenv("SystemDrive")), `\/`)
	if systemDrive == "" {
		systemDrive = `C:`
	}
	usersRoot := filepath.Join(systemDrive+`\`, "Users")
	matches, err := filepath.Glob(filepath.Join(usersRoot, username+"*"))
	if err != nil {
		return nil
	}
	var out []string
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil || !info.IsDir() {
			continue
		}
		name := filepath.Base(match)
		if strings.EqualFold(name, username) || strings.HasPrefix(strings.ToLower(name), strings.ToLower(username)+".") {
			out = append(out, match)
		}
	}
	return out
}

func isSandboxUsername(username string) bool {
	username = strings.TrimSpace(username)
	return strings.HasPrefix(username, "CaelisSbxOff") ||
		strings.HasPrefix(username, "CaelisSbxOn") ||
		strings.EqualFold(username, OfflineUser) ||
		strings.EqualFold(username, OnlineUser)
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

func prepareRunnerEnvironmentDirs(dirs setupstate.Dirs, users []string, capabilitySIDs []string) error {
	done := runnertrace.Span("windows-setup", "runtime_refresh.prepare_runner_environment_dirs")
	defer done()
	users = appendPrincipal(users...)
	capabilitySIDs = appendPrincipal(capabilitySIDs...)
	runnertrace.Printf("windows-setup", "runtime_refresh.prepare_runner_environment_dirs users=%d capabilities=%d", len(users), len(capabilitySIDs))
	if strings.TrimSpace(dirs.Sandbox) == "" || len(users) == 0 {
		return nil
	}
	if err := os.MkdirAll(dirs.Sandbox, 0o700); err != nil {
		return err
	}
	traversePrincipals, err := sandboxAncestorPrincipalSIDs(users...)
	if err != nil {
		return err
	}
	traversePrincipals = append(traversePrincipals, capabilitySIDs...)
	if err := grantPathWithInherit(dirs.Sandbox, traversePrincipals, "X", false); err != nil {
		return fmt.Errorf("prepare runner sandbox state root: %w", err)
	}

	homeRoot := filepath.Join(dirs.Sandbox, "runner-home")
	tmpRoot := filepath.Join(dirs.Sandbox, "runner-tmp")
	for _, root := range []string{homeRoot, tmpRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return err
		}
		if err := grantPathWithInherit(root, traversePrincipals, "X", false); err != nil {
			return fmt.Errorf("prepare runner env root %s: %w", root, err)
		}
	}

	for _, username := range users {
		name := sandboxEnvName(username)
		home := filepath.Join(homeRoot, name)
		tmp := filepath.Join(tmpRoot, name)
		localAppData := filepath.Join(home, "AppData", "Local")
		roamingAppData := filepath.Join(home, "AppData", "Roaming")
		envDirs := []string{home, tmp, localAppData, roamingAppData}
		for _, dir := range envDirs {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
		}
		modifyPrincipals, err := sandboxPrincipalSIDs(username)
		if err != nil {
			return err
		}
		modifyPrincipals = append(modifyPrincipals, capabilitySIDs...)
		for _, dir := range envDirs {
			if err := grantPathWithInherit(dir, modifyPrincipals, "F", true); err != nil {
				return fmt.Errorf("prepare runner env dir %s: %w", dir, err)
			}
		}
	}
	return nil
}

func sandboxEnvName(username string) string {
	name := strings.TrimSpace(username)
	if name == "" {
		return "current"
	}
	return strings.NewReplacer(`\`, "_", `/`, "_", ":", "_").Replace(name)
}

func runnerEnvironmentCapabilitySIDs(policy winpolicy.Policy) []string {
	values := append([]string{}, policy.CapabilitySIDs...)
	for _, sid := range policy.WriteRootCapabilitySIDs {
		values = append(values, sid)
	}
	return dedupeStrings(values...)
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
		ReadRoots:               cleanupRecordReadRoots(payload.Policy.ReadRoots),
		WriteRoots:              pathutil.Dedupe(payload.Policy.WriteRoots),
		TraverseRoots:           policyTraverseRoots(payload.Policy),
		DenyReadPaths:           pathutil.Dedupe(payload.Policy.DenyReadPaths),
		DenyWritePaths:          pathutil.Dedupe(payload.Policy.DenyWritePaths),
		PolicyHash:              strings.TrimSpace(payload.WorkspacePolicyHash),
		CapabilitySIDs:          append([]string(nil), payload.Policy.CapabilitySIDs...),
		WriteRootCapabilitySIDs: cloneStringMap(payload.Policy.WriteRootCapabilitySIDs),
		OfflineUsername:         strings.TrimSpace(payload.OfflineUsername),
		OwnerUsername:           strings.TrimSpace(payload.OwnerUsername),
		SetupVersion:            payload.Version,
	})
}

func cleanupRecordReadRoots(readRoots []string) []string {
	var out []string
	for _, root := range readRoots {
		root = pathutil.Normalize(root)
		if root == "" || isDefaultReadRoot(root) {
			continue
		}
		out = append(out, root)
	}
	return pathutil.Dedupe(out)
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
		legacyOnlineUsername(payload),
		OfflineUser,
		OnlineUser,
	}
	if marker, err := setupstate.ReadMarker(dirs.MarkerPath); err == nil {
		values = append(values, marker.OfflineUsername, marker.OnlineUsername)
	}
	if data, err := os.ReadFile(dirs.UsersPath); err == nil {
		var users UsersFile
		if json.Unmarshal(data, &users) == nil {
			values = append(values, users.Offline.Username)
			if users.Online != nil {
				values = append(values, users.Online.Username)
			}
		}
	}
	values = append(values, listCaelisSandboxUsers()...)
	return dedupeStrings(values...)
}

func listCaelisSandboxUsers() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := winexec.Run(ctx, "net.exe", []string{"user"}, winexec.Options{
		Timeout:        3 * time.Second,
		TraceComponent: "windows-setup",
		TraceName:      "setup_command",
	})
	if err != nil {
		return nil
	}
	var out []string
	for _, token := range strings.Fields(string(result.CombinedOutput())) {
		token = strings.TrimSpace(token)
		if strings.HasPrefix(token, "CaelisSbxOff") || strings.HasPrefix(token, "CaelisSbxOn") || strings.EqualFold(token, OfflineUser) || strings.EqualFold(token, OnlineUser) {
			out = append(out, token)
		}
	}
	return out
}

func cleanupRecordedWorkspaceACLs(record setupstate.WorkspaceRecord, users []string) {
	roots := recordedWorkspaceACLCleanupRoots(record)
	targets := append([]string{GroupName, record.OfflineUsername, record.OnlineUsername}, users...)
	targets = append(targets, record.CapabilitySIDs...)
	for _, sid := range record.WriteRootCapabilitySIDs {
		targets = append(targets, sid)
	}
	targets = dedupeStrings(targets...)
	runnertrace.Printf("windows-setup", "cleanup_recorded_workspace_acls roots=%d targets=%d", len(roots), len(targets))
	for _, root := range pathutil.Dedupe(roots) {
		if !pathExists(root) {
			continue
		}
		removeACLPrincipals(root, targets)
	}
}

func recordedWorkspaceACLCleanupRoots(record setupstate.WorkspaceRecord) []string {
	readRoots := cleanupRecordReadRoots(record.ReadRoots)
	roots := append([]string{record.WorkspaceRoot}, readRoots...)
	roots = append(roots, record.WriteRoots...)
	roots = append(roots, record.TraverseRoots...)
	for _, root := range append(append([]string{}, readRoots...), record.WriteRoots...) {
		roots = append(roots, ancestorPaths(root)...)
	}
	roots = append(roots, record.DenyReadPaths...)
	roots = append(roots, record.DenyWritePaths...)
	return pathutil.Dedupe(roots)
}

func cleanupCapabilityStoreACLs(store capabilityStoreSnapshot) {
	roots, sids := capabilityStoreRootsAndSIDs(store)
	runnertrace.Printf("windows-setup", "cleanup_capability_store_acls roots=%d sids=%d", len(roots), len(sids))
	cleanupCapabilityAncestorACLs(roots, sids)
	for _, root := range pathutil.Dedupe(roots) {
		if pathExists(root) {
			removeACLPrincipals(root, sids)
		}
	}
}

type capabilityStoreSnapshot struct {
	WorkspaceByCWD     map[string]string `json:"workspace_by_cwd,omitempty"`
	WritableRootByPath map[string]string `json:"writable_root_by_path,omitempty"`
}

func readCapabilityStore(path string) (capabilityStoreSnapshot, error) {
	if strings.TrimSpace(path) == "" {
		return capabilityStoreSnapshot{}, fmt.Errorf("capability store path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return capabilityStoreSnapshot{}, err
	}
	var store capabilityStoreSnapshot
	if err := json.Unmarshal(data, &store); err != nil {
		return capabilityStoreSnapshot{}, err
	}
	return store, nil
}

func capabilityStoreRootsAndSIDs(store capabilityStoreSnapshot) ([]string, []string) {
	var roots []string
	var sids []string
	for root, sid := range store.WorkspaceByCWD {
		roots = append(roots, pathutil.Normalize(root))
		sids = append(sids, sid)
	}
	for root, sid := range store.WritableRootByPath {
		roots = append(roots, pathutil.Normalize(root))
		sids = append(sids, sid)
	}
	return pathutil.Dedupe(roots), dedupeStrings(sids...)
}

func cleanupLegacyCapabilityAncestorACLs(stateRoot string, policy winpolicy.Policy) error {
	roots := append([]string{}, policy.ReadRoots...)
	roots = append(roots, policy.WriteRoots...)
	sids := append([]string{}, policy.CapabilitySIDs...)
	if strings.TrimSpace(stateRoot) != "" {
		if store, err := readCapabilityStore(setupstate.NewDirs(stateRoot).CapPath); err == nil {
			storeRoots, storeSIDs := capabilityStoreRootsAndSIDs(store)
			roots = append(roots, storeRoots...)
			sids = append(sids, storeSIDs...)
		}
	}
	cleanupCapabilityAncestorACLs(roots, sids)
	return nil
}

func cleanupCapabilityAncestorACLs(roots []string, sids []string) {
	sids = dedupeStrings(sids...)
	if len(sids) == 0 {
		return
	}
	var ancestors []string
	for _, root := range roots {
		ancestors = append(ancestors, ancestorPaths(root)...)
	}
	for _, ancestor := range pathutil.Dedupe(ancestors) {
		if pathExists(ancestor) {
			removeACLPrincipals(ancestor, sids)
		}
	}
}

func removeACLPrincipals(path string, principals []string) {
	principals = dedupeStrings(principals...)
	if strings.TrimSpace(path) == "" || len(principals) == 0 {
		return
	}
	runICACLSRemove(path, "/remove:g", principals)
	runICACLSRemove(path, "/remove:d", principals)
}

func runICACLSRemove(path string, mode string, principals []string) {
	const chunkSize = 32
	var normalized []string
	for _, principal := range principals {
		principal = icaclsPrincipal(principal)
		if principal != "" {
			normalized = append(normalized, principal)
		}
	}
	for start := 0; start < len(normalized); start += chunkSize {
		end := start + chunkSize
		if end > len(normalized) {
			end = len(normalized)
		}
		args := append([]string{path, mode}, normalized[start:end]...)
		_, _ = winexec.Run(context.Background(), "icacls.exe", args, winexec.Options{
			Timeout:        10 * time.Second,
			TraceComponent: "windows-setup",
			TraceName:      "icacls_remove",
		})
	}
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

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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

type ApplyPolicyACLOptions struct {
	StateRoot                         string
	Users                             []string
	CleanupLegacyAncestorCapabilities bool
}

func policyHasACLTargets(policy winpolicy.Policy) bool {
	return len(policy.ReadRoots) > 0 ||
		len(policy.WriteRoots) > 0 ||
		len(policy.DenyReadPaths) > 0 ||
		len(policy.DenyWritePaths) > 0
}

func ApplyMissingPolicyACLs(policy winpolicy.Policy, users ...string) error {
	return ApplyMissingPolicyACLsWithOptions(policy, ApplyPolicyACLOptions{Users: users})
}

func ApplyRuntimePolicyACLsWithOptions(policy winpolicy.Policy, opts ApplyPolicyACLOptions) error {
	return applyPolicyACLsWithOptions(policy, opts)
}

func ApplyMissingPolicyACLsWithOptions(policy winpolicy.Policy, opts ApplyPolicyACLOptions) error {
	if opts.CleanupLegacyAncestorCapabilities {
		if err := cleanupLegacyCapabilityAncestorACLs(opts.StateRoot, policy); err != nil {
			return err
		}
	}
	return applyPolicyACLsWithOptions(policy, opts)
}

func applyPolicyACLsWithOptions(policy winpolicy.Policy, opts ApplyPolicyACLOptions) error {
	done := runnertrace.Span("windows-setup", "apply_policy_acls")
	defer done()
	users := appendPrincipal(opts.Users...)
	principals, err := sandboxPrincipalSIDs(users...)
	if err != nil {
		return err
	}
	aclPlan := newPolicyACLPlan(policy)
	capabilities := aclPlan.allCapabilitySIDs
	runnertrace.Printf(
		"windows-setup",
		"apply_policy_acls roots read=%d write=%d deny_write=%d deny_read=%d users=%d principals=%d capabilities=%d",
		len(policy.ReadRoots),
		len(policy.WriteRoots),
		len(policy.DenyWritePaths),
		len(policy.DenyReadPaths),
		len(users),
		len(principals),
		len(capabilities),
	)
	materializeDenyWriteKeys := map[string]struct{}{}
	for _, root := range policy.MaterializeDenyWritePaths {
		key := aclPathKey(root)
		if key != "" {
			materializeDenyWriteKeys[key] = struct{}{}
		}
	}
	var tasks []aclTask
	for _, root := range policy.ReadRoots {
		if isDefaultReadRoot(root) {
			continue
		}
		if _, writable := aclPlan.writeRootKeys[aclPathKey(root)]; writable {
			continue
		}
		targets := joinPrincipals(principals, capabilities)
		root := root
		tasks = append(tasks, aclTask{
			key: pathutil.Key(root),
			run: func() error {
				return grantPath(root, targets, "RX")
			},
		})
	}
	for _, root := range policy.WriteRoots {
		sid := aclPlan.writeRootCapabilitySID(root)
		targets := joinPrincipals(principals, []string{sid})
		root := root
		tasks = append(tasks, aclTask{
			key: pathutil.Key(root),
			run: func() error {
				return grantPath(root, targets, "M")
			},
		})
	}
	for _, root := range policy.DenyWritePaths {
		targets := aclPlan.writeRootCapabilitySIDsForPath(root)
		if _, shouldMaterialize := materializeDenyWriteKeys[aclPathKey(root)]; shouldMaterialize {
			if err := os.MkdirAll(root, 0o700); err != nil {
				return fmt.Errorf("materialize deny-write path %s: %w", root, err)
			}
		}
		root := root
		tasks = append(tasks, aclTask{
			key: pathutil.Key(root),
			run: func() error {
				return denyPath(root, targets, "W")
			},
		})
	}
	for _, root := range policy.DenyReadPaths {
		root := root
		tasks = append(tasks, aclTask{
			key: pathutil.Key(root),
			run: func() error {
				return denyPath(root, principals, "RX")
			},
		})
	}
	return runACLTasks(tasks)
}

type aclTask struct {
	key string
	run func() error
}

func appendAncestorACLTasks(tasks []aclTask, root string, principals []string, seen map[string]struct{}) []aclTask {
	ancestors := ancestorPaths(root)
	for i := len(ancestors) - 1; i >= 0; i-- {
		ancestor := ancestors[i]
		key := aclPathKey(ancestor)
		if key == "" {
			continue
		}
		if seen != nil {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		ancestorPath := ancestor
		targets := append([]string(nil), principals...)
		tasks = append(tasks, aclTask{
			key: key,
			run: func() error {
				return grantPathWithInherit(ancestorPath, targets, "X", false)
			},
		})
	}
	return tasks
}

func runACLTasksSerial(name string, tasks []aclTask) error {
	if len(tasks) == 0 {
		return nil
	}
	done := runnertrace.Span("windows-setup", name)
	defer done()
	runnertrace.Printf("windows-setup", "%s count=%d", name, len(tasks))
	for _, task := range tasks {
		if task.run == nil {
			continue
		}
		if err := task.run(); err != nil {
			return err
		}
	}
	return nil
}

func runACLTasks(tasks []aclTask) error {
	if len(tasks) == 0 {
		return nil
	}
	done := runnertrace.Span("windows-setup", "acl_tasks")
	defer done()
	runnertrace.Printf("windows-setup", "acl_tasks count=%d", len(tasks))
	batches := batchACLTasks(tasks)
	for i, batch := range batches {
		if err := runACLTaskBatch(i+1, len(batches), batch); err != nil {
			return err
		}
	}
	return nil
}

type aclTaskGroup struct {
	key   string
	tasks []func() error
}

func batchACLTasks(tasks []aclTask) [][]aclTaskGroup {
	groups := make(map[string][]func() error)
	order := make([]string, 0, len(tasks))
	for i, task := range tasks {
		if task.run == nil {
			continue
		}
		key := strings.TrimSpace(task.key)
		if key == "" {
			key = fmt.Sprintf("__task_%d", i)
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], task.run)
	}
	var batches [][]aclTaskGroup
	for _, key := range order {
		group := aclTaskGroup{key: key, tasks: append([]func() error(nil), groups[key]...)}
		placed := false
		for i := range batches {
			if aclTaskGroupOverlapsBatch(group.key, batches[i]) {
				continue
			}
			batches[i] = append(batches[i], group)
			placed = true
			break
		}
		if !placed {
			batches = append(batches, []aclTaskGroup{group})
		}
	}
	return batches
}

func aclTaskGroupOverlapsBatch(key string, batch []aclTaskGroup) bool {
	for _, existing := range batch {
		if aclTaskKeysOverlap(key, existing.key) {
			return true
		}
	}
	return false
}

func aclTaskKeysOverlap(a string, b string) bool {
	return pathKeysOverlap(a, b)
}

func pathKeysOverlap(a string, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return pathKeyIsUnder(a, b) || pathKeyIsUnder(b, a)
}

func pathKeyIsUnder(target string, root string) bool {
	target = strings.TrimRight(strings.TrimSpace(target), `\/`)
	root = strings.TrimRight(strings.TrimSpace(root), `\/`)
	if target == "" || root == "" {
		return false
	}
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(filepath.Separator))
}

func runACLTaskBatch(index int, total int, batch []aclTaskGroup) error {
	if len(batch) == 0 {
		return nil
	}
	runnertrace.Printf("windows-setup", "acl_tasks batch=%d/%d groups=%d", index, total, len(batch))
	const maxConcurrent = 8
	sem := make(chan struct{}, maxConcurrent)
	errs := make(chan error, len(batch))
	var wg sync.WaitGroup
	for _, group := range batch {
		group := group
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			for _, task := range group.tasks {
				if err := task(); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	var messages []string
	for err := range errs {
		if err != nil {
			messages = append(messages, err.Error())
		}
	}
	if len(messages) > 0 {
		return fmt.Errorf("%s", strings.Join(messages, "; "))
	}
	return nil
}

type policyACLPlan struct {
	writeRootKeys              map[string]struct{}
	writeRootKeyOrder          []string
	writeRootCapabilityByKey   map[string]string
	allWriteRootCapabilitySIDs []string
	allCapabilitySIDs          []string
}

func newPolicyACLPlan(policy winpolicy.Policy) policyACLPlan {
	plan := policyACLPlan{
		writeRootKeys:            map[string]struct{}{},
		writeRootCapabilityByKey: map[string]string{},
		allCapabilitySIDs:        appendPrincipal(policy.CapabilitySIDs...),
	}
	for root, sid := range policy.WriteRootCapabilitySIDs {
		sid = strings.TrimSpace(sid)
		if sid == "" {
			continue
		}
		if key := aclPathKey(root); key != "" {
			plan.writeRootCapabilityByKey[key] = sid
		}
	}
	var rootCaps []string
	for _, root := range policy.WriteRoots {
		key := aclPathKey(root)
		if key == "" {
			continue
		}
		if _, ok := plan.writeRootKeys[key]; !ok {
			plan.writeRootKeys[key] = struct{}{}
			plan.writeRootKeyOrder = append(plan.writeRootKeyOrder, key)
		}
		sid := strings.TrimSpace(plan.writeRootCapabilityByKey[key])
		if sid == "" {
			resolvedKey := pathutil.Key(root)
			sid = strings.TrimSpace(plan.writeRootCapabilityByKey[resolvedKey])
			if sid != "" {
				plan.writeRootCapabilityByKey[key] = sid
			} else if resolvedKey != "" {
				for candidate, candidateSID := range policy.WriteRootCapabilitySIDs {
					if pathutil.Key(candidate) != resolvedKey {
						continue
					}
					sid = strings.TrimSpace(candidateSID)
					if sid != "" {
						plan.writeRootCapabilityByKey[key] = sid
						break
					}
				}
			}
		}
		if sid != "" {
			rootCaps = append(rootCaps, sid)
		}
	}
	plan.allWriteRootCapabilitySIDs = dedupeStrings(rootCaps...)
	return plan
}

func (p policyACLPlan) writeRootCapabilitySID(root string) string {
	return strings.TrimSpace(p.writeRootCapabilityByKey[aclPathKey(root)])
}

func (p policyACLPlan) writeRootCapabilitySIDsForPath(path string) []string {
	pathKey := aclPathKey(path)
	var out []string
	for _, rootKey := range p.writeRootKeyOrder {
		if pathKey != "" && !pathKeysOverlap(rootKey, pathKey) {
			continue
		}
		out = append(out, p.writeRootCapabilityByKey[rootKey])
	}
	if len(appendPrincipal(out...)) > 0 {
		return dedupeStrings(out...)
	}
	if len(p.allWriteRootCapabilitySIDs) > 0 {
		return append([]string(nil), p.allWriteRootCapabilitySIDs...)
	}
	return append([]string(nil), p.allCapabilitySIDs...)
}

func requiredPolicyACLTargets(policy winpolicy.Policy, users ...string) []policyACLTarget {
	users = appendPrincipal(users...)
	principals := sandboxPrincipals(users...)
	aclPlan := newPolicyACLPlan(policy)
	capabilities := aclPlan.allCapabilitySIDs
	var out []policyACLTarget
	for _, root := range policy.ReadRoots {
		if isDefaultReadRoot(root) {
			continue
		}
		if _, writable := aclPlan.writeRootKeys[aclPathKey(root)]; writable {
			continue
		}
		if !pathExists(root) {
			continue
		}
		targets := joinPrincipals(principals, capabilities)
		out = append(out, policyACLTarget{Path: root, Entries: aclEntries(targets, "RX", acl.Grant)})
	}
	for _, root := range policy.WriteRoots {
		if !pathExists(root) {
			continue
		}
		sid := aclPlan.writeRootCapabilitySID(root)
		targets := joinPrincipals(principals, []string{sid})
		out = append(out, policyACLTarget{Path: root, Entries: aclEntries(targets, "M", acl.Grant)})
	}
	for _, root := range policy.DenyWritePaths {
		if !pathExists(root) {
			continue
		}
		targets := aclPlan.writeRootCapabilitySIDsForPath(root)
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
	return nil
}

func appendAncestorACLTargets(out []policyACLTarget, root string, principals []string, seen map[string]struct{}) []policyACLTarget {
	for _, ancestor := range ancestorPaths(root) {
		key := aclPathKey(ancestor)
		if seen != nil {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
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

func aclPathKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return strings.ToLower(filepath.Clean(path))
}

func joinPrincipals(groups ...[]string) []string {
	var values []string
	for _, group := range groups {
		values = append(values, group...)
	}
	return dedupeStrings(values...)
}

func sandboxPrincipals(users ...string) []string {
	return []string{GroupName}
}

func sandboxPrincipalSIDs(users ...string) ([]string, error) {
	return resolvePrincipalSIDs(sandboxPrincipals(users...)...)
}

func sandboxTraversePrincipals(users ...string) []string {
	targets := appendPrincipal(users...)
	if len(targets) > 0 {
		return targets
	}
	return []string{GroupName}
}

func sandboxTraversePrincipalSIDs(users ...string) ([]string, error) {
	return resolvePrincipalSIDs(sandboxTraversePrincipals(users...)...)
}

func sandboxAncestorPrincipals(users ...string) []string {
	targets := appendPrincipal(append([]string{GroupName}, users...)...)
	if len(targets) > 0 {
		return targets
	}
	return []string{GroupName}
}

func sandboxAncestorPrincipalSIDs(users ...string) ([]string, error) {
	return resolvePrincipalSIDs(sandboxAncestorPrincipals(users...)...)
}

func resolvePrincipalSIDs(principals ...string) ([]string, error) {
	values := appendPrincipal(principals...)
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		resolved := strings.TrimSpace(value)
		if resolved == "" {
			continue
		}
		if !looksLikeSID(resolved) {
			lookupStarted := time.Now()
			sid, err := win32.LookupAccountSIDString(resolved)
			if err != nil {
				return nil, fmt.Errorf("resolve principal %s SID: %w", resolved, err)
			}
			runnertrace.Printf("windows-setup", "resolve_principal_sid principal=%q duration=%s", resolved, time.Since(lookupStarted).Round(time.Millisecond))
			resolved = sid
		}
		key := strings.ToUpper(strings.TrimSpace(resolved))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, strings.TrimSpace(resolved))
	}
	return out, nil
}

func looksLikeSID(value string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value)), "S-1-")
}

func writeRootCapabilitySID(policy winpolicy.Policy, root string) string {
	return newPolicyACLPlan(policy).writeRootCapabilitySID(root)
}

func writeRootCapabilitySIDsForPath(policy winpolicy.Policy, path string) []string {
	return newPolicyACLPlan(policy).writeRootCapabilitySIDsForPath(path)
}

func writeRootOverlapsPath(root string, path string) bool {
	return pathKeysOverlap(aclPathKey(root), aclPathKey(path))
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
	homeKey := userHomePathKey()
	var ancestors []string
	seen := map[string]struct{}{}
	for parent != "" {
		if isVolumeRoot(parent) {
			break
		}
		parentKey := aclPathKey(parent)
		if isUserProfileRootOrAboveKey(parentKey, homeKey) {
			break
		}
		if _, ok := seen[parentKey]; !ok {
			seen[parentKey] = struct{}{}
			ancestors = append(ancestors, parent)
		}
		next := filepath.Dir(parent)
		if next == "" || strings.EqualFold(next, parent) {
			break
		}
		parent = next
	}
	return ancestors
}

func isUserProfileRootOrAbove(path string) bool {
	return isUserProfileRootOrAboveKey(aclPathKey(path), userHomePathKey())
}

func userHomePathKey() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return aclPathKey(home)
}

func isUserProfileRootOrAboveKey(pathKey string, homeKey string) bool {
	if pathKey == "" || homeKey == "" {
		return false
	}
	return pathKey == homeKey || pathKeyIsUnder(homeKey, pathKey)
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
	done := traceACLPath("grant", path, principals, rights, inherit)
	defer done()
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
	done := traceACLPath("deny", path, principals, rights, true)
	defer done()
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

func traceACLPath(action string, path string, principals []string, rights string, inherit bool) func() {
	if !runnertrace.Enabled() {
		return func() {}
	}
	started := time.Now()
	runnertrace.Printf("windows-setup", "acl.%s path=%q principals=%d rights=%s inherit=%t start", action, path, len(appendPrincipal(principals...)), rights, inherit)
	return func() {
		runnertrace.Printf("windows-setup", "acl.%s path=%q done duration=%s", action, path, time.Since(started).Round(time.Millisecond))
	}
}

func aclRights(rights string) acl.Rights {
	switch strings.ToUpper(strings.TrimSpace(rights)) {
	case "F":
		return acl.FullControl
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

func writeUsersFile(path string, offlineUser string, offlinePassword string) error {
	offlineProtected, err := win32.ProtectMachineString(offlinePassword, "caelis sandbox offline password")
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(UsersFile{
		Offline: UserSecret{Username: offlineUser, PasswordProtected: offlineProtected},
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func legacyOnlineUsername(payload Payload) string {
	if strings.TrimSpace(payload.StateRoot) == "" {
		return ""
	}
	normalized := strings.ToLower(strings.TrimSpace(filepath.Clean(payload.StateRoot)))
	sum := sha256.Sum256([]byte(normalized))
	return "CaelisSbxOn" + hex.EncodeToString(sum[:])[:8]
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
	displayArgs := redactedCommandArgs(name, args)
	result, err := winexec.Run(context.Background(), name, args, winexec.Options{
		Timeout:        30 * time.Second,
		Stdin:          []byte(input),
		TraceComponent: "windows-setup",
		TraceName:      "setup_command",
		DisplayArgs:    displayArgs,
	})
	display := strings.Join(displayArgs, " ")
	if isCommandContextError(err) {
		return fmt.Errorf("%s %s: %w", name, display, err)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, display, err, strings.TrimSpace(string(result.CombinedOutput())))
	}
	return nil
}

func redactedCommandArgs(name string, args []string) []string {
	out := append([]string(nil), args...)
	if !strings.EqualFold(strings.TrimSpace(name), "net.exe") {
		return out
	}
	if len(out) >= 3 && strings.EqualFold(out[0], "user") {
		operation := ""
		for _, arg := range out[2:] {
			if strings.EqualFold(arg, "/add") || strings.EqualFold(arg, "/delete") {
				operation = strings.ToLower(arg)
				break
			}
		}
		if operation != "/delete" {
			out[2] = "<redacted>"
		}
	}
	return out
}

func isCommandContextError(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
