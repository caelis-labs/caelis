//go:build windows

package win32

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	createNoWindow      = 0x08000000
	disableMaxPrivilege = 0x00000001
	luaToken            = 0x00000004
	writeRestricted     = 0x00000008
)

var procCreateRestrictedToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")

type Token = windows.Token

type tokenDefaultDACLInfo struct {
	DefaultDACL *windows.ACL
}

type ProcessIdentity struct {
	PID          uint32 `json:"pid"`
	CreationTime uint64 `json:"creation_time"`
}

func CurrentUserLocalAppData() (string, error) {
	path, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", fmt.Errorf("win32: resolve LocalAppData known folder: %w", err)
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("win32: LocalAppData known folder is empty")
	}
	return path, nil
}

func CurrentProcessIdentity() (ProcessIdentity, error) {
	return processIdentity(windows.CurrentProcess(), windows.GetCurrentProcessId())
}

// ProcessIdentityAlive returns false only when PID+creation-time proves that
// the recorded process no longer exists. Inspection failures remain errors so
// lifecycle cleanup can fail closed rather than prune an unproven owner.
func ProcessIdentityAlive(identity ProcessIdentity) (bool, error) {
	if identity.PID == 0 || identity.CreationTime == 0 {
		return false, fmt.Errorf("win32: complete process identity is required")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, identity.PID)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		return false, fmt.Errorf("win32: open process %d: %w", identity.PID, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	current, err := processIdentity(handle, identity.PID)
	if err != nil {
		return false, err
	}
	if current.CreationTime != identity.CreationTime {
		return false, nil
	}
	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, err
	}
	return event == uint32(windows.WAIT_TIMEOUT), nil
}

func processIdentity(handle windows.Handle, pid uint32) (ProcessIdentity, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return ProcessIdentity{}, fmt.Errorf("win32: read process %d creation time: %w", pid, err)
	}
	created := uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
	if pid == 0 || created == 0 {
		return ProcessIdentity{}, fmt.Errorf("win32: process identity is incomplete")
	}
	return ProcessIdentity{PID: pid, CreationTime: created}, nil
}

func CurrentProcessUserSID() (string, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return "", fmt.Errorf("win32: open current process token: %w", err)
	}
	defer token.Close()
	sid, err := tokenUserSID(token)
	if err != nil {
		return "", err
	}
	return sid.String(), nil
}

// CurrentProcessAuthorizesOwner reports whether ownerSID is an acceptable
// filesystem owner for elevated work performed on behalf of hostUserSID. The
// Administrators group is accepted only when this process is still running as
// the Host user and has that group enabled in its token.
func CurrentProcessAuthorizesOwner(hostUserSID, ownerSID string) (bool, error) {
	hostUserSID, err := NormalizeSID(hostUserSID)
	if err != nil {
		return false, err
	}
	ownerSID, err = NormalizeSID(ownerSID)
	if err != nil {
		return false, err
	}
	if strings.EqualFold(hostUserSID, ownerSID) {
		return true, nil
	}
	administratorsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, fmt.Errorf("win32: resolve Administrators SID: %w", err)
	}
	if !strings.EqualFold(ownerSID, administratorsSID.String()) {
		return false, nil
	}
	currentUserSID, err := CurrentProcessUserSID()
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(hostUserSID, currentUserSID) {
		return false, nil
	}
	member, err := windows.Token(0).IsMember(administratorsSID)
	if err != nil {
		return false, fmt.Errorf("win32: inspect current Administrators membership: %w", err)
	}
	return member, nil
}

// NormalizeSID validates and canonicalizes a string SID for durable identity
// handoff between processes running under different Windows tokens.
func NormalizeSID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("win32: SID is required")
	}
	sid, err := windows.StringToSid(value)
	if err != nil {
		return "", fmt.Errorf("win32: parse SID %q: %w", value, err)
	}
	if sid == nil || !sid.IsValid() {
		return "", fmt.Errorf("win32: SID %q is invalid", value)
	}
	return sid.String(), nil
}

func RestrictedCurrentProcessTokenWithSIDs(restrictingSIDs []string) (Token, error) {
	var current windows.Token
	err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_QUERY|windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_ADJUST_SESSIONID,
		&current,
	)
	if err != nil {
		return 0, err
	}
	defer current.Close()
	restricting, keepAliveSIDs, err := restrictingSIDAttributes(current, restrictingSIDs)
	if err != nil {
		return 0, err
	}
	var restrictingPtr uintptr
	if len(restricting) > 0 {
		restrictingPtr = uintptr(unsafe.Pointer(&restricting[0]))
	}
	var restricted windows.Token
	flags := uintptr(disableMaxPrivilege)
	if len(restricting) > 0 {
		flags |= luaToken | writeRestricted
	}
	r1, _, callErr := syscall.SyscallN(
		procCreateRestrictedToken.Addr(),
		uintptr(current),
		flags,
		0,
		0,
		0,
		0,
		uintptr(len(restricting)),
		restrictingPtr,
		uintptr(unsafe.Pointer(&restricted)),
	)
	runtime.KeepAlive(restricting)
	runtime.KeepAlive(keepAliveSIDs)
	if r1 == 0 {
		return 0, callErr
	}
	if len(restricting) > 0 {
		if err := setDefaultDACL(restricted, keepAliveSIDs); err != nil {
			_ = restricted.Close()
			return 0, err
		}
		if err := enableSinglePrivilege(restricted, "SeChangeNotifyPrivilege"); err != nil {
			_ = restricted.Close()
			return 0, err
		}
	}
	return restricted, nil
}

