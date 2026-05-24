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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	if payload.Kind == SetupKindReadACLRefresh {
		return executeReadACLRefresh(payload, progress)
	}
	return WithMaintenanceLock(context.Background(), payload.StateRoot, setupMaintenanceLockTimeout, func() error {
		if !payload.ExpiresAt.IsZero() && time.Now().After(payload.ExpiresAt) {
			return fmt.Errorf("windows setup: operation %s expired before execution", strings.TrimSpace(payload.OperationID))
		}
		return executeWithProgressLocked(payload, progress)
	})
}

func WithMaintenanceLock(ctx context.Context, stateRoot string, timeout time.Duration, fn func() error) error {
	stateRoot = strings.TrimSpace(stateRoot)
	if stateRoot == "" {
		return fmt.Errorf("windows setup: state root is required")
	}
	return win32.WithNamedMutex(ctx, setupMaintenanceMutexName(stateRoot), timeout, fn)
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
	case SetupKindReadACLRefresh:
		return executeReadACLRefresh(payload, progress)
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
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "ensuring sandbox local users", Step: 3, Total: totalSteps})
	reusableOfflinePassword, reusableErr := reusableOfflineUserPassword(dirs.UsersPath, payload.OfflineUsername)
	if reusableErr != nil {
		reportDebugProgress(payload, progress, "existing offline sandbox credentials will be rotated: "+reusableErr.Error())
	}
	offlinePassword, err := ensureLocalUser(payload.OfflineUsername, reusableOfflinePassword)
	if err != nil {
		return err
	}
	onlinePassword := ""
	if strings.TrimSpace(payload.OnlineUsername) != "" {
		reusableOnlinePassword, reusableErr := reusableOnlineUserPassword(dirs.UsersPath, payload.OnlineUsername)
		if reusableErr != nil {
			reportDebugProgress(payload, progress, "existing online sandbox credentials will be rotated: "+reusableErr.Error())
		}
		onlinePassword, err = ensureLocalUser(payload.OnlineUsername, reusableOnlinePassword)
		if err != nil {
			return err
		}
	}
	sandboxUsers := payloadSandboxUsers(payload)
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "updating sandbox group membership", Step: 4, Total: totalSteps})
	for _, username := range sandboxUsers {
		if err := addUserToGroup(username, GroupName); err != nil {
			return err
		}
		if err := removeUserFromGroup(username, "Administrators"); err != nil {
			return err
		}
	}
	reportProgress(payload, progress, Progress{Phase: "accounts", Message: "hiding sandbox users from Windows sign-in", Step: 5, Total: totalSteps})
	hideSandboxUsers(sandboxUsers...)
	reportProgress(payload, progress, Progress{Phase: "state", Message: "protecting sandbox state directories", Step: 6, Total: totalSteps})
	if err := protectStateDirectories(dirs, payload.OwnerUsername); err != nil {
		return err
	}
	if policyHasACLTargets(payload.GlobalPolicy) {
		reportProgress(payload, progress, Progress{Phase: "acl", Message: "refreshing global sandbox ACL policy", Step: 7, Total: totalSteps})
		if err := ApplyMissingPolicyACLsWithOptions(payload.GlobalPolicy, ApplyPolicyACLOptions{
			StateRoot:                         payload.StateRoot,
			Users:                             sandboxUsers,
			CleanupLegacyAncestorCapabilities: true,
		}); err != nil {
			return err
		}
	}
	reportProgress(payload, progress, Progress{Phase: "acl", Message: "refreshing current workspace ACL policy", Step: 8, Total: totalSteps})
	if err := ApplyMissingPolicyACLsWithOptions(payload.Policy, ApplyPolicyACLOptions{
		StateRoot:                         payload.StateRoot,
		Users:                             sandboxUsers,
		CleanupLegacyAncestorCapabilities: true,
	}); err != nil {
		return err
	}
	if err := prepareRunnerEnvironmentDirs(dirs, sandboxUsers, runnerEnvironmentCapabilitySIDs(payload.Policy)); err != nil {
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
	if err := writeUsersFile(dirs.UsersPath, payload.OfflineUsername, offlinePassword, payload.OnlineUsername, onlinePassword); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "marker", Message: "writing setup marker", Step: 11, Total: totalSteps})
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
	startReadACLQueueHelperBestEffort(payload)
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "Windows sandbox setup is ready", Step: 12, Total: totalSteps, Done: true})
	return nil
}

func setupMaintenanceMutexName(stateRoot string) string {
	return stateMutexName(`Local\CaelisSandboxSetup-`, stateRoot)
}

