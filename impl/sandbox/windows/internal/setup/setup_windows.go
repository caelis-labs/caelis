//go:build windows

package setup

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/acl"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/netpolicy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/pathutil"
	winpolicy "github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/policy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setupstate"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
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
	if payload.RefreshOnly {
		return executeRefresh(payload, progress)
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
	reportProgress(payload, progress, Progress{Phase: "state", Message: "protecting sandbox state directories", Step: 6, Total: 11})
	if err := protectStateDirectories(dirs, payload.OwnerUsername); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "acl", Message: "refreshing filesystem ACL policy", Step: 7, Total: 11})
	if err := applyPolicyACLs(payload.Policy, payload.OfflineUsername, payload.OnlineUsername); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "firewall", Message: "refreshing Windows Firewall rules; this can take a while", Step: 8, Total: 11})
	if err := netpolicy.Refresh(netpolicy.Config{
		OfflineUsername: payload.OfflineUsername,
		OnlineUsername:  payload.OnlineUsername,
	}); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "secrets", Message: "writing sandbox credentials", Step: 9, Total: 11})
	if err := writeUsersFile(dirs.UsersPath, payload.OfflineUsername, offlinePassword, payload.OnlineUsername, onlinePassword); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "marker", Message: "writing setup marker", Step: 10, Total: 11})
	if err := setupstate.WriteMarker(dirs.MarkerPath, setupstate.Marker{
		Version:         payload.Version,
		RunnerHash:      payload.RunnerHash,
		PolicyHash:      payload.PolicyHash,
		OfflineUsername: payload.OfflineUsername,
		OnlineUsername:  payload.OnlineUsername,
		OwnerUsername:   payload.OwnerUsername,
	}); err != nil {
		return err
	}
	if err := setupstate.ClearError(dirs.ErrorPath); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "Windows sandbox setup is ready", Step: 11, Total: 11, Done: true})
	return nil
}

func executeRefresh(payload Payload, progress ProgressFunc) error {
	dirs := setupstate.NewDirs(payload.StateRoot)
	reportProgress(payload, progress, Progress{Phase: "refresh", Message: "validating existing sandbox setup", Step: 1, Total: 3})
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
	if err := setupstate.EnsureDirs(dirs); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "acl", Message: "refreshing request ACL policy", Step: 2, Total: 3})
	if err := applyPolicyACLs(payload.Policy, payload.OfflineUsername, payload.OnlineUsername); err != nil {
		return err
	}
	if err := setupstate.ClearError(dirs.ErrorPath); err != nil {
		return err
	}
	reportProgress(payload, progress, Progress{Phase: "complete", Message: "request ACL policy is ready", Step: 3, Total: 3, Done: true})
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

func applyPolicyACLs(policy winpolicy.Policy, users ...string) error {
	principals := appendPrincipal(append([]string{GroupName}, users...)...)
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
		if err := grantPath(root, principals, "RX"); err != nil {
			return err
		}
	}
	for _, root := range policy.WriteRoots {
		sid := writeRootCapabilitySID(policy, root)
		targets := append([]string{}, principals...)
		if sid != "" {
			targets = append(targets, sid)
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