func setDefaultDACL(token windows.Token, sids []*windows.SID) error {
	if len(sids) == 0 {
		return nil
	}
	// WRITE_RESTRICTED tokens also check the restricting SID list for kernel
	// objects created by sandboxed children. A default DACL that only names the
	// user SID breaks common native process patterns such as Git spawning
	// remote helpers over anonymous pipes.
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	seen := map[string]struct{}{}
	for _, sid := range sids {
		if sid == nil {
			continue
		}
		key := sid.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	if len(entries) == 0 {
		return nil
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("win32: build restricted token default DACL: %w", err)
	}
	info := tokenDefaultDACLInfo{DefaultDACL: dacl}
	if err := windows.SetTokenInformation(token, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return fmt.Errorf("win32: set restricted token default DACL: %w", err)
	}
	runtime.KeepAlive(entries)
	runtime.KeepAlive(sids)
	runtime.KeepAlive(dacl)
	return nil
}

func restrictingSIDAttributes(token windows.Token, values []string) ([]windows.SIDAndAttributes, []*windows.SID, error) {
	restrictingSIDs := make([]*windows.SID, 0, len(values)+2)
	seen := map[string]struct{}{}
	appendSID := func(sid *windows.SID) {
		if sid == nil {
			return
		}
		key := sid.String()
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		restrictingSIDs = append(restrictingSIDs, sid)
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		sid, err := windows.StringToSid(value)
		if err != nil {
			return nil, nil, fmt.Errorf("win32: parse restricting SID %q: %w", value, err)
		}
		appendSID(sid)
	}
	if len(restrictingSIDs) == 0 {
		nullSID, err := windows.CreateWellKnownSid(windows.WinNullSid)
		if err != nil {
			return nil, nil, err
		}
		appendSID(nullSID)
	}
	// Windows PowerShell's CLR startup needs write access to session/global
	// objects that are not ACLed for arbitrary synthetic SIDs. Keep these
	// compatibility SIDs narrow: do not add the current user, Users, or
	// Interactive, which commonly appear on filesystem DACLs.
	everyoneSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return nil, nil, err
	}
	logonSID, err := tokenLogonSID(token)
	if err != nil {
		return nil, nil, err
	}
	appendSID(logonSID)
	appendSID(everyoneSID)

	out := make([]windows.SIDAndAttributes, 0, len(restrictingSIDs))
	for _, sid := range restrictingSIDs {
		out = append(out, windows.SIDAndAttributes{Sid: sid})
	}
	return out, restrictingSIDs, nil
}

func tokenUserSID(token windows.Token) (*windows.SID, error) {
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("win32: get token user SID: %w", err)
	}
	sid, err := user.User.Sid.Copy()
	if err != nil {
		return nil, fmt.Errorf("win32: copy token user SID: %w", err)
	}
	return sid, nil
}

func tokenLogonSID(token windows.Token) (*windows.SID, error) {
	groups, err := token.GetTokenGroups()
	if err != nil {
		return nil, fmt.Errorf("win32: get token groups: %w", err)
	}
	for _, group := range groups.AllGroups() {
		if group.Attributes&windows.SE_GROUP_LOGON_ID != windows.SE_GROUP_LOGON_ID {
			continue
		}
		sid, err := group.Sid.Copy()
		if err != nil {
			return nil, fmt.Errorf("win32: copy token logon SID: %w", err)
		}
		return sid, nil
	}
	return nil, fmt.Errorf("win32: token logon SID not found")
}

func enableSinglePrivilege(token windows.Token, name string) error {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, namePtr, &luid); err != nil {
		return fmt.Errorf("win32: lookup privilege %s: %w", name, err)
	}
	privileges := windows.Tokenprivileges{PrivilegeCount: 1}
	privileges.Privileges[0] = windows.LUIDAndAttributes{
		Luid:       luid,
		Attributes: windows.SE_PRIVILEGE_ENABLED,
	}
	if err := windows.AdjustTokenPrivileges(token, false, &privileges, uint32(unsafe.Sizeof(privileges)), nil, nil); err != nil {
		return fmt.Errorf("win32: enable privilege %s: %w", name, err)
	}
	if err := windows.GetLastError(); err != nil {
		return fmt.Errorf("win32: enable privilege %s: %w", name, err)
	}
	return nil
}

func ConfigureHiddenConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