func stateMutexName(prefix string, stateRoot string) string {
	normalized := strings.ToLower(filepath.Clean(strings.TrimSpace(stateRoot)))
	hash, err := setupstate.HashJSON(normalized)
	if err != nil || len(hash) < 16 {
		return strings.TrimRight(prefix, "-")
	}
	return prefix + hash[:16]
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
		Users:     payloadSandboxUsers(payload),
	}); err != nil {
		return err
	}
	if err := prepareRunnerEnvironmentDirs(dirs, payloadSandboxUsers(payload), runnerEnvironmentCapabilitySIDs(payload.Policy)); err != nil {
		return err
	}
	clearDone := runnertrace.Span("windows-setup", "runtime_refresh.clear_error")
	if err := setupstate.ClearError(dirs.ErrorPath); err != nil {
		clearDone()
		return err
	}
	clearDone()
	startReadACLQueueHelperBestEffort(payload)
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "required request ACL policy is ready", Step: 3, Total: 3, Done: true})
	return nil
}

func executeReadACLRefresh(payload Payload, progress ProgressFunc) error {
	done := runnertrace.Span("windows-setup", "read_acl_refresh")
	defer done()
	dirs := setupstate.NewDirs(payload.StateRoot)
	reportProgress(payload, progress, Progress{Phase: "read-acl", Message: "validating existing sandbox setup", Step: 1, Total: 2})
	if err := validateGlobalSetup(payload, dirs); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "read-acl", Message: "draining queued read ACL grants", Step: 2, Total: 2})
	if err := drainReadACLQueue(payload.StateRoot); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "queued read ACL grants are ready", Step: 2, Total: 2, Done: true})
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
		Users:                             payloadSandboxUsers(payload),
		CleanupLegacyAncestorCapabilities: true,
	}); err != nil {
		return err
	}
	if err := prepareRunnerEnvironmentDirs(dirs, payloadSandboxUsers(payload), runnerEnvironmentCapabilitySIDs(payload.Policy)); err != nil {
		return err
	}
	if err := writeWorkspaceState(payload); err != nil {
		return err
	}
	if err := setupstate.ClearError(dirs.ErrorPath); err != nil {
		return err
	}
	startReadACLQueueHelperBestEffort(payload)
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "required workspace ACL policy is ready", Step: 3, Total: 3, Done: true})
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
	if strings.TrimSpace(payload.OfflineUsername) != "" && !strings.EqualFold(marker.OfflineUsername, payload.OfflineUsername) {
		return fmt.Errorf("windows setup: offline sandbox user changed; run full setup")
	}
	if strings.TrimSpace(payload.OnlineUsername) != "" && !strings.EqualFold(marker.OnlineUsername, payload.OnlineUsername) {
		return fmt.Errorf("windows setup: online sandbox user changed; run full setup")
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

func ensureLocalUser(username string, reusablePassword ...string) (string, error) {
	existingPassword := firstNonEmptyString(reusablePassword...)
	if err := runNet("user", username); err == nil {
		if existingPassword != "" {
			return existingPassword, runNet("user", username, "/active:yes", "/expires:never", "/passwordchg:no")
		}
		password, err := randomPassword()
		if err != nil {
			return "", err
		}
		return password, runNetWithInput("Y\r\n", "user", username, password, "/active:yes", "/expires:never", "/Y")
	}
	password, err := randomPassword()
	if err != nil {
		return "", err
	}
	if err := runNetWithInput("Y\r\n", "user", username, password, "/add", "/active:yes", "/expires:never", "/passwordchg:no", "/Y"); err != nil {
		return "", err
	}
	return password, nil
}

func reusableOfflineUserPassword(path string, username string) (string, error) {
	return reusableSandboxUserPassword(path, username, "offline")
}

func reusableOnlineUserPassword(path string, username string) (string, error) {
	return reusableSandboxUserPassword(path, username, "online")
}

func reusableSandboxUserPassword(path string, username string, kind string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", fmt.Errorf("%s sandbox username is required", kind)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var users UsersFile
	if err := json.Unmarshal(data, &users); err != nil {
		return "", fmt.Errorf("decode sandbox users file: %w", err)
	}
	secret, ok := users.secret(kind)
	if !ok {
		return "", fmt.Errorf("sandbox users file is missing %s account", kind)
	}
	if !strings.EqualFold(strings.TrimSpace(secret.Username), username) {
		return "", fmt.Errorf("sandbox users file does not match expected %s account", kind)
	}
	password, err := win32.UnprotectString(secret.PasswordProtected)
	if err != nil {
		return "", fmt.Errorf("unprotect %s sandbox password: %w", kind, err)
	}
	if err := win32.ValidateCredentials(win32.LogonCredentials{
		Username: username,
		Password: password,
	}); err != nil {
		return "", fmt.Errorf("validate %s sandbox credentials: %w", kind, err)
	}
	return password, nil
}

func (u UsersFile) secret(kind string) (UserSecret, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "offline":
		return u.Offline, strings.TrimSpace(u.Offline.Username) != ""
	case "online":
		if u.Online == nil {
			return UserSecret{}, false
		}
		return *u.Online, strings.TrimSpace(u.Online.Username) != ""
	default:
		return UserSecret{}, false
	}
}

func payloadSandboxUsers(payload Payload) []string {
	return appendPrincipal(payload.OfflineUsername, payload.OnlineUsername)
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
		OnlineUsername:          strings.TrimSpace(payload.OnlineUsername),
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
	return checkPolicyACLs(policy, false, users...)
}

func CheckSynchronousPolicyACLs(policy winpolicy.Policy, users ...string) ([]ACLCheckResult, error) {
	return checkPolicyACLs(policy, true, users...)
}

func checkPolicyACLs(policy winpolicy.Policy, synchronousOnly bool, users ...string) ([]ACLCheckResult, error) {
	var out []ACLCheckResult
	for _, target := range requiredPolicyACLTargetsWithOptions(policy, synchronousOnly, users...) {
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
	var readSpecs []readACLSpec
	var readTasks []aclTask
	var syncTasks []aclTask
	for _, root := range policy.ReadRoots {
		if isDefaultReadRoot(root) {
			continue
		}
		if _, writable := aclPlan.writeRootKeys[aclPathKey(root)]; writable {
			continue
		}
		targets := joinPrincipals(principals, capabilities)
		root := root
		readSpecs = append(readSpecs, readACLSpec{
			Path:       root,
			Principals: targets,
			Rights:     "RX",
		})
		readTasks = append(readTasks, aclTask{
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
		syncTasks = append(syncTasks, aclTask{
			key: pathutil.Key(root),
			run: func() error {
				return grantPath(root, targets, "M")
			},
		})
	}
	for _, root := range policy.DenyWritePaths {
		targets := joinPrincipals(principals, aclPlan.writeRootCapabilitySIDsForPath(root))
		if _, shouldMaterialize := materializeDenyWriteKeys[aclPathKey(root)]; shouldMaterialize {
			if err := os.MkdirAll(root, 0o700); err != nil {
				return fmt.Errorf("materialize deny-write path %s: %w", root, err)
			}
		}
		root := root
		syncTasks = append(syncTasks, aclTask{
			key: pathutil.Key(root),
			run: func() error {
				return denyPath(root, targets, "W")
			},
		})
	}
	for _, root := range policy.DenyReadPaths {
		root := root
		syncTasks = append(syncTasks, aclTask{
			key: pathutil.Key(root),
			run: func() error {
				return denyPath(root, principals, "RX")
			},
		})
	}
	if len(readSpecs) > 0 {
		if strings.TrimSpace(opts.StateRoot) == "" {
			syncTasks = append(syncTasks, readTasks...)
		} else {
			enqueueReadACLsBestEffort(opts.StateRoot, readSpecs)
		}
	}
	return runACLTasks(syncTasks)
}

type aclTask struct {
	key string
	run func() error
}

const (
	readACLQueueVersion     = 1
	readACLQueueLockTimeout = 5 * time.Second
	readACLDrainLockTimeout = 5 * time.Second
	readACLClaimLease       = 2 * time.Minute
	readACLDrainBudget      = 30 * time.Second
	readACLDrainBatchSize   = 32
	readACLHelperStartGap   = 5 * time.Second
	readACLLogSegmentBytes  = 256 * 1024
	readACLLogMaxBytes      = 1024 * 1024
	readACLLogSegments      = 4

	// Keep in sync with impl/sandbox/windows/helper.go and setupcmd.
	internalSetupHelperCommand = "__caelis_windows_sandbox_setup__"
)

type readACLSpec struct {
	Path       string   `json:"path"`
	Principals []string `json:"principals"`
	Rights     string   `json:"rights"`
}

type readACLQueueItem struct {
	Path            string    `json:"path"`
	Principals      []string  `json:"principals"`
	Rights          string    `json:"rights"`
	Attempts        int       `json:"attempts,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	LastAttemptAt   time.Time `json:"last_attempt_at,omitempty"`
	NextAttemptAt   time.Time `json:"next_attempt_at,omitempty"`
	ProcessingUntil time.Time `json:"processing_until,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type readACLQueueFile struct {
	Version     int                `json:"version"`
	Items       []readACLQueueItem `json:"items,omitempty"`
	UpdatedAt   time.Time          `json:"updated_at,omitempty"`
	LastStartAt time.Time          `json:"last_start_at,omitempty"`
}

func enqueueReadACLs(stateRoot string, specs []readACLSpec) error {
	stateRoot = strings.TrimSpace(stateRoot)
	if stateRoot == "" {
		return fmt.Errorf("windows setup: state root is required for read ACL queue")
	}
	specs = normalizeReadACLSpecs(specs)
	if len(specs) == 0 {
		return nil
	}
	dirs := setupstate.NewDirs(stateRoot)
	if err := setupstate.EnsureDirs(dirs); err != nil {
		return err
	}
	return win32.WithNamedMutex(context.Background(), readACLQueueMutexName(stateRoot), readACLQueueLockTimeout, func() error {
		now := time.Now().UTC()
		queue, err := readReadACLQueue(readACLQueuePath(stateRoot))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		queue.Items = mergeReadACLQueueItems(queue.Items, readACLQueueItemsFromSpecs(specs, now))
		queue.Version = readACLQueueVersion
		queue.UpdatedAt = now
		appendReadACLLog(stateRoot, "enqueue", fmt.Sprintf("queued read ACL grants count=%d pending=%d", len(specs), len(queue.Items)))
		return writeReadACLQueue(readACLQueuePath(stateRoot), queue)
	})
}

func enqueueReadACLsBestEffort(stateRoot string, specs []readACLSpec) {
	if err := enqueueReadACLs(stateRoot, specs); err != nil {
		logReadACLBackgroundError(stateRoot, "enqueue_failed", err)
	}
}

func startReadACLQueueHelper(payload Payload) error {
	stateRoot := strings.TrimSpace(payload.StateRoot)
	if stateRoot == "" {
		return nil
	}
	shouldStart, err := claimReadACLHelperStart(stateRoot)
	if err != nil {
		return err
	}
	if !shouldStart {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("windows setup: resolve read ACL helper executable: %w", err)
	}
	helperPayload := Payload{
		Version:         payload.Version,
		Kind:            SetupKindReadACLRefresh,
		StateRoot:       stateRoot,
		OfflineUsername: payload.OfflineUsername,
		OnlineUsername:  payload.OnlineUsername,
		OwnerUsername:   payload.OwnerUsername,
	}
	encoded, err := EncodePayload(helperPayload)
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, internalSetupHelperCommand, encoded)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		appendReadACLLog(stateRoot, "helper_start_failed", err.Error())
		return fmt.Errorf("windows setup: start read ACL helper: %w", err)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	appendReadACLLog(stateRoot, "helper_start", "started read ACL queue helper")
	return nil
}

func startReadACLQueueHelperBestEffort(payload Payload) {
	if err := startReadACLQueueHelper(payload); err != nil {
		logReadACLBackgroundError(payload.StateRoot, "helper_start_best_effort_failed", err)
	}
}

func KickReadACLQueue(payload Payload) error {
	payload = payload.Normalize()
	if err := startReadACLQueueHelper(payload); err != nil {
		logReadACLBackgroundError(payload.StateRoot, "helper_kick_failed", err)
	}
	return nil
}

func ReadACLQueuePending(stateRoot string) (int, string, error) {
	stateRoot = strings.TrimSpace(stateRoot)
	if stateRoot == "" {
		return 0, "", nil
	}
	pending := 0
	lastError := ""
	err := win32.WithNamedMutex(context.Background(), readACLQueueMutexName(stateRoot), readACLQueueLockTimeout, func() error {
		queue, err := readReadACLQueue(readACLQueuePath(stateRoot))
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, item := range queue.Items {
			if len(appendPrincipal(item.Principals...)) == 0 {
				continue
			}
			pending++
			if strings.TrimSpace(item.LastError) != "" {
				lastError = strings.TrimSpace(item.LastError)
			}
		}
		return nil
	})
	return pending, lastError, err
}

func drainReadACLQueue(stateRoot string) error {
	stateRoot = strings.TrimSpace(stateRoot)
	if stateRoot == "" {
		return fmt.Errorf("windows setup: state root is required for read ACL queue")
	}
	ctx, cancel := context.WithTimeout(context.Background(), readACLDrainLockTimeout)
	defer cancel()
	startedAt := time.Now()
	err := win32.WithNamedMutex(ctx, readACLDrainMutexName(stateRoot), 0, func() error {
		appendReadACLLog(stateRoot, "drain_start", "draining read ACL queue")
		for {
			if time.Since(startedAt) >= readACLDrainBudget {
				pending, _, _ := ReadACLQueuePending(stateRoot)
				appendReadACLLog(stateRoot, "drain_budget_exhausted", fmt.Sprintf("read ACL drain budget exhausted pending=%d", pending))
				return nil
			}
			items, err := claimReadACLQueueBatch(stateRoot, time.Now().UTC(), readACLDrainBatchSize)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				pending, _, _ := ReadACLQueuePending(stateRoot)
				appendReadACLLog(stateRoot, "drain_complete", fmt.Sprintf("read ACL drain complete pending=%d", pending))
				return nil
			}
			processReadACLQueueItems(stateRoot, items)
		}
	})
	return err
}

func processReadACLQueueItems(stateRoot string, items []readACLQueueItem) {
	specs := readACLSpecsFromItems(items)
	if len(specs) == 0 {
		return
	}
	if err := runACLTasks(readACLTasksFromSpecs(specs)); err == nil {
		_ = completeReadACLQueueItems(stateRoot, specs)
		appendReadACLLog(stateRoot, "batch_success", fmt.Sprintf("applied read ACL batch count=%d", len(specs)))
		return
	}
	appendReadACLLog(stateRoot, "batch_retry", fmt.Sprintf("read ACL batch failed; retrying items individually count=%d", len(specs)))
	for _, spec := range specs {
		if err := runACLTasks(readACLTasksFromSpecs([]readACLSpec{spec})); err != nil {
			_ = failReadACLQueueItems(stateRoot, []readACLSpec{spec}, err)
			appendReadACLLog(stateRoot, "item_failed", fmt.Sprintf("read ACL grant failed path=%s error=%s", spec.Path, err.Error()))
			continue
		}
		_ = completeReadACLQueueItems(stateRoot, []readACLSpec{spec})
		appendReadACLLog(stateRoot, "item_success", fmt.Sprintf("applied read ACL grant path=%s", spec.Path))
	}
}

func readACLTasksFromSpecs(specs []readACLSpec) []aclTask {
	specs = normalizeReadACLSpecs(specs)
	tasks := make([]aclTask, 0, len(specs))
	for _, spec := range specs {
		spec := spec
		tasks = append(tasks, aclTask{
			key: pathutil.Key(spec.Path),
			run: func() error {
				return grantPath(spec.Path, spec.Principals, spec.Rights)
			},
		})
	}
	return tasks
}

func takeReadACLQueue(stateRoot string) ([]readACLSpec, error) {
	var items []readACLQueueItem
	now := time.Now().UTC()
	err := win32.WithNamedMutex(context.Background(), readACLQueueMutexName(stateRoot), readACLQueueLockTimeout, func() error {
		path := readACLQueuePath(stateRoot)
		queue, err := readReadACLQueue(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		items = append([]readACLQueueItem(nil), queue.Items...)
		queue.Items = nil
		queue.UpdatedAt = now
		if err := writeReadACLQueue(path, queue); err != nil {
			return err
		}
		return nil
	})
	return readACLSpecsFromItems(items), err
}

func readACLQueueHasItems(stateRoot string) (bool, error) {
	pending, _, err := ReadACLQueuePending(stateRoot)
	return pending > 0, err
}

func claimReadACLHelperStart(stateRoot string) (bool, error) {
	stateRoot = strings.TrimSpace(stateRoot)
	if stateRoot == "" {
		return false, nil
	}
	shouldStart := false
	err := win32.WithNamedMutex(context.Background(), readACLQueueMutexName(stateRoot), readACLQueueLockTimeout, func() error {
		now := time.Now().UTC()
		path := readACLQueuePath(stateRoot)
		queue, err := readReadACLQueue(readACLQueuePath(stateRoot))
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !readACLQueueHasReadyItems(queue, now) {
			return nil
		}
		if !queue.LastStartAt.IsZero() && now.Sub(queue.LastStartAt) < readACLHelperStartGap {
			return nil
		}
		queue.LastStartAt = now
		queue.UpdatedAt = now
		if err := writeReadACLQueue(path, queue); err != nil {
			return err
		}
		shouldStart = true
		return nil
	})
	return shouldStart, err
}

func readACLQueueHasReadyItems(queue readACLQueueFile, now time.Time) bool {
	for _, item := range queue.Items {
		if len(appendPrincipal(item.Principals...)) == 0 {
			continue
		}
		if !item.ProcessingUntil.IsZero() && item.ProcessingUntil.After(now) {
			continue
		}
		if !item.NextAttemptAt.IsZero() && item.NextAttemptAt.After(now) {
			continue
		}
		return true
	}
	return false
}

func claimReadACLQueueBatch(stateRoot string, now time.Time, limit int) ([]readACLQueueItem, error) {
	if limit <= 0 {
		limit = readACLDrainBatchSize
	}
	var claimed []readACLQueueItem
	err := win32.WithNamedMutex(context.Background(), readACLQueueMutexName(stateRoot), readACLQueueLockTimeout, func() error {
		path := readACLQueuePath(stateRoot)
		queue, err := readReadACLQueue(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		changed := false
		for i := range queue.Items {
			if len(claimed) >= limit {
				break
			}
			item := queue.Items[i]
			if len(appendPrincipal(item.Principals...)) == 0 {
				continue
			}
			if !item.ProcessingUntil.IsZero() && item.ProcessingUntil.After(now) {
				continue
			}
			if !item.NextAttemptAt.IsZero() && item.NextAttemptAt.After(now) {
				continue
			}
			item.Attempts++
			item.LastAttemptAt = now
			item.ProcessingUntil = now.Add(readACLClaimLease)
			item.UpdatedAt = now
			queue.Items[i] = item
			claimed = append(claimed, item)
			changed = true
		}
		if !changed {
			return nil
		}
		queue.UpdatedAt = now
		return writeReadACLQueue(path, queue)
	})
	return claimed, err
}

func completeReadACLQueueItems(stateRoot string, specs []readACLSpec) error {
	specs = normalizeReadACLSpecs(specs)
	if len(specs) == 0 {
		return nil
	}
	return win32.WithNamedMutex(context.Background(), readACLQueueMutexName(stateRoot), readACLQueueLockTimeout, func() error {
		path := readACLQueuePath(stateRoot)
		queue, err := readReadACLQueue(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		queue.Items = removeReadACLSpecsFromItems(queue.Items, specs)
		queue.UpdatedAt = time.Now().UTC()
		return writeReadACLQueue(path, queue)
	})
}

func failReadACLQueueItems(stateRoot string, specs []readACLSpec, cause error) error {
	specs = normalizeReadACLSpecs(specs)
	if len(specs) == 0 {
		return nil
	}
	message := ""
	if cause != nil {
		message = strings.TrimSpace(cause.Error())
	}
	now := time.Now().UTC()
	return win32.WithNamedMutex(context.Background(), readACLQueueMutexName(stateRoot), readACLQueueLockTimeout, func() error {
		path := readACLQueuePath(stateRoot)
		queue, err := readReadACLQueue(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		failed := readACLQueueItemsFromSpecs(specs, now)
		for i := range failed {
			failed[i].LastError = message
			failed[i].NextAttemptAt = now.Add(readACLRetryDelay(failed[i].Attempts))
		}
		queue.Items = mergeReadACLQueueItems(queue.Items, failed)
		for i := range queue.Items {
			for _, spec := range specs {
				if readACLItemMatchesSpec(queue.Items[i], spec) {
					queue.Items[i].ProcessingUntil = time.Time{}
					if queue.Items[i].LastError == "" {
						queue.Items[i].LastError = message
					}
					queue.Items[i].NextAttemptAt = now.Add(readACLRetryDelay(queue.Items[i].Attempts))
					queue.Items[i].UpdatedAt = now
				}
			}
		}
		queue.UpdatedAt = now
		return writeReadACLQueue(path, queue)
	})
}

func readReadACLQueue(path string) (readACLQueueFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return readACLQueueFile{}, err
	}
	var queue readACLQueueFile
	if err := json.Unmarshal(data, &queue); err != nil {
		return readACLQueueFile{}, fmt.Errorf("decode read ACL queue: %w", err)
	}
	queue.Items = normalizeReadACLQueueItems(queue.Items)
	return queue, nil
}

func writeReadACLQueue(path string, queue readACLQueueFile) error {
	queue.Items = normalizeReadACLQueueItems(queue.Items)
	if len(queue.Items) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if queue.Version == 0 {
		queue.Version = readACLQueueVersion
	}
	if queue.UpdatedAt.IsZero() {
		queue.UpdatedAt = time.Now().UTC()
	}
	data, err := json.MarshalIndent(queue, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".read_acl_queue.*.tmp")
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
	_ = os.Remove(path)
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func readACLQueueItemsFromSpecs(specs []readACLSpec, now time.Time) []readACLQueueItem {
	specs = normalizeReadACLSpecs(specs)
	items := make([]readACLQueueItem, 0, len(specs))
	for _, spec := range specs {
		item := readACLQueueItem{
			Path:       spec.Path,
			Principals: append([]string(nil), spec.Principals...),
			Rights:     spec.Rights,
			UpdatedAt:  now,
		}
		items = append(items, item)
	}
	return normalizeReadACLQueueItems(items)
}

func readACLSpecsFromItems(items []readACLQueueItem) []readACLSpec {
	specs := make([]readACLSpec, 0, len(items))
	for _, item := range normalizeReadACLQueueItems(items) {
		specs = append(specs, readACLSpec{
			Path:       item.Path,
			Principals: append([]string(nil), item.Principals...),
			Rights:     item.Rights,
		})
	}
	return normalizeReadACLSpecs(specs)
}

func normalizeReadACLQueueItems(items []readACLQueueItem) []readACLQueueItem {
	merged := mergeReadACLQueueItems(nil, items)
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func mergeReadACLQueueItems(base []readACLQueueItem, extra []readACLQueueItem) []readACLQueueItem {
	type item struct {
		value readACLQueueItem
		seen  map[string]struct{}
	}
	items := map[string]*item{}
	order := make([]string, 0, len(base)+len(extra))
	add := func(next readACLQueueItem) {
		path := pathutil.Normalize(next.Path)
		if path == "" {
			return
		}
		rights := strings.ToUpper(strings.TrimSpace(next.Rights))
		if rights == "" {
			rights = "RX"
		}
		principals := appendPrincipal(next.Principals...)
		if len(principals) == 0 {
			return
		}
		key := pathutil.Key(path) + "\x00" + rights
		current := items[key]
		if current == nil {
			next.Path = path
			next.Rights = rights
			next.Principals = nil
			current = &item{value: next, seen: map[string]struct{}{}}
			items[key] = current
			order = append(order, key)
		}
		for _, principal := range principals {
			principalKey := strings.ToLower(principal)
			if _, ok := current.seen[principalKey]; ok {
				continue
			}
			current.seen[principalKey] = struct{}{}
			current.value.Principals = append(current.value.Principals, principal)
		}
		if next.Attempts > current.value.Attempts {
			current.value.Attempts = next.Attempts
		}
		if strings.TrimSpace(next.LastError) != "" {
			current.value.LastError = strings.TrimSpace(next.LastError)
		}
		if next.LastAttemptAt.After(current.value.LastAttemptAt) {
			current.value.LastAttemptAt = next.LastAttemptAt
		}
		if current.value.NextAttemptAt.IsZero() || next.NextAttemptAt.IsZero() || next.NextAttemptAt.Before(current.value.NextAttemptAt) {
			current.value.NextAttemptAt = next.NextAttemptAt
		}
		if next.ProcessingUntil.After(current.value.ProcessingUntil) {
			current.value.ProcessingUntil = next.ProcessingUntil
		}
		if next.UpdatedAt.After(current.value.UpdatedAt) {
			current.value.UpdatedAt = next.UpdatedAt
		}
	}
	for _, item := range base {
		add(item)
	}
	for _, item := range extra {
		add(item)
	}
	out := make([]readACLQueueItem, 0, len(order))
	for _, key := range order {
		current := items[key]
		if current == nil || len(current.value.Principals) == 0 {
			continue
		}
		out = append(out, current.value)
	}
	return out
}

func removeReadACLSpecsFromItems(items []readACLQueueItem, specs []readACLSpec) []readACLQueueItem {
	specs = normalizeReadACLSpecs(specs)
	if len(specs) == 0 {
		return normalizeReadACLQueueItems(items)
	}
	var out []readACLQueueItem
	for _, item := range normalizeReadACLQueueItems(items) {
		remaining := append([]string(nil), item.Principals...)
		for _, spec := range specs {
			if !readACLItemMatchesSpec(item, spec) {
				continue
			}
			remaining = subtractPrincipals(remaining, spec.Principals)
		}
		item.Principals = appendPrincipal(remaining...)
		if len(item.Principals) == 0 {
			continue
		}
		item.ProcessingUntil = time.Time{}
		out = append(out, item)
	}
	return normalizeReadACLQueueItems(out)
}

func subtractPrincipals(base []string, remove []string) []string {
	removeSet := map[string]struct{}{}
	for _, principal := range appendPrincipal(remove...) {
		removeSet[strings.ToLower(principal)] = struct{}{}
	}
	var out []string
	for _, principal := range appendPrincipal(base...) {
		if _, ok := removeSet[strings.ToLower(principal)]; ok {
			continue
		}
		out = append(out, principal)
	}
	return out
}

func readACLItemMatchesSpec(item readACLQueueItem, spec readACLSpec) bool {
	itemPath := pathutil.Key(item.Path)
	specPath := pathutil.Key(spec.Path)
	if itemPath == "" || specPath == "" || itemPath != specPath {
		return false
	}
	itemRights := strings.ToUpper(strings.TrimSpace(item.Rights))
	if itemRights == "" {
		itemRights = "RX"
	}
	specRights := strings.ToUpper(strings.TrimSpace(spec.Rights))
	if specRights == "" {
		specRights = "RX"
	}
	return itemRights == specRights
}

func readACLRetryDelay(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return 2 * time.Second
	case attempts == 2:
		return 5 * time.Second
	case attempts == 3:
		return 15 * time.Second
	default:
		return time.Minute
	}
}

func appendReadACLLog(stateRoot string, event string, message string) {
	event = strings.TrimSpace(event)
	message = strings.TrimSpace(message)
	if event == "" && message == "" {
		return
	}
	_ = win32.WithNamedMutex(context.Background(), readACLLogMutexName(), time.Second, func() error {
		path, err := readACLLogPath()
		if err != nil {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil
		}
		if err := rotateReadACLLogIfNeeded(path); err != nil {
			return nil
		}
		stateHash := ""
		if hash, err := setupstate.HashJSON(strings.ToLower(filepath.Clean(strings.TrimSpace(stateRoot)))); err == nil && len(hash) >= 12 {
			stateHash = hash[:12]
		}
		record := map[string]any{
			"time":    time.Now().UTC().Format(time.RFC3339Nano),
			"event":   event,
			"message": message,
		}
		if stateHash != "" {
			record["state"] = stateHash
		}
		data, err := json.Marshal(record)
		if err != nil {
			return nil
		}
		data = append(data, '\n')
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil
		}
		defer file.Close()
		_, _ = file.Write(data)
		return nil
	})
}

func logReadACLBackgroundError(stateRoot string, event string, err error) {
	if err == nil {
		return
	}
	message := strings.TrimSpace(err.Error())
	appendReadACLLog(stateRoot, event, message)
	runnertrace.Printf("windows-setup", "read_acl_background.%s err=%v", strings.TrimSpace(event), err)
}

func rotateReadACLLogIfNeeded(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return enforceReadACLLogTotal(path)
		}
		return err
	}
	if info.Size() < readACLLogSegmentBytes {
		return enforceReadACLLogTotal(path)
	}
	maxIndex := readACLLogSegments - 1
	if maxIndex < 1 {
		maxIndex = 1
	}
	_ = os.Remove(readACLLogSegmentPath(path, maxIndex))
	for i := maxIndex - 1; i >= 1; i-- {
		from := readACLLogSegmentPath(path, i)
		to := readACLLogSegmentPath(path, i+1)
		if _, err := os.Stat(from); err == nil {
			_ = os.Remove(to)
			_ = os.Rename(from, to)
		}
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(readACLLogSegmentPath(path, 1))
		_ = os.Rename(path, readACLLogSegmentPath(path, 1))
	}
	return enforceReadACLLogTotal(path)
}

func enforceReadACLLogTotal(path string) error {
	total := int64(0)
	paths := []string{path}
	for i := 1; i < readACLLogSegments; i++ {
		paths = append(paths, readACLLogSegmentPath(path, i))
	}
	for _, candidate := range paths {
		if info, err := os.Stat(candidate); err == nil {
			total += info.Size()
		}
	}
	for i := len(paths) - 1; total > readACLLogMaxBytes && i >= 1; i-- {
		info, err := os.Stat(paths[i])
		if err != nil {
			continue
		}
		if err := os.Remove(paths[i]); err != nil {
			return err
		}
		total -= info.Size()
	}
	return nil
}

func readACLLogSegmentPath(path string, index int) string {
	if index <= 0 {
		return path
	}
	return fmt.Sprintf("%s.%d", path, index)
}

func readACLLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("user home directory is unavailable")
	}
	return filepath.Join(home, ".caelis", "logs", "windows-read-acl.log"), nil
}

func readACLLogMutexName() string {
	return `Local\CaelisSandboxReadACLLog`
}

func normalizeReadACLSpecs(specs []readACLSpec) []readACLSpec {
	merged := mergeReadACLSpecs(nil, specs)
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func mergeReadACLSpecs(base []readACLSpec, extra []readACLSpec) []readACLSpec {
	type item struct {
		path       string
		rights     string
		principals []string
		seen       map[string]struct{}
	}
	items := map[string]*item{}
	order := make([]string, 0, len(base)+len(extra))
	add := func(spec readACLSpec) {
		path := pathutil.Normalize(spec.Path)
		if path == "" {
			return
		}
		rights := strings.ToUpper(strings.TrimSpace(spec.Rights))
		if rights == "" {
			rights = "RX"
		}
		key := pathutil.Key(path) + "\x00" + rights
		current := items[key]
		if current == nil {
			current = &item{
				path:   path,
				rights: rights,
				seen:   map[string]struct{}{},
			}
			items[key] = current
			order = append(order, key)
		}
		for _, principal := range appendPrincipal(spec.Principals...) {
			principalKey := strings.ToLower(principal)
			if _, ok := current.seen[principalKey]; ok {
				continue
			}
			current.seen[principalKey] = struct{}{}
			current.principals = append(current.principals, principal)
		}
	}
	for _, spec := range base {
		add(spec)
	}
	for _, spec := range extra {
		add(spec)
	}
	out := make([]readACLSpec, 0, len(order))
	for _, key := range order {
		current := items[key]
		if current == nil || len(current.principals) == 0 {
			continue
		}
		out = append(out, readACLSpec{
			Path:       current.path,
			Principals: current.principals,
			Rights:     current.rights,
		})
	}
	return out
}

func readACLQueuePath(stateRoot string) string {
	return filepath.Join(setupstate.NewDirs(stateRoot).Sandbox, "read_acl_queue.json")
}

func readACLQueueMutexName(stateRoot string) string {
	return stateMutexName(`Local\CaelisSandboxReadACLQueue-`, stateRoot)
}

func readACLDrainMutexName(stateRoot string) string {
	return stateMutexName(`Local\CaelisSandboxReadACLDrain-`, stateRoot)
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
	return requiredPolicyACLTargetsWithOptions(policy, false, users...)
}

func requiredPolicyACLTargetsWithOptions(policy winpolicy.Policy, synchronousOnly bool, users ...string) []policyACLTarget {
	users = appendPrincipal(users...)
	principals := sandboxPrincipals(users...)
	aclPlan := newPolicyACLPlan(policy)
	capabilities := aclPlan.allCapabilitySIDs
	var out []policyACLTarget
	if !synchronousOnly {
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
		targets := joinPrincipals(principals, aclPlan.writeRootCapabilitySIDsForPath(root))
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

func writeUsersFile(path string, offlineUser string, offlinePassword string, onlineUser string, onlinePassword string) error {
	offlineProtected, err := win32.ProtectMachineString(offlinePassword, "caelis sandbox offline password")
	if err != nil {
		return err
	}
	users := UsersFile{
		Offline: UserSecret{Username: offlineUser, PasswordProtected: offlineProtected},
	}
	if strings.TrimSpace(onlineUser) != "" {
		onlineProtected, err := win32.ProtectMachineString(onlinePassword, "caelis sandbox online password")
		if err != nil {
			return err
		}
		users.Online = &UserSecret{Username: onlineUser, PasswordProtected: onlineProtected}
	}
	data, err := json.MarshalIndent(users, "", "  ")
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
